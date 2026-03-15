package weekendr

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