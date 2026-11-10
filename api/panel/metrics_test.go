package panel

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lighttous/ppanel-node/conf"
)

func TestReportServerMetricsUsesSignedEndpoint(t *testing.T) {
	var received bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/node/metrics" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Node-Signature") == "" {
			t.Fatal("missing node signature")
		}
		received = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeID:    9,
		NodeType:  "metrics",
		SecretKey: "secret",
	})
	if err != nil {
		t.Fatalf("NewClientV1() error = %v", err)
	}
	err = client.ReportServerMetrics(t.Context(), &ServerMetricsReport{
		SchemaVersion:  1,
		AgentSessionID: "79cc3d19-65f2-4286-b501-ed836b12571e",
		Sequence:       1,
		BootID:         "boot-a",
		BootTime:       100,
		InterfaceName:  "eth0",
	})
	if err != nil {
		t.Fatalf("ReportServerMetrics() error = %v", err)
	}
	if !received {
		t.Fatal("metrics request was not received")
	}
}

func TestReportServerMetricsMapsNotFoundToUnsupported(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeID:    9,
		NodeType:  "metrics",
		SecretKey: "secret",
	})
	if err != nil {
		t.Fatalf("NewClientV1() error = %v", err)
	}

	err = client.ReportServerMetrics(t.Context(), &ServerMetricsReport{})
	if !errors.Is(err, ErrMetricsUnsupported) {
		t.Fatalf("ReportServerMetrics() error = %v, want ErrMetricsUnsupported", err)
	}
}
