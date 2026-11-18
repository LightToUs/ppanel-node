package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrMetricsUnsupported = errors.New("control plane does not support realtime metrics")

type ServerMetricsReport struct {
	SchemaVersion     int                       `json:"schema_version"`
	ManagedInstanceID int64                     `json:"managed_instance_id,omitempty"`
	AgentSessionID    string                    `json:"agent_session_id"`
	Sequence          uint64                    `json:"sequence"`
	CollectedAt       int64                     `json:"collected_at"`
	BootID            string                    `json:"boot_id"`
	BootTime          int64                     `json:"boot_time"`
	UptimeSeconds     uint64                    `json:"uptime_seconds"`
	InterfaceName     string                    `json:"interface_name"`
	UploadBytes       uint64                    `json:"upload_bytes"`
	DownloadBytes     uint64                    `json:"download_bytes"`
	CPUPercent        float64                   `json:"cpu_percent"`
	MemoryPercent     float64                   `json:"memory_percent"`
	DiskPercent       float64                   `json:"disk_percent"`
	Certificates      []CertificateStatusReport `json:"certificates,omitempty"`
}

type CertificateStatusReport struct {
	ProtocolKey string `json:"protocol_key"`
	Domain      string `json:"domain"`
	Fingerprint string `json:"fingerprint"`
	NotAfter    int64  `json:"not_after"`
}

type ServerTrafficBudgetDirective struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode"`
	Status         string `json:"status"`
	Action         string `json:"action"`
	LimitBytes     uint64 `json:"limit_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	SuspendAtBytes uint64 `json:"suspend_at_bytes"`
	PeriodStart    int64  `json:"period_start"`
	PeriodEnd      int64  `json:"period_end"`
	ServerTime     int64  `json:"server_time"`
}

type serverMetricsResponse struct {
	Code int                           `json:"code"`
	Msg  string                        `json:"msg"`
	Data *ServerTrafficBudgetDirective `json:"data"`
}

func (c *ClientV1) ReportServerMetrics(
	ctx context.Context,
	report *ServerMetricsReport,
) (*ServerTrafficBudgetDirective, error) {
	const endpoint = "/v2/node/metrics"
	report.ManagedInstanceID = c.ManagedInstanceID
	body, err := c.postSignedJSON(ctx, endpoint, report)
	if err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == 404 {
			return nil, ErrMetricsUnsupported
		}
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("server metrics response is empty")
	}
	var result serverMetricsResponse
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", endpoint, err)
	}
	if result.Code != 0 && result.Code != 200 {
		return nil, fmt.Errorf("request %s failed: %s", endpoint, result.Msg)
	}
	if result.Data == nil {
		return nil, errors.New("server metrics response has no traffic budget directive")
	}
	return result.Data, nil
}
