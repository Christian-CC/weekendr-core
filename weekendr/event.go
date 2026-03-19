package weekendr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EventMode represents whether an event is live or retrospective.
type EventMode string

// EventState represents the current lifecycle state of an event.
type EventState string

// Event holds the metadata for a Weekendr event.
type Event struct {
	ID            string
	Name          string
	HostDeviceID  string
	Mode          string
	State         string
	StartsAt      int64
	EndsAt        int64
	PhotoFolderID string // "photos-{eventID}-{deviceID}"
	MetaFolderID  string // "meta-{eventID}"
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

// createEventFolders creates the Syncthing folders for an event on this device:
//   - A SendOnly photo folder for this device's own uploads
//   - A SendReceive meta folder shared by all participants
func createEventFolders(c *Client, event *Event) error {
	deviceIDLower := strings.ToLower(c.deviceID)
	eventIDLower := strings.ToLower(event.ID)

	event.PhotoFolderID = fmt.Sprintf("photos-%s-%s", eventIDLower, deviceIDLower)
	event.MetaFolderID = fmt.Sprintf("meta-%s", eventIDLower)

	photoPath := filepath.Join(c.dataDir, "events", event.ID, "photos", c.deviceID)
	if err := os.MkdirAll(photoPath, 0700); err != nil {
		return fmt.Errorf("creating photo folder: %w", err)
	}

	metaPath := filepath.Join(c.dataDir, "events", event.ID, "meta")
	if err := os.MkdirAll(metaPath, 0700); err != nil {
		return fmt.Errorf("creating meta folder: %w", err)
	}

	return nil
}

// CreateEvent creates a new event on the server and returns the event ID and invite secret.
func (c *Client) CreateEvent(params *CreateEventParams) (*Event, error) {
	event := &Event{
		ID:    "stub-event-id",
		Name:  params.Name,
		Mode:  params.Mode,
		State: "upcoming",
	}
	if err := createEventFolders(c, event); err != nil {
		return nil, err
	}
	return event, nil
}

// JoinEvent joins an existing event using an invite secret.
func (c *Client) JoinEvent(inviteSecret string) (*Event, error) {
	event := &Event{
		ID:    "stub-event-id",
		Name:  "Stub Event",
		State: "active",
	}
	if err := createEventFolders(c, event); err != nil {
		return nil, err
	}
	return event, nil
}

// GetEvent returns the current event metadata.
func (c *Client) GetEvent(eventID string) (*Event, error) {
	return &Event{
		ID:    eventID,
		Name:  "Stub Event",
		State: "active",
	}, nil
}

// addParticipantPhotoFolder creates a ReceiveOnly folder for a discovered participant's photos.
// It is called internally by the MetaWatcher when a new device is discovered.
func (c *Client) addParticipantPhotoFolder(eventID, participantDeviceID string) error {
	participantPath := filepath.Join(c.dataDir, "events", eventID, "photos", participantDeviceID)
	if err := os.MkdirAll(participantPath, 0700); err != nil {
		return fmt.Errorf("creating participant photo folder: %w", err)
	}
	return nil
}
