package panel

import (
	"encoding/json"
	"fmt"
	"path"
	"time"
)

type NodeInfo struct {
	Id                     int
	Type                   string
	PushInterval           int
	AccessAuditEnabled     bool
	PresenceMode           string
	PresenceLeaseTTL       int
	PullInterval           int
	TrafficReportThreshold int
	Protocol               *Protocol
}

type ServerPushStatusRequest struct {
	Cpu       float64 `json:"cpu"`
	Mem       float64 `json:"mem"`
	Disk      float64 `json:"disk"`
	UpdatedAt int64   `json:"updated_at"`
}

type NodeStatus struct {
	CPU    float64
	Mem    float64
	Disk   float64
	Uptime uint64
}

func (c *ClientV1) ReportNodeStatus(nodeStatus *NodeStatus) (err error) {
	p := "/v1/server/status"
	status := ServerPushStatusRequest{
		Cpu:       nodeStatus.CPU,
		Mem:       nodeStatus.Mem,
		Disk:      nodeStatus.Disk,
		UpdatedAt: time.Now().UnixMilli(),
	}
	response, err := c.Client.R().SetBody(status).ForceContentType("application/json").Post(p)
	if err != nil {
		return fmt.Errorf("访问 %s 失败: %v", path.Join(c.APIHost+p), err.Error())
	}
	if response.StatusCode() >= 400 {
		return fmt.Errorf("访问 %s 失败: %s", path.Join(c.APIHost+p), string(response.Body()))
	}
	if len(response.Body()) == 0 {
		return nil
	}
	var result serverPushUserTrafficResponse
	if err = json.Unmarshal(response.Body(), &result); err != nil {
		return fmt.Errorf("解析 %s 响应失败: %w", path.Join(c.APIHost+p), err)
	}
	if result.Code != 0 && result.Code != 200 {
		return fmt.Errorf(
			"访问 %s 失败: %s",
			path.Join(c.APIHost+p),
			result.Msg,
		)
	}
	return nil
}
