package node

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/conf"
	vCore "github.com/lighttous/ppanel-node/core"
)

type Node struct {
	controllers []*Controller
	relayAgent  *RelayAgent
}

func New(core *vCore.XrayCore, config *conf.Conf, serverconfig *panel.ServerConfigResponse) (*Node, error) {
	serverType := strings.ToLower(strings.TrimSpace(serverconfig.Data.ServerType))
	if serverType == "relay_egress" {
		client, err := panel.NewClientV1(&conf.NodeApiConfig{
			APIHost:   config.ApiConfig.ApiHost,
			NodeType:  "relay_egress",
			NodeID:    config.ApiConfig.ServerId,
			SecretKey: config.ApiConfig.SecretKey,
		})
		if err != nil {
			return nil, err
		}
		pushInterval := serverconfig.Data.PushInterval
		if pushInterval <= 0 {
			pushInterval = 60
		}
		return &Node{
			controllers: make([]*Controller, 0),
			relayAgent: NewRelayAgent(
				client,
				config.StatePath,
				config.ApiConfig.ServerId,
				pushInterval,
				core,
			),
		}, nil
	}
	node := &Node{
		controllers: make([]*Controller, len(*serverconfig.Data.Protocols)),
	}
	pushinterval := serverconfig.Data.PushInterval
	if pushinterval <= 0 {
		pushinterval = 60
	}
	pullinterval := serverconfig.Data.PullInterval
	if pullinterval <= 0 {
		pullinterval = 60
	}
	for i, nodeconfig := range *serverconfig.Data.Protocols {
		normalizeProtocolForRuntime(&nodeconfig)
		n := &panel.NodeInfo{
			Id:                     config.ApiConfig.ServerId,
			Type:                   nodeconfig.Type,
			TrafficReportThreshold: serverconfig.Data.TrafficReportThreshold,
			AccessAuditEnabled:     serverconfig.Data.AccessAuditEnabled,
			PresenceMode:           serverconfig.Data.PresenceMode,
			PresenceLeaseTTL:       serverconfig.Data.PresenceLeaseTTL,
			PushInterval:           pushinterval,
			PullInterval:           pullinterval,
			Protocol:               &nodeconfig,
		}
		p, err := panel.NewClientV1(&conf.NodeApiConfig{
			APIHost:   config.ApiConfig.ApiHost,
			NodeType:  nodeconfig.Type,
			NodeID:    config.ApiConfig.ServerId,
			SecretKey: config.ApiConfig.SecretKey,
		})
		if err != nil {
			return nil, err
		}
		node.controllers[i] = NewController(core, p, n, config.StatePath)
	}

	return node, nil
}

func normalizeProtocolForRuntime(protocol *panel.Protocol) {
	if protocol == nil {
		return
	}

	switch strings.ToLower(strings.TrimSpace(protocol.Type)) {
	case "hy2", "hysteria2":
		protocol.Security = "tls"
		if protocol.CertMode == "" || protocol.CertMode == "none" {
			protocol.CertMode = "self"
		}
	}
}

func (n *Node) Start() error {
	if n.relayAgent != nil {
		return n.relayAgent.Start()
	}
	for i := range n.controllers {
		if !n.controllers[i].info.Protocol.Enable {
			continue
		}
		err := n.controllers[i].Start()
		if err != nil {
			for j := 0; j < i; j++ {
				_ = n.controllers[j].Close()
			}
			return fmt.Errorf("启动节点 [%s-%s-%d] 失败: %s",
				n.controllers[i].apiClient.APIHost,
				n.controllers[i].info.Type,
				n.controllers[i].info.Id,
				err)
		}
	}
	return nil
}

func (n *Node) Close() error {
	if n == nil {
		return nil
	}
	var closeErr error
	if n.relayAgent != nil {
		closeErr = errors.Join(closeErr, n.relayAgent.Close())
		n.relayAgent = nil
	}
	for _, c := range n.controllers {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	n.controllers = nil
	return closeErr
}
