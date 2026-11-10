package core

import (
	"encoding/json"
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
)

// build default freedom outbund
func buildDefaultOutbound() (*core.OutboundHandlerConfig, error) {
	outboundDetourConfig := &conf.OutboundDetourConfig{}
	outboundDetourConfig.Protocol = "freedom"
	outboundDetourConfig.Tag = "Default"
<<<<<<< HEAD
	// #region debug-point B:generated-outbound-strategy
	log.WithFields(log.Fields{"sessionId": "japan-a1-dns-errors", "runId": "post-fix", "hypothesisId": "B", "location": "core/outbound.go:buildDefaultOutbound", "domainStrategy": "AsIs", "strategyLayer": "system-dialer"}).Info("[DEBUG] Generated Freedom outbound strategy")
	// #endregion
=======
	outboundDetourConfig.StreamSetting = &conf.StreamConfig{
		SocketSettings: &conf.SocketConfig{
			DomainStrategy: defaultOutboundDomainStrategy(hasPublicIPv6()),
		},
	}
>>>>>>> 4fb9e11e3bbf15568901c53f9bb5da0277797b29
	//sendthrough := "origin"
	//outboundDetourConfig.SendThrough = &sendthrough

	proxySetting := &conf.FreedomConfig{}
	var setting json.RawMessage
	setting, err := json.Marshal(proxySetting)
	if err != nil {
		return nil, fmt.Errorf("marshal proxy config error: %s", err)
	}
	outboundDetourConfig.Settings = &setting
	return outboundDetourConfig.Build()
}

func defaultOutboundDomainStrategy(hasIPv6 bool) string {
	if hasIPv6 {
		return "UseIPv4v6"
	}
	return "UseIPv4"
}

// build block outbund
func buildBlockOutbound() (*core.OutboundHandlerConfig, error) {
	outboundDetourConfig := &conf.OutboundDetourConfig{}
	outboundDetourConfig.Protocol = "blackhole"
	outboundDetourConfig.Tag = "block"
	return outboundDetourConfig.Build()
}

// build dns outbound
func buildDnsOutbound() (*core.OutboundHandlerConfig, error) {
	outboundDetourConfig := &conf.OutboundDetourConfig{}
	outboundDetourConfig.Protocol = "dns"
	outboundDetourConfig.Tag = "dns_out"
	return outboundDetourConfig.Build()
}
