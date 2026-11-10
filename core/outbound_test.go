package core

import (
	"testing"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/transport/internet"
)

func TestBuildDefaultOutboundUsesSystemResolver(t *testing.T) {
	outbound, err := buildDefaultOutbound()
	if err != nil {
		t.Fatalf("build default outbound: %v", err)
	}

	message, err := outbound.SenderSettings.GetInstance()
	if err != nil {
		t.Fatalf("decode sender settings: %v", err)
	}
	sender, ok := message.(*proxyman.SenderConfig)
	if !ok {
		t.Fatalf("sender settings type = %T, want *proxyman.SenderConfig", message)
	}
	if sender.TargetStrategy != internet.DomainStrategy_AS_IS {
		t.Fatalf("target strategy = %v, want AS_IS", sender.TargetStrategy)
	}
	if sender.StreamSettings != nil && sender.StreamSettings.SocketSettings != nil &&
		sender.StreamSettings.SocketSettings.DomainStrategy.HasStrategy() {
		t.Fatalf("socket domain strategy = %v, want AS_IS", sender.StreamSettings.SocketSettings.DomainStrategy)
	}
}
