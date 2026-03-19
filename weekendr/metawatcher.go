package weekendr

import (
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
// When a new devices/{deviceID}.json file appears, it calls addParticipantPhotoFolder
// for that device and tracks it in knownDevices to avoid duplicate calls.
func (c *Client) StartMetaWatcher(eventID string) error {
	if _, running := c.watchers[eventID]; running {
		return nil
	}

	stop := make(chan struct{})
	c.watchers[eventID] = stop

	devicesDir := filepath.Join(c.dataDir, "events", eventID, "meta", "devices")

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
					deviceID := strings.TrimSuffix(name, ".json")

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

// AnnounceDevice writes this device's presence to the meta-folder.
func (c *Client) AnnounceDevice(eventID string) error {
	return nil
}
