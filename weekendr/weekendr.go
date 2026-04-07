// weekendr/weekendr.go
package weekendr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// Version is incremented manually on each xcframework build.
const Version = "0.1.22"

// CoreVersion returns the build version so Swift can read it via gomobile.
func CoreVersion() string { return Version }

// SyncthingClient is the subset of the Syncthing API required by Weekendr.
//
// In production this interface is satisfied by an adapter around
// *sushitrain.Client (SushitrainCore module). The adapter maps:
//
//	AddFolder   → sushitrain.Client.AddSpecialFolder(id, "basic", path, type)
//	AddPeer     → sushitrain.Client.AddPeer(deviceID)
//	ShareFolder → sushitrain.Client.FolderWithID(id).ShareWithDevice(deviceID, true, "")
//
// Discovery and relay server URLs are configured via configureServers, which
// uses a type assertion so that []string never appears in this interface
// (gomobile cannot bridge slice parameters).
//
// When syncthing is nil (tests, offline mode) all P2P registration calls are
// skipped gracefully; OS directories are still created as before.
type SyncthingClient interface {
	// AddFolder registers a new folder with Syncthing at the given OS path.
	// folderType must be "sendonly", "receiveonly", or "sendreceive".
	AddFolder(folderID, folderPath, folderType string) error

	// AddPeer adds a remote device to Syncthing so connections can be made.
	AddPeer(deviceID string) error

	// ShareFolder shares an already-registered folder with a remote device.
	ShareFolder(folderID, deviceID string) error

	// ShareFolderEncrypted shares a folder with a remote device using an encryption password.
	ShareFolderEncrypted(folderID, deviceID, encryptionPassword string) error

	// RescanFolder triggers an immediate rescan of a folder so that
	// newly written files are picked up without waiting for the periodic interval.
	RescanFolder(folderID string) error

	// SetFolderRescanInterval sets the periodic rescan interval for a folder.
	SetFolderRescanInterval(folderID string, seconds int) error

	// FolderExists returns true if the folder is registered in Syncthing config.
	FolderExists(folderID string) bool

	// FolderIDs returns the IDs of all folders registered in Syncthing config.
	FolderIDs() *StringList

	// RemoveFolder unlinks a folder from Syncthing config without deleting files.
	RemoveFolder(folderID string) error
}

// StringList wraps a string slice for gomobile compatibility (gomobile cannot
// bridge []string return types).
type StringList struct{ items []string }

// NewStringList creates an empty StringList. Use Add() to append items.
func NewStringList() *StringList { return &StringList{} }

// Add appends a string to the list.
func (l *StringList) Add(s string) { l.items = append(l.items, s) }

// Get returns the string at index i.
func (l *StringList) Get(i int) string { return l.items[i] }

// Size returns the number of strings in the list.
func (l *StringList) Size() int { return len(l.items) }

// watcherEntry tracks a running metawatcher goroutine.
type watcherEntry struct {
	stop  chan struct{} // closed to signal the goroutine to exit
	alive chan struct{} // closed when the goroutine actually exits
}

// Client is the main entry point for the Weekendr Go core.
// All state is held here and accessed via methods.
type Client struct {
	deviceID              string
	userID                string // persistent identity set by SetUserID; used for photo folder naming when non-empty
	dataDir               string
	activeEventID         string // set by CreateEvent/JoinEvent; used by StartSyncthing to register folders
	watchers              map[string]*watcherEntry
	syncthing             SyncthingClient // nil until SetSyncthing is called
	syncthingReady        chan struct{}
	processedParticipants map[string]bool   // keyed by "eventID:deviceID", prevents duplicate processing
	folderKeys            map[string]string // eventID → folderKey, set by SetFolderKey
}

// NewClient initialises the Weekendr core.
// The deviceID is empty until StartSyncthing sets it from Sushitrain's TLS cert.
func NewClient(dataDir string) (*Client, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}
	c := &Client{
		dataDir:               dataDir,
		watchers:              make(map[string]*watcherEntry),
		syncthingReady:        make(chan struct{}),
		processedParticipants: make(map[string]bool),
		folderKeys:            make(map[string]string),
	}
	return c, nil
}

// SetSyncthing wires a live Syncthing back-end into the client and immediately
// configures it to use Weekendr's private discovery and relay servers exclusively.
// Call this before CreateEvent, JoinEvent, or StartMetaWatcher to enable P2P sync.
// The concrete value must be an adapter around *sushitrain.Client.
func (c *Client) SetSyncthing(s SyncthingClient) {
	c.syncthing = s
	configureServers(s)
}

// configureServers sets Weekendr's private discovery and relay servers on s if
// the concrete type supports it. The methods are accessed via a local interface
// rather than SyncthingClient so that []string never appears in the exported
// interface (gomobile cannot bridge slice parameters).
func configureServers(s SyncthingClient) {
	type serverConfigurer interface {
		SetDiscoveryServers(urls []string) error
		SetRelayServers(urls []string) error
	}
	if sc, ok := s.(serverConfigurer); ok {
		_ = sc.SetDiscoveryServers([]string{"https://discovery.getweekendr.app"})
		_ = sc.SetRelayServers([]string{"relay://relay.getweekendr.app:22067/?id=2ITVNHO-U3GGZSU-XXLLEUM-RMDH5M5-XSUSVXK-6ENQX6F-C5PA377-LPK62Q7"})
	}
}

// WaitForSyncthing blocks until Syncthing is ready or the timeout expires.
// Returns true if Syncthing is ready, false on timeout.
func (c *Client) WaitForSyncthing(timeoutSeconds int) bool {
	select {
	case <-c.syncthingReady:
		return true
	case <-time.After(time.Duration(timeoutSeconds) * time.Second):
		return false
	}
}

// DeviceID returns this device's Syncthing device ID.
func (c *Client) DeviceID() string {
	return c.deviceID
}

// SetUserID sets the persistent user identity used for photo folder naming.
// Call this once at app startup, before EnsureFoldersRegistered.
func (c *Client) SetUserID(userID string) {
	c.userID = userID
}

// UserID returns the persistent user ID (empty if not set).
func (c *Client) UserID() string {
	return c.userID
}

// folderIdentity returns the identity string used for photo folder naming.
// Prefers userID when set; falls back to deviceID for backward compatibility.
func (c *Client) folderIdentity() string {
	if c.userID != "" {
		return c.userID
	}
	return c.deviceID
}

// RescanFolder triggers an immediate rescan of the given folder so that
// newly written files are picked up by Syncthing without waiting for the
// periodic interval.
func (c *Client) RescanFolder(folderID string) error {
	if c.syncthing == nil {
		return nil
	}
	return c.syncthing.RescanFolder(folderID)
}

// SetFolderKey stores the encryption folder key for an event.
// Call this after joining or creating an event so that
// SharePhotoFolderWithHub can derive the encryption password.
func (c *Client) SetFolderKey(eventID, folderKey string) {
	c.folderKeys[strings.ToLower(eventID)] = folderKey
}

// FolderKey returns the stored folder key for an event (empty if not set).
func (c *Client) FolderKey(eventID string) string {
	return c.folderKeys[strings.ToLower(eventID)]
}

// SharePhotoFolderWithHub registers the hub as a Syncthing peer and shares
// this device's photo folder with it using the correct encryption password.
// The meta folder is shared without encryption.
func (c *Client) SharePhotoFolderWithHub(eventID, hubDeviceID, folderKey, hubAddress string) error {
	if c.syncthing == nil {
		return nil
	}
	if c.userID == "" {
		return fmt.Errorf("SharePhotoFolderWithHub: userID not set")
	}

	eventIDLower := strings.ToLower(eventID)
	userIDLower := strings.ToLower(c.userID)

	// 1. Add hub as known peer and set its direct address
	if err := c.syncthing.AddPeer(hubDeviceID); err != nil {
		log.Printf("GoCore: SharePhotoFolderWithHub: AddPeer(%s): %v", hubDeviceID, err)
	}
	if hubAddress != "" {
		type deviceAddresser interface {
			SetDeviceAddresses(deviceID string, addresses []string) error
		}
		if da, ok := c.syncthing.(deviceAddresser); ok {
			if err := da.SetDeviceAddresses(hubDeviceID, []string{hubAddress}); err != nil {
				log.Printf("GoCore: SharePhotoFolderWithHub: SetDeviceAddresses(%s): %v", hubDeviceID, err)
			} else {
				log.Printf("GoCore: SharePhotoFolderWithHub: set hub address %s", hubAddress)
			}
		}
	}

	// 2. Share photo folder with hub using encryption password
	photoFolderID := "photos-" + eventIDLower + "-" + userIDLower
	encPassword := photoEncryptionPassword(folderKey)

	// Wait for folder to be registered (ensureFoldersRegistered may still be running)
	for i := 0; i < 10; i++ {
		if c.syncthing.FolderExists(photoFolderID) {
			break
		}
		log.Printf("GoCore: SharePhotoFolderWithHub: waiting for folder %s (%d/10)", photoFolderID, i+1)
		time.Sleep(500 * time.Millisecond)
	}
	if !c.syncthing.FolderExists(photoFolderID) {
		log.Printf("GoCore: SharePhotoFolderWithHub: folder %s still not found after 5s", photoFolderID)
		return fmt.Errorf("photo folder not ready after timeout")
	}

	if err := c.syncthing.ShareFolderEncrypted(photoFolderID, hubDeviceID, encPassword); err != nil {
		return fmt.Errorf("SharePhotoFolderWithHub: share photo folder: %w", err)
	}
	log.Printf("GoCore: SharePhotoFolderWithHub: shared %s with %s (encrypted)", photoFolderID, hubDeviceID)

	// 3. Share meta folder with hub (no encryption)
	metaFolderID := "meta-" + eventIDLower
	if c.syncthing.FolderExists(metaFolderID) {
		if err := c.syncthing.ShareFolder(metaFolderID, hubDeviceID); err != nil {
			log.Printf("GoCore: SharePhotoFolderWithHub: share meta folder: %v", err)
		} else {
			log.Printf("GoCore: SharePhotoFolderWithHub: shared %s with %s", metaFolderID, hubDeviceID)
		}
	}

	return nil
}

// photoEncryptionPassword derives the encryption password for photo folders.
// SHA256(folderKey), hex-encoded, first 32 chars. Event-wide, same for all participants.
// Identical to server-side photoEncryptionPassword.
func photoEncryptionPassword(folderKey string) string {
	h := sha256.Sum256([]byte(folderKey))
	return hex.EncodeToString(h[:])[:32]
}
