package core

import (
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/conf"
)

func TestRelayModeDoesNotStartXrayInstance(t *testing.T) {
	protocols := make([]panel.Protocol, 0)
	serverConfig := &panel.ServerConfigResponse{
		Data: &panel.Data{
			ServerType:   "relay_egress",
			PullInterval: 3600,
			Protocols:    &protocols,
		},
	}
	xrayCore := New(&conf.Conf{}, nil)
	xrayCore.ReloadCh = make(chan struct{}, 1)
	if err := xrayCore.Start(serverConfig); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if xrayCore.Server != nil {
		t.Fatal("relay mode started an Xray instance")
	}
	if xrayCore.serverConfigMonitorPeriodic == nil {
		t.Fatal("relay mode did not start config monitoring")
	}
	if err := xrayCore.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
