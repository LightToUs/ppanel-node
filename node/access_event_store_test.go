package node

import (
	"path/filepath"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
)

func TestAccessEventStorePersistsUntilAcknowledged(t *testing.T) {
	statePath := t.TempDir()
	event := panel.AccessEvent{
		EventID:     "a3d7f493-cc2d-49a4-941f-06af9bafaa73",
		UID:         42,
		ClientIP:    "203.0.113.8",
		ConnectedAt: 1_700_000_000_000,
	}

	store, err := openAccessEventStore(statePath, 49, "hysteria2")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err = store.Put(event); err != nil {
		t.Fatalf("put event: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	store, err = openAccessEventStore(statePath, 49, "hysteria2")
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := store.List(500)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0] != event {
		t.Fatalf("events = %#v, want %#v", events, event)
	}
	if err = store.Delete(events); err != nil {
		t.Fatalf("delete events: %v", err)
	}
	events, err = store.List(500)
	if err != nil {
		t.Fatalf("list acknowledged events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("acknowledged events remain: %#v", events)
	}

	wantPath := filepath.Join(statePath, "access-events", "49-hysteria2.db")
	if store.db.Path() != wantPath {
		t.Fatalf("database path = %q, want %q", store.db.Path(), wantPath)
	}
}

func TestAccessEventStorePutIsIdempotent(t *testing.T) {
	store, err := openAccessEventStore(t.TempDir(), 49, "vless")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	event := panel.AccessEvent{
		EventID:     "be3d7796-2c71-40dc-8ea2-7116849a52fe",
		UID:         7,
		ClientIP:    "2001:db8::1",
		ConnectedAt: 1_700_000_000_000,
	}
	if err = store.Put(event); err != nil {
		t.Fatalf("put event: %v", err)
	}
	if err = store.Put(event); err != nil {
		t.Fatalf("put duplicate event: %v", err)
	}
	events, err := store.List(500)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
}
