package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lighttous/ppanel-node/conf"
)

func TestReportNodeStatusChecksBusinessCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":500,"msg":"update failed"}`))
	}))
	defer server.Close()

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeID:    57,
		NodeType:  "relay_egress",
		SecretKey: "secret",
	})
	if err != nil {
		t.Fatalf("NewClientV1() error = %v", err)
	}
	err = client.ReportNodeStatus(&NodeStatus{})
	if err == nil || !strings.Contains(err.Error(), "update failed") {
		t.Fatalf("ReportNodeStatus() error = %v; want business error", err)
	}
}
