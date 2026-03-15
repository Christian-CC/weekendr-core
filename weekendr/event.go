cat > ~/src/weekendr-core/weekendr/event.go << 'EOF'
package weekendr

// EventMode represents whether an event is live or retrospective.
type EventMode string

// EventState represents the current lifecycle state of an event.
type EventState string

// Event holds the metadata for a Weekendr event.
type Event struct {
	ID          string
	Name        string
	HostDeviceID string
	Mode        string
	State       string
	StartsAt    int64
	EndsAt      int64
}

// CreateEventParams holds the parameters for creating a new event.
type CreateEventParams struct {
	Name             string
	Mode             string
	StartsAt         int64
	EndsAt           int64
	LocationWeather  bool
	CollectionWindow int64
}

// CreateEvent creates a new event on the server and returns the event ID and invite secret.
func (c *Client) CreateEvent(params *CreateEventParams) (*Event, error) {
	return &Event{
		ID:    "stub-event-id",
		Name:  params.Name,
		Mode:  params.Mode,
		State: "upcoming",
	}, nil
}

// JoinEvent joins an existing event using an invite secret.
func (c *Client) JoinEvent(inviteSecret string) (*Event, error) {
	return &Event{
		ID:    "stub-event-id",
		Name:  "Stub Event",
		State: "active",
	}, nil
}

// GetEvent returns the current event metadata.
func (c *Client) GetEvent(eventID string) (*Event, error) {
	return &Event{
		ID:    eventID,
		Name:  "Stub Event",
		State: "active",
	}, nil
}
EOF