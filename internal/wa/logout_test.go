package wa

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestDeleteSessionRemovesPersistedDeviceAndKeysWithoutConnection(t *testing.T) {
	path := newPairedSessionStore(t)
	c, err := New(Options{StorePath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.client.Store.Sessions.PutSession(t.Context(), "fixture-peer", []byte("fixture-key")); err != nil {
		t.Fatal(err)
	}
	if !c.IsAuthed() || c.IsConnected() {
		t.Fatal("fixture must contain an offline retained device")
	}
	if err := c.DeleteSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	devices, err := c.container.GetAllDevices(t.Context())
	if err != nil || len(devices) != 0 || c.IsAuthed() {
		t.Fatalf("devices after cleanup = %v, %v", devices, err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var keys int
	if err := db.QueryRow("SELECT count(*) FROM whatsmeow_sessions").Scan(&keys); err != nil || keys != 0 {
		t.Fatalf("session keys after cleanup = %d, %v", keys, err)
	}
	c.Close()
	fresh, err := New(Options{StorePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := fresh.client.GetQRChannel(ctx); err != nil {
		t.Fatalf("reopened client cannot start pairing: %v", err)
	}
	if err := fresh.DeleteSession(t.Context()); err != nil {
		t.Fatalf("already absent device: %v", err)
	}
}

func TestDeleteSessionPersistenceFailureRetainsDevice(t *testing.T) {
	path := newPairedSessionStore(t)
	c, err := New(Options{StorePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER refuse_device_delete BEFORE DELETE ON whatsmeow_device BEGIN SELECT RAISE(ABORT, 'fixture deletion refused'); END`); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSession(t.Context()); err == nil || !strings.Contains(err.Error(), "fixture deletion refused") {
		t.Fatalf("DeleteSession = %v, want persistence failure", err)
	}
	devices, err := c.container.GetAllDevices(t.Context())
	if err != nil || len(devices) != 1 || !c.IsAuthed() {
		t.Fatalf("device lost after failed cleanup: %v, %v", devices, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.DeleteSession(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteSession = %v, want cancellation", err)
	}
	c.Close()
	if err := c.DeleteSession(t.Context()); err == nil {
		t.Fatal("closed client reported successful cleanup")
	}
}
