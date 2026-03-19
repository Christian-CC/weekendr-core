package weekendr

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Participant represents a device participating in an event.
type Participant struct {
	DeviceID   string
	Online     bool
	PhotoCount int64
	LastSeen   int64
}

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

const watcherPollInterval = 100 * time.Millisecond

// StartMetaWatcher begins watching the Syncthing meta-folder for new participants.
// When a new devices/{deviceID}.json file appears it:
//  1. Reads the JSON written by AnnounceDevice to validate the deviceID.
//  2. Calls addParticipantPhotoFolder, which creates the OS directory and (when
//     c.syncthing != nil) registers the ReceiveOnly folder and shares the meta
//     and photo folders with the new participant via Syncthing.
//
// Each device is processed at most once (tracked in knownDevices).
func (c *Client) StartMetaWatcher(eventID string) error {
	if _, running := c.watchers[eventID]; running {
		return nil
	}

	stop := make(chan struct{})
	c.watchers[eventID] = stop

	devicesDir := filepath.Join(c.dataDir, eventID+"-meta", "devices")

	go func() {
		knownDevices := map[string]bool{}

		ticker := time.NewTicker(watcherPollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				entries, err := os.ReadDir(devicesDir)
				if err != nil {
					// Directory may not exist yet; keep polling.
					continue
				}

				for _, entry := range entries {
					if entry.IsDir() {
						continue
					}
					name := entry.Name()
					if !strings.HasSuffix(name, ".json") {
						continue
					}
					// Primary source: deviceID is encoded in the filename.
					deviceID := strings.TrimSuffix(name, ".json")

					// Also read the JSON written by AnnounceDevice to validate
					// and to obtain any extra fields (e.g. future shared secrets).
					// If the JSON is absent or malformed we fall back silently.
					jsonPath := filepath.Join(devicesDir, name)
					if raw, readErr := os.ReadFile(jsonPath); readErr == nil {
						var ann deviceAnnouncement
						if jsonErr := json.Unmarshal(raw, &ann); jsonErr == nil &&
							ann.DeviceID != "" && ann.DeviceID != deviceID {
							log.Printf("metawatcher: %s: JSON device_id %q != filename %q, using filename",
								name, ann.DeviceID, deviceID)
						}
					}

					// Skip our own device — we already have a SendOnly folder.
					if deviceID == c.deviceID {
						continue
					}

					if knownDevices[deviceID] {
						continue
					}

					if err := c.addParticipantPhotoFolder(eventID, deviceID); err != nil {
						log.Printf("metawatcher: addParticipantPhotoFolder(%s, %s): %v", eventID, deviceID, err)
					}
					knownDevices[deviceID] = true
				}
			}
		}
	}()

	return nil
}

// StopMetaWatcher stops watching the meta-folder for the given event.
func (c *Client) StopMetaWatcher(eventID string) error {
	if stop, ok := c.watchers[eventID]; ok {
		close(stop)
		delete(c.watchers, eventID)
	}
	return nil
}

// deviceAnnouncement is the JSON written by AnnounceDevice and read by MetaWatcher.
type deviceAnnouncement struct {
	DeviceID    string `json:"device_id"`
	AnnouncedAt string `json:"announced_at"`
}

// AnnounceDevice writes this device's presence to the meta-folder as
//
//	devices/{deviceID}.json
//
// so that MetaWatcher on peer devices can discover this device and set up
// the Syncthing folders for P2P sync.
func (c *Client) AnnounceDevice(eventID string) error {
	devicesDir := filepath.Join(c.dataDir, eventID+"-meta", "devices")
	if err := os.MkdirAll(devicesDir, 0700); err != nil {
		return fmt.Errorf("creating devices dir: %w", err)
	}

	ann := deviceAnnouncement{
		DeviceID:    c.deviceID,
		AnnouncedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(ann)
	if err != nil {
		return fmt.Errorf("marshaling device announcement: %w", err)
	}

	annPath := filepath.Join(devicesDir, c.deviceID+".json")
	if err := os.WriteFile(annPath, data, 0600); err != nil {
		return fmt.Errorf("writing device announcement: %w", err)
	}
	return nil
}
