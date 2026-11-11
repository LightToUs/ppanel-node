package core

import (
	"testing"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/transport/internet"
)

func TestBuildDefaultOutboundUsesHostSocketDomainStrategy(t *testing.T) {
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
	if sender.StreamSettings == nil || sender.StreamSettings.SocketSettings == nil {
		t.Fatal("default outbound is missing socket settings")
	}
	expected := internet.DomainStrategy_USE_IP4
	if hasPublicIPv6() {
		expected = internet.DomainStrategy_USE_IP46
	}
	if sender.StreamSettings.SocketSettings.DomainStrategy != expected {
		t.Fatalf(
			"socket domain strategy = %v, want %v",
			sender.StreamSettings.SocketSettings.DomainStrategy,
			expected,
		)
	}
}

func TestDefaultOutboundDomainStrategy(t *testing.T) {
	tests := []struct {
		name    string
		hasIPv6 bool
		want    string
	}{
		{name: "IPv4 only", want: "UseIPv4"},
		{name: "dual stack", hasIPv6: true, want: "UseIPv4v6"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := defaultOutboundDomainStrategy(test.hasIPv6); got != test.want {
				t.Fatalf("defaultOutboundDomainStrategy(%t) = %q, want %q", test.hasIPv6, got, test.want)
			}
		})
	}
}
