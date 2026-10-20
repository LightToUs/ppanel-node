package panel

import (
	"context"
	"encoding/json"
	"fmt"
)

type ServerMetricsReport struct {
	SchemaVersion  int     `json:"schema_version"`
	AgentSessionID string  `json:"agent_session_id"`
	Sequence       uint64  `json:"sequence"`
	CollectedAt    int64   `json:"collected_at"`
	BootID         string  `json:"boot_id"`
	BootTime       int64   `json:"boot_time"`
	UptimeSeconds  uint64  `json:"uptime_seconds"`
	InterfaceName  string  `json:"interface_name"`
	UploadBytes    uint64  `json:"upload_bytes"`
	DownloadBytes  uint64  `json:"download_bytes"`
	CPUPercent     float64 `json:"cpu_percent"`
	MemoryPercent  float64 `json:"memory_percent"`
	DiskPercent    float64 `json:"disk_percent"`
}

func (c *ClientV1) ReportServerMetrics(
	ctx context.Context,
	report *ServerMetricsReport,
) error {
	const endpoint = "/v2/node/metrics"
	body, err := c.postSignedJSON(ctx, endpoint, report)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	var result serverPushUserTrafficResponse
	if err = json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode %s response: %w", endpoint, err)
	}
	if result.Code != 0 && result.Code != 200 {
		return fmt.Errorf("request %s failed: %s", endpoint, result.Msg)
	}
	return nil
}
