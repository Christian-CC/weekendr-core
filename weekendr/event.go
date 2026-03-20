package weekendr

import (
	"crypto/rand"
	"encoding/json"
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

// eventsFile is the JSON file that persists active event IDs across app restarts.
type eventsFile struct {
	EventIDs []string `json:"event_ids"`
}

// persistEventID appends eventID to {dataDir}/events.json, deduplicating.
func persistEventID(dataDir, eventID string) error {
	ids := loadPersistedEventIDs(dataDir)
	for _, id := range ids {
		if id == eventID {
			return nil // already persisted
		}
	}
	ids = append(ids, eventID)
	data, err := json.Marshal(eventsFile{EventIDs: ids})
	if err != nil {
		return fmt.Errorf("marshalling events.json: %w", err)
	}
	path := filepath.Join(dataDir, "events.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing events.json: %w", err)
	}
	return nil
}

// loadPersistedEventIDs returns all event IDs from {dataDir}/events.json.
// Returns nil if the file does not exist or cannot be parsed.
func loadPersistedEventIDs(dataDir string) []string {
	data, err := os.ReadFile(filepath.Join(dataDir, "events.json"))
	if err != nil {
		return nil
	}
	var f eventsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil
	}
	return f.EventIDs
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

	photoPath := filepath.Join(c.dataDir, eventIDLower+"-"+deviceIDLower+"-photos")
	if err := os.MkdirAll(photoPath, 0700); err != nil {
		return fmt.Errorf("creating photo folder: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(photoPath, ".stfolder"), 0755); err != nil {
		return fmt.Errorf("creating photo .stfolder marker: %w", err)
	}

	metaPath := filepath.Join(c.dataDir, eventIDLower+"-meta")
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
	c.activeEventID = eventID
	if err := persistEventID(c.dataDir, eventID); err != nil {
		log.Printf("GoCore: failed to persist event ID: %v", err)
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
	c.activeEventID = eventID
	if err := persistEventID(c.dataDir, eventID); err != nil {
		log.Printf("GoCore: failed to persist event ID: %v", err)
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

// ensureFoldersRegistered (re-)registers the meta and own photo folders with
// Syncthing using the correct IDs derived from c.deviceID. This must be called
// AFTER StartSyncthing sets c.deviceID, because createEventFolders runs before
// Syncthing is started and therefore constructs folder IDs with an empty device ID.
func (c *Client) ensureFoldersRegistered(eventID string) error {
	if c.syncthing == nil {
		return nil
	}

	eventIDLower := strings.ToLower(eventID)
	deviceIDLower := strings.ToLower(c.deviceID)

	// Meta folder — sendreceive, shared by all participants.
	metaFolderID := "meta-" + eventIDLower
	metaPath := filepath.Join(c.dataDir, eventIDLower+"-meta")
	if err := os.MkdirAll(metaPath, 0700); err != nil {
		return fmt.Errorf("creating meta folder: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(metaPath, ".stfolder"), 0755); err != nil {
		return fmt.Errorf("creating meta .stfolder marker: %w", err)
	}
	if err := c.syncthing.AddFolder(metaFolderID, metaPath, "sendreceive"); err != nil {
		return fmt.Errorf("registering meta folder with Syncthing: %w", err)
	}

	// Own photo folder — sendonly, this device's uploads.
	photoFolderID := "photos-" + eventIDLower + "-" + deviceIDLower
	photoPath := filepath.Join(c.dataDir, eventIDLower+"-"+deviceIDLower+"-photos")
	if err := os.MkdirAll(photoPath, 0700); err != nil {
		return fmt.Errorf("creating photo folder: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(photoPath, ".stfolder"), 0755); err != nil {
		return fmt.Errorf("creating photo .stfolder marker: %w", err)
	}
	if err := c.syncthing.AddFolder(photoFolderID, photoPath, "sendonly"); err != nil {
		return fmt.Errorf("registering photo folder with Syncthing: %w", err)
	}

	return nil
}

// BootstrapConnection connects a joining device to the event host by adding
// the host as a Syncthing peer and sharing the meta and photo folders with it.
// Errors are logged but not returned — bootstrapping is best-effort so that a
// transient Syncthing issue does not block the join flow.
func (c *Client) BootstrapConnection(eventID, hostDeviceID string) error {
	log.Printf("GoCore: BootstrapConnection START — syncthing=%v deviceID=%s", c.syncthing != nil, c.deviceID)
	log.Printf("GoCore: BootstrapConnection called — eventID=%s hostDeviceID=%s", eventID, hostDeviceID)

	if c.syncthing == nil {
		log.Printf("GoCore: BootstrapConnection — syncthing is nil, skipping")
		return nil
	}

	eventIDLower := strings.ToLower(eventID)

	// 1. Add host as peer.
	log.Printf("GoCore: AddPeer called with deviceID='%s' (len=%d)", hostDeviceID, len(hostDeviceID))
	err := c.syncthing.AddPeer(hostDeviceID)
	log.Printf("GoCore: AddPeer(%s) result: %v", hostDeviceID, err)

	// 2. Share meta folder with host.
	metaFolderID := "meta-" + eventIDLower
	log.Printf("GoCore: ShareFolder called with folderID='%s' deviceID='%s'", metaFolderID, hostDeviceID)
	err = c.syncthing.ShareFolder(metaFolderID, hostDeviceID)
	log.Printf("GoCore: ShareFolder(%s, %s) result: %v", metaFolderID, hostDeviceID, err)

	// 3. Share our photo folder with host.
	photoFolderID := "photos-" + eventIDLower + "-" + strings.ToLower(c.deviceID)
	log.Printf("GoCore: ShareFolder called with folderID='%s' deviceID='%s'", photoFolderID, hostDeviceID)
	err = c.syncthing.ShareFolder(photoFolderID, hostDeviceID)
	log.Printf("GoCore: ShareFolder(%s, %s) result: %v", photoFolderID, hostDeviceID, err)

	// 4. Create a ReceiveOnly folder for the host's photos so we can pull them.
	hostPhotoFolderID := "photos-" + eventIDLower + "-" + strings.ToLower(hostDeviceID)
	hostPhotoPath := filepath.Join(c.dataDir, eventIDLower+"-"+strings.ToLower(hostDeviceID)+"-photos")
	if err := os.MkdirAll(hostPhotoPath, 0700); err != nil {
		log.Printf("GoCore: BootstrapConnection mkdir host photos error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostPhotoPath, ".stfolder"), 0755); err != nil {
		log.Printf("GoCore: BootstrapConnection mkdir host .stfolder error: %v", err)
	}
	log.Printf("GoCore: BootstrapConnection — about to AddFolder hostPhotoFolderID=%s hostPhotoPath=%s", hostPhotoFolderID, hostPhotoPath)
	if err := c.syncthing.AddFolder(hostPhotoFolderID, hostPhotoPath, "receiveonly"); err != nil {
		log.Printf("GoCore: BootstrapConnection AddFolder host photos error: %v", err)
	}
	if err := c.syncthing.ShareFolder(hostPhotoFolderID, hostDeviceID); err != nil {
		log.Printf("GoCore: BootstrapConnection ShareFolder host photos error: %v", err)
	}
	log.Printf("GoCore: BootstrapConnection — created host photo folder %s at %s", hostPhotoFolderID, hostPhotoPath)

	log.Printf("GoCore: BootstrapConnection — done")
	return nil
}

// AddParticipant is the exported entry point for adding a newly discovered
// participant. It delegates to addParticipantPhotoFolder which creates the OS
// directory and (when c.syncthing != nil) registers Syncthing folders/peers.
func (c *Client) AddParticipant(eventID, participantDeviceID string) error {
	return c.addParticipantPhotoFolder(eventID, participantDeviceID)
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
	participantPath := filepath.Join(c.dataDir, strings.ToLower(eventID)+"-"+strings.ToLower(participantDeviceID)+"-photos")
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

	// 2b. Share the participant's photo folder WITH the participant so they send to it.
	if err := c.syncthing.ShareFolder(participantPhotoFolderID, participantDeviceID); err != nil {
		return fmt.Errorf("sharing participant photo folder with participant: %w", err)
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
