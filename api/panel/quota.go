package panel

import (
	"context"
	"fmt"

	"encoding/json/v2"
)

type QuotaLeaseSyncEntry struct {
	UID           int    `json:"uid"`
	LeaseID       string `json:"lease_id,omitempty"`
	Epoch         int64  `json:"epoch,omitempty"`
	Sequence      int64  `json:"sequence"`
	UploadTotal   int64  `json:"upload_total"`
	DownloadTotal int64  `json:"download_total"`
	RequestRaw    int64  `json:"request_raw"`
	Release       bool   `json:"release,omitempty"`
}

type QuotaLeaseSyncRequest struct {
	BatchID      string                `json:"batch_id"`
	NodeInstance string                `json:"node_instance"`
	Entries      []QuotaLeaseSyncEntry `json:"entries"`
}

type QuotaLeaseGrant struct {
	UID                  int    `json:"uid"`
	Required             bool   `json:"required"`
	Allowed              bool   `json:"allowed"`
	LeaseID              string `json:"lease_id"`
	Epoch                int64  `json:"epoch"`
	Sequence             int64  `json:"sequence"`
	GrantedRaw           int64  `json:"granted_raw"`
	ReportedUploadRaw    int64  `json:"reported_upload_raw"`
	ReportedDownloadRaw  int64  `json:"reported_download_raw"`
	BillingFactorPPM     int64  `json:"billing_factor_ppm"`
	ExpiresAt            int64  `json:"expires_at"`
	RemainingBilledBytes int64  `json:"remaining_billed_bytes"`
	Reason               string `json:"reason"`
}

type QuotaLeaseSyncDecision struct {
	Mode       string            `json:"mode"`
	ServerTime int64             `json:"server_time"`
	LeaseTTL   int64             `json:"lease_ttl"`
	Grants     []QuotaLeaseGrant `json:"grants"`
}

type quotaLeaseSyncResponse struct {
	Code int                     `json:"code"`
	Msg  string                  `json:"msg"`
	Data *QuotaLeaseSyncDecision `json:"data"`
}

type QuotaSyncResponseError struct {
	Code    int
	Message string
}

func (e *QuotaSyncResponseError) Error() string {
	return fmt.Sprintf(
		"traffic quota sync rejected with code %d: %s",
		e.Code,
		e.Message,
	)
}

func (c *ClientV1) SyncTrafficQuota(
	ctx context.Context,
	request QuotaLeaseSyncRequest,
) (*QuotaLeaseSyncDecision, error) {
	const requestPath = "/v2/node/traffic/quota/sync"
	body, err := c.postSignedJSON(ctx, requestPath, request)
	if err != nil {
		return nil, err
	}
	var response quotaLeaseSyncResponse
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode traffic quota response: %w", err)
	}
	if response.Code != 0 && response.Code != 200 {
		return nil, &QuotaSyncResponseError{
			Code:    response.Code,
			Message: response.Msg,
		}
	}
	if response.Data == nil {
		return nil, fmt.Errorf("traffic quota response did not include a decision")
	}
	return response.Data, nil
}
