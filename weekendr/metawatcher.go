package weekendr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
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
// Each device is processed at most once per event, tracked both in the
// goroutine-local knownDevices map and the client-level processedParticipants
// map (which survives watcher restarts within the same session).
func (c *Client) StartMetaWatcher(eventID string) error {
	log.Printf("GoCore: StartMetaWatcher called for event %s, watchers map has %d entries", eventID, len(c.watchers))
	if entry, exists := c.watchers[eventID]; exists {
		log.Printf("DEBUG metawatcher: found existing watcher entry for %s, checking alive channel", eventID)
		select {
		case <-entry.alive:
			log.Printf("DEBUG metawatcher: dead watcher found for %s — restarting", eventID)
			delete(c.watchers, eventID)
		default:
			log.Printf("DEBUG metawatcher: watcher still alive for %s — skipping", eventID)
			return nil
		}
	} else {
		log.Printf("DEBUG metawatcher: no existing watcher for %s — starting fresh", eventID)
	}

	entry := &watcherEntry{
		stop:  make(chan struct{}),
		alive: make(chan struct{}),
	}
	c.watchers[eventID] = entry

	devicesDir := filepath.Join(c.dataDir, "meta-"+eventID, "devices")
	log.Printf("DEBUG metawatcher: watching devicesDir=%s", devicesDir)

	go func() {
		log.Printf("DEBUG metawatcher: goroutine LAUNCHED for event %s", eventID)
		defer close(entry.alive)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("ERROR metawatcher: PANIC: %v", r)
			}
			log.Printf("DEBUG metawatcher: goroutine EXITED for event %s", eventID)
		}()
		log.Printf("DEBUG metawatcher: devicesDir=%s", devicesDir)

		// Immediate ReadDir to check directory state on startup.
		entries, err := os.ReadDir(devicesDir)
		if err != nil {
			log.Printf("DEBUG metawatcher: INITIAL ReadDir failed: %v", err)
		} else {
			log.Printf("DEBUG metawatcher: INITIAL scan found %d files", len(entries))
			for _, e := range entries {
				log.Printf("DEBUG metawatcher: found file: %s", e.Name())
			}
		}

		knownDevices := map[string]bool{}
		var scanLogCounter int

		ticker := time.NewTicker(watcherPollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-entry.stop:
				return
			case <-ticker.C:
				scanLogCounter++
				entries, err := os.ReadDir(devicesDir)
				if err != nil {
					// Log ReadDir failures every ~30 seconds so we can see
					// whether the directory exists at all.
					if scanLogCounter%300 == 0 {
						log.Printf("DEBUG metawatcher: ReadDir(%s) failed: %v", devicesDir, err)
					}
					continue
				}

				// Log scan status every ~30 seconds (300 ticks at 100ms).
				if scanLogCounter%300 == 0 {
					log.Printf("DEBUG metawatcher: scanning %s, found %d files", devicesDir, len(entries))
					for _, e := range entries {
						if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
							log.Printf("DEBUG metawatcher: device file: %s", e.Name())
						}
					}
				}

				for _, entry := range entries {
					if entry.IsDir() {
						continue
					}
					name := entry.Name()
					if !strings.HasSuffix(name, ".json") {
						continue
					}
					// Syncthing-generated conflict copies must never be promoted
					// to a participant: the filename suffix encodes a timestamp,
					// not a device id, and would corrupt knownDevices/folder
					// registration if treated as one.
					if strings.Contains(name, ".sync-conflict-") {
						continue
					}
					// Primary source: deviceID is encoded in the filename.
					deviceID := strings.ToLower(strings.TrimSuffix(name, ".json"))

					// Read the JSON written by AnnounceDevice to validate
					// and extract the participant name and userID.
					var announceName string
					var announceUserID string
					jsonPath := filepath.Join(devicesDir, name)
					if raw, readErr := os.ReadFile(jsonPath); readErr == nil {
						var ann deviceAnnouncement
						if jsonErr := json.Unmarshal(raw, &ann); jsonErr == nil {
							announceName = ann.Name
							announceUserID = ann.UserID
							if ann.DeviceID != "" && ann.DeviceID != deviceID {
								log.Printf("metawatcher: %s: JSON device_id %q != filename %q, using filename",
									name, ann.DeviceID, deviceID)
							}
						}
					}

					// Skip our own device — we already have a SendOnly folder.
					if deviceID == strings.ToLower(c.deviceID) {
						continue
					}

					if knownDevices[deviceID] {
						continue
					}

					// Client-level dedup: skip if already processed in this session
					// (e.g. watcher restarted for the same event).
					participantKey := eventID + ":" + deviceID
					if c.processedParticipants[participantKey] {
						log.Printf("DEBUG metawatcher: skipping known participant %s (event %s) — already processed this session", deviceID, eventID)
						knownDevices[deviceID] = true
						continue
					}

					// Use participant's userID for folder naming if available
					participantIdentity := announceUserID
					if participantIdentity == "" {
						participantIdentity = deviceID
					}

					log.Printf("GoCore: MetaWatcher found new device %s (name: %s, userID: %s)", deviceID, announceName, announceUserID)
					log.Printf("DEBUG metawatcher: calling addParticipantPhotoFolder for %s identity=%s (event %s)", deviceID, participantIdentity, eventID)
					if err := c.addParticipantPhotoFolder(eventID, deviceID, participantIdentity); err != nil {
						log.Printf("metawatcher: addParticipantPhotoFolder(%s, %s): %v", eventID, deviceID, err)
					}
					c.processedParticipants[participantKey] = true
					knownDevices[deviceID] = true
				}
			}
		}
	}()

	// Periodic catch-up: retry hub sharing for receiveonly photo folders every
	// 60 seconds. This covers cases where the initial shareReceiveOnlyFolderWithHub
	// call failed (e.g. hub info not yet available) or where timing was unlucky.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		eventIDLower := strings.ToLower(eventID)
		for {
			select {
			case <-entry.stop:
				return
			case <-ticker.C:
				log.Printf("GoCore: hub catch-up tick for event %s", eventIDLower)
				log.Printf("[PAUSE-CONFLICT-CANDIDATE] metawatcher 60s tick: shareKnownReceiveOnlyFoldersWithHub iteration: event=%s", eventIDLower)
				c.shareKnownReceiveOnlyFoldersWithHub(eventIDLower)
			}
		}
	}()

	return nil
}

// StopMetaWatcher stops watching the meta-folder for the given event.
func (c *Client) StopMetaWatcher(eventID string) error {
	if entry, ok := c.watchers[eventID]; ok {
		close(entry.stop)
		delete(c.watchers, eventID)
	}
	return nil
}

// PhotoIndexEntry represents a single photo/video in the device's contribution index.
// Used for sync transparency, deduplication, Photo Map, and location clustering.
type PhotoIndexEntry struct {
	Filename  string   `json:"filename"`
	TakenAt   string   `json:"taken_at"`            // ISO 8601, sourced from EXIF DateTimeOriginal
	Size      int64    `json:"size"`                // Bytes
	Hash      string   `json:"hash"`                // MD5 over raw file bytes
	Latitude  *float64 `json:"latitude,omitempty"`  // GPS from EXIF; nil if unavailable
	Longitude *float64 `json:"longitude,omitempty"` // GPS from EXIF; nil if unavailable
}

// deviceAnnouncement is the JSON written by AnnounceDevice / UpdatePhotoIndex
// and read by MetaWatcher and peer RemotePhotoIndexStore implementations.
type deviceAnnouncement struct {
	DeviceID    string            `json:"device_id"`
	UserID      string            `json:"user_id"`
	Name        string            `json:"name"`
	AnnouncedAt string            `json:"announced_at"`
	PhotoIndex  []PhotoIndexEntry `json:"photo_index"`
	LastSeen    string            `json:"last_seen"`
	PhotoCount  int               `json:"photo_count"`
	VideoCount  int               `json:"video_count"`
}

// AnnounceDevice writes this device's presence to the meta-folder as
//
//	devices/{deviceID}.json
//
// so that MetaWatcher on peer devices can discover this device and set up
// the Syncthing folders for P2P sync.
//
// To avoid triggering Syncthing conflict resolution on the send-receive
// meta-folder, the file is only written if the content has actually changed.
func (c *Client) AnnounceDevice(eventID string, name string) error {
	log.Printf("GoCore: AnnounceDevice called for event %s (name: %s)", eventID, name)
	devicesDir := filepath.Join(c.dataDir, "meta-"+eventID, "devices")
	if err := os.MkdirAll(devicesDir, 0700); err != nil {
		return fmt.Errorf("creating devices dir: %w", err)
	}

	deviceIDLower := strings.ToLower(c.deviceID)
	ann := deviceAnnouncement{
		DeviceID:    deviceIDLower,
		UserID:      c.folderIdentity(),
		Name:        name,
		AnnouncedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(ann)
	if err != nil {
		return fmt.Errorf("marshaling device announcement: %w", err)
	}

	annPath := filepath.Join(devicesDir, deviceIDLower+".json")

	// Skip write if the file already exists with identical content.
	// Re-writing an unchanged file triggers Syncthing's conflict resolution
	// on the send-receive meta-folder ("rename .syncthing.*.tmp: file exists").
	if existing, readErr := os.ReadFile(annPath); readErr == nil {
		// Compare only device_id and name — ignore announced_at so that
		// repeated calls with the same identity are truly no-ops.
		var existingAnn deviceAnnouncement
		if json.Unmarshal(existing, &existingAnn) == nil &&
			existingAnn.DeviceID == ann.DeviceID &&
			existingAnn.UserID == ann.UserID &&
			existingAnn.Name == ann.Name {
			log.Printf("GoCore: AnnounceDevice skipped (unchanged) %s", annPath)
			return nil
		}
	}

	if err := os.WriteFile(annPath, data, 0600); err != nil {
		// Syncthing may have written the file concurrently via
		// meta-folder sync — treat EEXIST as success.
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("writing device announcement: %w", err)
		}
		log.Printf("GoCore: announceDevice: file already exists (Syncthing race), ignoring")
	}
	log.Printf("DEBUG announceDevice: wrote %s", annPath)
	_, statErr := os.Stat(annPath)
	log.Printf("DEBUG announceDevice: file exists after write = %v", statErr == nil)

	// Same-process writes are not picked up by Syncthing's fsWatcher, so the
	// peer never gets the index update announced. Trigger an explicit rescan.
	metaFolderID := "meta-" + eventID
	log.Printf("GoCore: rescan %s after AnnounceDevice (name=%s)", metaFolderID, name)
	if err := c.RescanFolder(metaFolderID); err != nil {
		log.Printf("GoCore: rescan %s after AnnounceDevice failed: %v", metaFolderID, err)
	}
	return nil
}

// UpdatePhotoIndex rewrites this device's announcement JSON with a fresh
// photo index. Counts are derived from filename extensions (.mov / .mp4
// count as video; everything else is a photo).
//
// Name and AnnouncedAt are preserved from the existing announcement so that
// repeated UpdatePhotoIndex calls do not clobber the identity fields that
// AnnounceDevice is responsible for. LastSeen is refreshed on every call.
//
// Matches AnnounceDevice's non-atomic os.WriteFile pattern for consistency;
// Syncthing's meta-folder sync tolerates the occasional partial read.
func (c *Client) UpdatePhotoIndex(eventID string, entries []PhotoIndexEntry) error {
	log.Printf("GoCore: UpdatePhotoIndex eventID=%s entries=%d", eventID, len(entries))
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Filename < entries[j].Filename
	})

	photoCount := 0
	videoCount := 0
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Filename))
		if ext == ".mov" || ext == ".mp4" {
			videoCount++
		} else {
			photoCount++
		}
	}

	devicesDir := filepath.Join(c.dataDir, "meta-"+eventID, "devices")
	if err := os.MkdirAll(devicesDir, 0700); err != nil {
		return fmt.Errorf("creating devices dir: %w", err)
	}

	deviceIDLower := strings.ToLower(c.deviceID)
	annPath := filepath.Join(devicesDir, deviceIDLower+".json")

	// Preserve Name + AnnouncedAt from any existing announcement. Only
	// AnnounceDevice knows the display name — debounced index flushes must
	// not blank it out.
	var name, announcedAt string
	if existing, err := os.ReadFile(annPath); err == nil {
		var prev deviceAnnouncement
		if json.Unmarshal(existing, &prev) == nil {
			name = prev.Name
			announcedAt = prev.AnnouncedAt
		}
	}
	if announcedAt == "" {
		announcedAt = time.Now().UTC().Format(time.RFC3339)
	}

	ann := deviceAnnouncement{
		DeviceID:    deviceIDLower,
		UserID:      c.folderIdentity(),
		Name:        name,
		AnnouncedAt: announcedAt,
		PhotoIndex:  entries,
		LastSeen:    time.Now().UTC().Format(time.RFC3339),
		PhotoCount:  photoCount,
		VideoCount:  videoCount,
	}

	data, err := json.MarshalIndent(ann, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling announcement: %w", err)
	}

	if err := os.WriteFile(annPath, data, 0600); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("writing photo index announcement: %w", err)
		}
	}

	// Same-process writes are not picked up by Syncthing's fsWatcher, so the
	// peer never gets the index update announced. Trigger an explicit rescan.
	metaFolderID := "meta-" + eventID
	log.Printf("GoCore: rescan %s after UpdatePhotoIndex (entries=%d)", metaFolderID, len(entries))
	if err := c.RescanFolder(metaFolderID); err != nil {
		log.Printf("GoCore: rescan %s after UpdatePhotoIndex failed: %v", metaFolderID, err)
	}
	return nil
}

// PhotoIndexInfo carries per-filename metadata returned by
// GetPhotoIndexForEvent: EXIF takenAt, contributing userID, and the full
// PhotoIndexEntry payload (size/hash/GPS). Serialized as the value type of
// the returned JSON map. gomobile skips binding this struct directly
// (because of the *float64 fields), but that is fine — it is only
// JSON-serialized, never crossed over the Objc boundary.
type PhotoIndexInfo struct {
	TakenAt   string   `json:"taken_at"`
	UserID    string   `json:"user_id"`
	Size      int64    `json:"size"`
	Hash      string   `json:"hash"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

// GetPhotoIndexForEvent flattens the photo_index arrays from every device
// announcement in meta-{eventID}/devices/*.json into a single
// "userID/filename" → PhotoIndexInfo map and returns it as a JSON-encoded
// string. The composite key keeps contributions distinct when two users
// export a photo with the same filename (e.g. IMG_0001.JPG from two iPhones).
//
// Return shape:
//
//	{"abc/IMG_0001.JPG": {"taken_at": "2025-04-16T10:00:00Z", "user_id": "abc"}, ...}
//
// A missing devices directory returns "{}" (not an error).
//
// Gomobile-friendly: string return is bridged to NSString, error to NSError**.
func (c *Client) GetPhotoIndexForEvent(eventID string) (string, error) {
	devicesDir := filepath.Join(c.dataDir, "meta-"+eventID, "devices")

	entries, err := os.ReadDir(devicesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "{}", nil
		}
		return "", fmt.Errorf("reading devices dir: %w", err)
	}

	result := make(map[string]PhotoIndexInfo)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		// Skip Syncthing conflict copies — their photo_index would otherwise
		// be merged in alongside the live announcement and produce duplicate
		// composite keys with stale data.
		if strings.Contains(entry.Name(), ".sync-conflict-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(devicesDir, entry.Name()))
		if err != nil {
			continue
		}
		var ann deviceAnnouncement
		if err := json.Unmarshal(data, &ann); err != nil {
			continue
		}
		for _, pe := range ann.PhotoIndex {
			result[ann.UserID+"/"+pe.Filename] = PhotoIndexInfo{
				TakenAt:   pe.TakenAt,
				UserID:    ann.UserID,
				Size:      pe.Size,
				Hash:      pe.Hash,
				Latitude:  pe.Latitude,
				Longitude: pe.Longitude,
			}
		}
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshaling photo index: %w", err)
	}
	return string(out), nil
}
