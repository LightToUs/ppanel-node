package node

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
)

func TestParsePortHoppingRange(t *testing.T) {
	start, end, err := parsePortHoppingRange("40000-41000")
	if err != nil {
		t.Fatalf("parse range: %v", err)
	}
	if start != 40000 || end != 41000 {
		t.Fatalf("range = %d-%d, want 40000-41000", start, end)
	}

	for _, value := range []string{
		"",
		"40000",
		"41000-40000",
		"0-100",
		"40000-70000",
		"10000-15000",
		"40000-41000; delete table inet filter",
	} {
		t.Run(value, func(t *testing.T) {
			if _, _, err := parsePortHoppingRange(value); err == nil {
				t.Fatalf("parsePortHoppingRange(%q) succeeded unexpectedly", value)
			}
		})
	}
}

func TestPortHoppingManagerAppliesAndRemovesDedicatedTable(t *testing.T) {
	originalRunner := executeNFT
	t.Cleanup(func() {
		executeNFT = originalRunner
	})

	var calls [][]string
	var inputs []string
	executeNFT = func(input string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		inputs = append(inputs, input)
		if reflect.DeepEqual(args, []string{"delete", "table", "inet", "ppanel_hy2_165_40512"}) && len(calls) == 1 {
			return []byte("No such file or directory"), errors.New("exit status 1")
		}
		return nil, nil
	}

	manager, err := newPortHoppingManager(165, 40512, "40000-41000")
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if err := manager.Apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	wantCalls := [][]string{
		{"delete", "table", "inet", "ppanel_hy2_165_40512"},
		{"-f", "-"},
		{"delete", "table", "inet", "ppanel_hy2_165_40512"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("nft calls = %#v, want %#v", calls, wantCalls)
	}
	if !strings.Contains(inputs[1], "udp dport 40000-41000 counter redirect to :40512") {
		t.Fatalf("unexpected nft ruleset: %s", inputs[1])
	}
}

func TestPortHoppingManagerOnlyEnablesHY2WithConfiguredRange(t *testing.T) {
	tests := []struct {
		name string
		info *panel.NodeInfo
		want bool
	}{
		{
			name: "hysteria2",
			info: &panel.NodeInfo{
				Id:   165,
				Type: "hysteria2",
				Protocol: &panel.Protocol{
					Port:     40512,
					HopPorts: "40000-41000",
				},
			},
			want: true,
		},
		{
			name: "range disabled",
			info: &panel.NodeInfo{
				Id:       165,
				Type:     "hysteria2",
				Protocol: &panel.Protocol{Port: 40512},
			},
		},
		{
			name: "other protocol",
			info: &panel.NodeInfo{
				Id:   165,
				Type: "vless",
				Protocol: &panel.Protocol{
					Port:     40512,
					HopPorts: "40000-41000",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := newPortHoppingManagerForNode(tt.info)
			if err != nil {
				t.Fatalf("new manager: %v", err)
			}
			if (manager != nil) != tt.want {
				t.Fatalf("manager present = %v, want %v", manager != nil, tt.want)
			}
		})
	}
}
