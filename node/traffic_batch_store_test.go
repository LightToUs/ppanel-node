package node

import (
	"path/filepath"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
	vCore "github.com/lighttous/ppanel-node/core"
)

func TestTrafficBatchStorePersistsUntilAcknowledged(t *testing.T) {
	statePath := t.TempDir()
	batch := &vCore.UserTrafficBatch{
		BatchID: "4a7775f0-a230-43a8-874d-e252b6706e77",
		Traffic: []panel.UserTraffic{{UID: 42, Upload: 100, Download: 200}},
	}
	store, err := openTrafficBatchStore(statePath, 49, "hysteria2")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err = store.Put(batch); err != nil {
		t.Fatalf("put batch: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	store, err = openTrafficBatchStore(statePath, 49, "hysteria2")
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restored, err := store.Load()
	if err != nil {
		t.Fatalf("load batch: %v", err)
	}
	if restored == nil || restored.BatchID != batch.BatchID ||
		len(restored.Traffic) != 1 || restored.Traffic[0] != batch.Traffic[0] {
		t.Fatalf("restored batch = %#v, want %#v", restored, batch)
	}
	if err = store.Delete(batch.BatchID); err != nil {
		t.Fatalf("delete batch: %v", err)
	}
	restored, err = store.Load()
	if err != nil {
		t.Fatalf("load acknowledged batch: %v", err)
	}
	if restored != nil {
		t.Fatalf("acknowledged batch remains: %#v", restored)
	}

	wantPath := filepath.Join(statePath, "traffic-batches", "49-hysteria2.db")
	if store.db.Path() != wantPath {
		t.Fatalf("database path = %q, want %q", store.db.Path(), wantPath)
	}
}

func TestTrafficBatchStoreRejectsMismatchedAcknowledgement(t *testing.T) {
	store, err := openTrafficBatchStore(t.TempDir(), 49, "vless")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	batch := &vCore.UserTrafficBatch{
		BatchID: "4a7775f0-a230-43a8-874d-e252b6706e77",
		Traffic: []panel.UserTraffic{{UID: 42, Upload: 100, Download: 200}},
	}
	if err = store.Put(batch); err != nil {
		t.Fatalf("put batch: %v", err)
	}
	otherBatch := &vCore.UserTrafficBatch{
		BatchID: "f5447719-80ce-4090-81d3-a27a54f157d0",
		Traffic: []panel.UserTraffic{{UID: 7, Upload: 1, Download: 2}},
	}
	if err = store.Put(otherBatch); err == nil {
		t.Fatal("Put() overwrote a pending batch")
	}
	if err = store.Delete("f5447719-80ce-4090-81d3-a27a54f157d0"); err == nil {
		t.Fatal("Delete() accepted a different batch ID")
	}
	restored, err := store.Load()
	if err != nil || restored == nil {
		t.Fatalf("batch was removed after mismatched acknowledgement: %#v, %v", restored, err)
	}
}
