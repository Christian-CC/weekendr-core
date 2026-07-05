package sushitrain

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/protocol"
)

// newTestFolder builds a minimal Folder backed by a real config.Wrapper, not
// a mock. ShareWithDevice's bug (and this fix) live entirely in how it
// mutates config.FolderConfiguration.Devices and whether that mutation
// survives Syncthing's own reflect.DeepEqual no-op check in
// config.Wrapper.Modify — a mock that doesn't replicate that check couldn't
// catch a regression here.
func newTestFolder(t *testing.T, folderID string) (*Folder, config.Wrapper) {
	t.Helper()
	myID := protocol.NewDeviceID([]byte("weekendr-test-self"))
	cfg := config.New(myID)
	cfg.Folders = append(cfg.Folders, config.FolderConfiguration{
		ID:   folderID,
		Path: t.TempDir(),
	})
	// Client.changeConfiguration calls config.Save() unconditionally after
	// every Modify (regardless of whether anything actually changed), so the
	// wrapper needs a real writable path even though this test's assertions
	// are about the in-memory CommitConfiguration/restart signal, not disk
	// persistence.
	configPath := filepath.Join(t.TempDir(), "config.xml")
	w := config.Wrap(configPath, cfg, myID, events.NoopLogger)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	client := &Client{config: w}
	return &Folder{client: client, FolderID: folderID}, w
}

// countingCommitter counts how many times Syncthing's config wrapper decided
// the configuration actually changed (i.e. did NOT take the reflect.DeepEqual
// no-op fast path in Modify/Serve). This is the same signal
// model.CommitConfiguration reacts to in the real app to decide whether to
// call restartFolder and resend ClusterConfig.
type countingCommitter struct{ commits int }

func (c *countingCommitter) CommitConfiguration(from, to config.Configuration) bool {
	c.commits++
	return true
}

func (c *countingCommitter) String() string { return "countingCommitter" }

// registerDevice mirrors what Client.AddPeer does in production: a folder-
// level device entry that prepare()'s prepareFolders → ensureExistingDevices
// step doesn't recognize (i.e. isn't also in the top-level cfg.Devices list)
// gets silently pruned when the config is next committed. The real call
// sites (SharePhotoFolderWithHub, shareReceiveOnlyFolderWithHub) always call
// AddPeer before ShareFolderEncrypted for exactly this reason.
func registerDevice(t *testing.T, fld *Folder, deviceID string) {
	t.Helper()
	if err := fld.client.AddPeer(deviceID); err != nil {
		t.Fatalf("AddPeer(%s): %v", deviceID, err)
	}
}

func devicesOf(t *testing.T, w config.Wrapper, folderID string) []config.FolderDeviceConfiguration {
	t.Helper()
	fc, ok := w.Folders()[folderID]
	if !ok {
		t.Fatalf("folder %s not found", folderID)
	}
	return fc.Devices
}

func TestShareWithDevice_UpdateInPlacePreservesOrder(t *testing.T) {
	const folderID = "test-folder"
	fld, w := newTestFolder(t, folderID)

	spy := &countingCommitter{}
	w.Subscribe(spy)
	defer w.Unsubscribe(spy)

	otherID := protocol.NewDeviceID([]byte("other-device")).String()
	hubID := protocol.NewDeviceID([]byte("hub-device")).String()
	registerDevice(t, fld, hubID)
	registerDevice(t, fld, otherID)

	// Reproduce the steady state from the field: hub shared first, another
	// peer shared second — so hub sits at index 0, NOT last.
	if err := fld.ShareWithDevice(hubID, true, "secret"); err != nil {
		t.Fatalf("share hub: %v", err)
	}
	if err := fld.ShareWithDevice(otherID, true, ""); err != nil {
		t.Fatalf("share other: %v", err)
	}

	// Syncthing's own config prepare() step also auto-appends the local
	// ("self") device to any folder's Devices list once it commits
	// (ensureDevicePresent) — so by this point there are 3 devices, not 2,
	// and hub is very likely NOT last. That's exactly the field precondition
	// that exposed the reordering bug, so assert it rather than assuming a
	// fixed device count.
	before := devicesOf(t, w, folderID)
	hubIndex := -1
	for i, d := range before {
		if d.DeviceID.String() == hubID {
			hubIndex = i
			break
		}
	}
	if hubIndex < 0 {
		t.Fatalf("setup: hub not found in %+v", before)
	}
	if hubIndex == len(before)-1 {
		t.Fatalf("setup: hub ended up last (%+v) — test no longer reproduces the ordering bug's precondition", before)
	}
	commitsBefore := spy.commits

	// Re-share the hub with the identical password. Before this fix,
	// ShareWithDevice's remove-then-append moved hub to the end of the
	// slice even though nothing semantically changed, which fails
	// Syncthing's reflect.DeepEqual no-op check and triggers a real
	// restartFolder + ClusterConfig resend on every redundant call.
	if err := fld.ShareWithDevice(hubID, true, "secret"); err != nil {
		t.Fatalf("re-share hub: %v", err)
	}

	after := devicesOf(t, w, folderID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("re-sharing with identical device+password changed the Devices slice:\nbefore: %+v\nafter:  %+v", before, after)
	}
	if spy.commits != commitsBefore {
		t.Fatalf("re-sharing with identical device+password triggered a config commit (restartFolder/ClusterConfig resend): commits went from %d to %d", commitsBefore, spy.commits)
	}
}

func TestShareWithDevice_ChangedPasswordUpdatesInPlace(t *testing.T) {
	const folderID = "test-folder"
	fld, w := newTestFolder(t, folderID)

	hubID := protocol.NewDeviceID([]byte("hub-device")).String()
	otherID := protocol.NewDeviceID([]byte("other-device")).String()
	registerDevice(t, fld, hubID)
	registerDevice(t, fld, otherID)

	if err := fld.ShareWithDevice(hubID, true, "old-secret"); err != nil {
		t.Fatalf("share hub: %v", err)
	}
	if err := fld.ShareWithDevice(otherID, true, ""); err != nil {
		t.Fatalf("share other: %v", err)
	}

	before := devicesOf(t, w, folderID)
	hubIndex := -1
	for i, d := range before {
		if d.DeviceID.String() == hubID {
			hubIndex = i
			break
		}
	}
	if hubIndex < 0 {
		t.Fatalf("setup: hub not found in %+v", before)
	}

	if err := fld.ShareWithDevice(hubID, true, "new-secret"); err != nil {
		t.Fatalf("re-share hub with new password: %v", err)
	}

	after := devicesOf(t, w, folderID)
	if len(after) != len(before) {
		t.Fatalf("password rotation changed device count: before=%+v after=%+v", before, after)
	}
	for i, d := range after {
		if i == hubIndex {
			if d.DeviceID.String() != hubID {
				t.Fatalf("hub no longer at its original index %d: %+v", hubIndex, after)
			}
			if d.EncryptionPassword != "new-secret" {
				t.Fatalf("password not updated: got %q", d.EncryptionPassword)
			}
			continue
		}
		if !reflect.DeepEqual(d, before[i]) {
			t.Fatalf("password rotation for hub disturbed another device at index %d: before=%+v after=%+v", i, before[i], d)
		}
	}
}

func TestShareWithDevice_RemoveIsNoOpWhenAbsent(t *testing.T) {
	const folderID = "test-folder"
	fld, w := newTestFolder(t, folderID)

	// Warm up: force one real commit first so the folder's Devices field has
	// gone through prepare() at least once. On a completely virgin folder,
	// FolderConfiguration.Devices is nil (never set) but
	// FolderConfiguration.Copy() always allocates a non-nil (if empty)
	// replacement slice — reflect.DeepEqual(nil, []T{}) is false, so the
	// very first commit ever would spuriously "change" the config regardless
	// of what ShareWithDevice does. That's a config.Copy() quirk on a never-
	// touched folder, not something ShareWithDevice can or needs to fix: in
	// production a folder always goes through at least one prior commit
	// (AddFolder/AddSpecialFolder) before ShareWithDevice is ever called.
	registerDevice(t, fld, protocol.NewDeviceID([]byte("warm-up-device")).String())

	spy := &countingCommitter{}
	w.Subscribe(spy)
	defer w.Unsubscribe(spy)

	absentID := protocol.NewDeviceID([]byte("never-shared")).String()

	if err := fld.ShareWithDevice(absentID, false, ""); err != nil {
		t.Fatalf("unshare absent device: %v", err)
	}
	if spy.commits != 0 {
		t.Fatalf("removing an absent device should not trigger a config commit, got %d", spy.commits)
	}
}

func TestIsSharedWithEncrypted(t *testing.T) {
	const folderID = "test-folder"
	fld, _ := newTestFolder(t, folderID)

	hubID := protocol.NewDeviceID([]byte("hub-device")).String()
	registerDevice(t, fld, hubID)

	if fld.IsSharedWithEncrypted(hubID, "secret") {
		t.Fatalf("expected false before sharing")
	}

	if err := fld.ShareWithDevice(hubID, true, "secret"); err != nil {
		t.Fatalf("share hub: %v", err)
	}

	if !fld.IsSharedWithEncrypted(hubID, "secret") {
		t.Fatalf("expected true after sharing with matching password")
	}
	if fld.IsSharedWithEncrypted(hubID, "wrong-password") {
		t.Fatalf("expected false for mismatched password")
	}
}
