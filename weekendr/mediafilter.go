cat > ~/src/weekendr-core/weekendr/mediafilter.go << 'EOF'
package weekendr

// MediaItem represents a photo or video in the event album.
type MediaItem struct {
	ID        string
	DeviceID  string
	Filename  string
	Timestamp int64
	IsVideo   bool
	LocalPath string
}

// StartMediaSync begins syncing photos from the device camera roll
// into the participant's Syncthing folder for the given event.
func (c *Client) StartMediaSync(eventID string) error {
	return nil
}

// StopMediaSync stops syncing media for the given event.
func (c *Client) StopMediaSync(eventID string) error {
	return nil
}

// GetMediaItems returns all media items currently synced for an event.
func (c *Client) GetMediaItems(eventID string) ([]*MediaItem, error) {
	return []*MediaItem{}, nil
}

// RemoveMediaItem removes a media item from the participant's folder,
// effectively unsharing it from the event.
func (c *Client) RemoveMediaItem(eventID string, itemID string) error {
	return nil
}
EOF