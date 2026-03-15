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

// MediaItemList wraps a slice of MediaItems for gomobile compatibility.
type MediaItemList struct {
	Items []*MediaItem
}

// Count returns the number of media items.
func (l *MediaItemList) Count() int { return len(l.Items) }

// Get returns the media item at index i.
func (l *MediaItemList) Get(i int) *MediaItem { return l.Items[i] }

// GetMediaItems returns all media items currently synced for an event.
func (c *Client) GetMediaItems(eventID string) (*MediaItemList, error) {
	return &MediaItemList{Items: []*MediaItem{}}, nil
}

// StartMediaSync begins syncing photos from the device camera roll.
func (c *Client) StartMediaSync(eventID string) error {
	return nil
}

// StopMediaSync stops syncing medi