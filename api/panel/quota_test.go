package panel

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lighttous/ppanel-node/conf"
)

func TestSyncTrafficQuotaReturnsBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(
			`{"code":400,"msg":"Entries must contain at maximum 1,000 items"}`,
		))
	}))
	defer server.Close()

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeID:    63,
		NodeType:  "vless",
		SecretKey: "test-secret",
	})
	if err != nil {
		t.Fatalf("NewClientV1: %v", err)
	}
	_, err = client.SyncTrafficQuota(t.Context(), QuotaLeaseSyncRequest{
		BatchID:      "test-batch",
		NodeInstance: "test-node",
		Entries: []QuotaLeaseSyncEntry{{
			UID: 1,
		}},
	})
	if err == nil {
		t.Fatal("SyncTrafficQuota unexpectedly accepted a business error")
	}
	var responseErr *QuotaSyncResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("error type = %T, want *QuotaSyncResponseError", err)
	}
	if responseErr.Code != 400 {
		t.Fatalf("business error code = %d, want 400", responseErr.Code)
	}
	if !strings.Contains(err.Error(), "maximum 1,000 items") {
		t.Fatalf("business error = %q, want server message", err)
	}
}
