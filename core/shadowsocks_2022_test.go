package core

import (
	"encoding/base64"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
)

func TestBuildShadowsocks2022InboundAcceptsRawAndEncodedServerKeys(t *testing.T) {
	const rawKey = "0123456789abcdef"
	for _, serverKey := range []string{
		rawKey,
		base64.StdEncoding.EncodeToString([]byte(rawKey)),
	} {
		_, err := buildInbound(&panel.NodeInfo{
			Type: "shadowsocks",
			Protocol: &panel.Protocol{
				Port:      8388,
				Cipher:    "2022-blake3-aes-128-gcm",
				ServerKey: serverKey,
			},
		}, "shadowsocks-test")
		if err != nil {
			t.Fatalf("buildInbound(serverKey=%q) error = %v", serverKey, err)
		}
	}
}

func TestNormalizeShadowsocks2022KeyRejectsWrongLength(t *testing.T) {
	if _, err := normalizeShadowsocks2022Key("too-short", 16); err == nil {
		t.Fatal("normalizeShadowsocks2022Key() accepted an invalid key")
	}
}
