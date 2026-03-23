package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

var db *sql.DB

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "weekendr.db"
	}

	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("opening database: %v", err)
	}
	defer db.Close()

	if err := migrate(db); err != nil {
		log.Fatalf("migrating database: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /events/{id}", handleGetEvent)
	mux.HandleFunc("PATCH /events/{id}", handlePatchEvent)
	mux.HandleFunc("GET /invite/{secret}", handleGetInvite)

	log.Printf("weekendr-server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// migrate applies all schema migrations.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS events (
			id                       TEXT PRIMARY KEY,
			name                     TEXT NOT NULL DEFAULT '',
			host_device_id           TEXT NOT NULL DEFAULT '',
			state                    TEXT NOT NULL DEFAULT 'upcoming',
			invite_secret            TEXT UNIQUE,
			collection_window_ends_at INTEGER DEFAULT 0,
			join_locked              INTEGER DEFAULT 0,
			created_at               INTEGER DEFAULT 0
		);
	`)
	if err != nil {
		return err
	}

	// Add join_locked column if it doesn't already exist (idempotent).
	_, _ = db.Exec(`ALTER TABLE events ADD COLUMN join_locked INTEGER DEFAULT 0`)

	return nil
}

// ---------- GET /events/:id ----------

func handleGetEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")

	var (
		name         string
		hostDeviceID string
		state        string
		joinLocked   int
	)

	err := db.QueryRow(`
		SELECT name, host_device_id, state, join_locked
		FROM events WHERE id = ?
	`, eventID).Scan(&name, &hostDeviceID, &state, &joinLocked)

	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":   "not_found",
			"message": "Event not found.",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":   "internal",
			"message": "Database error.",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"eventID":      eventID,
		"name":         name,
		"hostDeviceID": hostDeviceID,
		"state":        state,
		"joinLocked":   joinLocked == 1,
	})
}

// ---------- PATCH /events/:id ----------

type patchEventRequest struct {
	JoinLocked *bool `json:"joinLocked"`
}

func handlePatchEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	deviceID := r.Header.Get("X-Device-ID")
	if deviceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":   "unauthorized",
			"message": "Missing X-Device-ID header.",
		})
		return
	}

	var body patchEventRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "bad_request",
			"message": "Invalid JSON body.",
		})
		return
	}

	// Verify the caller is the host.
	var hostDeviceID string
	err := db.QueryRow(`SELECT host_device_id FROM events WHERE id = ?`, eventID).Scan(&hostDeviceID)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":   "not_found",
			"message": "Event not found.",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":   "internal",
			"message": "Database error.",
		})
		return
	}

	if hostDeviceID != deviceID {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "forbidden",
			"message": "Only the host can modify this event.",
		})
		return
	}

	if body.JoinLocked != nil {
		val := 0
		if *body.JoinLocked {
			val = 1
		}
		if _, err := db.Exec(`UPDATE events SET join_locked = ? WHERE id = ?`, val, eventID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error":   "internal",
				"message": "Failed to update event.",
			})
			return
		}
	}

	// Read back current state.
	var joinLocked int
	_ = db.QueryRow(`SELECT join_locked FROM events WHERE id = ?`, eventID).Scan(&joinLocked)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"joinLocked": joinLocked == 1,
	})
}

// ---------- GET /invite/:secret ----------

func handleGetInvite(w http.ResponseWriter, r *http.Request) {
	secret := r.PathValue("secret")

	var (
		eventID              string
		name                 string
		hostDeviceID         string
		state                string
		collectionWindowEnds int64
		joinLocked           int
	)

	err := db.QueryRow(`
		SELECT id, name, host_device_id, state, collection_window_ends_at, join_locked
		FROM events WHERE invite_secret = ?
	`, secret).Scan(&eventID, &name, &hostDeviceID, &state, &collectionWindowEnds, &joinLocked)

	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":   "not_found",
			"message": "Invalid invite link.",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":   "internal",
			"message": "Database error.",
		})
		return
	}

	// Check join eligibility in priority order.

	// a) Event ended or archived.
	if state == "ended" || state == "archived" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "event_ended",
			"message": "This event has already ended.",
		})
		return
	}

	// b) Collection window expired.
	if state == "collecting" && collectionWindowEnds > 0 && collectionWindowEnds < time.Now().Unix() {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "collection_expired",
			"message": "The collection window for this event has closed.",
		})
		return
	}

	// c) Host locked joins.
	if joinLocked == 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "join_locked",
			"message": "The host has locked this event.",
		})
		return
	}

	// All clear — return normal invite response.
	writeJSON(w, http.StatusOK, map[string]any{
		"eventID":      eventID,
		"name":         name,
		"hostDeviceID": hostDeviceID,
		"state":        state,
	})
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
