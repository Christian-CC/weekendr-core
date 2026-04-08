// weekendr/hubpersist.go
package weekendr

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// persistedHubInfo is the on-disk shape for the per-event hub coordinates
// and folder encryption key. Stored at {dataDir}/hub-{eventIDLower}.json so
// that hub coordinates survive app restarts and shareReceiveOnlyFolderWithHub
// can re-encrypt links to the hub for events that were known at startup.
//
// Without this persistence, hubInfos/folderKeys live only in memory and are
// only populated by SharePhotoFolderWithHub. After an app restart with events
// loaded via SetActiveEventIDs, that call never happens for already-joined
// events, so every shareReceiveOnlyFolderWithHub call would be a no-op.
type persistedHubInfo struct {
	HubDeviceID string `json:"hub_device_id"`
	HubAddress  string `json:"hub_address"`
	FolderKey   string `json:"folder_key"` // hex string, identical to what SharePhotoFolderWithHub received
}

// hubInfoPath returns the absolute path of the hub info file for an event.
// eventIDLower MUST already be lowercase — the file name uses it verbatim
// so that the same eventID always maps to the same file.
func hubInfoPath(dataDir, eventIDLower string) string {
	return filepath.Join(dataDir, "hub-"+eventIDLower+".json")
}

// writeHubInfo persists the hub info for an event atomically (write to a
// .tmp file then rename). Renaming on the same filesystem is atomic on
// POSIX, so a crash mid-write cannot leave a half-written hub-*.json
// behind that loadAllHubInfos would later parse as garbage.
func writeHubInfo(dataDir, eventIDLower string, info persistedHubInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshalling hub info: %w", err)
	}
	finalPath := hubInfoPath(dataDir, eventIDLower)
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// Best-effort cleanup of the tmp file before bailing.
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming %s → %s: %w", tmpPath, finalPath, err)
	}
	return nil
}

// removeHubInfo deletes the hub info file for an event. Missing-file errors
// are ignored so that callers can use this without first checking existence.
func removeHubInfo(dataDir, eventIDLower string) error {
	if err := os.Remove(hubInfoPath(dataDir, eventIDLower)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// loadAllHubInfos returns the persisted hub info for every event that has a
// hub-*.json file in dataDir. Keys in the returned map are lowercase event
// IDs (matching the in-memory hubInfos/folderKeys convention). Files that
// fail to parse are logged and skipped — a single broken file must not
// prevent the rest from loading.
func loadAllHubInfos(dataDir string) map[string]persistedHubInfo {
	out := make(map[string]persistedHubInfo)

	matches, err := filepath.Glob(filepath.Join(dataDir, "hub-*.json"))
	if err != nil {
		// filepath.Glob only errors on a malformed pattern, which our
		// hard-coded one is not. Log defensively and return empty.
		log.Printf("GoCore: loadAllHubInfos: glob error: %v", err)
		return out
	}

	for _, path := range matches {
		// Skip half-written tmp files left behind by a crashed writeHubInfo.
		base := filepath.Base(path)
		if strings.HasSuffix(base, ".tmp") {
			continue
		}

		// Extract the lowercase event ID from "hub-{eventIDLower}.json".
		name := strings.TrimSuffix(strings.TrimPrefix(base, "hub-"), ".json")
		if name == "" || name == base {
			// Did not match our pattern — skip.
			continue
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			log.Printf("GoCore: loadAllHubInfos: reading %s: %v", path, err)
			continue
		}
		var info persistedHubInfo
		if err := json.Unmarshal(raw, &info); err != nil {
			log.Printf("GoCore: loadAllHubInfos: parsing %s: %v", path, err)
			continue
		}
		out[name] = info
	}
	return out
}
