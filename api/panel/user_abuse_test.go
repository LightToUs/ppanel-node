package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lighttous/ppanel-node/conf"
)

func TestReportAbuseAggregatesIncludesManagedInstanceIdentity(t *testing.T) {
	var got AbuseAggregateBody
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":0,"data":{"accepted":1}}`))
	}))
	defer server.Close()

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:           server.URL,
		NodeType:          "hysteria2",
		NodeID:            49,
		ManagedInstanceID: 73,
		SecretKey:         "node-secret",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	err = client.ReportAbuseAggregates(t.Context(), []AbuseAggregate{{
		EventID:     "c4de18e5-94e3-43aa-bff4-431945ce5755",
		UID:         42,
		Category:    "smtp",
		Protocol:    "hysteria2",
		Count:       1,
		BucketStart: 1_700_000_000_000,
		FirstSeenAt: 1_700_000_000_000,
		LastSeenAt:  1_700_000_000_000,
	}})
	if err != nil {
		t.Fatalf("report abuse aggregates: %v", err)
	}
	if got.ManagedInstanceID != 73 {
		t.Fatalf("managed instance ID = %d, want 73", got.ManagedInstanceID)
	}
}

func TestReportAbuseAggregatesRejectsBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":500,"msg":"Internal Server Error"}`))
	}))
	defer server.Close()

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeType:  "wireguard",
		NodeID:    49,
		SecretKey: "node-secret",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	err = client.ReportAbuseAggregates(t.Context(), []AbuseAggregate{{
		EventID:     "c4de18e5-94e3-43aa-bff4-431945ce5756",
		UID:         42,
		Category:    "private-network",
		Protocol:    "wireguard",
		Count:       1,
		BucketStart: 1_700_000_000_000,
		FirstSeenAt: 1_700_000_000_000,
		LastSeenAt:  1_700_000_000_000,
	}})
	if err == nil {
		t.Fatal("expected business response error")
	}
}
