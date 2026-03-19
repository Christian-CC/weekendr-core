// weekendr/weekendr.go
package weekendr

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SyncthingClient is the subset of the Syncthing API required by Weekendr.
//
// In production this interface is satisfied by an adapter around
// *sushitrain.Client (SushitrainCore module). The adapter maps:
//
//	AddFolder          → sushitrain.Client.AddSpecialFolder(id, "basic", path, type)
//	AddPeer            → sushitrain.Client.AddPeer(deviceID)
//	ShareFolder        → sushitrain.Client.FolderWithID(id).ShareWithDevice(deviceID, true, "")
//	SetDiscoveryServers → sushitrain.Client.SetDiscoveryAddresses(List(urls))
//	SetRelayServers    → sushitrain.Client.SetRelayAddresses(List(urls))
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

	// SetDiscoveryServers replaces the global discovery server list with the
	// given URLs. Pass a single private URL to avoid the public Syncthing pool.
	SetDiscoveryServers(urls []string) error

	// SetRelayServers replaces the relay server list with the given URLs.
	// Pass a single private relay URL to avoid the public Syncthing relay pool.
	SetRelayServers(urls []string) error
}

// Client is the main entry point for the Weekendr Go core.
// All state is held here and accessed via methods.
type Client struct {
	deviceID  string
	dataDir   string
	watchers  map[string]chan struct{}
	syncthing SyncthingClient // nil until SetSyncthing is called
}

const deviceIDFile = "device_id.json"

type deviceIDStore struct {
	DeviceID string `json:"device_id"`
}

// loadOrCreateDeviceID reads the persisted device ID from dataDir, or generates
// and persists a new one if none exists.
func loadOrCreateDeviceID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, deviceIDFile)

	data, err := os.ReadFile(path)
	if err == nil {
		var store deviceIDStore
		if json.Unmarshal(data, &store) == nil && store.DeviceID != "" {
			return store.DeviceID, nil
		}
	}

	id, err := generateDeviceID()
	if err != nil {
		return "", fmt.Errorf("generating device ID: %w", err)
	}

	store := deviceIDStore{DeviceID: id}
	data, err = json.Marshal(store)
	if err != nil {
		return "", fmt.Errorf("marshaling device ID: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return "", fmt.Errorf("creating data dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("writing device ID: %w", err)
	}

	return id, nil
}

// generateDeviceID creates a random Syncthing-format device ID.
// 35 random bytes → base32 (no padding) → 56 chars → 8 groups of 7, hyphen-separated.
func generateDeviceID() (string, error) {
	b := make([]byte, 35)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)

	var parts [8]string
	for i := range parts {
		parts[i] = enc[i*7 : (i+1)*7]
	}
	return strings.Join(parts[:], "-"), nil
}

// NewClient initialises the Weekendr core.
func NewClient(dataDir string) (*Client, error) {
	id, err := loadOrCreateDeviceID(dataDir)
	if err != nil {
		return nil, err
	}
	return &Client{deviceID: id, dataDir: dataDir, watchers: make(map[string]chan struct{})}, nil
}

// SetSyncthing wires a live Syncthing back-end into the client and immediately
// configures it to use Weekendr's private discovery and relay servers exclusively.
// Call this before CreateEvent, JoinEvent, or StartMetaWatcher to enable P2P sync.
// The concrete value must be an adapter around *sushitrain.Client.
func (c *Client) SetSyncthing(s SyncthingClient) {
	c.syncthing = s
	_ = s.SetDiscoveryServers([]string{"https://discovery.getweekendr.app"})
	_ = s.SetRelayServers([]string{"relay://relay.getweekendr.app:22067"})
}

// DeviceID returns this device's Syncthing device ID.
func (c *Client) DeviceID() string {
	return c.deviceID
}
