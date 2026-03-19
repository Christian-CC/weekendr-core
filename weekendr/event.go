package weekendr

import (
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// generateEventID creates a URL-safe, lowercase random event ID.
func generateEventID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("evt-%x", b)
}

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
	EventID          string // server-assigned ID; if empty, one is generated locally
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
//
// It first creates the OS directories, then (when c.syncthing != nil) registers
// both folders with Syncthing so that P2P sync can take place.
func createEventFolders(c *Client, event *Event) error {
	eventIDLower := strings.ToLower(event.ID)
	deviceIDLower := strings.ToLower(c.deviceID)

	event.PhotoFolderID = "photos-" + eventIDLower + "-" + deviceIDLower
	event.MetaFolderID = "meta-" + eventIDLower

	photoPath := filepath.Join(c.dataDir, event.ID+"-"+c.deviceID+"-photos")
	if err := os.MkdirAll(photoPath, 0700); err != nil {
		return fmt.Errorf("creating photo folder: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(photoPath, ".stfolder"), 0755); err != nil {
		return fmt.Errorf("creating photo .stfolder marker: %w", err)
	}

	metaPath := filepath.Join(c.dataDir, event.ID+"-meta")
	if err := os.MkdirAll(metaPath, 0700); err != nil {
		return fmt.Errorf("creating meta folder: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(metaPath, ".stfolder"), 0755); err != nil {
		return fmt.Errorf("creating meta .stfolder marker: %w", err)
	}

	if c.syncthing != nil {
		// Register this device's SendOnly photo folder with Syncthing.
		// Other participants will add a ReceiveOnly mirror when they discover us.
		if err := c.syncthing.AddFolder(event.PhotoFolderID, photoPath, "sendonly"); err != nil {
			return fmt.Errorf("registering photo folder with Syncthing: %w", err)
		}

		// Register the shared SendReceive meta folder. All participants sync
		// device announcements and event metadata through this folder.
		if err := c.syncthing.AddFolder(event.MetaFolderID, metaPath, "sendreceive"); err != nil {
			return fmt.Errorf("registering meta folder with Syncthing: %w", err)
		}
	}

	return nil
}

// CreateEvent creates a new event on the server and returns the event ID and invite secret.
func (c *Client) CreateEvent(params *CreateEventParams) (*Event, error) {
	eventID := params.EventID
	if eventID == "" {
		eventID = generateEventID()
	}
	event := &Event{
		ID:    eventID,
		Name:  params.Name,
		Mode:  params.Mode,
		State: "upcoming",
	}
	if err := createEventFolders(c, event); err != nil {
		return nil, err
	}
	return event, nil
}

// JoinEvent joins an existing event using an invite secret and the resolved event ID.
func (c *Client) JoinEvent(inviteSecret string, eventID string) (*Event, error) {
	event := &Event{
		ID:    eventID,
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

// BootstrapConnection connects a joining device to the event host by adding
// the host as a Syncthing peer and sharing the meta and photo folders with it.
// Errors are logged but not returned — bootstrapping is best-effort so that a
// transient Syncthing issue does not block the join flow.
func (c *Client) BootstrapConnection(eventID, hostDeviceID string) error {
	log.Printf("GoCore: BootstrapConnection called — eventID=%s hostDeviceID=%s", eventID, hostDeviceID)

	if c.syncthing == nil {
		log.Printf("GoCore: BootstrapConnection — syncthing is nil, skipping")
		return nil
	}

	// 1. Add host as peer.
	log.Printf("GoCore: BootstrapConnection — adding peer %s", hostDeviceID)
	if err := c.syncthing.AddPeer(hostDeviceID); err != nil {
		log.Printf("GoCore: BootstrapConnection AddPeer error: %v", err)
	}

	// 2. Share meta folder with host.
	log.Printf("GoCore: BootstrapConnection — sharing meta folder")
	if err := c.syncthing.ShareFolder("meta-"+eventID, hostDeviceID); err != nil {
		log.Printf("GoCore: BootstrapConnection ShareFolder meta error: %v", err)
	}

	// 3. Share our photo folder with host.
	log.Printf("GoCore: BootstrapConnection — sharing photo folder")
	photoFolderID := "photos-" + eventID + "-" + strings.ToLower(c.deviceID)
	if err := c.syncthing.ShareFolder(photoFolderID, hostDeviceID); err != nil {
		log.Printf("GoCore: BootstrapConnection ShareFolder photos error: %v", err)
	}

	log.Printf("GoCore: BootstrapConnection — done")
	return nil
}

// addParticipantPhotoFolder sets up everything needed to receive photos from a
// newly discovered participant. It is called by the MetaWatcher goroutine.
//
// OS side: creates the local directory for the participant's photos.
//
// Syncthing side (when c.syncthing != nil):
//  1. Registers the remote device with Syncthing (AddPeer).
//  2. Registers a ReceiveOnly folder for the participant's photos.
//  3. Shares the meta folder with the new participant so both devices sync
//     event metadata bidirectionally.
//  4. Shares our own SendOnly photo folder with the participant so they can
//     pull our photos.
func (c *Client) addParticipantPhotoFolder(eventID, participantDeviceID string) error {
	participantPath := filepath.Join(c.dataDir, eventID+"-"+participantDeviceID+"-photos")
	if err := os.MkdirAll(participantPath, 0700); err != nil {
		return fmt.Errorf("creating participant photo folder: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(participantPath, ".stfolder"), 0755); err != nil {
		return fmt.Errorf("creating participant .stfolder marker: %w", err)
	}

	if c.syncthing == nil {
		return nil
	}

	eventIDLower := strings.ToLower(eventID)
	participantIDLower := strings.ToLower(participantDeviceID)

	// 1. Add the remote device to Syncthing so we can connect to it.
	if err := c.syncthing.AddPeer(participantDeviceID); err != nil {
		return fmt.Errorf("adding participant device to Syncthing: %w", err)
	}

	// 2. Register a ReceiveOnly folder to pull the participant's photos.
	participantPhotoFolderID := "photos-" + eventIDLower + "-" + participantIDLower
	if err := c.syncthing.AddFolder(participantPhotoFolderID, participantPath, "receiveonly"); err != nil {
		return fmt.Errorf("registering participant photo folder with Syncthing: %w", err)
	}

	// 3. Share the meta folder with the new participant for bidirectional metadata sync.
	metaFolderID := "meta-" + eventIDLower
	if err := c.syncthing.ShareFolder(metaFolderID, participantDeviceID); err != nil {
		return fmt.Errorf("sharing meta folder with participant: %w", err)
	}

	// 4. Share our SendOnly photo folder with the participant so they can receive our photos.
	ourPhotoFolderID := "photos-" + eventIDLower + "-" + strings.ToLower(c.deviceID)
	if err := c.syncthing.ShareFolder(ourPhotoFolderID, participantDeviceID); err != nil {
		return fmt.Errorf("sharing our photo folder with participant: %w", err)
	}

	return nil
}
