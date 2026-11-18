package node

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
)

func TestTrafficSuspensionFirewallUsesBusinessPortsOnly(t *testing.T) {
	firewall := newTrafficSuspensionFirewall([]panel.Protocol{
		{Type: "vless", Port: 443, Enable: true},
		{Type: "hysteria2", Port: 8443, HopPorts: "40000-40002", Enable: true},
		{Type: "tuic", Port: 10443, Enable: false},
	})
	if got := formatPorts(firewall.tcpPorts); got != "443" {
		t.Fatalf("TCP ports = %q, want 443", got)
	}
	if got := formatPorts(firewall.udpPorts); got != "443, 8443, 40000, 40001, 40002" {
		t.Fatalf("UDP ports = %q", got)
	}

	original := executeNFT
	t.Cleanup(func() { executeNFT = original })
	var calls [][]string
	var inputs []string
	executeNFT = func(input string, args ...string) ([]byte, error) {
		inputs = append(inputs, input)
		calls = append(calls, append([]string(nil), args...))
		return nil, nil
	}
	if err := firewall.Apply(); err != nil {
		t.Fatalf("apply firewall: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("nft calls = %d, want 2", len(calls))
	}
	rules := inputs[1]
	for _, expected := range []string{
		"table inet " + trafficSuspensionTable,
		"tcp dport { 443 }",
		"udp dport { 443, 8443, 40000, 40001, 40002 }",
	} {
		if !strings.Contains(rules, expected) {
			t.Fatalf("ruleset does not contain %q:\n%s", expected, rules)
		}
	}
	if err := firewall.Close(); err != nil {
		t.Fatalf("close firewall: %v", err)
	}
}

func TestTrafficSuspensionFirewallCloseIgnoresMissingTable(t *testing.T) {
	original := executeNFT
	t.Cleanup(func() { executeNFT = original })
	executeNFT = func(string, ...string) ([]byte, error) {
		return []byte("No such file or directory"), errors.New("exit status 1")
	}
	firewall := &trafficSuspensionFirewall{tcpPorts: []uint16{443}}
	if err := firewall.Close(); err != nil {
		t.Fatalf("close missing firewall table: %v", err)
	}
}

func TestTrafficSuspensionRemainsPersistedWhenFirewallCannotClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic-suspended")
	t.Setenv("PPANEL_TRAFFIC_SUSPENSION_FILE", path)
	original := executeNFT
	t.Cleanup(func() { executeNFT = original })
	executeNFT = func(string, ...string) ([]byte, error) {
		return nil, nil
	}
	agent := &MetricsAgent{
		suspensionFirewall: &trafficSuspensionFirewall{tcpPorts: []uint16{443}},
	}
	agent.setTrafficSuspended(true)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("suspension state was not persisted: %v", err)
	}

	executeNFT = func(string, ...string) ([]byte, error) {
		return []byte("permission denied"), errors.New("exit status 1")
	}
	agent.setTrafficSuspended(false)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("failed firewall close removed suspension state: %v", err)
	}
}
