package panel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lighttous/ppanel-node/conf"
)

func TestReportNodeOnlineSnapshotUsesSignedRequestWithoutQuerySecret(t *testing.T) {
	const secret = "node-secret"
	var requestErr error
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/node/online/snapshot" {
			requestErr = fmt.Errorf("path = %q", request.URL.Path)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.URL.RawQuery != "" {
			requestErr = fmt.Errorf("signed request leaked query params: %q", request.URL.RawQuery)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			requestErr = err
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		timestamp := request.Header.Get("X-Node-Timestamp")
		nonce := request.Header.Get("X-Node-Nonce")
		bodyHash := sha256.Sum256(body)
		canonical := fmt.Sprintf(
			"POST\n/v2/node/online/snapshot\n%s\n%s\n49\nhysteria2\n%s",
			timestamp,
			nonce,
			hex.EncodeToString(bodyHash[:]),
		)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(canonical))
		if request.Header.Get("X-Node-Signature") != hex.EncodeToString(mac.Sum(nil)) {
			requestErr = fmt.Errorf("invalid request signature")
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":0,"msg":""}`))
	}))
	defer server.Close()

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeType:  "hysteria2",
		NodeID:    49,
		SecretKey: secret,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	users := []OnlineUser{{UID: 42, IP: "203.0.113.8"}}
	if err = client.ReportNodeOnlineSnapshot(t.Context(), &users); err != nil {
		t.Fatalf("report online snapshot: %v (server: %v)", err, requestErr)
	}
	if requestErr != nil {
		t.Fatal(requestErr)
	}
}
