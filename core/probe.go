package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/policy"
	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	xraycore "github.com/xtls/xray-core/core"
	coreconf "github.com/xtls/xray-core/infra/conf"
)

type ProbeClient struct {
	instance *xraycore.Instance
}

func NewProbeClient(outbound panel.Outbound) (*ProbeClient, error) {
	var err error
	outbound, err = prepareProbeOutbound(outbound)
	if err != nil {
		return nil, err
	}
	protocol, settings, err := buildOutboundSettings(outbound)
	if err != nil {
		return nil, err
	}
	if settings == nil {
		return nil, fmt.Errorf("unsupported probe protocol %q", outbound.Protocol)
	}
	stream, err := buildProbeStreamConfig(outbound)
	if err != nil {
		return nil, err
	}
	if stream == nil {
		stream = &coreconf.StreamConfig{}
	}
	if protocol == "hysteria" {
		stream.HysteriaSettings = &coreconf.HysteriaConfig{
			Version: 2,
			Auth:    outbound.Password,
		}
		if outbound.Obfs != "" && outbound.Obfs != "none" && outbound.ObfsPassword != "" {
			raw, marshalErr := json.Marshal(map[string]string{
				"password": outbound.ObfsPassword,
			})
			if marshalErr != nil {
				return nil, fmt.Errorf("marshal Hysteria2 obfuscation: %w", marshalErr)
			}
			rawMessage := json.RawMessage(raw)
			stream.FinalMask = &coreconf.FinalMask{
				Udp: []coreconf.Mask{{
					Type:     outbound.Obfs,
					Settings: &rawMessage,
				}},
			}
		}
	}
	detour := &coreconf.OutboundDetourConfig{
		Protocol:      protocol,
		Tag:           "connectivity-probe",
		Settings:      settings,
		StreamSetting: stream,
	}
	builtOutbound, err := detour.Build()
	if err != nil {
		return nil, fmt.Errorf("build probe outbound: %w", err)
	}
	config := &xraycore.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
			serial.ToTypedMessage(&policy.Config{}),
		},
		Outbound: []*xraycore.OutboundHandlerConfig{builtOutbound},
	}
	instance, err := xraycore.New(config)
	if err != nil {
		return nil, fmt.Errorf("create probe Xray instance: %w", err)
	}
	if err = instance.Start(); err != nil {
		_ = instance.Close()
		return nil, fmt.Errorf("start probe Xray instance: %w", err)
	}
	return &ProbeClient{instance: instance}, nil
}

func prepareProbeOutbound(outbound panel.Outbound) (panel.Outbound, error) {
	if normalizeOutboundProtocol(outbound.Protocol) != "shadowsocks" ||
		!strings.Contains(strings.ToLower(outbound.Cipher), "2022") {
		return outbound, nil
	}
	password, err := composeShadowsocks2022ClientKey(
		outbound.ServerKey,
		outbound.Password,
		outbound.Cipher,
	)
	if err != nil {
		return panel.Outbound{}, err
	}
	outbound.Password = password
	return outbound, nil
}

func buildProbeStreamConfig(outbound panel.Outbound) (*coreconf.StreamConfig, error) {
	if outbound.AllowInsecure {
		return nil, errors.New(
			"TLS probe requires certificate verification; allow_insecure is not supported",
		)
	}
	stream, err := buildOutboundStreamConfig(outbound)
	if err != nil {
		return nil, err
	}
	if stream == nil || !strings.EqualFold(stream.Security, "tls") {
		return stream, nil
	}
	if stream.TLSSettings == nil {
		return nil, errors.New("TLS probe has no TLS settings")
	}
	if stream.TLSSettings.AllowInsecure {
		return nil, errors.New(
			"TLS probe requires certificate verification; allow_insecure is not supported",
		)
	}
	serverName := strings.TrimSpace(stream.TLSSettings.ServerName)
	if serverName == "" {
		return nil, errors.New("TLS probe requires a non-empty SNI")
	}

	// Probe admission must authenticate the endpoint certificate, not merely
	// complete a TLS handshake.
	stream.TLSSettings.VerifyPeerCertByName = serverName
	return stream, nil
}

func (c *ProbeClient) Dial(ctx context.Context, host string, port uint16) (net.Conn, error) {
	if c == nil || c.instance == nil {
		return nil, errors.New("probe Xray client is not initialized")
	}
	return xraycore.Dial(
		ctx,
		c.instance,
		xnet.TCPDestination(xnet.ParseAddress(host), xnet.Port(port)),
	)
}

func (c *ProbeClient) Close() error {
	if c == nil || c.instance == nil {
		return nil
	}
	instance := c.instance
	c.instance = nil
	return instance.Close()
}
