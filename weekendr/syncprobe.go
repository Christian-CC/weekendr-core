package weekendr

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// probeOwnPhotoSyncState checks, for this device's own outgoing photo folder
// in eventID, which of the folder's connected devices (participants + hub)
// still need each artifact — then stamps ConfirmedBy/CheckedAt onto the
// matching PhotoIndexEntry.Artifacts and flushes via the usual debounced
// photo-index path, so the result reaches every device (and the server's
// meta-folder mirror) the same way any other index update does.
//
// This only works for the OWNER of the folder: Weekendr's mesh connects
// every participant directly with the folder's owner (plus the hub as a
// cache), but participants are never connected to each other's copies of
// this folder (see addParticipantPhotoFolder) — so the owner's device is the
// only one with a live Syncthing connection to every device that has this
// folder, and hence the only one that can answer "who has this file yet".
// Called from StartMetaWatcher's existing 60s catch-up ticker.
func (c *Client) probeOwnPhotoSyncState(eventID string) {
	if c.syncthing == nil {
		return
	}
	eventIDLower := strings.ToLower(eventID)
	folderID := "photos-" + eventIDLower + "-" + strings.ToLower(c.folderIdentity())

	deviceList, err := c.syncthing.FolderDeviceIDs(folderID)
	if err != nil {
		log.Printf("GoCore: probeOwnPhotoSyncState: FolderDeviceIDs(%s): %v", folderID, err)
		return
	}
	if deviceList.Size() == 0 {
		return // no peers on this folder yet (folder not created, or nobody has joined)
	}

	// stillNeeds[deviceID] = filenames that device has not received yet.
	stillNeeds := make(map[string]map[string]bool, deviceList.Size())
	for i := 0; i < deviceList.Size(); i++ {
		devID := deviceList.Get(i)
		needed, err := c.syncthing.FilesNeededBy(folderID, devID)
		if err != nil {
			log.Printf("GoCore: probeOwnPhotoSyncState: FilesNeededBy(%s, %s): %v", folderID, devID, err)
			continue
		}
		set := make(map[string]bool, needed.Size())
		for j := 0; j < needed.Size(); j++ {
			set[needed.Get(j)] = true
		}
		stillNeeds[devID] = set
	}

	annPath := filepath.Join(c.dataDir, "meta-"+eventIDLower, "devices", strings.ToLower(c.deviceID)+".json")
	data, err := os.ReadFile(annPath)
	if err != nil {
		return // no announcement written yet — nothing to update
	}
	var ann deviceAnnouncement
	if err := json.Unmarshal(data, &ann); err != nil {
		log.Printf("GoCore: probeOwnPhotoSyncState: unmarshal announcement: %v", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for i, entry := range ann.PhotoIndex {
		// Bootstrap Artifacts from the legacy scalar fields the first time a
		// probe touches an entry written before this field existed.
		if len(entry.Artifacts) == 0 && entry.Hash != "" {
			entry.Artifacts = []PhotoArtifact{{Kind: ArtifactOriginal, Hash: entry.Hash, Size: entry.Size}}
		}
		for ai := range entry.Artifacts {
			artifact := &entry.Artifacts[ai]
			// .thumb.jpg matches the naming convention SyncStatusProbe (iOS)
			// already relies on. Video (Live Photo .mov companion) has no
			// artifact yet to check — PhotoExporter does not index it (see
			// exportLivePhoto) — so it never enters this loop today.
			needleName := entry.Filename
			if artifact.Kind == ArtifactThumb {
				needleName = entry.Filename + ".thumb.jpg"
			}
			var confirmedBy []string
			for devID, needs := range stillNeeds {
				if !needs[needleName] {
					confirmedBy = append(confirmedBy, devID)
				}
			}
			sort.Strings(confirmedBy)
			artifact.ConfirmedBy = confirmedBy
			artifact.CheckedAt = now
		}
		ann.PhotoIndex[i] = entry
	}

	c.SchedulePhotoIndexUpdate(eventID, ann.PhotoIndex)
}
