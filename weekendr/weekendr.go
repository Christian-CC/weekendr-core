// weekendr/weekendr.go
package weekendr

import (
	"fmt"
	"os"
	"time"
)

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
}

// Client is the main entry point for the Weekendr Go core.
// All state is held here and accessed via methods.
type Client struct {
	deviceID       string
	dataDir        string
	activeEventID  string // set by CreateEvent/JoinEvent; used by StartSyncthing to register folders
	watchers       map[string]chan struct{}
	syncthing      SyncthingClient // nil until SetSyncthing is called
	syncthingReady chan struct{}
}

// NewClient initialises the Weekendr core.
// The deviceID is empty until StartSyncthing sets it from Sushitrain's TLS cert.
func NewClient(dataDir string) (*Client, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}
	c := &Client{
		dataDir:        dataDir,
		watchers:       make(map[string]chan struct{}),
		syncthingReady: make(chan struct{}),
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
		_ = sc.SetRelayServers([]string{"relay://relay.getweekendr.app:22067"})
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
