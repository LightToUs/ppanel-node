package panel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lighttous/ppanel-node/conf"
)

func TestGetUserListDistinguishesEmptyListFromNotModified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"users":[]}`))
	}))
	defer server.Close()

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeID:    1,
		NodeType:  "wireguard",
		SecretKey: "test-secret",
	})
	if err != nil {
		t.Fatalf("NewClientV1() error = %v", err)
	}
	users, err := client.GetUserList(t.Context())
	if err != nil {
		t.Fatalf("GetUserList() error = %v", err)
	}
	if users == nil {
		t.Fatal("empty user list must be non-nil so it is not treated as HTTP 304")
	}
	if len(users) != 0 {
		t.Fatalf("len(users) = %d, want 0", len(users))
	}
}
