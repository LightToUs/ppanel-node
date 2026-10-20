package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
)

type RelayTrafficReport struct {
	InterfaceName string `json:"interface_name"`
	SessionID     string `json:"session_id"`
	Sequence      uint64 `json:"sequence"`
	UploadTotal   uint64 `json:"upload_total"`
	DownloadTotal uint64 `json:"download_total"`
	ReportedAt    int64  `json:"reported_at"`
}

func (c *ClientV1) ReportRelayTraffic(
	ctx context.Context,
	report *RelayTrafficReport,
) error {
	const endpoint = "/v1/server/relay/traffic"
	response, err := c.Client.R().
		SetContext(ctx).
		SetBody(report).
		ForceContentType("application/json").
		Post(endpoint)
	if err != nil {
		return fmt.Errorf("访问 %s 失败: %w", path.Join(c.APIHost+endpoint), err)
	}
	if response.StatusCode() >= 400 {
		return fmt.Errorf(
			"访问 %s 失败: %s",
			path.Join(c.APIHost+endpoint),
			string(response.Body()),
		)
	}
	if len(response.Body()) == 0 {
		return nil
	}
	var result serverPushUserTrafficResponse
	if err = json.Unmarshal(response.Body(), &result); err != nil {
		return fmt.Errorf("解析 %s 响应失败: %w", path.Join(c.APIHost+endpoint), err)
	}
	if result.Code != 0 && result.Code != 200 {
		return fmt.Errorf("访问 %s 失败: %s", path.Join(c.APIHost+endpoint), result.Msg)
	}
	return nil
}
