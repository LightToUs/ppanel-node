package node

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/conf"
)

func TestIsMetricsInterface(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "eth0", want: true},
		{name: "ens3", want: true},
		{name: "lo", want: false},
		{name: "docker0", want: false},
		{name: "veth123", want: false},
		{name: "wg0", want: false},
		{name: "pp-egress", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isMetricsInterface(test.name); got != test.want {
				t.Fatalf("isMetricsInterface(%q) = %v, want %v", test.name, got, test.want)
			}
		})
	}
}

func TestBootIdentityFallbackIsStableForSample(t *testing.T) {
	bootTime, bootID := bootIdentity(60)
	if bootTime <= 0 {
		t.Fatalf("bootIdentity() boot time = %d", bootTime)
	}
	if bootID == "" {
		t.Fatal("bootIdentity() returned empty boot ID")
	}
}

func TestMetricsAgentDisablesReportingAfterUnsupportedResponse(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.NotFound(w, nil)
	}))
	t.Cleanup(server.Close)

	client, err := panel.NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeID:    9,
		NodeType:  "metrics",
		SecretKey: "secret",
	})
	if err != nil {
		t.Fatalf("NewClientV1() error = %v", err)
	}
	agent := NewMetricsAgent(client, nil, "")

	if err = agent.report(t.Context()); err != nil {
		t.Fatalf("first report() error = %v", err)
	}
	if err = agent.report(t.Context()); err != nil {
		t.Fatalf("second report() error = %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("metrics requests = %d, want 1", got)
	}
	if !agent.unsupported {
		t.Fatal("metrics agent was not marked unsupported")
	}
}
