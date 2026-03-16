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

// Client is the main entry point for the Weekendr Go core.
// All state is held here and accessed via methods.
type Client struct {
	deviceID string
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
	return &Client{deviceID: id}, nil
}

// DeviceID returns this device's Syncthing device ID.
func (c *Client) DeviceID() string {
	return c.deviceID
}
