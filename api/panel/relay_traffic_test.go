package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lighttous/ppanel-node/conf"
)

func TestReportRelayTrafficUsesDedicatedEndpoint(t *testing.T) {
	var received RelayTrafficReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/server/relay/traffic" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("protocol") != "relay_egress" {
			t.Fatalf("protocol = %q", r.URL.Query().Get("protocol"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":""}`))
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
	report := &RelayTrafficReport{
		InterfaceName: "pp-egress",
		SessionID:     "4cbba9e1-abce-4f29-85de-570b30789786",
		Sequence:      7,
		UploadTotal:   100,
		DownloadTotal: 200,
		ReportedAt:    1234,
	}
	if err = client.ReportRelayTraffic(t.Context(), report); err != nil {
		t.Fatalf("ReportRelayTraffic() error = %v", err)
	}
	if received != *report {
		t.Fatalf("received = %#v, want %#v", received, *report)
	}
}
