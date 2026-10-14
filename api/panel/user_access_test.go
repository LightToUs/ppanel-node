package panel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/lighttous/ppanel-node/conf"
)

func TestReportAccessEventsUsesSignedRequestWithoutQuerySecret(t *testing.T) {
	const secret = "node-secret"
	var requestErr error
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/node/access/events" {
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
			"POST\n/v2/node/access/events\n%s\n%s\n49\nhysteria2\n%s",
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
		if _, err = strconv.ParseInt(timestamp, 10, 64); err != nil {
			requestErr = fmt.Errorf("invalid timestamp: %w", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":0,"data":{"accepted":1}}`))
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
	err = client.ReportAccessEvents(t.Context(), []AccessEvent{{
		EventID:     "c4de18e5-94e3-43aa-bff4-431945ce5753",
		UID:         42,
		ClientIP:    "203.0.113.8",
		ConnectedAt: 1_700_000_000_000,
	}})
	if err != nil {
		t.Fatalf("report access events: %v (server: %v)", err, requestErr)
	}
	if requestErr != nil {
		t.Fatal(requestErr)
	}
}

func TestReportAccessEventsRejectsBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":500,"msg":"Internal Server Error"}`))
	}))
	defer server.Close()

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeType:  "wireguard",
		NodeID:    49,
		SecretKey: "node-secret",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	err = client.ReportAccessEvents(t.Context(), []AccessEvent{{
		EventID:     "c4de18e5-94e3-43aa-bff4-431945ce5754",
		UID:         42,
		PeerID:      "1",
		TunnelIP:    "10.240.49.2",
		ConnectedAt: 1_700_000_000_000,
	}})
	if err == nil {
		t.Fatal("expected business response error")
	}
}

func TestAcquirePresenceDecodesDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/node/presence/acquire" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":0,"msg":"","data":{"allowed":false,"mode":"observe","online":3,"limit":2,"lease_ttl":90}}`))
	}))
	defer server.Close()

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeType:  "vless",
		NodeID:    49,
		SecretKey: "node-secret",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	decision, err := client.AcquirePresence(t.Context(), 42, "203.0.113.8")
	if err != nil {
		t.Fatalf("acquire presence: %v", err)
	}
	if decision.Allowed || decision.Mode != "observe" || decision.Online != 3 || decision.Limit != 2 {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}
