package core

import (
	"strings"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/xtls/xray-core/app/router"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	routing_session "github.com/xtls/xray-core/features/routing/session"
	coreConf "github.com/xtls/xray-core/infra/conf"
)

const securityRuleCount = 3

func assertSecurityRules(t *testing.T, rules []*router.RoutingRule) {
	t.Helper()
	if len(rules) < securityRuleCount {
		t.Fatalf("route rules len = %d, want at least %d security rules", len(rules), securityRuleCount)
	}

	privateRule := rules[0]
	if privateRule.GetRuleTag() != "security-private-network" ||
		privateRule.GetTag() != "block" ||
		len(privateRule.GetIp()) != len(privateTargetCIDRs) {
		t.Fatalf("private network rule = %v, want mandatory private target block", privateRule)
	}

	smtpRule := rules[1]
	if smtpRule.GetRuleTag() != "security-smtp" ||
		smtpRule.GetTag() != "block" ||
		len(smtpRule.GetNetworks()) != 1 ||
		smtpRule.GetNetworks()[0] != xnet.Network_TCP {
		t.Fatalf("SMTP rule = %v, want mandatory TCP block", smtpRule)
	}
	wantPorts := []uint32{25, 465, 587}
	gotPorts := smtpRule.GetPortList().Ports()
	if len(gotPorts) != len(wantPorts) {
		t.Fatalf("SMTP blocked ports = %v, want %v", gotPorts, wantPorts)
	}
	for i := range wantPorts {
		if gotPorts[i] != wantPorts[i] {
			t.Fatalf("SMTP blocked ports = %v, want %v", gotPorts, wantPorts)
		}
	}

	bittorrentRule := rules[2]
	if bittorrentRule.GetRuleTag() != "security-bittorrent" ||
		bittorrentRule.GetTag() != "block" ||
		len(bittorrentRule.GetProtocol()) != 1 ||
		bittorrentRule.GetProtocol()[0] != "bittorrent" {
		t.Fatalf("BitTorrent rule = %v, want mandatory protocol block", bittorrentRule)
	}
}

func TestSecurityRulesBlockAbuseWithoutMatchingNormalProxyTraffic(t *testing.T) {
	rawRules, err := buildSecurityRules()
	if err != nil {
		t.Fatalf("buildSecurityRules() error = %v", err)
	}
	config := &coreConf.RouterConfig{RuleList: rawRules}
	built, err := config.Build()
	if err != nil {
		t.Fatalf("build security router config: %v", err)
	}

	testCases := []struct {
		name      string
		target    xnet.Destination
		protocol  string
		wantMatch int
	}{
		{
			name:      "normal HTTPS",
			target:    xnet.TCPDestination(xnet.ParseAddress("8.8.8.8"), 443),
			protocol:  "tls",
			wantMatch: -1,
		},
		{
			name:      "private target",
			target:    xnet.TCPDestination(xnet.ParseAddress("10.0.0.1"), 443),
			protocol:  "tls",
			wantMatch: 0,
		},
		{
			name:      "SMTP submission",
			target:    xnet.TCPDestination(xnet.ParseAddress("8.8.8.8"), 587),
			protocol:  "tls",
			wantMatch: 1,
		},
		{
			name:      "BitTorrent",
			target:    xnet.TCPDestination(xnet.ParseAddress("8.8.8.8"), 443),
			protocol:  "bittorrent",
			wantMatch: 2,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			context := &routing_session.Context{
				Outbound: &session.Outbound{Target: testCase.target},
				Content:  &session.Content{Protocol: testCase.protocol},
			}
			gotMatch := -1
			for i, rule := range built.GetRule() {
				condition, err := rule.BuildCondition()
				if err != nil {
					t.Fatalf("BuildCondition() error = %v", err)
				}
				if condition.Apply(context) {
					gotMatch = i
					break
				}
			}
			if gotMatch != testCase.wantMatch {
				t.Fatalf("matched security rule %d, want %d", gotMatch, testCase.wantMatch)
			}
		})
	}
}

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

	if got := routeConfig.GetDomainStrategy(); got != router.Config_IpOnDemand {
		t.Fatalf("domain strategy = %v, want IP-on-demand private target validation", got)
	}
	assertSecurityRules(t, routeConfig.GetRule())
	if got := len(routeConfig.GetRule()); got != securityRuleCount+1 {
		t.Fatalf("route rules len = %d, want security rules + default DNS rule", got)
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
	assertSecurityRules(t, routeConfig.GetRule())
	if got := len(routeConfig.GetRule()); got != securityRuleCount+2 {
		t.Fatalf("route rules len = %d, want security rules + default DNS + custom outbound", got)
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
	assertSecurityRules(t, routeConfig.GetRule())
	if got := len(routeConfig.GetRule()); got != securityRuleCount+2 {
		t.Fatalf("route rules len = %d, want security rules + default DNS + custom outbound", got)
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
	assertSecurityRules(t, routeConfig.GetRule())
	if got := len(routeConfig.GetRule()); got != securityRuleCount+1 {
		t.Fatalf("route rules len = %d, want security rules + default DNS rule", got)
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
	assertSecurityRules(t, routeConfig.GetRule())
	if got := len(routeConfig.GetRule()); got != securityRuleCount+1 {
		t.Fatalf("route rules len = %d, want security rules + default DNS rule", got)
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
	assertSecurityRules(t, rules)
	if got := len(rules); got != securityRuleCount+2 {
		t.Fatalf("route rules len = %d, want security rules + custom block + full-route", got)
	}
	if got := rules[securityRuleCount].GetTag(); got != "block" {
		t.Fatalf("custom block route tag = %q, want block", got)
	}
	if got := rules[securityRuleCount+1].GetTag(); got != "server-egress" {
		t.Fatalf("full route tag = %q, want server-egress", got)
	}
	if got := rules[securityRuleCount+1].GetNetworks(); len(got) != 2 ||
		got[0] != xnet.Network_TCP || got[1] != xnet.Network_UDP {
		t.Fatalf("full route networks = %v, want TCP and UDP", got)
	}
	if rules[securityRuleCount].GetPortList() != nil ||
		rules[securityRuleCount+1].GetPortList() != nil {
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
	assertSecurityRules(t, rules)
	if got := len(rules); got != securityRuleCount+2 {
		t.Fatalf("route rules len = %d, want security rules + UDP block + managed TCP route", got)
	}
	if got := rules[securityRuleCount].GetTag(); got != "block" {
		t.Fatalf("unsupported network route tag = %q, want block", got)
	}
	if got := rules[securityRuleCount].GetNetworks(); len(got) != 1 || got[0] != xnet.Network_UDP {
		t.Fatalf("blocked networks = %v, want UDP", got)
	}
	if got := rules[securityRuleCount+1].GetTag(); got != "managed-egress" {
		t.Fatalf("managed route tag = %q, want managed-egress", got)
	}
	if got := rules[securityRuleCount+1].GetNetworks(); len(got) != 1 || got[0] != xnet.Network_TCP {
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
	assertSecurityRules(t, routeConfig.GetRule())
	if got := len(routeConfig.GetRule()); got != securityRuleCount+1 {
		t.Fatalf("route rules len = %d, want security rules + default DNS route", got)
	}
}
