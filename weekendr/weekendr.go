// weekendr/weekendr.go
package weekendr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

// Version is incremented manually on each xcframework build.
const Version = "0.1.26"

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

// hubInfo holds the per-event Weekendr hub coordinates so that receiveonly
// folders created later (host or other participants) can also be shared with
// the hub. Populated by SharePhotoFolderWithHub.
type hubInfo struct {
	deviceID string
	address  string
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
	processedParticipants map[string]bool     // keyed by "eventID:deviceID", prevents duplicate processing
	folderKeys            map[string]string   // eventID → folderKey, set by SetFolderKey
	hubInfos              map[string]*hubInfo // eventID → hub deviceID/address, set by SharePhotoFolderWithHub
	hubSharedFolders      map[string]bool     // folderID → true once the hub has been added as peer to that folder
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
		hubInfos:              make(map[string]*hubInfo),
		hubSharedFolders:      make(map[string]bool),
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
	fmt.Println("GoCore: SharePhotoFolderWithHub ENTER eventID=" + eventID + " hub=" + hubDeviceID + " addr=" + hubAddress + " folderKeyLen=" + fmt.Sprint(len(folderKey)))
	if c.syncthing == nil {
		fmt.Println("GoCore: SharePhotoFolderWithHub: syncthing is nil, skipping")
		return nil
	}
	if c.userID == "" {
		fmt.Println("GoCore: SharePhotoFolderWithHub: userID not set")
		return fmt.Errorf("SharePhotoFolderWithHub: userID not set")
	}
	fmt.Println("GoCore: SharePhotoFolderWithHub: userID=" + c.userID)

	eventIDLower := strings.ToLower(eventID)
	userIDLower := strings.ToLower(c.userID)

	// Remember hub coordinates for this event so receiveonly folders that are
	// registered later (host's photo folder via BootstrapConnection, other
	// participants' photo folders via addParticipantPhotoFolder) can also be
	// shared with the hub. Without this, Syncthing would only know to pull
	// from the participant directly and could never sync via the hub relay.
	c.hubInfos[eventIDLower] = &hubInfo{deviceID: hubDeviceID, address: hubAddress}

	// 1. Add hub as known peer and set its direct address
	if err := c.syncthing.AddPeer(hubDeviceID); err != nil {
		fmt.Println("GoCore: SharePhotoFolderWithHub: AddPeer error: " + err.Error())
	} else {
		fmt.Println("GoCore: SharePhotoFolderWithHub: AddPeer OK")
	}
	if hubAddress != "" {
		type deviceAddresser interface {
			SetDeviceAddresses(deviceID string, addresses []string) error
		}
		if da, ok := c.syncthing.(deviceAddresser); ok {
			if err := da.SetDeviceAddresses(hubDeviceID, []string{hubAddress}); err != nil {
				fmt.Println("GoCore: SharePhotoFolderWithHub: SetDeviceAddresses error: " + err.Error())
			} else {
				fmt.Println("GoCore: SharePhotoFolderWithHub: set hub address " + hubAddress)
			}
		}
	}

	// 2. Share photo folder with hub using encryption password
	photoFolderID := "photos-" + eventIDLower + "-" + userIDLower
	encPassword := photoEncryptionPassword(folderKey)
	fmt.Println("GoCore: SharePhotoFolderWithHub: photoFolderID=" + photoFolderID + " encPwd=" + encPassword[:8] + "...")

	// Wait for folder to be registered (ensureFoldersRegistered may still be running)
	for i := 0; i < 10; i++ {
		if c.syncthing.FolderExists(photoFolderID) {
			fmt.Println("GoCore: SharePhotoFolderWithHub: folder found on attempt " + fmt.Sprint(i+1))
			break
		}
		fmt.Println("GoCore: SharePhotoFolderWithHub: waiting for folder " + photoFolderID + " (" + fmt.Sprint(i+1) + "/10)")
		time.Sleep(500 * time.Millisecond)
	}
	if !c.syncthing.FolderExists(photoFolderID) {
		fmt.Println("GoCore: SharePhotoFolderWithHub: folder " + photoFolderID + " still not found after 5s")
		return fmt.Errorf("photo folder not ready after timeout")
	}

	if err := c.syncthing.ShareFolderEncrypted(photoFolderID, hubDeviceID, encPassword); err != nil {
		fmt.Println("GoCore: SharePhotoFolderWithHub: ShareFolderEncrypted error: " + err.Error())
		return fmt.Errorf("SharePhotoFolderWithHub: share photo folder: %w", err)
	}
	fmt.Println("GoCore: SharePhotoFolderWithHub: shared " + photoFolderID + " with " + hubDeviceID + " (encrypted)")

	// 3. Share meta folder with hub (no encryption)
	metaFolderID := "meta-" + eventIDLower
	if c.syncthing.FolderExists(metaFolderID) {
		if err := c.syncthing.ShareFolder(metaFolderID, hubDeviceID); err != nil {
			fmt.Println("GoCore: SharePhotoFolderWithHub: share meta folder error: " + err.Error())
		} else {
			fmt.Println("GoCore: SharePhotoFolderWithHub: shared " + metaFolderID + " with " + hubDeviceID)
		}
	}

	fmt.Println("GoCore: SharePhotoFolderWithHub: DONE")
	return nil
}

// shareReceiveOnlyFolderWithHub registers the Weekendr hub as an additional
// peer of a freshly-created receiveonly photo folder so that Syncthing can
// pull the encrypted blobs from the hub when the originating device is offline
// or unreachable.
//
// Called from BootstrapConnection (for the host's photo folder) and from
// addParticipantPhotoFolder (for every other participant's photo folder).
//
// The folder is shared WITHOUT an encryption password — only the host's own
// sendonly folder is uploaded to the hub via ShareFolderEncrypted; the
// receiveonly side just consumes whatever the hub serves over the protocol.
//
// Idempotent: tracks per-folder state so repeated calls (e.g. metawatcher
// invoking addParticipantPhotoFolder more than once) only act once.
//
// No-op if hub coordinates for the event are not yet known — that happens
// when SharePhotoFolderWithHub has not been called yet for this event, in
// which case the host's own SharePhotoFolderWithHub call will populate
// c.hubInfos and any subsequent receiveonly folder registration will pick it
// up automatically.
func (c *Client) shareReceiveOnlyFolderWithHub(eventID, folderID string) {
	if c.syncthing == nil {
		return
	}
	if c.hubSharedFolders[folderID] {
		return
	}
	hub, ok := c.hubInfos[strings.ToLower(eventID)]
	if !ok || hub == nil || hub.deviceID == "" {
		fmt.Println("GoCore: shareReceiveOnlyFolderWithHub: no hub info for event " + eventID + ", skipping " + folderID)
		return
	}

	// Register the hub as a known device (idempotent at the Syncthing layer).
	if err := c.syncthing.AddPeer(hub.deviceID); err != nil {
		fmt.Println("GoCore: shareReceiveOnlyFolderWithHub: AddPeer error: " + err.Error())
	}

	// Pin the hub's direct address so the relay can be bypassed when reachable.
	if hub.address != "" {
		type deviceAddresser interface {
			SetDeviceAddresses(deviceID string, addresses []string) error
		}
		if da, ok := c.syncthing.(deviceAddresser); ok {
			if err := da.SetDeviceAddresses(hub.deviceID, []string{hub.address}); err != nil {
				fmt.Println("GoCore: shareReceiveOnlyFolderWithHub: SetDeviceAddresses error: " + err.Error())
			}
		}
	}

	// Add hub as peer of the receiveonly folder. NO encryption password — only
	// the sender uploads encrypted; the receiver just pulls whatever the hub
	// has on disk.
	if err := c.syncthing.ShareFolder(folderID, hub.deviceID); err != nil {
		fmt.Println("GoCore: shareReceiveOnlyFolderWithHub: ShareFolder(" + folderID + ", " + hub.deviceID + ") error: " + err.Error())
		return
	}
	c.hubSharedFolders[folderID] = true
	fmt.Println("GoCore: shareReceiveOnlyFolderWithHub: shared " + folderID + " with hub " + hub.deviceID)
}

// photoEncryptionPassword derives the encryption password for photo folders.
// SHA256(folderKey), hex-encoded, first 32 chars. Event-wide, same for all participants.
// Identical to server-side photoEncryptionPassword.
func photoEncryptionPassword(folderKey string) string {
	h := sha256.Sum256([]byte(folderKey))
	return hex.EncodeToString(h[:])[:32]
}
