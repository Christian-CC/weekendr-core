package weekendr

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	sushitrain "t-shaped.nl/sushitrain/v2/src"
)

// sushitrainAdapter wraps *sushitrain.Client to implement both the
// SyncthingClient interface and the serverConfigurer interface expected
// by configureServers. Keeping this in the same Go runtime avoids the
// gomobile two-runtime crash on iOS.
type sushitrainAdapter struct {
	st *sushitrain.Client
}

func (a *sushitrainAdapter) AddFolder(folderID, folderPath, folderType string) error {
	return a.st.AddSpecialFolder(folderID, "basic", folderPath, folderType)
}

func (a *sushitrainAdapter) AddPeer(deviceID string) error {
	log.Printf("GoCore: sushitrainAdapter.AddPeer('%s')", deviceID)
	if err := a.st.AddPeer(deviceID); err != nil {
		log.Printf("GoCore: AddPeer error: %v", err)
		return err
	}
	log.Printf("GoCore: AddPeer succeeded for %s", deviceID)
	return nil
}

func (a *sushitrainAdapter) FolderExists(folderID string) bool {
	return a.st.FolderWithID(folderID) != nil
}

func (a *sushitrainAdapter) FolderIDs() *StringList {
	list := a.st.Folders()
	if list == nil {
		return &StringList{}
	}
	ids := make([]string, list.Count())
	for i := 0; i < list.Count(); i++ {
		ids[i] = list.ItemAt(i)
	}
	return &StringList{items: ids}
}

func (a *sushitrainAdapter) RemoveFolder(folderID string) error {
	folder := a.st.FolderWithID(folderID)
	if folder == nil {
		return fmt.Errorf("folder not found: %s", folderID)
	}
	return folder.Unlink()
}

func (a *sushitrainAdapter) ShareFolder(folderID, deviceID string) error {
	log.Printf("GoCore: sushitrainAdapter.ShareFolder('%s', '%s')", folderID, deviceID)
	folder := a.st.FolderWithID(folderID)
	if folder == nil {
		log.Printf("GoCore: ShareFolder error: folder not found: %s", folderID)
		return fmt.Errorf("folder not found: %s", folderID)
	}
	if err := folder.ShareWithDevice(deviceID, true, ""); err != nil {
		log.Printf("GoCore: ShareFolder error: %v", err)
		return err
	}
	log.Printf("GoCore: ShareFolder succeeded for folder=%s device=%s", folderID, deviceID)
	return nil
}

// SetDiscoveryServers implements the serverConfigurer interface used by
// configureServers via type assertion.
func (a *sushitrainAdapter) SetDiscoveryServers(urls []string) error {
	return a.st.SetDiscoveryAddresses(sushitrain.List(urls))
}

// SetRelayServers implements the serverConfigurer interface used by
// configureServers via type assertion.
func (a *sushitrainAdapter) SetRelayServers(urls []string) error {
	return a.st.SetRelayAddresses(sushitrain.List(urls))
}

func (a *sushitrainAdapter) RescanFolder(folderID string) error {
	folder := a.st.FolderWithID(folderID)
	if folder == nil {
		return fmt.Errorf("folder not found: %s", folderID)
	}
	return folder.Rescan()
}

func (a *sushitrainAdapter) SetFolderRescanInterval(folderID string, seconds int) error {
	folder := a.st.FolderWithID(folderID)
	if folder == nil {
		return fmt.Errorf("folder not found: %s", folderID)
	}
	return folder.SetRescanInterval(seconds)
}

// PendingFolderIDs returns all folder IDs that connected peers are offering
// but we have not yet registered locally.
func (a *sushitrainAdapter) PendingFolderIDs() ([]string, error) {
	ids, err := a.st.PendingFolderIDs()
	if err != nil {
		return nil, err
	}
	result := make([]string, ids.Count())
	for i := 0; i < ids.Count(); i++ {
		result[i] = ids.ItemAt(i)
	}
	return result, nil
}

// DevicesPendingFolder returns the device IDs that are offering a given folder.
func (a *sushitrainAdapter) DevicesPendingFolder(folderID string) ([]string, error) {
	devs, err := a.st.DevicesPendingFolder(folderID)
	if err != nil {
		return nil, err
	}
	result := make([]string, devs.Count())
	for i := 0; i < devs.Count(); i++ {
		result[i] = devs.ItemAt(i)
	}
	return result, nil
}

// expectedFolderPath derives the correct folder path from a folder ID and the
// current dataDir. The folder path basename matches the folder ID:
//
//	meta-{eventID}                 → {dataDir}/meta-{eventID}
//	photos-{eventID}-{userID}      → {dataDir}/photos-{eventID}-{userID}
//
// Returns "" if the folder ID doesn't match a known pattern.
func expectedFolderPath(folderID, dataDir string) string {
	switch {
	case strings.HasPrefix(folderID, "meta-"):
		return filepath.Join(dataDir, folderID)
	case strings.HasPrefix(folderID, "photos-"):
		// Validate format: photos-{eventID}-{userID}; at least two segments after "photos-".
		rest := strings.TrimPrefix(folderID, "photos-")
		if idx := strings.LastIndex(rest, "-"); idx > 0 && idx < len(rest)-1 {
			return filepath.Join(dataDir, folderID)
		}
		return ""
	default:
		return ""
	}
}

// migrateFolderPaths updates stale folder paths in Syncthing config to match
// the current dataDir. This handles iOS container UUID changes without losing
// folder configuration (peers, shares, sync state).
// Folders with unrecognised IDs are removed.
func migrateFolderPaths(st *sushitrain.Client, dataDir string) {
	folders := st.Folders()
	if folders == nil {
		return
	}
	for i := 0; i < folders.Count(); i++ {
		folderID := folders.ItemAt(i)
		folder := st.FolderWithID(folderID)
		if folder == nil {
			continue
		}

		expected := expectedFolderPath(folderID, dataDir)
		if expected == "" {
			// Unrecognised folder ID — leftover junk, remove it.
			log.Printf("migrateFolderPaths: removing unknown folder %s", folderID)
			if err := folder.Unlink(); err != nil {
				log.Printf("migrateFolderPaths: unlink %s: %v", folderID, err)
			}
			continue
		}

		current := folder.Path()
		if current != expected {
			log.Printf("migrateFolderPaths: updating %s path %q → %q", folderID, current, expected)
			if err := folder.SetPath(expected); err != nil {
				log.Printf("migrateFolderPaths: SetPath %s: %v", folderID, err)
				continue
			}
		}

		// Ensure the directory and .stfolder marker exist.
		if err := os.MkdirAll(expected, 0700); err != nil {
			log.Printf("migrateFolderPaths: mkdir %s: %v", expected, err)
		}
		if err := os.MkdirAll(filepath.Join(expected, ".stfolder"), 0755); err != nil {
			log.Printf("migrateFolderPaths: .stfolder %s: %v", expected, err)
		}
	}
}

// StartSyncthing creates a Sushitrain client inside this Go runtime,
// loads and starts it, then wires it into the Weekendr client via
// SetSyncthing. This avoids spawning a second Go runtime from Swift.
func (c *Client) StartSyncthing(dataDir string) error {
	log.Printf("GoCore: WeekendrCore version %s starting", Version)
	configDir := filepath.Join(dataDir, "syncthing", "config")

	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("creating sushitrain config dir: %w", err)
	}

	// Delete stale config whose folder paths reference an old iOS container
	// UUID. This forces Sushitrain to generate a fresh config that uses the
	// current dataDir. On normal launches the dataDir is present in the
	// config so nothing is deleted.
	configFile := filepath.Join(configDir, "config.xml")
	if data, err := os.ReadFile(configFile); err == nil {
		if !strings.Contains(string(data), dataDir) {
			os.Remove(configFile)
			log.Printf("GoCore: removed stale Sushitrain config (container UUID changed)")
			// Re-create config dir in case the removal left it missing
			os.MkdirAll(configDir, 0700)
		}
	}

	st := sushitrain.NewClient(configDir, dataDir, false)
	if st == nil {
		return fmt.Errorf("SushitrainNewClient returned nil")
	}

	if err := st.Load(false); err != nil {
		return fmt.Errorf("sushitrain Load: %w", err)
	}

	// Fix folder paths that point to old iOS container UUIDs.
	// Updates paths in-place so peer/share config is preserved.
	migrateFolderPaths(st, dataDir)

	// Force relay-only connections. Direct TCP/QUIC connections fail on iOS
	// because the OS aggressively closes incoming TLS handshakes ("broken
	// pipe"). By omitting tcp:// and quic:// listen addresses, all traffic
	// goes through the private relay server. configureServers below appends
	// the relay address to the listen list via SetRelayAddresses.
	if err := st.SetListenAddresses(sushitrain.List([]string{})); err != nil {
		return fmt.Errorf("setting listen addresses: %w", err)
	}

	// Configure private servers BEFORE Start() so Syncthing never
	// attempts to connect to public relay/discovery pools.
	adapter := &sushitrainAdapter{st: st}
	configureServers(adapter)

	// Enable relays so Syncthing can fall back to our private relay
	// (relay.getweekendr.app) when direct connections fail. Public
	// relay pools are not contacted because configureServers above
	// replaced the relay list with only our private server.
	if err := st.SetRelaysEnabled(true); err != nil {
		return fmt.Errorf("enabling relays: %w", err)
	}

	// Shorter reconnect interval (default 60s is too long for mobile).
	// This makes Syncthing retry relay connections every 10s after a
	// disconnect, improving recovery from iOS background suspensions.
	if err := st.SetReconnectIntervalS(10); err != nil {
		return fmt.Errorf("setting reconnect interval: %w", err)
	}

	if err := st.Start(); err != nil {
		return fmt.Errorf("sushitrain Start: %w", err)
	}

	// Read the real device ID from Sushitrain's TLS certificate.
	// This is the authoritative identity — the cert persists across launches
	// at {dataDir}/syncthing/config/cert.pem.
	c.deviceID = st.DeviceID()
	log.Printf("GoCore: Sushitrain started — device ID: %s", c.deviceID)

	c.syncthing = adapter

	// Now that c.deviceID and c.syncthing are set, register event folders
	// with correct IDs. createEventFolders ran before StartSyncthing so it
	// could only create OS directories — folder IDs had an empty device ID.
	// On app restart c.activeEventID is empty, so we also load persisted
	// event IDs from disk to re-register all previously joined events.
	//
	// Only register events whose meta-folder directory exists on disk.
	// This avoids registering stale events that were removed server-side
	// but left in events.json. The Swift layer can call SetActiveEventIDs()
	// before StartSyncthing to prune the list using the API.
	registered := map[string]bool{}
	if c.activeEventID != "" {
		if err := c.ensureFoldersRegistered(c.activeEventID); err != nil {
			return fmt.Errorf("registering event folders after Syncthing start: %w", err)
		}
		registered[c.activeEventID] = true
	}
	for _, eventID := range loadPersistedEventIDs(c.dataDir) {
		if registered[eventID] {
			continue
		}
		metaDir := filepath.Join(c.dataDir, "meta-"+strings.ToLower(eventID))
		if _, statErr := os.Stat(metaDir); statErr != nil {
			log.Printf("GoCore: skipping stale event %s (meta-folder %s not found)", eventID, metaDir)
			continue
		}
		if err := c.ensureFoldersRegistered(eventID); err != nil {
			log.Printf("GoCore: ensureFoldersRegistered(%s): %v", eventID, err)
		}
	}

	// Remove Syncthing folders for events no longer in events.json or
	// whose meta-folder has been deleted.
	c.cleanupStaleFolders()

	// Signal that Syncthing is ready for use.
	select {
	case <-c.syncthingReady:
		// already closed
	default:
		close(c.syncthingReady)
	}

	// Accept pending folders after a delay to give peers time to connect via
	// relay and announce their folders after Syncthing starts.
	go func() {
		log.Printf("GoCore: acceptPendingFolders goroutine started, waiting 30s...")
		time.Sleep(30 * time.Second)
		c.acceptPendingFolders()
	}()

	// Persistent connection monitor — logs peer and folder status every 5s.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			var connectedPeers []string
			peers := st.Peers()
			if peers != nil {
				for j := 0; j < peers.Count(); j++ {
					peerID := peers.ItemAt(j)
					if peerID == st.DeviceID() {
						continue
					}
					peer := st.PeerWithID(peerID)
					if peer != nil && peer.IsConnected() {
						connectedPeers = append(connectedPeers, peerID)
					}
				}
			}
			log.Printf("GoCore: [status] connected peers: %v", connectedPeers)

			var folderStates []string
			folders := st.Folders()
			if folders != nil {
				for j := 0; j < folders.Count(); j++ {
					folderID := folders.ItemAt(j)
					folder := st.FolderWithID(folderID)
					if folder == nil {
						continue
					}
					state, err := folder.State()
					if err != nil {
						folderStates = append(folderStates, fmt.Sprintf("%s=error:%v", folderID, err))
					} else {
						folderStates = append(folderStates, fmt.Sprintf("%s=%s", folderID, state))
					}
				}
			}
			log.Printf("GoCore: [status] folder states: %v", folderStates)

			// Auto-accept folders offered by peers since last check.
			log.Printf("GoCore: [ticker] checking pending folders...")
			c.acceptPendingFolders()
		}
	}()

	return nil
}

// GetConnectionStatus returns a JSON string describing this device's P2P
// connection state: our device ID, which peers are connected, and the sync
// status of every folder that matches the given eventID.
func (c *Client) GetConnectionStatus(eventID string) string {
	type status struct {
		DeviceID       string            `json:"deviceID"`
		ConnectedPeers []string          `json:"connectedPeers"`
		FolderStatus   map[string]string `json:"folderStatus"`
	}

	s := status{
		DeviceID:       c.deviceID,
		ConnectedPeers: []string{},
		FolderStatus:   map[string]string{},
	}

	adapter, ok := c.syncthing.(*sushitrainAdapter)
	if !ok || adapter == nil || adapter.st == nil {
		b, _ := json.Marshal(s)
		return string(b)
	}

	st := adapter.st

	// Collect connected peers.
	peers := st.Peers()
	if peers != nil {
		for i := 0; i < peers.Count(); i++ {
			peerID := peers.ItemAt(i)
			if peerID == st.DeviceID() {
				continue
			}
			peer := st.PeerWithID(peerID)
			if peer != nil && peer.IsConnected() {
				s.ConnectedPeers = append(s.ConnectedPeers, peerID)
			}
		}
	}

	// Collect folder states for folders matching the eventID.
	folders := st.Folders()
	if folders != nil {
		for i := 0; i < folders.Count(); i++ {
			folderID := folders.ItemAt(i)
			if eventID != "" && !strings.Contains(folderID, eventID) {
				continue
			}
			folder := st.FolderWithID(folderID)
			if folder == nil {
				continue
			}
			state, err := folder.State()
			if err != nil {
				s.FolderStatus[folderID] = "error: " + err.Error()
			} else {
				s.FolderStatus[folderID] = state
			}
		}
	}

	b, _ := json.Marshal(s)
	return string(b)
}

// acceptPendingFolders checks for folders that connected peers are offering and
// automatically accepts them. Photo folders (photos-{eventID}-{userID}) are
// registered as ReceiveOnly; meta folders (meta-{eventID}) as SendReceive.
// This handles the case where a remote device announces folders before we've
// registered them (e.g. the host announces its photo folder to a joiner that
// hasn't run BootstrapConnection yet, or ran it before Syncthing was ready).
func (c *Client) acceptPendingFolders() {
	adapter, ok := c.syncthing.(*sushitrainAdapter)
	if !ok || adapter == nil {
		return
	}

	pendingIDs, err := adapter.PendingFolderIDs()
	if err != nil {
		log.Printf("GoCore: acceptPendingFolders error: %v", err)
		return
	}
	log.Printf("GoCore: acceptPendingFolders — found %d pending folders: %v", len(pendingIDs), pendingIDs)
	if len(pendingIDs) == 0 {
		log.Printf("GoCore: acceptPendingFolders — no pending folders")
		return
	}

	// Build set of known event IDs.
	knownEvents := make(map[string]bool)
	if c.activeEventID != "" {
		knownEvents[strings.ToLower(c.activeEventID)] = true
	}
	for _, eid := range loadPersistedEventIDs(c.dataDir) {
		knownEvents[strings.ToLower(eid)] = true
	}

	for _, folderID := range pendingIDs {
		var folderType string // "receiveonly" or "sendreceive"
		var eventID string

		switch {
		case strings.HasPrefix(folderID, "photos-"):
			// Parse photos-{eventID}-{userID}; find the last dash to split.
			rest := strings.TrimPrefix(folderID, "photos-")
			idx := strings.LastIndex(rest, "-")
			if idx <= 0 || idx >= len(rest)-1 {
				continue
			}
			eventID = rest[:idx]
			folderType = "receiveonly"

		case strings.HasPrefix(folderID, "meta-"):
			eventID = strings.TrimPrefix(folderID, "meta-")
			folderType = "sendreceive"

		default:
			continue
		}

		if !knownEvents[eventID] {
			continue
		}

		// Determine the local path for this folder.
		folderPath := expectedFolderPath(folderID, c.dataDir)
		if folderPath == "" {
			continue
		}
		log.Printf("GoCore: acceptPendingFolders: folderID=%s → path=%s", folderID, folderPath)

		// Find which devices are offering this folder.
		devices, err := adapter.DevicesPendingFolder(folderID)
		if err != nil {
			log.Printf("GoCore: acceptPendingFolders: DevicesPendingFolder(%s): %v", folderID, err)
			continue
		}

		log.Printf("GoCore: acceptPendingFolders: accepting %s as %s (offered by %v)", folderID, folderType, devices)

		// Create OS directory and .stfolder marker.
		if err := os.MkdirAll(folderPath, 0755); err != nil {
			log.Printf("GoCore: acceptPendingFolders: mkdir FAILED %s: %v", folderPath, err)
		} else {
			log.Printf("GoCore: acceptPendingFolders: mkdir OK %s", folderPath)
		}
		if err := os.MkdirAll(folderPath+"/.stfolder", 0755); err != nil {
			log.Printf("GoCore: acceptPendingFolders: .stfolder FAILED %s: %v", folderPath, err)
		} else {
			log.Printf("GoCore: acceptPendingFolders: .stfolder OK %s", folderPath)
		}

		// Register the folder with the appropriate type.
		if err := c.syncthing.AddFolder(folderID, folderPath, folderType); err != nil {
			log.Printf("GoCore: acceptPendingFolders: AddFolder(%s): %v", folderID, err)
			continue
		}

		// Share the folder with each offering device so Syncthing syncs.
		for _, devID := range devices {
			log.Printf("GoCore: acceptPendingFolders — accepting folder %s from device %s",
				folderID, devID)
			if err := c.syncthing.ShareFolder(folderID, devID); err != nil {
				log.Printf("GoCore: acceptPendingFolders: ShareFolder(%s, %s): %v", folderID, devID, err)
			}
		}
	}
}
