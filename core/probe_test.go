package core

import (
	"encoding/base64"
	"testing"

	"github.com/lighttous/ppanel-node/api/panel"
)

func TestNewProbeClientBuildsSupportedProtocols(t *testing.T) {
	const credential = "d9ee6b2d-7311-5f94-a290-077cfcb28d9e"
	tests := []struct {
		name     string
		outbound panel.Outbound
	}{
		{
			name: "vless reality",
			outbound: panel.Outbound{
				Protocol:         "vless",
				Address:          "203.0.113.10",
				Port:             443,
				UUID:             credential,
				Security:         "reality",
				SNI:              "example.com",
				Transport:        "tcp",
				RealityPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				RealityShortID:   "0123456789abcdef",
			},
		},
		{
			name: "vmess websocket tls",
			outbound: panel.Outbound{
				Protocol:  "vmess",
				Address:   "203.0.113.10",
				Port:      443,
				UUID:      credential,
				Cipher:    "auto",
				Security:  "tls",
				SNI:       "example.com",
				Transport: "websocket",
				Host:      "example.com",
				Path:      "/ws",
			},
		},
		{
			name: "trojan tls",
			outbound: panel.Outbound{
				Protocol:  "trojan",
				Address:   "203.0.113.10",
				Port:      443,
				Password:  credential,
				Security:  "tls",
				SNI:       "example.com",
				Transport: "tcp",
			},
		},
		{
			name: "shadowsocks",
			outbound: panel.Outbound{
				Protocol: "shadowsocks",
				Address:  "203.0.113.10",
				Port:     8388,
				Password: credential,
				Cipher:   "chacha20-ietf-poly1305",
			},
		},
		{
			name: "hysteria2",
			outbound: panel.Outbound{
				Protocol:     "hysteria2",
				Address:      "203.0.113.10",
				Port:         8443,
				Password:     credential,
				Security:     "tls",
				SNI:          "example.com",
				Transport:    "hysteria",
				Obfs:         "salamander",
				ObfsPassword: "probe-obfs",
			},
		},
		{
			name: "tuic",
			outbound: panel.Outbound{
				Protocol:  "tuic",
				Address:   "203.0.113.10",
				Port:      8443,
				UUID:      credential,
				Password:  credential,
				Security:  "tls",
				SNI:       "example.com",
				Transport: "tuic",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewProbeClient(test.outbound)
			if err != nil {
				t.Fatalf("NewProbeClient() error = %v", err)
			}
			if err = client.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		})
	}
}

func TestBuildProbeStreamConfigRequiresVerifiedTLS(t *testing.T) {
	_, err := buildProbeStreamConfig(panel.Outbound{
		Protocol:      "hysteria2",
		Security:      "tls",
		SNI:           "example.com",
		AllowInsecure: true,
		Transport:     "hysteria",
	})
	if err == nil {
		t.Fatal("buildProbeStreamConfig() accepted allow_insecure")
	}

	stream, err := buildProbeStreamConfig(panel.Outbound{
		Protocol:  "tuic",
		Security:  "tls",
		SNI:       "example.com",
		Transport: "tuic",
	})
	if err != nil {
		t.Fatalf("buildProbeStreamConfig() error = %v", err)
	}
	if stream.TLSSettings.VerifyPeerCertByName != "example.com" {
		t.Fatalf(
			"VerifyPeerCertByName = %q, want example.com",
			stream.TLSSettings.VerifyPeerCertByName,
		)
	}
}

func TestPrepareProbeOutboundComposesShadowsocks2022Keys(t *testing.T) {
	const (
		serverKey  = "0123456789abcdef"
		credential = "d9ee6b2d-7311-5f94-a290-077cfcb28d9e"
	)
	outbound, err := prepareProbeOutbound(panel.Outbound{
		Protocol:  "shadowsocks",
		Password:  credential,
		Cipher:    "2022-blake3-aes-128-gcm",
		ServerKey: serverKey,
	})
	if err != nil {
		t.Fatalf("prepareProbeOutbound() error = %v", err)
	}
	want := base64.StdEncoding.EncodeToString([]byte(serverKey)) + ":" +
		base64.StdEncoding.EncodeToString([]byte(credential[:16]))
	if outbound.Password != want {
		t.Fatalf("Password = %q, want %q", outbound.Password, want)
	}
}

func TestNewProbeClientBuildsShadowsocks2022AES(t *testing.T) {
	client, err := NewProbeClient(panel.Outbound{
		Protocol:  "shadowsocks",
		Address:   "203.0.113.10",
		Port:      8388,
		Password:  "d9ee6b2d-7311-5f94-a290-077cfcb28d9e",
		Cipher:    "2022-blake3-aes-128-gcm",
		ServerKey: "0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("NewProbeClient() error = %v", err)
	}
	if err = client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestNewProbeClientRejectsShadowsocks2022ChaChaMultiUser(t *testing.T) {
	_, err := NewProbeClient(panel.Outbound{
		Protocol:  "shadowsocks",
		Address:   "203.0.113.10",
		Port:      8388,
		Password:  "d9ee6b2d-7311-5f94-a290-077cfcb28d9e",
		Cipher:    "2022-blake3-chacha20-poly1305",
		ServerKey: "0123456789abcdef0123456789abcdef",
	})
	if err == nil {
		t.Fatal("NewProbeClient() accepted unsupported Shadowsocks 2022 ChaCha20 multi-user mode")
	}
}
