package core

import (
	"strings"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
	xnet "github.com/xtls/xray-core/common/net"
)

func TestGetCustomConfigSkipsEmptyBlockRules(t *testing.T) {
	dns := []panel.DNSItem{}
	block := []string{}
	outbound := []panel.Outbound{}
	protocols := []panel.Protocol{}

	_, _, routeConfig, err := GetCustomConfig(&panel.ServerConfigResponse{
		Data: &panel.Data{
			IPStrategy: "prefer_ipv4",
			DNS:        &dns,
			Block:      &block,
			Outbound:   &outbound,
			Protocols:  &protocols,
		},
	})
	if err != nil {
		t.Fatalf("GetCustomConfig() error = %v", err)
	}

	if got := len(routeConfig.GetRule()); got != 1 {
		t.Fatalf("route rules len = %d, want only default DNS rule", got)
	}
}

func TestBuildRouteDomainsSanitizesRules(t *testing.T) {
	got := buildRouteDomains([]string{
		" suffix:example.com ",
		"",
		"suffix:example.com",
		"keyword:",
		" keyword:google ",
		"plain.example",
	})
	want := []string{"domain:example.com", "google", "full:plain.example"}

	if len(got) != len(want) {
		t.Fatalf("buildRouteDomains() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("buildRouteDomains()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGetCustomConfigBuildsTypedOutbound(t *testing.T) {
	dns := []panel.DNSItem{}
	block := []string{}
	outbound := []panel.Outbound{
		{
			Name:      "proxy",
			Protocol:  "vless",
			Address:   "example.com",
			Port:      443,
			UUID:      "00000000-0000-0000-0000-000000000001",
			Security:  "tls",
			SNI:       "example.com",
			Transport: "websocket",
			Host:      "example.com",
			Path:      "/ws",
			Rules:     []string{"suffix:example.com"},
		},
	}
	protocols := []panel.Protocol{}

	_, outbounds, routeConfig, err := GetCustomConfig(&panel.ServerConfigResponse{
		Data: &panel.Data{
			IPStrategy: "prefer_ipv4",
			DNS:        &dns,
			Block:      &block,
			Outbound:   &outbound,
			Protocols:  &protocols,
		},
	})
	if err != nil {
		t.Fatalf("GetCustomConfig() error = %v", err)
	}

	if got := len(outbounds); got != 4 {
		t.Fatalf("outbounds len = %d, want 4", got)
	}
	if got := len(routeConfig.GetRule()); got != 2 {
		t.Fatalf("route rules len = %d, want default DNS + custom outbound", got)
	}
}

func TestGetCustomConfigUsesRawOutboundSettings(t *testing.T) {
	dns := []panel.DNSItem{}
	block := []string{}
	outbound := []panel.Outbound{
		{
			Name:     "direct-cn",
			Protocol: "direct",
			Settings: `{"domainStrategy":"UseIPv4"}`,
			Rules:    []string{"suffix:cn"},
		},
	}
	protocols := []panel.Protocol{}

	_, outbounds, routeConfig, err := GetCustomConfig(&panel.ServerConfigResponse{
		Data: &panel.Data{
			IPStrategy: "prefer_ipv4",
			DNS:        &dns,
			Block:      &block,
			Outbound:   &outbound,
			Protocols:  &protocols,
		},
	})
	if err != nil {
		t.Fatalf("GetCustomConfig() error = %v", err)
	}

	if got := len(outbounds); got != 4 {
		t.Fatalf("outbounds len = %d, want 4", got)
	}
	if got := outbounds[3].Tag; got != "direct-cn" {
		t.Fatalf("custom outbound tag = %q, want direct-cn", got)
	}
	if got := len(routeConfig.GetRule()); got != 2 {
		t.Fatalf("route rules len = %d, want default DNS + custom outbound", got)
	}
}

func TestGetCustomConfigSkipsUnsupportedOutboundWithoutRawSettings(t *testing.T) {
	dns := []panel.DNSItem{}
	block := []string{}
	outbound := []panel.Outbound{
		{
			Name:     "legacy-naive",
			Protocol: "naive",
			Address:  "example.com",
			Port:     443,
			Rules:    []string{"suffix:example.com"},
		},
	}
	protocols := []panel.Protocol{}

	_, outbounds, routeConfig, err := GetCustomConfig(&panel.ServerConfigResponse{
		Data: &panel.Data{
			IPStrategy: "prefer_ipv4",
			DNS:        &dns,
			Block:      &block,
			Outbound:   &outbound,
			Protocols:  &protocols,
		},
	})
	if err != nil {
		t.Fatalf("GetCustomConfig() error = %v", err)
	}

	if got := len(outbounds); got != 3 {
		t.Fatalf("outbounds len = %d, want only default outbounds", got)
	}
	if got := len(routeConfig.GetRule()); got != 1 {
		t.Fatalf("route rules len = %d, want only default DNS rule", got)
	}
}

func TestGetCustomConfigKeepsCompatibilityForInvalidLegacyOutbound(t *testing.T) {
	dns := []panel.DNSItem{}
	block := []string{}
	outbound := []panel.Outbound{
		{
			Name:     "legacy-ss",
			Protocol: "shadowsocks",
			Address:  "192.0.2.1",
			Port:     8388,
			Cipher:   "chacha20-ietf-poly1305",
		},
	}
	protocols := []panel.Protocol{}

	_, outbounds, routeConfig, err := GetCustomConfig(&panel.ServerConfigResponse{
		Data: &panel.Data{
			IPStrategy: "prefer_ipv4",
			DNS:        &dns,
			Block:      &block,
			Outbound:   &outbound,
			Protocols:  &protocols,
		},
	})
	if err != nil {
		t.Fatalf("GetCustomConfig() error = %v", err)
	}
	if got := len(outbounds); got != 3 {
		t.Fatalf("outbounds len = %d, want invalid legacy outbound skipped", got)
	}
	if got := len(routeConfig.GetRule()); got != 1 {
		t.Fatalf("route rules len = %d, want only default DNS rule", got)
	}
}

func TestGetCustomConfigRoutesAllTCPAndUDPThroughWireGuard(t *testing.T) {
	dns := []panel.DNSItem{}
	block := []string{"suffix:blocked.example"}
	outbound := []panel.Outbound{
		{
			Name:     "server-egress",
			Protocol: "wireguard",
			Settings: `{
				"noKernelTun": true,
				"secretKey": "6NhRmR7pFeutiPY5nVKBa+xLR9efGITwITDiECkxYlo=",
				"address": ["10.255.57.2/32"],
				"peers": [{
					"publicKey": "Zn0TiehjsnBIyEPrkfZ7E5ZPD0wnUscUNIjQYeqByyM=",
					"endpoint": "192.0.2.1:51888",
					"keepAlive": 25,
					"allowedIPs": ["0.0.0.0/0"]
				}],
				"mtu": 1380,
				"remoteDNS": ["1.1.1.1"]
			}`,
		},
	}
	protocols := []panel.Protocol{}

	_, outbounds, routeConfig, err := GetCustomConfig(&panel.ServerConfigResponse{
		Data: &panel.Data{
			IPStrategy: "prefer_ipv4",
			DNS:        &dns,
			Block:      &block,
			Outbound:   &outbound,
			Protocols:  &protocols,
		},
	})
	if err != nil {
		t.Fatalf("GetCustomConfig() error = %v", err)
	}

	if got := len(outbounds); got != 4 {
		t.Fatalf("outbounds len = %d, want 4", got)
	}
	rules := routeConfig.GetRule()
	if got := len(rules); got != 2 {
		t.Fatalf("route rules len = %d, want block + full-route", got)
	}
	if got := rules[0].GetTag(); got != "block" {
		t.Fatalf("first route tag = %q, want block", got)
	}
	if got := rules[1].GetTag(); got != "server-egress" {
		t.Fatalf("full route tag = %q, want server-egress", got)
	}
	if got := rules[1].GetNetworks(); len(got) != 2 ||
		got[0] != xnet.Network_TCP || got[1] != xnet.Network_UDP {
		t.Fatalf("full route networks = %v, want TCP and UDP", got)
	}
	if rules[0].GetPortList() != nil || rules[1].GetPortList() != nil {
		t.Fatal("full-route mode must not install the local UDP 53 interception rule")
	}
}

func TestGetCustomConfigRejectsMultipleWireGuardOutbounds(t *testing.T) {
	dns := []panel.DNSItem{}
	block := []string{}
	outbound := []panel.Outbound{
		{Name: "egress-a", Protocol: "wireguard", Settings: `{}`},
		{Name: "egress-b", Protocol: "wireguard", Settings: `{}`},
	}
	protocols := []panel.Protocol{}

	_, _, _, err := GetCustomConfig(&panel.ServerConfigResponse{
		Data: &panel.Data{
			DNS:       &dns,
			Block:     &block,
			Outbound:  &outbound,
			Protocols: &protocols,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "only one wireguard") {
		t.Fatalf("GetCustomConfig() error = %v, want duplicate WireGuard rejection", err)
	}
}

func TestGetCustomConfigRejectsWireGuardWithoutSettings(t *testing.T) {
	dns := []panel.DNSItem{}
	block := []string{}
	outbound := []panel.Outbound{
		{Name: "server-egress", Protocol: "wireguard"},
	}
	protocols := []panel.Protocol{}

	_, _, _, err := GetCustomConfig(&panel.ServerConfigResponse{
		Data: &panel.Data{
			DNS:       &dns,
			Block:     &block,
			Outbound:  &outbound,
			Protocols: &protocols,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires settings") {
		t.Fatalf("GetCustomConfig() error = %v, want missing settings rejection", err)
	}
}

func TestGetCustomConfigUsesManagedSocksEgressAndBlocksUnsupportedUDP(t *testing.T) {
	dns := []panel.DNSItem{}
	block := []string{}
	outbound := []panel.Outbound{
		{
			Name:     "domain-route",
			Protocol: "direct",
			Rules:    []string{"suffix:example.com"},
		},
	}
	protocols := []panel.Protocol{}

	_, outbounds, routeConfig, err := GetCustomConfig(&panel.ServerConfigResponse{
		Data: &panel.Data{
			DNS:       &dns,
			Block:     &block,
			Outbound:  &outbound,
			Protocols: &protocols,
			Egress: &panel.Egress{
				Enabled:       true,
				Mode:          "all",
				FailurePolicy: "block",
				Networks:      []string{"tcp"},
				Revision:      4,
				Outbound: panel.Outbound{
					Name:     "managed-egress",
					Protocol: "socks",
					Address:  "192.0.2.5",
					Port:     1080,
					User:     "proxy-user",
					Password: "proxy-password",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("GetCustomConfig() error = %v", err)
	}
	if got := len(outbounds); got != 5 {
		t.Fatalf("outbounds len = %d, want defaults + custom + managed", got)
	}
	rules := routeConfig.GetRule()
	if got := len(rules); got != 2 {
		t.Fatalf("route rules len = %d, want UDP block + managed TCP route", got)
	}
	if got := rules[0].GetTag(); got != "block" {
		t.Fatalf("unsupported network route tag = %q, want block", got)
	}
	if got := rules[0].GetNetworks(); len(got) != 1 || got[0] != xnet.Network_UDP {
		t.Fatalf("blocked networks = %v, want UDP", got)
	}
	if got := rules[1].GetTag(); got != "managed-egress" {
		t.Fatalf("managed route tag = %q, want managed-egress", got)
	}
	if got := rules[1].GetNetworks(); len(got) != 1 || got[0] != xnet.Network_TCP {
		t.Fatalf("managed networks = %v, want TCP", got)
	}
}

func TestGetCustomConfigIgnoresDisabledManagedEgress(t *testing.T) {
	dns := []panel.DNSItem{}
	block := []string{}
	outbound := []panel.Outbound{}
	protocols := []panel.Protocol{}

	_, outbounds, routeConfig, err := GetCustomConfig(&panel.ServerConfigResponse{
		Data: &panel.Data{
			DNS:       &dns,
			Block:     &block,
			Outbound:  &outbound,
			Protocols: &protocols,
			Egress: &panel.Egress{
				Enabled: false,
				Outbound: panel.Outbound{
					Name:     "must-not-load",
					Protocol: "socks",
					Address:  "192.0.2.5",
					Port:     1080,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("GetCustomConfig() error = %v", err)
	}
	if got := len(outbounds); got != 3 {
		t.Fatalf("outbounds len = %d, want only defaults", got)
	}
	if got := len(routeConfig.GetRule()); got != 1 {
		t.Fatalf("route rules len = %d, want default DNS route", got)
	}
}
