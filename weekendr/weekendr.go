// weekendr/weekendr.go
package weekendr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Version is incremented manually on each xcframework build.
const Version = "0.1.38"

// CoreVersion returns the build version so Swift can read it via gomobile.
func CoreVersion() string { return Version }

// Private discovery and relay server URLs, injected by the embedding app via
// Configure() before StartSyncthing runs. Empty by default so the public
// repo contains no deployment-specific URLs.
var (
	discoveryServerURL string
	relayServerURL     string
)

// Configure stores the private discovery and relay server URLs used by
// configureServers when SetSyncthing is called. Must be called by the
// embedding app (e.g. from Swift before StartSyncthing) to enable P2P sync
// through Weekendr's private infrastructure. When both values are empty
// (the default), configureServers leaves the Syncthing defaults untouched.
func Configure(discoveryURL string, relayURL string) {
	discoveryServerURL = discoveryURL
	relayServerURL = relayURL
}

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

	// FolderSharedWith returns true if the folder is shared with the given device.
	FolderSharedWith(folderID, deviceID string) bool

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
	pendingTombstones     map[string]map[string]bool // eventID → set of filenames queued for deletion from photo_index
	pendingTombstonesMu   sync.Mutex
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
		pendingTombstones:     make(map[string]map[string]bool),
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
// the concrete type supports it and Configure has been called with non-empty
// URLs. The methods are accessed via a local interface rather than
// SyncthingClient so that []string never appears in the exported interface
// (gomobile cannot bridge slice parameters).
//
// When both discoveryServerURL and relayServerURL are empty, this function is
// a no-op and the Syncthing defaults remain in effect.
func configureServers(s SyncthingClient) {
	if discoveryServerURL == "" && relayServerURL == "" {
		return
	}
	type serverConfigurer interface {
		SetDiscoveryServers(urls []string) error
		SetRelayServers(urls []string) error
	}
	sc, ok := s.(serverConfigurer)
	if !ok {
		return
	}
	if discoveryServerURL != "" {
		_ = sc.SetDiscoveryServers([]string{discoveryServerURL})
	}
	if relayServerURL != "" {
		_ = sc.SetRelayServers([]string{relayServerURL})
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
// The folder is shared via ShareFolderEncrypted with the per-event password
// (SHA256(folderKey)[:32], hex). The two sides of the link have asymmetric
// roles in the encrypted-folder model:
//
//   - Hub side (weekendr-server, AddDeviceToFolder): the guest's device entry
//     on the hub-side receiveencrypted folder has encryptionPassword="". The
//     hub serves the existing ciphertext as-is and does NOT re-encrypt for the
//     guest.
//   - Guest side (here): ShareFolderEncrypted with the password ⇒ the local
//     Syncthing knows the blobs arriving from the hub are ciphertext and
//     decrypts them locally with the supplied password.
//
// If we used plain ShareFolder here, the local Syncthing would treat the
// incoming data as plaintext and surface "remote expects to exchange
// encrypted data, but is configured for plain data". If the hub-side had a
// non-empty password it would re-encrypt the blobs and the guest would get
// garbage even with the correct local password.
//
// Idempotent: tracks per-folder state so repeated calls (e.g. metawatcher
// invoking addParticipantPhotoFolder more than once) only act once.
//
// No-op if hub coordinates or the folderKey for the event are not yet known
// — that happens when SharePhotoFolderWithHub has not been called yet for
// this event AND no persisted hub-{eventID}.json was rehydrated by NewClient.
// The host's own SharePhotoFolderWithHub call will populate c.hubInfos and
// c.folderKeys and the deferred catch-up loop will retroactively share any
// already-registered receiveonly folders.
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

	// Local Syncthing must know the per-event password so it can decrypt the
	// ciphertext blobs the hub serves. Without the folderKey we cannot derive
	// the password — refuse rather than fall back to plain ShareFolder, which
	// would surface "remote expects to exchange encrypted data, but is
	// configured for plain data".
	folderKey := c.folderKeys[eventIDLower]
	if folderKey == "" {
		fmt.Println("GoCore: shareReceiveOnlyFolderWithHub: no folderKey for event " + eventID + ", refusing to share " + folderID)
		return
	}
	encPassword := photoEncryptionPassword(folderKey)

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

	// Add hub as peer of the receiveonly folder using the per-event encryption
	// password — same password the host derived in SharePhotoFolderWithHub.
	// The hub-side device entry for us is empty (weekendr-server passes "" in
	// AddDeviceToFolder), so the hub serves ciphertext as-is and we decrypt
	// it locally using this password.
	if err := c.syncthing.ShareFolderEncrypted(folderID, hub.deviceID, encPassword); err != nil {
		fmt.Println("GoCore: shareReceiveOnlyFolderWithHub: ShareFolderEncrypted(" + folderID + ", " + hub.deviceID + ") error: " + err.Error())
		return
	}
	c.hubSharedFolders[folderID] = true
	fmt.Println("GoCore: shareReceiveOnlyFolderWithHub: shared " + folderID + " with hub " + hub.deviceID + " (encrypted)")
}

// photoEncryptionPassword derives the encryption password for photo folders.
// SHA256(folderKey), hex-encoded, first 32 chars. Event-wide, same for all participants.
// Identical to server-side photoEncryptionPassword.
func photoEncryptionPassword(folderKey string) string {
	h := sha256.Sum256([]byte(folderKey))
	return hex.EncodeToString(h[:])[:32]
}

// AddPhotoIndexEntry appends a single photo to the pending photo-index for
// eventID (dedup by filename — a subsequent call with the same filename
// replaces the earlier entry) and schedules a debounced flush via
// SchedulePhotoIndexUpdate. Swift calls this per-photo during export;
// bursts coalesce into a single disk write 2s after the last add.
//
// latitude/longitude are optional — pass nil when EXIF GPS is absent.
func (c *Client) AddPhotoIndexEntry(eventID, filename, takenAt string, size int64, hash string, latitude, longitude *float64) error {
	entry := PhotoIndexEntry{
		Filename:  filename,
		TakenAt:   takenAt,
		Size:      size,
		Hash:      hash,
		Latitude:  latitude,
		Longitude: longitude,
	}

	pendingMutex.Lock()
	existing := pendingEntries[eventID]
	filtered := make([]PhotoIndexEntry, 0, len(existing)+1)
	for _, e := range existing {
		if e.Filename != filename {
			filtered = append(filtered, e)
		}
	}
	filtered = append(filtered, entry)
	pendingEntries[eventID] = filtered
	pendingMutex.Unlock()

	// A prior RemovePhotoIndexEntry in the same debounce window would have
	// set a tombstone for this filename; clear it so the merge does not
	// delete the re-added entry. Must happen before SchedulePhotoIndexUpdate
	// so the next flush sees the cleared tombstone set.
	c.pendingTombstonesMu.Lock()
	if ts, ok := c.pendingTombstones[eventID]; ok {
		delete(ts, filename)
	}
	c.pendingTombstonesMu.Unlock()

	c.SchedulePhotoIndexUpdate(eventID, filtered)
	return nil
}

// AddPhotoIndexEntryWithLocation is a gomobile-friendly wrapper around
// AddPhotoIndexEntry. gomobile cannot bridge *float64, so this variant
// accepts latitude/longitude as plain float64 plus a hasLocation flag and
// internally constructs the pointers expected by AddPhotoIndexEntry.
func (c *Client) AddPhotoIndexEntryWithLocation(eventID, filename, takenAt string, size int64, hash string, hasLocation bool, latitude, longitude float64) error {
	var lat, lng *float64
	if hasLocation {
		lat = &latitude
		lng = &longitude
	}
	return c.AddPhotoIndexEntry(eventID, filename, takenAt, size, hash, lat, lng)
}

// AddPhotoIndexEntryNoLocation is a gomobile-friendly wrapper around
// AddPhotoIndexEntry for the no-GPS case.
func (c *Client) AddPhotoIndexEntryNoLocation(eventID, filename, takenAt string, size int64, hash string) error {
	return c.AddPhotoIndexEntry(eventID, filename, takenAt, size, hash, nil, nil)
}

// RemovePhotoIndexEntry marks filename for deletion from the photo_index by
// recording a tombstone and immediately filtering the in-memory pending
// buffer, then arms the debouncer so the next flush picks up the tombstone.
// The merge step in SchedulePhotoIndexUpdate applies tombstones AFTER the
// disk+pending merge, so a concurrent re-add of the same filename before the
// flush cannot resurrect the entry — the tombstone always wins for the
// current debounce window.
func (c *Client) RemovePhotoIndexEntry(eventID, filename string) error {
	c.pendingTombstonesMu.Lock()
	if c.pendingTombstones[eventID] == nil {
		c.pendingTombstones[eventID] = make(map[string]bool)
	}
	c.pendingTombstones[eventID][filename] = true
	c.pendingTombstonesMu.Unlock()

	pendingMutex.Lock()
	existing := pendingEntries[eventID]
	cleaned := make([]PhotoIndexEntry, 0, len(existing))
	for _, e := range existing {
		if e.Filename != filename {
			cleaned = append(cleaned, e)
		}
	}
	pendingEntries[eventID] = cleaned
	pendingMutex.Unlock()

	// Passing nil arms the debouncer without overwriting the pending slice
	// we just filtered — otherwise concurrent adds staged before this delete
	// would be lost.
	c.SchedulePhotoIndexUpdate(eventID, nil)
	return nil
}

// SchedulePhotoIndexFlush is a poke that ensures whatever is currently in
// pendingEntries[eventID] gets flushed to disk — it (re)arms the 2s debouncer
// with the current entries. Useful on app background / event switch so
// queued-but-not-yet-flushed adds don't get dropped.
func (c *Client) SchedulePhotoIndexFlush(eventID string) error {
	pendingMutex.Lock()
	entries := pendingEntries[eventID]
	pendingMutex.Unlock()

	if len(entries) > 0 {
		c.SchedulePhotoIndexUpdate(eventID, entries)
	}
	return nil
}
