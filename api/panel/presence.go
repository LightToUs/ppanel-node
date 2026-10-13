package panel

import (
	"context"
	"fmt"

	"encoding/json/v2"
)

type PresenceRequest struct {
	UID      int    `json:"uid"`
	ClientIP string `json:"client_ip"`
}

type PresenceDecision struct {
	Allowed  bool   `json:"allowed"`
	Mode     string `json:"mode"`
	Online   int64  `json:"online"`
	Limit    int64  `json:"limit"`
	LeaseTTL int64  `json:"lease_ttl"`
}

type presenceResponse struct {
	Code int               `json:"code"`
	Msg  string            `json:"msg"`
	Data *PresenceDecision `json:"data"`
}

func (c *ClientV1) AcquirePresence(ctx context.Context, uid int, clientIP string) (*PresenceDecision, error) {
	const requestPath = "/v2/node/presence/acquire"
	body, err := c.postSignedJSON(ctx, requestPath, PresenceRequest{
		UID:      uid,
		ClientIP: clientIP,
	})
	if err != nil {
		return nil, err
	}
	var response presenceResponse
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode presence response: %w", err)
	}
	if response.Data == nil {
		return nil, fmt.Errorf("presence response did not include a decision")
	}
	return response.Data, nil
}
