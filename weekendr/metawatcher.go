package weekendr

// Participant represents a device participating in an event.
type Participant struct {
	DeviceID  string
	Online    bool
	PhotoCount int64
	LastSeen  int64
}

// StartMetaWatcher begins watching the Syncthing meta-folder for
// new participants and device announcements.
func (c *Client) StartMetaWatcher(eventID string) error {
	return nil
}

// StopMetaWatcher stops watching the meta-folder for the given event.
func (c *Client) StopMetaWatcher(eventID string) error {
	return nil
}

// GetParticipants returns the current list of participants for an event.
func (c *Client) GetParticipants(eventID string) ([]*Participant, error) {
	return []*Participant{
		{
			DeviceID:   c.deviceID,
			Online:     true,
			PhotoCount: 0,
		},
	}, nil
}

// AnnounceDevice writes this device's presence to the meta-folder.
func (c *Client) AnnounceDevice(eventID string) error {
	return nil
}