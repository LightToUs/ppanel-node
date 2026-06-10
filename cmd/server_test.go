package cmd

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/conf"
	"github.com/lighttous/ppanel-node/core"
	"github.com/lighttous/ppanel-node/node"
)

func restoreRuntimeHooks() {
	prepareRuntimeFn = prepareRuntime
	startRuntimeFn = startRuntime
	shutdownRuntimeFn = shutdownRuntime
	rollbackRuntimeFn = rollbackRuntime
	configureLoggingFn = configureLogging
}

func newTestRuntime() (*node.Node, *core.XrayCore) {
	nodes := new(node.Node)
	xcore := new(core.XrayCore)
	xcore.Config = conf.New()
	xcore.ServerConfig = &panel.ServerConfigResponse{
		Data: &panel.Data{Total: 1},
	}
	xcore.ReloadCh = make(chan struct{}, 1)
	return nodes, xcore
}

func writeTestConfig(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "ppnode-config-*.yml")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer f.Close()
	content := "Api:\n  ApiHost: https://example.com\n  ServerID: 1\n  SecretKey: test\n"
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	return f.Name()
}

func TestReloadContinuesWhenOldShutdownFails(t *testing.T) {
	defer restoreRuntimeHooks()
	configPath := writeTestConfig(t)

	oldNodes, oldCore := newTestRuntime()
	newNodes, newCore := newTestRuntime()
	var shutdownCalls int
	var startCalls int
	configureCalled := false

	prepareRuntimeFn = func(*conf.Conf) (*core.XrayCore, *node.Node, *panel.ServerConfigResponse, error) {
		return newCore, newNodes, &panel.ServerConfigResponse{
			Data: &panel.Data{Total: 2},
		}, nil
	}
	shutdownRuntimeFn = func(nodes *node.Node, xraycore *core.XrayCore) error {
		shutdownCalls++
		if nodes == oldNodes && xraycore == oldCore {
			return errors.New("old shutdown failed")
		}
		return nil
	}
	startRuntimeFn = func(nodesCore *core.XrayCore, nodesValue *node.Node, serverconfig *panel.ServerConfigResponse) error {
		startCalls++
		if nodesCore != newCore || nodesValue != newNodes || serverconfig.Data.Total != 2 {
			t.Fatalf("unexpected runtime start arguments: %#v %#v %#v", nodesCore, nodesValue, serverconfig)
		}
		return nil
	}
	configureLoggingFn = func(*conf.Conf) {
		configureCalled = true
	}

	if err := reload(configPath, &oldNodes, &oldCore); err != nil {
		t.Fatalf("reload() error = %v, want nil", err)
	}
	if oldNodes != newNodes || oldCore != newCore {
		t.Fatalf("reload() did not swap to new runtime")
	}
	if shutdownCalls != 1 {
		t.Fatalf("shutdown calls = %d, want 1", shutdownCalls)
	}
	if startCalls != 1 {
		t.Fatalf("start calls = %d, want 1", startCalls)
	}
	if !configureCalled {
		t.Fatalf("configureLogging should be called after successful reload")
	}
}

func TestReloadRollsBackWhenNewRuntimeStartFails(t *testing.T) {
	defer restoreRuntimeHooks()
	configPath := writeTestConfig(t)

	oldNodes, oldCore := newTestRuntime()
	newNodes, newCore := newTestRuntime()
	rollbackNodes, rollbackCore := newTestRuntime()
	var cleanupNewCalls int

	prepareRuntimeFn = func(*conf.Conf) (*core.XrayCore, *node.Node, *panel.ServerConfigResponse, error) {
		return newCore, newNodes, &panel.ServerConfigResponse{
			Data: &panel.Data{Total: 3},
		}, nil
	}
	shutdownRuntimeFn = func(nodes *node.Node, xraycore *core.XrayCore) error {
		if nodes == newNodes && xraycore == newCore {
			cleanupNewCalls++
		}
		return nil
	}
	startRuntimeFn = func(nodesCore *core.XrayCore, nodesValue *node.Node, _ *panel.ServerConfigResponse) error {
		if nodesCore == newCore && nodesValue == newNodes {
			return errors.New("new runtime start failed")
		}
		return nil
	}
	rollbackRuntimeFn = func(*conf.Conf, *panel.ServerConfigResponse, chan struct{}) (*core.XrayCore, *node.Node, error) {
		return rollbackCore, rollbackNodes, nil
	}
	configureLoggingFn = func(*conf.Conf) {
		t.Fatalf("configureLogging should not be called when reload fails")
	}

	err := reload(configPath, &oldNodes, &oldCore)
	if err == nil {
		t.Fatalf("reload() error = nil, want rollback error")
	}
	if !strings.Contains(err.Error(), "已回滚旧实例") {
		t.Fatalf("reload() error = %q, want rollback message", err)
	}
	if oldNodes != rollbackNodes || oldCore != rollbackCore {
		t.Fatalf("reload() did not restore rollback runtime")
	}
	if cleanupNewCalls != 1 {
		t.Fatalf("cleanup new runtime calls = %d, want 1", cleanupNewCalls)
	}
}
