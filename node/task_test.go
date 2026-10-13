package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/conf"
	vCore "github.com/lighttous/ppanel-node/core"
)

func TestCompareUserListDetectsUpdatedUser(t *testing.T) {
	oldUsers := []panel.UserInfo{
		{Id: 1, Uuid: "u1", SpeedLimit: 10, DeviceLimit: 1},
		{Id: 2, Uuid: "u2", SpeedLimit: 20, DeviceLimit: 2},
	}
	newUsers := []panel.UserInfo{
		{Id: 1, Uuid: "u1", SpeedLimit: 10, DeviceLimit: 3},
		{Id: 3, Uuid: "u3", SpeedLimit: 30, DeviceLimit: 1},
	}

	deleted, added, updated := compareUserList(oldUsers, newUsers)
	if len(deleted) != 1 || deleted[0].Uuid != "u2" {
		t.Fatalf("deleted = %#v, want only u2", deleted)
	}
	if len(added) != 1 || added[0].Uuid != "u3" {
		t.Fatalf("added = %#v, want only u3", added)
	}
	if len(updated) != 1 || updated[0].Uuid != "u1" || updated[0].DeviceLimit != 3 {
		t.Fatalf("updated = %#v, want u1 device_limit=3", updated)
	}
}

func TestReportUserTrafficRetriesSameBatch(t *testing.T) {
	var requests []panel.ServerPushUserTrafficRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body panel.ServerPushUserTrafficRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		requests = append(requests, body)
		if len(requests) == 1 {
			http.Error(writer, "response unavailable", http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := panel.NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeType:  "hysteria2",
		NodeID:    49,
		SecretKey: "node-secret",
	})
	if err != nil {
		t.Fatalf("new panel client: %v", err)
	}
	batch := &vCore.UserTrafficBatch{
		BatchID: "4a7775f0-a230-43a8-874d-e252b6706e77",
		Traffic: []panel.UserTraffic{{UID: 42, Upload: 100, Download: 200}},
	}
	store, err := openTrafficBatchStore(t.TempDir(), 49, "hysteria2")
	if err != nil {
		t.Fatalf("open traffic batch store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.Put(batch); err != nil {
		t.Fatalf("persist traffic batch: %v", err)
	}
	controller := &Controller{
		apiClient:           client,
		tag:                 "node-tag",
		pendingTrafficBatch: batch,
		trafficBatches:      store,
	}

	controller.reportUserTraffic(t.Context(), 0)
	if controller.pendingTrafficBatch != batch {
		t.Fatal("failed report did not retain the original batch")
	}
	controller.reportUserTraffic(t.Context(), 0)
	if controller.pendingTrafficBatch != nil {
		t.Fatal("successful retry did not clear the pending batch")
	}
	stored, err := store.Load()
	if err != nil {
		t.Fatalf("load acknowledged batch: %v", err)
	}
	if stored != nil {
		t.Fatalf("acknowledged batch remains in WAL: %#v", stored)
	}
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if requests[0].BatchID != requests[1].BatchID || requests[0].BatchID != batch.BatchID {
		t.Fatalf("batch IDs = %q, %q; want %q", requests[0].BatchID, requests[1].BatchID, batch.BatchID)
	}
	if len(requests[0].Traffic) != 1 || len(requests[1].Traffic) != 1 ||
		requests[0].Traffic[0] != requests[1].Traffic[0] {
		t.Fatalf("retry payload changed: first=%#v second=%#v", requests[0], requests[1])
	}
}
