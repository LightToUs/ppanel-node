package node

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/conf"
	vCore "github.com/lighttous/ppanel-node/core"
)

type Node struct {
	controllers  []*Controller
	relayAgent   *RelayAgent
	metricsAgent *MetricsAgent
}

type ControllerSnapshot struct {
	Type     string
	Users    []panel.UserInfo
	AliveMap map[int]int
}

type RuntimeSnapshot struct {
	Controllers []ControllerSnapshot
}

func New(core *vCore.XrayCore, config *conf.Conf, serverconfig *panel.ServerConfigResponse) (*Node, error) {
	serverType := strings.ToLower(strings.TrimSpace(serverconfig.Data.ServerType))
	if serverType == "relay_egress" {
		client, err := panel.NewClientV1(&conf.NodeApiConfig{
			APIHost:           config.ApiConfig.ApiHost,
			NodeType:          "relay_egress",
			NodeID:            config.ApiConfig.ServerId,
			ManagedInstanceID: config.ApiConfig.ManagedInstanceID,
			SecretKey:         config.ApiConfig.SecretKey,
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
			metricsAgent: NewMetricsAgent(client, core, ""),
		}, nil
	}
	metricsClient, err := panel.NewClientV1(&conf.NodeApiConfig{
		APIHost:           config.ApiConfig.ApiHost,
		NodeType:          "metrics",
		NodeID:            config.ApiConfig.ServerId,
		ManagedInstanceID: config.ApiConfig.ManagedInstanceID,
		SecretKey:         config.ApiConfig.SecretKey,
	})
	if err != nil {
		return nil, err
	}
	node := &Node{
		controllers:  make([]*Controller, len(*serverconfig.Data.Protocols)),
		metricsAgent: NewMetricsAgent(metricsClient, core, ""),
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
			QuotaLeaseMode:         serverconfig.Data.QuotaLeaseMode,
			QuotaLeaseTTL:          serverconfig.Data.QuotaLeaseTTL,
			QuotaLeaseGrantBytes:   serverconfig.Data.QuotaLeaseGrantBytes,
			PushInterval:           pushinterval,
			PullInterval:           pullinterval,
			Protocol:               &nodeconfig,
		}
		p, err := panel.NewClientV1(&conf.NodeApiConfig{
			APIHost:           config.ApiConfig.ApiHost,
			NodeType:          nodeconfig.Type,
			NodeID:            config.ApiConfig.ServerId,
			ManagedInstanceID: config.ApiConfig.ManagedInstanceID,
			SecretKey:         config.ApiConfig.SecretKey,
		})
		if err != nil {
			return nil, err
		}
		node.controllers[i] = NewController(core, p, n, config.StatePath)
	}
	node.metricsAgent.SetBusinessProtocols(*serverconfig.Data.Protocols)
	node.metricsAgent.SetCertificateSource(node.certificateReports)

	return node, nil
}

// Prepare fetches remote users and certificates while the current runtime is
// still serving. After this succeeds, Start only performs local operations.
func (n *Node) Prepare() error {
	if n == nil || n.relayAgent != nil {
		return nil
	}
	for _, controller := range n.controllers {
		if controller == nil || !controller.info.Protocol.Enable {
			continue
		}
		if err := controller.Prepare(); err != nil {
			return fmt.Errorf(
				"准备节点 [%s-%s-%d] 失败: %w",
				controller.apiClient.APIHost,
				controller.info.Type,
				controller.info.Id,
				err,
			)
		}
	}
	return nil
}

func (n *Node) Snapshot() *RuntimeSnapshot {
	if n == nil || n.relayAgent != nil {
		return &RuntimeSnapshot{}
	}
	snapshot := &RuntimeSnapshot{
		Controllers: make([]ControllerSnapshot, len(n.controllers)),
	}
	for i, controller := range n.controllers {
		if controller == nil {
			continue
		}
		aliveMap := make(map[int]int, len(controller.aliveMap))
		for userID, value := range controller.aliveMap {
			aliveMap[userID] = value
		}
		snapshot.Controllers[i] = ControllerSnapshot{
			Type:     controller.info.Type,
			Users:    cloneUserList(controller.userList),
			AliveMap: aliveMap,
		}
	}
	return snapshot
}

// RestoreSnapshot seeds a replacement runtime from the last in-memory state,
// allowing rollback even while the control plane is unreachable.
func (n *Node) RestoreSnapshot(snapshot *RuntimeSnapshot) error {
	if n == nil || n.relayAgent != nil {
		return nil
	}
	if snapshot == nil || len(snapshot.Controllers) != len(n.controllers) {
		return errors.New("runtime snapshot does not match controller count")
	}
	for i, controller := range n.controllers {
		state := snapshot.Controllers[i]
		if controller == nil || state.Type != controller.info.Type {
			return errors.New("runtime snapshot does not match controller protocol")
		}
		controller.userList = cloneUserList(state.Users)
		controller.aliveMap = make(map[int]int, len(state.AliveMap))
		for userID, value := range state.AliveMap {
			controller.aliveMap[userID] = value
		}
		controller.prepared = true
	}
	return nil
}

func cloneUserList(users []panel.UserInfo) []panel.UserInfo {
	cloned := append([]panel.UserInfo(nil), users...)
	for i := range cloned {
		cloned[i].AllowedIPs = append([]string(nil), users[i].AllowedIPs...)
	}
	return cloned
}

func (n *Node) certificateReports() []panel.CertificateStatusReport {
	if n == nil {
		return nil
	}
	reports := make([]panel.CertificateStatusReport, 0, len(n.controllers))
	for _, controller := range n.controllers {
		if controller == nil {
			continue
		}
		report, err := certificateStatusReport(controller.info)
		if err != nil || report == nil {
			continue
		}
		reports = append(reports, *report)
	}
	return reports
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
	if n.metricsAgent != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := n.metricsAgent.Prime(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("initialize server traffic budget: %w", err)
		}
	}
	if n.relayAgent != nil {
		if err := n.relayAgent.Start(); err != nil {
			return err
		}
		if n.metricsAgent != nil {
			n.metricsAgent.Start()
		}
		return nil
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
	if n.metricsAgent != nil {
		n.metricsAgent.Start()
	}
	return nil
}

func (n *Node) Close() error {
	if n == nil {
		return nil
	}
	var closeErr error
	if n.metricsAgent != nil {
		n.metricsAgent.Close()
	}
	if n.relayAgent != nil {
		closeErr = errors.Join(closeErr, n.relayAgent.Close())
	}
	for _, c := range n.controllers {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return closeErr
}
