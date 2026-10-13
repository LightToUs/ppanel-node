package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lighttous/ppanel-node/conf"
)

func TestReportUserTrafficIncludesBatchID(t *testing.T) {
	const batchID = "4a7775f0-a230-43a8-874d-e252b6706e77"
	var received ServerPushUserTrafficRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/server/push" {
			http.NotFound(writer, request)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeType:  "hysteria2",
		NodeID:    49,
		SecretKey: "node-secret",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	traffic := []UserTraffic{{UID: 42, Upload: 100, Download: 200}}
	if err = client.ReportUserTraffic(t.Context(), batchID, &traffic); err != nil {
		t.Fatalf("ReportUserTraffic() error = %v", err)
	}
	if received.BatchID != batchID {
		t.Fatalf("batch_id = %q, want %q", received.BatchID, batchID)
	}
	if len(received.Traffic) != 1 || received.Traffic[0] != traffic[0] {
		t.Fatalf("traffic = %#v, want %#v", received.Traffic, traffic)
	}
}

func TestReportUserTrafficRejectsBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":500,"msg":"enqueue failed"}`))
	}))
	defer server.Close()

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeType:  "hysteria2",
		NodeID:    49,
		SecretKey: "node-secret",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	traffic := []UserTraffic{{UID: 42, Upload: 100, Download: 200}}
	err = client.ReportUserTraffic(
		t.Context(),
		"4a7775f0-a230-43a8-874d-e252b6706e77",
		&traffic,
	)
	if err == nil {
		t.Fatal("ReportUserTraffic() accepted a business error response")
	}
}
