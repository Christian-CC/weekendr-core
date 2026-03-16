package weekendr

import (
	"os"
	"regexp"
	"testing"
)

var syncthingIDRe = regexp.MustCompile(`^([A-Z2-7]{7}-){7}[A-Z2-7]{7}$`)

func TestLoadOrCreateDeviceID(t *testing.T) {
	dir := t.TempDir()

	// (1) First call: ID is created and persisted.
	id1, err := loadOrCreateDeviceID(dir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if id1 == "" {
		t.Fatal("first call returned empty ID")
	}
	if _, err := os.Stat(dir + "/device_id.json"); err != nil {
		t.Fatalf("device_id.json not created: %v", err)
	}

	// (2) Second call with same dataDir returns the same ID.
	id2, err := loadOrCreateDeviceID(dir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("ID changed between calls: %q → %q", id1, id2)
	}

	// (3) ID matches Syncthing format: 8 groups of 7 chars separated by hyphens = 63 chars total.
	if len(id1) != 63 {
		t.Fatalf("ID length: want 63 (8×7 chars + 7 hyphens), got %d: %q", len(id1), id1)
	}
	if !syncthingIDRe.MatchString(id1) {
		t.Fatalf("ID does not match Syncthing format: %q", id1)
	}
}
