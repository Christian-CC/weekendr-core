package weekendr

import (
	"fmt"
	"os"
	"path/filepath"

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

// StartSyncthing creates a Sushitrain client inside this Go runtime,
// loads and starts it, then wires it into the Weekendr client via
// SetSyncthing. This avoids spawning a second Go runtime from Swift.
func (c *Client) StartSyncthing(dataDir string) error {
	configDir := filepath.Join(dataDir, "SushitrainConfig")
	filesDir := filepath.Join(dataDir, "SushitrainFiles")

	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("creating sushitrain config dir: %w", err)
	}
	if err := os.MkdirAll(filesDir, 0700); err != nil {
		return fmt.Errorf("creating sushitrain files dir: %w", err)
	}

	st := sushitrain.NewClient(configDir, filesDir, false)
	if st == nil {
		return fmt.Errorf("SushitrainNewClient returned nil")
	}

	if err := st.Load(false); err != nil {
		return fmt.Errorf("sushitrain Load: %w", err)
	}

	// Configure private servers BEFORE Start() so Syncthing never
	// attempts to connect to public relay/discovery pools.
	adapter := &sushitrainAdapter{st: st}
	configureServers(adapter)

	if err := st.Start(); err != nil {
		return fmt.Errorf("sushitrain Start: %w", err)
	}

	c.syncthing = adapter
	return nil
}
