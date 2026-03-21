package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Christian-CC/weekendr-core/weekendr"
)

func main() {
	role := flag.String("role", "host", "host or guest")
	dataDir := flag.String("data", "", "data directory (auto-created if empty)")
	eventID := flag.String("event", "", "event ID (required for guest)")
	hostDeviceID := flag.String("host-device", "", "host device ID (required for guest)")
	flag.Parse()

	if *dataDir == "" {
		dir, _ := os.MkdirTemp("", "weekendr-"+*role+"-*")
		*dataDir = dir
		defer os.RemoveAll(dir)
	}

	log.Printf("[%s] Using dataDir: %s", *role, *dataDir)

	client, err := weekendr.NewClient(*dataDir)
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}

	if *role == "host" {
		runHost(client, *dataDir)
	} else {
		if *eventID == "" || *hostDeviceID == "" {
			log.Fatal("guest requires --event and --host-device flags")
		}
		runGuest(client, *dataDir, *eventID, *hostDeviceID)
	}
}

func runHost(client *weekendr.Client, dataDir string) {
	// Create event
	params := &weekendr.CreateEventParams{
		EventID: "test-p2p-001",
		Name:    "P2P Test Event",
		Mode:    "live",
	}
	event, err := client.CreateEvent(params)
	if err != nil {
		log.Fatalf("CreateEvent: %v", err)
	}
	log.Printf("[host] Created event: %s", event.ID)

	// Start Syncthing
	log.Printf("[host] Starting Syncthing...")
	if err := client.StartSyncthing(dataDir); err != nil {
		log.Fatalf("StartSyncthing: %v", err)
	}
	log.Printf("[host] Waiting for Syncthing to be ready (up to 60s)...")
	if !client.WaitForSyncthing(60) {
		log.Fatal("Syncthing not ready after 60s")
	}
	log.Printf("[host] Syncthing ready — device ID: %s", client.DeviceID())

	// Start meta watcher so the host auto-discovers the guest
	if err := client.StartMetaWatcher(event.ID); err != nil {
		log.Fatalf("StartMetaWatcher: %v", err)
	}

	// Announce ourselves in the meta folder
	if err := client.AnnounceDevice(event.ID); err != nil {
		log.Fatalf("AnnounceDevice: %v", err)
	}

	log.Printf("[host] ===================================")
	log.Printf("[host] Run guest with:")
	log.Printf("[host]   --role=guest --event=%s --host-device=%s", event.ID, client.DeviceID())
	log.Printf("[host] ===================================")
	log.Printf("[host] Waiting 120s for guest to join...")

	// Wait for guest, then check meta folder
	time.Sleep(120 * time.Second)

	devicesDir := filepath.Join(dataDir, "meta-"+event.ID, "devices")
	entries, err := os.ReadDir(devicesDir)
	if err != nil {
		log.Printf("[host] Could not read devices dir: %v", err)
	} else {
		log.Printf("[host] Found %d device files:", len(entries))
		for _, e := range entries {
			log.Printf("  - %s", e.Name())
		}
		if len(entries) > 1 {
			log.Printf("[host] P2P bootstrap successful!")
		} else {
			log.Printf("[host] No guest device file received")
		}
	}

	// Keep alive for further observation
	log.Printf("[host] Keeping alive for 60s...")
	time.Sleep(60 * time.Second)
}

func runGuest(client *weekendr.Client, dataDir, eventID, hostDeviceID string) {
	// Join event
	event, err := client.JoinEvent("test-secret", eventID)
	if err != nil {
		log.Fatalf("JoinEvent: %v", err)
	}
	log.Printf("[guest] Joined event: %s", event.ID)

	// Start Syncthing
	if err := client.StartSyncthing(dataDir); err != nil {
		log.Fatalf("StartSyncthing: %v", err)
	}
	if !client.WaitForSyncthing(30) {
		log.Fatal("Syncthing not ready after 30s")
	}
	log.Printf("[guest] Syncthing ready — device ID: %s", client.DeviceID())

	// Wait for relay connection to establish before bootstrapping
	log.Printf("[guest] Waiting 10s for relay connection...")
	time.Sleep(10 * time.Second)

	// Bootstrap connection to host
	log.Printf("[guest] Bootstrapping connection to host...")
	if err := client.BootstrapConnection(eventID, hostDeviceID); err != nil {
		log.Fatalf("BootstrapConnection: %v", err)
	}
	log.Printf("[guest] Bootstrap complete")

	// Announce device
	if err := client.AnnounceDevice(eventID); err != nil {
		log.Fatalf("AnnounceDevice: %v", err)
	}
	log.Printf("[guest] Announced device in meta folder")

	// Start meta watcher so guest discovers host too
	if err := client.StartMetaWatcher(eventID); err != nil {
		log.Fatalf("StartMetaWatcher: %v", err)
	}

	// Keep running while logging status
	log.Printf("[guest] Waiting for sync...")
	for i := 0; i < 12; i++ {
		time.Sleep(10 * time.Second)
		log.Printf("[guest] Still running... (%ds)", (i+1)*10)
	}
	log.Printf("[guest] Done — check host terminal for results")
}
