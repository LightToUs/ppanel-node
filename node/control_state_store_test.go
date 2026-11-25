package node

import (
	"os"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
)

func TestControlStatePersistsRevokedSubscriptions(t *testing.T) {
	statePath := t.TempDir()
	want := map[int64]int64{42: 7, 99: 3}
	if err := saveControlState(statePath, 12, "hysteria2", want); err != nil {
		t.Fatalf("save control state: %v", err)
	}
	got, err := loadControlState(statePath, 12, "hysteria2")
	if err != nil {
		t.Fatalf("load control state: %v", err)
	}
	if len(got) != len(want) || got[42] != 7 || got[99] != 3 {
		t.Fatalf("loaded control state = %v, want %v", got, want)
	}
	info, err := os.Stat(controlStatePath(statePath, 12, "hysteria2"))
	if err != nil {
		t.Fatalf("stat control state: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("control state permissions = %o, want 600", permissions)
	}
}

func TestControllerLoadsRevokedSubscriptionsForNextSnapshot(t *testing.T) {
	statePath := t.TempDir()
	if err := saveControlState(
		statePath,
		12,
		"hysteria2",
		map[int64]int64{42: 7},
	); err != nil {
		t.Fatalf("save control state: %v", err)
	}
	controller := &Controller{
		statePath:        statePath,
		info:             &panel.NodeInfo{Id: 12, Type: "hysteria2"},
		controlRevisions: make(map[int64]int64),
		controlStates:    make(map[int64]string),
	}
	if err := controller.loadControlState(); err != nil {
		t.Fatalf("load controller state: %v", err)
	}
	tracked := controller.trackedSubscriptions()
	if len(tracked) != 1 || tracked[0] != 42 {
		t.Fatalf("tracked subscriptions = %v, want [42]", tracked)
	}
}
