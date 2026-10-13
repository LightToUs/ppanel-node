package panel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"encoding/json/v2"

	"github.com/google/uuid"
)

func (c *ClientV1) postSignedJSON(ctx context.Context, requestPath string, body any) ([]byte, error) {
	if c == nil || c.SignedClient == nil {
		return nil, fmt.Errorf("signed node client is not configured")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode signed node request: %w", err)
	}
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := uuid.NewString()
	bodyHash := sha256.Sum256(payload)
	canonical := fmt.Sprintf(
		"POST\n%s\n%s\n%s\n%d\n%s\n%s",
		requestPath,
		timestamp,
		nonce,
		c.NodeId,
		c.NodeType,
		hex.EncodeToString(bodyHash[:]),
	)
	mac := hmac.New(sha256.New, []byte(c.SecretKey))
	_, _ = mac.Write([]byte(canonical))

	response, err := c.SignedClient.R().
		SetContext(ctx).
		SetHeaders(map[string]string{
			"Content-Type":     "application/json",
			"X-Node-ID":        strconv.Itoa(c.NodeId),
			"X-Node-Protocol":  c.NodeType,
			"X-Node-Timestamp": timestamp,
			"X-Node-Nonce":     nonce,
			"X-Node-Signature": hex.EncodeToString(mac.Sum(nil)),
		}).
		SetBody(payload).
		ForceContentType("application/json").
		Post(requestPath)
	if err != nil {
		return nil, fmt.Errorf("request %s failed: %w", requestPath, err)
	}
	if response.StatusCode() >= 400 {
		return nil, fmt.Errorf("request %s failed with status %d: %s", requestPath, response.StatusCode(), string(response.Body()))
	}
	return append([]byte(nil), response.Body()...), nil
}
