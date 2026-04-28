package weekendr

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Package-level debouncer state. Shared by SchedulePhotoIndexUpdate and the
// public AddPhotoIndexEntry / SchedulePhotoIndexFlush entry points.
var (
	pendingUpdates = make(map[string]*time.Timer)
	pendingEntries = make(map[string][]PhotoIndexEntry)
	pendingMutex   sync.Mutex
)

// SchedulePhotoIndexUpdate cancels any pending photo-index flush for the given
// event and starts a new 2-second debounce window. When the window elapses the
// on-disk photo_index is merged with the current pendingEntries[eventID], any
// tombstoned filenames (set via RemovePhotoIndexEntry) are removed from the
// result, and the merged list is written back via UpdatePhotoIndex.
//
// Rapid bursts of photo adds (e.g. selecting 20 photos in the picker) collapse
// into a single disk write at the end of the burst. Because pendingEntries
// starts empty on every app launch and is never hydrated from disk, the flush
// merges with the existing announcement so that previously-indexed photos
// survive across sessions.
//
// A nil entries argument arms the debouncer without replacing pendingEntries.
// RemovePhotoIndexEntry uses this to flush a tombstone without clobbering
// concurrent adds that have not yet been flushed.
func (c *Client) SchedulePhotoIndexUpdate(eventID string, entries []PhotoIndexEntry) {
	pendingMutex.Lock()
	defer pendingMutex.Unlock()

	if timer, exists := pendingUpdates[eventID]; exists {
		timer.Stop()
	}

	if entries != nil {
		pendingEntries[eventID] = entries
	}

	log.Printf("GoCore: SchedulePhotoIndexUpdate eventID=%s entries=%d (2s timer armed)", eventID, len(entries))

	pendingUpdates[eventID] = time.AfterFunc(2*time.Second, func() {
		pendingMutex.Lock()
		pending := pendingEntries[eventID]
		log.Printf("GoCore: photo index timer fired eventID=%s pending=%d", eventID, len(pending))
		delete(pendingUpdates, eventID)
		pendingMutex.Unlock()

		c.pendingTombstonesMu.Lock()
		var tombstones map[string]bool
		if m := c.pendingTombstones[eventID]; len(m) > 0 {
			tombstones = make(map[string]bool, len(m))
			for k := range m {
				tombstones[k] = true
			}
		}
		c.pendingTombstonesMu.Unlock()

		merged, err := mergePendingWithDisk(c, eventID, pending, tombstones)
		if err != nil {
			log.Printf("GoCore: mergePendingWithDisk failed eventID=%s err=%v", eventID, err)
			return
		}
		if err := c.UpdatePhotoIndex(eventID, merged); err != nil {
			log.Printf("GoCore: UpdatePhotoIndex failed eventID=%s err=%v", eventID, err)
			return
		}

		pendingMutex.Lock()
		delete(pendingEntries, eventID)
		pendingMutex.Unlock()

		c.pendingTombstonesMu.Lock()
		delete(c.pendingTombstones, eventID)
		c.pendingTombstonesMu.Unlock()
	})
}

// mergePendingWithDisk reads the existing photo_index from
// devices/{deviceID}.json and merges the pending entries on top of it, keyed
// by Filename — pending wins on conflict. Tombstones are applied AFTER the
// merge so that a concurrent add for the same filename cannot resurrect an
// entry that the current debounce window has deleted. A missing announcement
// file is treated as empty. The result is sorted by Filename for deterministic
// writes.
func mergePendingWithDisk(c *Client, eventID string, pending []PhotoIndexEntry, tombstones map[string]bool) ([]PhotoIndexEntry, error) {
	annPath := filepath.Join(c.dataDir, "meta-"+eventID, "devices", strings.ToLower(c.deviceID)+".json")

	var disk []PhotoIndexEntry
	data, err := os.ReadFile(annPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading announcement: %w", err)
		}
	} else {
		var ann deviceAnnouncement
		if err := json.Unmarshal(data, &ann); err != nil {
			return nil, fmt.Errorf("unmarshaling announcement: %w", err)
		}
		disk = ann.PhotoIndex
	}

	byName := make(map[string]PhotoIndexEntry, len(disk)+len(pending))
	for _, e := range disk {
		byName[e.Filename] = e
	}
	for _, e := range pending {
		byName[e.Filename] = e
	}
	for filename := range tombstones {
		delete(byName, filename)
	}

	merged := make([]PhotoIndexEntry, 0, len(byName))
	for _, e := range byName {
		merged = append(merged, e)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Filename < merged[j].Filename
	})
	return merged, nil
}
