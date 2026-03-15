// weekendr/weekendr.go
package weekendr

// Client is the main entry point for the Weekendr Go core.
// All state is held here and accessed via methods.
type Client struct {
    deviceID string
}

// NewClient initialises the Weekendr core.
func NewClient(dataDir string) (*Client, error) {
    return &Client{deviceID: "stub"}, nil
}

// DeviceID returns this device's Syncthing device ID.
func (c *Client) DeviceID() string {
    return c.deviceID
}