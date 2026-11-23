package node

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestTrafficSuspensionPersistsAcrossAgentRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traffic-suspended")
	t.Setenv("PPANEL_TRAFFIC_SUSPENSION_FILE", path)
	t.Setenv(
		"PPANEL_TRAFFIC_BUDGET_FILE",
		filepath.Join(dir, "traffic-budget.json"),
	)

	agent := NewMetricsAgent(nil, nil, "")
	agent.setTrafficSuspended(true)
	if !trafficSuspensionPersisted() {
		t.Fatal("traffic suspension marker was not persisted")
	}

	_ = NewMetricsAgent(nil, nil, "")
	if !trafficSuspensionPersisted() {
		t.Fatal("traffic suspension marker was not restored")
	}

	agent.setTrafficSuspended(false)
	if trafficSuspensionPersisted() {
		t.Fatal("traffic suspension marker was not cleared")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("traffic suspension marker still exists: %v", err)
	}
}

func TestManagedInstanceStartsFailClosedUntilBudgetArrives(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(
		"PPANEL_TRAFFIC_SUSPENSION_FILE",
		filepath.Join(dir, "traffic-suspended"),
	)
	t.Setenv(
		"PPANEL_TRAFFIC_BUDGET_FILE",
		filepath.Join(dir, "traffic-budget.json"),
	)

	agent := NewMetricsAgent(
		&panel.ClientV1{ManagedInstanceID: 42},
		nil,
		"",
	)
	if !trafficSuspensionPersisted() {
		t.Fatal("managed instance did not start with traffic suspended")
	}

	directive := &panel.ServerTrafficBudgetDirective{
		Enabled:        true,
		Mode:           "enforce",
		Action:         "none",
		LimitBytes:     30_000,
		UsedBytes:      3_000,
		SuspendAtBytes: 27_000,
	}
	agent.applyBudgetDirective(directive, "eth0", 100, 200)
	if trafficSuspensionPersisted() {
		t.Fatal("fresh enforceable budget did not release startup suspension")
	}
	persisted, err := loadTrafficBudgetDirective()
	if err != nil {
		t.Fatalf("loadTrafficBudgetDirective() error = %v", err)
	}
	if persisted.SuspendAtBytes != directive.SuspendAtBytes {
		t.Fatalf(
			"persisted suspend threshold = %d, want %d",
			persisted.SuspendAtBytes,
			directive.SuspendAtBytes,
		)
	}

	agent.budgetMu.Lock()
	agent.lastBudgetReportAt = time.Now().Add(-metricsFreshnessLimit - time.Second)
	agent.budgetMu.Unlock()
	if err = agent.enforceBudget(context.Background()); err != nil {
		t.Fatalf("enforceBudget() error = %v", err)
	}
	if !trafficSuspensionPersisted() {
		t.Fatal("stale managed budget did not suspend traffic")
	}
	agent.applyBudgetDirective(directive, "eth0", 100, 200)

	agent.suspendManagedTrafficOnMetricsFailure(
		errors.New("control plane unavailable"),
	)
	if !trafficSuspensionPersisted() {
		t.Fatal("managed metrics failure did not suspend traffic")
	}

	_ = NewMetricsAgent(nil, nil, "")
	if !trafficSuspensionPersisted() {
		t.Fatal("persisted budget did not restore fail-closed startup")
	}
}

func TestLocalBudgetUsageFailsClosedOnOverflow(t *testing.T) {
	if _, err := localBudgetUsage(math.MaxUint64-1, 1, 1); err == nil {
		t.Fatal("localBudgetUsage() accepted an overflowing total")
	}
	got, err := localBudgetUsage(1_000, 200, 300)
	if err != nil {
		t.Fatalf("localBudgetUsage() error = %v", err)
	}
	if got != 1_500 {
		t.Fatalf("localBudgetUsage() = %d, want 1500", got)
	}
}
