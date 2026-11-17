package node

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		NodeType:  "hysteria2",
		SecretKey: "secret",
	})
	if err != nil {
		t.Fatalf("NewClientV1() error = %v", err)
	}
	agent := NewMetricsAgent(client, nil, "")
	agent.interfaceOverride = "eth0"
	agent.readCounters = func(string) (uint64, uint64, error) {
		return 100, 200, nil
	}

	if err = agent.Prime(t.Context()); err != nil {
		t.Fatalf("Prime() error = %v", err)
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

func TestMetricsAgentPrimeReturnsControlPlaneFailure(t *testing.T) {
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

	if err = agent.Prime(t.Context()); err == nil {
		t.Fatal("Prime() accepted a failed initial control-plane response")
	}
}

func TestMetricsAgentWarningRateLimitIsConcurrentSafe(t *testing.T) {
	agent := NewMetricsAgent(nil, nil, "")
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 100 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			agent.warnRateLimited("shared", "test warning", errors.New("test"))
		}()
	}
	close(start)
	workers.Wait()

	if got := len(agent.warningAt); got != 1 {
		t.Fatalf("warning entries = %d, want 1", got)
	}
}

func TestMetricsAgentStaleGuardCannotOverrideNewDirective(t *testing.T) {
	agent := NewMetricsAgent(nil, nil, "")
	agent.budgetDirective = &panel.ServerTrafficBudgetDirective{
		Enabled:        true,
		Mode:           "enforce",
		Action:         "none",
		UsedBytes:      9,
		SuspendAtBytes: 10,
	}
	agent.budgetGeneration = 1
	agent.budgetInterface = "eth0"

	counterStarted := make(chan struct{})
	releaseCounter := make(chan struct{})
	agent.readCounters = func(string) (uint64, uint64, error) {
		close(counterStarted)
		<-releaseCounter
		return 0, 1, nil
	}
	var callsMu sync.Mutex
	var calls []bool
	agent.setSuspended = func(suspended bool) {
		callsMu.Lock()
		calls = append(calls, suspended)
		callsMu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		_ = agent.enforceBudget(t.Context())
		close(done)
	}()
	select {
	case <-counterStarted:
	case <-time.After(time.Second):
		t.Fatal("guard did not start reading counters")
	}
	agent.applyBudgetDirective(
		&panel.ServerTrafficBudgetDirective{Enabled: false, Action: "none"},
		"eth0",
		0,
		0,
	)
	close(releaseCounter)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("guard did not finish")
	}

	callsMu.Lock()
	defer callsMu.Unlock()
	if len(calls) != 1 || calls[0] {
		t.Fatalf("suspension calls = %v, want only resume from the new directive", calls)
	}
}
