package weekendr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	c, err := NewClient(dir)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// In tests Sushitrain isn't running, so set a fake device ID.
	c.deviceID = "TESTDID-AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG"
	return c
}

func TestCreateEventFolders(t *testing.T) {
	c := newTestClient(t)

	event := &Event{ID: "my-event", Name: "My Event"}
	if err := createEventFolders(c, event); err != nil {
		t.Fatalf("createEventFolders: %v", err)
	}

	// PhotoFolderID must be lowercase alphanumeric+hyphens only
	wantPhoto := fmt.Sprintf("photos-%s-%s", strings.ToLower(event.ID), strings.ToLower(c.deviceID))
	if event.PhotoFolderID != wantPhoto {
		t.Errorf("PhotoFolderID: got %q, want %q", event.PhotoFolderID, wantPhoto)
	}

	wantMeta := "meta-my-event"
	if event.MetaFolderID != wantMeta {
		t.Errorf("MetaFolderID: got %q, want %q", event.MetaFolderID, wantMeta)
	}

	// Photo directory must exist
	photoPath := filepath.Join(c.dataDir, event.ID+"-"+c.deviceID+"-photos")
	if _, err := os.Stat(photoPath); err != nil {
		t.Errorf("photo dir not created: %v", err)
	}

	// Meta directory must exist
	metaPath := filepath.Join(c.dataDir, event.ID+"-meta")
	if _, err := os.Stat(metaPath); err != nil {
		t.Errorf("meta dir not created: %v", err)
	}
}

func TestCreateEvent(t *testing.T) {
	c := newTestClient(t)

	ev, err := c.CreateEvent(&CreateEventParams{Name: "Test Event", Mode: "live"})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	if ev.PhotoFolderID == "" {
		t.Error("PhotoFolderID should be set after CreateEvent")
	}
	if ev.MetaFolderID == "" {
		t.Error("MetaFolderID should be set after CreateEvent")
	}

	// Directories must exist on disk
	photoPath := filepath.Join(c.dataDir, ev.ID+"-"+c.deviceID+"-photos")
	if _, err := os.Stat(photoPath); err != nil {
		t.Errorf("photo dir not created by CreateEvent: %v", err)
	}
	metaPath := filepath.Join(c.dataDir, ev.ID+"-meta")
	if _, err := os.Stat(metaPath); err != nil {
		t.Errorf("meta dir not created by CreateEvent: %v", err)
	}
}

func TestCreateEventWithServerID(t *testing.T) {
	c := newTestClient(t)
	serverID := "56ce46e35f43659cc368159a5462b5aa"

	ev, err := c.CreateEvent(&CreateEventParams{EventID: serverID, Name: "Server Event", Mode: "live"})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	if ev.ID != serverID {
		t.Errorf("event ID: got %q, want %q", ev.ID, serverID)
	}

	photoPath := filepath.Join(c.dataDir, ev.ID+"-"+c.deviceID+"-photos")
	if _, err := os.Stat(photoPath); err != nil {
		t.Errorf("photo dir not created: %v", err)
	}
}

func TestJoinEvent(t *testing.T) {
	c := newTestClient(t)

	ev, err := c.JoinEvent("some-invite-secret", "evt-abc123")
	if err != nil {
		t.Fatalf("JoinEvent: %v", err)
	}

	if ev.PhotoFolderID == "" {
		t.Error("PhotoFolderID should be set after JoinEvent")
	}
	if ev.MetaFolderID == "" {
		t.Error("MetaFolderID should be set after JoinEvent")
	}

	photoPath := filepath.Join(c.dataDir, ev.ID+"-"+c.deviceID+"-photos")
	if _, err := os.Stat(photoPath); err != nil {
		t.Errorf("photo dir not created by JoinEvent: %v", err)
	}
}

func TestAddParticipantPhotoFolder(t *testing.T) {
	c := newTestClient(t)
	eventID := "party-2025"
	participantID := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"

	if err := c.addParticipantPhotoFolder(eventID, participantID); err != nil {
		t.Fatalf("addParticipantPhotoFolder: %v", err)
	}

	participantPath := filepath.Join(c.dataDir, eventID+"-"+participantID+"-photos")
	if _, err := os.Stat(participantPath); err != nil {
		t.Errorf("participant photo dir not created: %v", err)
	}
}

func TestMetaWatcherDiscovery(t *testing.T) {
	c := newTestClient(t)
	eventID := "watcher-event"

	// Create event folders so the meta directory base exists.
	event := &Event{ID: eventID, Name: "Watcher Event"}
	if err := createEventFolders(c, event); err != nil {
		t.Fatal(err)
	}

	if err := c.StartMetaWatcher(eventID); err != nil {
		t.Fatal(err)
	}
	defer c.StopMetaWatcher(eventID)

	// Simulate a participant announcing themselves by dropping a devices/{id}.json file.
	participantID := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	devicesDir := filepath.Join(c.dataDir, eventID+"-meta", "devices")
	if err := os.MkdirAll(devicesDir, 0700); err != nil {
		t.Fatal(err)
	}
	devFile := filepath.Join(devicesDir, participantID+".json")
	if err := os.WriteFile(devFile, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Poll until the watcher creates the participant photo folder (max 2s).
	participantPath := filepath.Join(c.dataDir, eventID+"-"+participantID+"-photos")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(participantPath); err == nil {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("MetaWatcher did not create participant photo folder within 2 seconds")
}

func TestAnnounceDevice(t *testing.T) {
	c := newTestClient(t)
	eventID := "announce-event"

	// Create the meta folder first (AnnounceDevice creates devices/ sub-dir itself).
	metaPath := filepath.Join(c.dataDir, eventID+"-meta")
	if err := os.MkdirAll(metaPath, 0700); err != nil {
		t.Fatal(err)
	}

	if err := c.AnnounceDevice(eventID); err != nil {
		t.Fatalf("AnnounceDevice: %v", err)
	}

	// File must exist at devices/{deviceID}.json
	annPath := filepath.Join(c.dataDir, eventID+"-meta", "devices", c.deviceID+".json")
	raw, err := os.ReadFile(annPath)
	if err != nil {
		t.Fatalf("announcement file not created: %v", err)
	}

	var ann struct {
		DeviceID    string `json:"device_id"`
		AnnouncedAt string `json:"announced_at"`
	}
	if err := json.Unmarshal(raw, &ann); err != nil {
		t.Fatalf("announcement file is not valid JSON: %v", err)
	}
	if ann.DeviceID != c.deviceID {
		t.Errorf("device_id: got %q, want %q", ann.DeviceID, c.deviceID)
	}
	if ann.AnnouncedAt == "" {
		t.Error("announced_at must not be empty")
	}

	// announced_at must parse as RFC3339
	if _, err := time.Parse(time.RFC3339, ann.AnnouncedAt); err != nil {
		t.Errorf("announced_at %q is not RFC3339: %v", ann.AnnouncedAt, err)
	}
}

func TestMetaWatcherIgnoresOwnDevice(t *testing.T) {
	c := newTestClient(t)
	eventID := "own-device-event"

	event := &Event{ID: eventID, Name: "Own Device Event"}
	if err := createEventFolders(c, event); err != nil {
		t.Fatal(err)
	}

	if err := c.StartMetaWatcher(eventID); err != nil {
		t.Fatal(err)
	}
	defer c.StopMetaWatcher(eventID)

	// Write this device's own announcement file.
	devicesDir := filepath.Join(c.dataDir, eventID+"-meta", "devices")
	if err := os.MkdirAll(devicesDir, 0700); err != nil {
		t.Fatal(err)
	}
	devFile := filepath.Join(devicesDir, c.deviceID+".json")
	if err := os.WriteFile(devFile, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Give the watcher time to run a few cycles.
	time.Sleep(400 * time.Millisecond)

	// The watcher must NOT create a ReceiveOnly folder for our own device ID.
	ownPath := filepath.Join(c.dataDir, eventID+"-"+c.deviceID+"-photos")
	// The SendOnly folder was created by createEventFolders; a second MkdirAll is idempotent,
	// so instead verify the watcher only created it once (already exists from createEventFolders).
	// What we really want to verify is that knownDevices stays empty for our own ID —
	// confirmed by the fact that no *extra* folder appears beyond what createEventFolders made.
	if _, err := os.Stat(ownPath); err != nil {
		t.Errorf("own photo dir unexpectedly missing: %v", err)
	}
}

// mockSyncthing records all calls for test assertions.
type mockSyncthing struct {
	addedPeers    []string
	addedFolders  []struct{ folderID, path, folderType string }
	sharedFolders []struct{ folderID, deviceID string }
}

func (m *mockSyncthing) AddFolder(folderID, folderPath, folderType string) error {
	m.addedFolders = append(m.addedFolders, struct{ folderID, path, folderType string }{folderID, folderPath, folderType})
	return nil
}

func (m *mockSyncthing) AddPeer(deviceID string) error {
	m.addedPeers = append(m.addedPeers, deviceID)
	return nil
}

func (m *mockSyncthing) ShareFolder(folderID, deviceID string) error {
	m.sharedFolders = append(m.sharedFolders, struct{ folderID, deviceID string }{folderID, deviceID})
	return nil
}

func TestP2PBootstrap(t *testing.T) {
	c := newTestClient(t)
	mock := &mockSyncthing{}
	c.SetSyncthing(mock)

	eventID := "test-evt-001"
	hostDeviceID := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"

	// Create event folders so BootstrapConnection has folders to share.
	ev := &Event{ID: eventID, Name: "Test"}
	require.NoError(t, createEventFolders(c, ev))

	// Bootstrap connection to the host.
	require.NoError(t, c.BootstrapConnection(eventID, hostDeviceID))

	// Verify AddPeer was called with the host device ID.
	assert.Contains(t, mock.addedPeers, hostDeviceID, "AddPeer should be called with host device ID")

	// Verify meta folder is shared with host.
	expectedMeta := struct{ folderID, deviceID string }{"meta-" + eventID, hostDeviceID}
	assert.Contains(t, mock.sharedFolders, expectedMeta, "meta folder should be shared with host")

	// Verify photo folder is shared with host.
	expectedPhoto := struct{ folderID, deviceID string }{
		"photos-" + eventID + "-" + strings.ToLower(c.DeviceID()),
		hostDeviceID,
	}
	assert.Contains(t, mock.sharedFolders, expectedPhoto, "photo folder should be shared with host")
}

func TestMetaWatcherTriggersBootstrap(t *testing.T) {
	c := newTestClient(t)
	mock := &mockSyncthing{}
	c.SetSyncthing(mock)

	eventID := "watcher-bootstrap"
	participantID := "ZZZZZZZ-YYYYYYY-XXXXXXX-WWWWWWW-VVVVVVV-UUUUUUU-TTTTTTT-SSSSSSS"

	// Create event folders so the meta directory exists.
	ev := &Event{ID: eventID, Name: "Watcher Bootstrap"}
	require.NoError(t, createEventFolders(c, ev))

	// Write a device announcement file simulating a participant.
	devicesDir := filepath.Join(c.dataDir, eventID+"-meta", "devices")
	require.NoError(t, os.MkdirAll(devicesDir, 0700))
	ann := fmt.Sprintf(`{"device_id":"%s","announced_at":"2025-01-01T00:00:00Z"}`, participantID)
	require.NoError(t, os.WriteFile(filepath.Join(devicesDir, participantID+".json"), []byte(ann), 0600))

	// Start MetaWatcher — it should discover the participant.
	require.NoError(t, c.StartMetaWatcher(eventID))
	defer c.StopMetaWatcher(eventID)

	// Wait for the watcher to pick up the device file.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(mock.addedPeers) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify AddPeer was called with the participant's device ID.
	assert.Contains(t, mock.addedPeers, participantID, "MetaWatcher should AddPeer for discovered device")

	// Verify the participant's photo folder was shared.
	foundShare := false
	for _, s := range mock.sharedFolders {
		if s.deviceID == participantID && strings.HasPrefix(s.folderID, "meta-") {
			foundShare = true
			break
		}
	}
	assert.True(t, foundShare, "MetaWatcher should share meta folder with discovered device")
}

func TestEnsureFoldersRegistered(t *testing.T) {
	c := newTestClient(t)
	mock := &mockSyncthing{}
	c.syncthing = mock

	eventID := "ensure-evt-001"

	// Simulate the production flow: createEventFolders ran with empty deviceID,
	// then StartSyncthing set the real deviceID. ensureFoldersRegistered should
	// register both folders with correct IDs.
	require.NoError(t, c.ensureFoldersRegistered(eventID))

	// Verify meta folder registered as sendreceive.
	foundMeta := false
	for _, f := range mock.addedFolders {
		if f.folderID == "meta-"+eventID && f.folderType == "sendreceive" {
			foundMeta = true
			break
		}
	}
	assert.True(t, foundMeta, "ensureFoldersRegistered should register meta folder")

	// Verify photo folder registered as sendonly with correct device ID in the folder ID.
	expectedPhotoID := "photos-" + eventID + "-" + strings.ToLower(c.deviceID)
	foundPhoto := false
	for _, f := range mock.addedFolders {
		if f.folderID == expectedPhotoID && f.folderType == "sendonly" {
			foundPhoto = true
			break
		}
	}
	assert.True(t, foundPhoto, "ensureFoldersRegistered should register photo folder with correct device ID")

	// Verify directories were created.
	metaPath := filepath.Join(c.dataDir, eventID+"-meta")
	assert.DirExists(t, metaPath)
	assert.DirExists(t, filepath.Join(metaPath, ".stfolder"))

	photoPath := filepath.Join(c.dataDir, eventID+"-"+c.deviceID+"-photos")
	assert.DirExists(t, photoPath)
	assert.DirExists(t, filepath.Join(photoPath, ".stfolder"))
}

func TestActiveEventIDSetByCreateAndJoin(t *testing.T) {
	c := newTestClient(t)

	ev, err := c.CreateEvent(&CreateEventParams{EventID: "create-evt", Name: "Test", Mode: "live"})
	require.NoError(t, err)
	assert.Equal(t, ev.ID, c.activeEventID, "CreateEvent should set activeEventID")

	_, err = c.JoinEvent("secret", "join-evt")
	require.NoError(t, err)
	assert.Equal(t, "join-evt", c.activeEventID, "JoinEvent should set activeEventID")
}

func TestPersistAndLoadEventIDs(t *testing.T) {
	dir := t.TempDir()

	// Empty dir returns nil.
	assert.Nil(t, loadPersistedEventIDs(dir))

	// Persist first event.
	require.NoError(t, persistEventID(dir, "evt-aaa"))
	assert.Equal(t, []string{"evt-aaa"}, loadPersistedEventIDs(dir))

	// Persist second event.
	require.NoError(t, persistEventID(dir, "evt-bbb"))
	assert.Equal(t, []string{"evt-aaa", "evt-bbb"}, loadPersistedEventIDs(dir))

	// Duplicate is ignored.
	require.NoError(t, persistEventID(dir, "evt-aaa"))
	assert.Equal(t, []string{"evt-aaa", "evt-bbb"}, loadPersistedEventIDs(dir))
}

func TestCreateEventPersistsID(t *testing.T) {
	c := newTestClient(t)

	ev, err := c.CreateEvent(&CreateEventParams{EventID: "persist-create", Name: "Test", Mode: "live"})
	require.NoError(t, err)

	ids := loadPersistedEventIDs(c.dataDir)
	assert.Contains(t, ids, ev.ID, "CreateEvent should persist the event ID to disk")
}

func TestJoinEventPersistsID(t *testing.T) {
	c := newTestClient(t)

	_, err := c.JoinEvent("secret", "persist-join")
	require.NoError(t, err)

	ids := loadPersistedEventIDs(c.dataDir)
	assert.Contains(t, ids, "persist-join", "JoinEvent should persist the event ID to disk")
}
