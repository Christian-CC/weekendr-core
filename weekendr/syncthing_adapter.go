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
	return a.st.AddPeer(deviceID)
}

func (a *sushitrainAdapter) ShareFolder(folderID, deviceID string) error {
	folder := a.st.FolderWithID(folderID)
	if folder == nil {
		return fmt.Errorf("folder not found: %s", folderID)
	}
	return folder.ShareWithDevice(deviceID, true, "")
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

// expectedFolderPath derives the correct folder path from a folder ID and the
// current dataDir using the naming convention:
//
//	meta-{eventID}                 → {dataDir}/{eventID}-meta
//	photos-{eventID}-{deviceID}    → {dataDir}/{eventID}-{deviceID}-photos
//
// Returns "" if the folder ID doesn't match a known pattern.
func expectedFolderPath(folderID, dataDir string) string {
	switch {
	case strings.HasPrefix(folderID, "meta-"):
		eventID := strings.TrimPrefix(folderID, "meta-")
		return filepath.Join(dataDir, eventID+"-meta")
	case strings.HasPrefix(folderID, "photos-"):
		// photos-{eventID}-{deviceID} — deviceID is always the last 63 chars
		rest := strings.TrimPrefix(folderID, "photos-")
		// Device IDs are 63 chars (8×7 + 7 hyphens). Split from the right.
		if len(rest) > 64 && rest[len(rest)-64] == '-' {
			eventID := rest[:len(rest)-64]
			deviceID := rest[len(rest)-63:]
			return filepath.Join(dataDir, eventID+"-"+deviceID+"-photos")
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

	// Configure private servers BEFORE Start() so Syncthing never
	// attempts to connect to public relay/discovery pools.
	adapter := &sushitrainAdapter{st: st}
	configureServers(adapter)

	// Disable the dynamic relay client entirely. Without this,
	// Syncthing spawns dynamicClient.serve() which does HTTP lookups
	// against public relay pools and crashes on iOS.
	if err := st.SetRelaysEnabled(false); err != nil {
		return fmt.Errorf("disabling dynamic relays: %w", err)
	}

	if err := st.Start(); err != nil {
		return fmt.Errorf("sushitrain Start: %w", err)
	}

	c.syncthing = adapter

	// Diagnostic: log connection status every 10s for the first 2 minutes.
	go func() {
		for i := 0; i < 12; i++ {
			time.Sleep(10 * time.Second)

			log.Printf("=== Weekendr Syncthing Status [%d/12] ===", i+1)
			log.Printf("  Our Device ID: %s", st.DeviceID())
			log.Printf("  Connected peers: %d", st.ConnectedPeerCount())

			peers := st.Peers()
			if peers != nil {
				for j := 0; j < peers.Count(); j++ {
					peerID := peers.ItemAt(j)
					if peerID == st.DeviceID() {
						continue
					}
					peer := st.PeerWithID(peerID)
					if peer != nil {
						log.Printf("  Peer %s connected=%v", peerID, peer.IsConnected())
					}
				}
			}

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
						log.Printf("  Folder %s state=error: %v", folderID, err)
					} else {
						log.Printf("  Folder %s state=%s", folderID, state)
					}
				}
			}

			log.Printf("========================================")
		}
		log.Printf("Weekendr: diagnostic polling complete")
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
