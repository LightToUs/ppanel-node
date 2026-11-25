package panel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const controlPath = "/v2/node/control"

type ControlMessage struct {
	Type            string `json:"type"`
	EventID         string `json:"event_id"`
	UserSubscribeID int64  `json:"user_subscribe_id"`
	AuthRevision    int64  `json:"auth_revision"`
	DesiredState    string `json:"desired_state"`
	EventType       string `json:"event_type"`
	CreatedAt       int64  `json:"created_at"`
}

type ControlRequest struct {
	Type            string  `json:"type"`
	SubscriptionIDs []int64 `json:"subscription_ids,omitempty"`
}

func (c *ClientV1) DialControl(ctx context.Context) (*websocket.Conn, error) {
	if c == nil {
		return nil, fmt.Errorf("node client is not configured")
	}
	endpoint, err := url.Parse(c.APIHost)
	if err != nil {
		return nil, fmt.Errorf("parse control API URL: %w", err)
	}
	switch endpoint.Scheme {
	case "https":
		endpoint.Scheme = "wss"
	case "http":
		endpoint.Scheme = "ws"
	default:
		return nil, fmt.Errorf("unsupported control API scheme %q", endpoint.Scheme)
	}
	endpoint.Path = path.Join(endpoint.Path, controlPath)
	query := endpoint.Query()
	query.Set("managed_instance_id", strconv.FormatInt(c.ManagedInstanceID, 10))
	endpoint.RawQuery = query.Encode()

	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := uuid.NewString()
	bodyHash := sha256.Sum256(nil)
	canonical := fmt.Sprintf(
		"GET\n%s\n%s\n%s\n%d\ncontrol\n%s",
		endpoint.Path,
		timestamp,
		nonce,
		c.NodeId,
		hex.EncodeToString(bodyHash[:]),
	)
	mac := hmac.New(sha256.New, []byte(c.SecretKey))
	_, _ = mac.Write([]byte(canonical))
	headers := http.Header{
		"X-Node-ID":           []string{strconv.Itoa(c.NodeId)},
		"X-Node-Protocol":     []string{"control"},
		"X-Node-Timestamp":    []string{timestamp},
		"X-Node-Nonce":        []string{nonce},
		"X-Node-Signature":    []string{hex.EncodeToString(mac.Sum(nil))},
		"X-Node-Version":      []string{nodeVersion},
		"X-Node-Capabilities": []string{nodeCapabilities},
	}
	conn, response, err := websocket.DefaultDialer.DialContext(
		ctx,
		endpoint.String(),
		headers,
	)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("connect node control channel: %w", err)
	}
	return conn, nil
}
