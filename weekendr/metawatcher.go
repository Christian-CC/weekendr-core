package weekendr

// ParticipantList wraps a slice of Participants for gomobile compatibility.
type ParticipantList struct {
	Items []*Participant
}

// Count returns the number of participants.
func (l *ParticipantList) Count() int { return len(l.Items) }

// Get returns the participant at index i.
func (l *ParticipantList) Get(i int) *Participant { return l.Items[i] }

// GetParticipants returns the current list of participants for an event.
func (c *Client) GetParticipants(eventID string) (*ParticipantList, error) {
	return &ParticipantList{
		Items: []*Participant{
			{DeviceID: c.deviceID, Online: true, PhotoCount: 0},
		},
	}, nil
}