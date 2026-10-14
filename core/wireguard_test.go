package core

import (
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/xtls/xray-core/proxy/wireguard"
)

func TestBuildWireGuardUser(t *testing.T) {
	user, err := buildWireGuardUser("wireguard-49", &panel.UserInfo{
		Id:                  42,
		Uuid:                "wg:456",
		PeerId:              "456",
		PublicKey:           "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		AllowedIPs:          []string{"10.200.0.2/32", "fd00:200::2/128"},
		PersistentKeepalive: 25,
	})
	if err != nil {
		t.Fatalf("buildWireGuardUser() error = %v", err)
	}
	memoryUser, err := user.ToMemoryUser()
	if err != nil {
		t.Fatalf("ToMemoryUser() error = %v", err)
	}
	account, ok := memoryUser.Account.(*wireguard.MemoryAccount)
	if !ok {
		t.Fatalf("account type = %T", memoryUser.Account)
	}
	if memoryUser.Email != "wireguard-49|wg:456" {
		t.Fatalf("email = %q", memoryUser.Email)
	}
	if len(account.AllowedIPs) != 2 || account.KeepAlive != "25" {
		t.Fatalf("account = %#v", account)
	}
}

func TestBuildWireGuardInbound(t *testing.T) {
	config, err := buildInbound(&panel.NodeInfo{
		Type: "wireguard",
		Protocol: &panel.Protocol{
			Type:                      "wireguard",
			Port:                      51820,
			WireGuardIPv4CIDR:         "10.200.0.1/24",
			WireGuardMTU:              1408,
			WireGuardServerPrivateKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		},
	}, "wireguard-49")
	if err != nil {
		t.Fatalf("buildInbound() error = %v", err)
	}
	if config.Tag != "wireguard-49" {
		t.Fatalf("tag = %q", config.Tag)
	}
}
