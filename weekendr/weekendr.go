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
const Version = "0.1.34"

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

	// Hydrate hubInfos and folderKeys from disk before any folder registration
	// runs. After an app restart with already-joined events, SharePhotoFolderWithHub
	// is never called again — without this hydration step, every later
	// shareReceiveOnlyFolderWithHub call would no-op for lack of hub coordinates,
	// and the receiveonly folders would never sync via the hub.
	for eventIDLower, info := range loadAllHubInfos(dataDir) {
		if info.HubDeviceID != "" {
			c.hubInfos[eventIDLower] = &hubInfo{deviceID: info.HubDeviceID, address: info.HubAddress}
		}
		if info.FolderKey != "" {
			c.folderKeys[eventIDLower] = info.FolderKey
		}
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

	// Cache the folderKey on the client so that shareReceiveOnlyFolderWithHub
	// can derive the same encryption password the host used. The Swift layer
	// is supposed to call SetFolderKey separately, but caching here makes the
	// helper independent of call order — the host's own SharePhotoFolderWithHub
	// always populates this map before any catch-up loop runs.
	c.folderKeys[eventIDLower] = folderKey

	// Remember hub coordinates for this event so receiveonly folders that are
	// registered later (host's photo folder via BootstrapConnection, other
	// participants' photo folders via addParticipantPhotoFolder) can also be
	// shared with the hub. Without this, Syncthing would only know to pull
	// from the participant directly and could never sync via the hub relay.
	c.hubInfos[eventIDLower] = &hubInfo{deviceID: hubDeviceID, address: hubAddress}

	// Persist hub coordinates and folder key to disk so that the next app
	// launch can rehydrate them via NewClient → loadAllHubInfos. Without this,
	// SharePhotoFolderWithHub would only ever be called once per join (Swift
	// does not re-issue it for already-known events on restart) and every
	// subsequent shareReceiveOnlyFolderWithHub call would no-op.
	if err := writeHubInfo(c.dataDir, eventIDLower, persistedHubInfo{
		HubDeviceID: hubDeviceID,
		HubAddress:  hubAddress,
		FolderKey:   folderKey,
	}); err != nil {
		fmt.Println("GoCore: SharePhotoFolderWithHub: persist hub info error: " + err.Error())
		// Non-fatal: in-memory state is correct, only persistence is broken.
	} else {
		fmt.Println("GoCore: SharePhotoFolderWithHub: persisted hub info for event " + eventIDLower)
	}

	// Retroactively share any receiveonly photo folders that were already
	// registered for this event before the hub coordinates were known. On the
	// Guest, BootstrapConnection runs before SharePhotoFolderWithHub and
	// creates the host's receiveonly folder while c.hubInfos is still empty —
	// without this catch-up loop the hub would never be added as a peer of
	// that folder and Syncthing could not pull the host's photos via the hub.
	// Done at the end of the function (after the hub address has been pinned)
	// so the per-folder shareReceiveOnlyFolderWithHub call only has to do the
	// ShareFolder step on already-known peer state.
	defer c.shareKnownReceiveOnlyFoldersWithHub(eventIDLower)

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

// shareKnownReceiveOnlyFoldersWithHub iterates the receiveonly photo folders
// already registered with Syncthing for the given event and shares each with
// the hub. Called from SharePhotoFolderWithHub after c.hubInfos has been
// populated, so that receiveonly folders created earlier (before the hub
// coordinates were known — typically the host's folder created by the Guest's
// BootstrapConnection) catch up to the new hub-as-peer state.
//
// "Receiveonly" is identified by the folder ID prefix `photos-{eventID}-`
// excluding our own sendonly folder `photos-{eventID}-{userID}`. This avoids
// querying the Syncthing folder type via the interface (which would require a
// new method) at the cost of a string comparison.
func (c *Client) shareKnownReceiveOnlyFoldersWithHub(eventIDLower string) {
	if c.syncthing == nil {
		return
	}
	ownPhotoFolderID := "photos-" + eventIDLower + "-" + strings.ToLower(c.userID)
	prefix := "photos-" + eventIDLower + "-"

	folderIDs := c.syncthing.FolderIDs()
	for i := 0; i < folderIDs.Size(); i++ {
		folderID := folderIDs.Get(i)
		if !strings.HasPrefix(folderID, prefix) {
			continue
		}
		// Skip our own sendonly folder. The explicit c.userID != "" check
		// guards against the degenerate "photos-{event}-" prefix that would
		// otherwise match no real folder anyway, but mirrors the same shape
		// as the guard in acceptPendingFolders for consistency.
		if c.userID != "" && folderID == ownPhotoFolderID {
			continue
		}
		fmt.Println("GoCore: shareKnownReceiveOnlyFoldersWithHub: catching up " + folderID)
		c.shareReceiveOnlyFolderWithHub(eventIDLower, folderID)
	}
}

// shareReceiveOnlyFolderWithHub registers the Weekendr hub as an additional
// peer of a freshly-created receiveonly photo folder so that Syncthing can
// pull the photos from the hub when the originating device is offline or
// unreachable.
//
// Called from BootstrapConnection (for the host's photo folder) and from
// addParticipantPhotoFolder (for every other participant's photo folder).
//
// The folder is shared via plain ShareFolder WITHOUT an encryption password.
// The hub stores the folder as receiveencrypted (because the host uploaded
// via ShareFolderEncrypted), but for the receiveonly side to consume the
// data correctly, the device entry on BOTH sides must have an empty
// encryptionPassword:
//
//   - On the receiveonly side (this device): plain ShareFolder ⇒ empty
//     password on our local hub-device entry
//   - On the hub side: weekendr-server adds the guest device to the host
//     photo folder via AddDeviceToFolder with encryptionPassword=""
//
// If either side is non-empty, Syncthing reports either "remote has
// encrypted data and encrypts that data for us — this is impossible"
// (this side passes a password) or the hub re-encrypts the blobs and the
// guest gets garbage (hub side passes a password).
//
// Idempotent: tracks per-folder state so repeated calls (e.g. metawatcher
// invoking addParticipantPhotoFolder more than once) only act once.
//
// No-op if hub coordinates for the event are not yet known — that happens
// when SharePhotoFolderWithHub has not been called yet for this event AND
// no persisted hub-{eventID}.json was rehydrated by NewClient. The host's
// own SharePhotoFolderWithHub call will populate c.hubInfos and the deferred
// catch-up loop will retroactively share any already-registered receiveonly
// folders.
func (c *Client) shareReceiveOnlyFolderWithHub(eventID, folderID string) {
	if c.syncthing == nil {
		return
	}
	if c.hubSharedFolders[folderID] {
		return
	}
	eventIDLower := strings.ToLower(eventID)

	// Hard chokepoint: never plain-share our OWN sendonly folder. The hub
	// already has it as receiveencrypted (uploaded via ShareFolderEncrypted in
	// SharePhotoFolderWithHub), so a plain ShareFolder against the hub here
	// would trigger "remote expects to exchange encrypted data, but is
	// configured for plain data". The catch-up loop already filters out the
	// own folder, but addParticipantPhotoFolder can still reach this with our
	// folder ID when another device announces itself with the same userID
	// (e.g. the same account logged in twice) — metawatcher only de-dupes by
	// deviceID, not by userID, so the participant path constructs a folder ID
	// that collides with our own sendonly registration.
	if c.userID != "" && folderID == "photos-"+eventIDLower+"-"+strings.ToLower(c.userID) {
		fmt.Println("GoCore: shareReceiveOnlyFolderWithHub: refusing to share own sendonly folder " + folderID)
		return
	}

	hub, ok := c.hubInfos[eventIDLower]
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

	// Add hub as peer of the receiveonly folder. NO encryption password — the
	// hub's matching device-entry on the host photo folder also has an empty
	// password (set by weekendr-server's AddDeviceToFolder call), and that
	// symmetry is what makes Syncthing serve the ciphertext as-is so the
	// receiveonly peer can decrypt locally.
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
