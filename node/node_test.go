package node

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/conf"
)

func TestNodeCloseIgnoresUnstartedControllers(t *testing.T) {
	n := &Node{
		controllers: []*Controller{
			{},
			nil,
			{},
		},
	}

	if err := n.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if n.controllers != nil {
		t.Fatalf("controllers = %#v, want nil after close", n.controllers)
	}
}

func TestNodeStartRequiresInitialMetricsDirective(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
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
	agent := NewMetricsAgent(client, nil, "eth0")
	agent.readCounters = func(string) (uint64, uint64, error) {
		return 100, 200, nil
	}
	n := &Node{metricsAgent: agent}

	if err = n.Start(); err == nil {
		t.Fatal("Start() accepted a failed initial traffic budget sync")
	}
	if n.metricsAgent.reportTask != nil || n.metricsAgent.guardTask != nil {
		t.Fatal("periodic metrics tasks started before the initial directive")
	}
}

func TestNormalizeProtocolForRuntimeHysteria(t *testing.T) {
	protocol := panel.Protocol{
		Type:     "hy2",
		CertMode: "none",
	}

	normalizeProtocolForRuntime(&protocol)

	if protocol.Type != "hy2" {
		t.Fatalf("type = %q, want original alias hy2", protocol.Type)
	}
	if protocol.Security != "tls" {
		t.Fatalf("security = %q, want tls", protocol.Security)
	}
	if protocol.CertMode != "self" {
		t.Fatalf("cert mode = %q, want self", protocol.CertMode)
	}
}

func TestNewKeepsFeaturesDisabledForLegacyServerResponse(t *testing.T) {
	protocols := []panel.Protocol{{
		Type:   "vless",
		Enable: true,
	}}
	serverConfig := &panel.ServerConfigResponse{
		Data: &panel.Data{
			Protocols: &protocols,
		},
	}
	runtimeConfig := &conf.Conf{
		ApiConfig: conf.ServerApiConfig{
			ApiHost:   "https://panel.example.com",
			ServerId:  49,
			SecretKey: "node-secret",
		},
	}

	node, err := New(nil, runtimeConfig, serverConfig)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if len(node.controllers) != 1 {
		t.Fatalf("controller count = %d, want 1", len(node.controllers))
	}
	info := node.controllers[0].info
	if info.AccessAuditEnabled {
		t.Fatal("legacy response unexpectedly enabled access audit")
	}
	if mode := normalizedPresenceMode(info.PresenceMode); mode != "disabled" {
		t.Fatalf("legacy response presence mode = %q, want disabled", mode)
	}
	if ttl := presenceLeaseTTL(info.PresenceLeaseTTL); ttl != 90*time.Second {
		t.Fatalf("legacy response presence TTL = %s, want 90s", ttl)
	}
}

func TestNewCreatesRelayAgentWithoutUserControllers(t *testing.T) {
	protocols := make([]panel.Protocol, 0)
	serverConfig := &panel.ServerConfigResponse{
		Data: &panel.Data{
			ServerType:   "relay_egress",
			PushInterval: 30,
			Protocols:    &protocols,
		},
	}
	runtimeConfig := &conf.Conf{
		ApiConfig: conf.ServerApiConfig{
			ApiHost:   "https://panel.example.com",
			ServerId:  57,
			SecretKey: "node-secret",
		},
		StatePath: t.TempDir(),
	}

	relayNode, err := New(nil, runtimeConfig, serverConfig)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if relayNode.relayAgent == nil {
		t.Fatal("relay agent was not created")
	}
	if len(relayNode.controllers) != 0 {
		t.Fatalf("relay controllers = %d, want 0", len(relayNode.controllers))
	}
}
