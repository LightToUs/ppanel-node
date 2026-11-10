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

func TestGetUserListReusesResponseETag(t *testing.T) {
	const etag = `"users-v1"`
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestCount++
		if requestCount == 1 {
			response.Header().Set("Content-Type", "application/json")
			response.Header().Set("ETag", etag)
			_, _ = response.Write([]byte(`{"users":[{"id":1,"uuid":"uuid-1"}]}`))
			return
		}
		if got := request.Header.Get("If-None-Match"); got != etag {
			t.Fatalf("If-None-Match = %q, want %q", got, etag)
		}
		response.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	client, err := NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeID:    1,
		NodeType:  "vless",
		SecretKey: "test-secret",
	})
	if err != nil {
		t.Fatalf("NewClientV1() error = %v", err)
	}
	users, err := client.GetUserList(t.Context())
	if err != nil {
		t.Fatalf("first GetUserList() error = %v", err)
	}
	if len(users) != 1 || users[0].Uuid != "uuid-1" {
		t.Fatalf("first users = %#v, want uuid-1", users)
	}

	users, err = client.GetUserList(t.Context())
	if err != nil {
		t.Fatalf("second GetUserList() error = %v", err)
	}
	if users != nil {
		t.Fatalf("second users = %#v, want nil for HTTP 304", users)
	}
}
