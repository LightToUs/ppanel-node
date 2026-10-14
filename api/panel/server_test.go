package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lighttous/ppanel-node/conf"
)

func TestGetServerConfigRejectsEmptyData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"code":404,"msg":"not found","data":null}`))
	}))
	defer server.Close()

	client := NewClientV2(&conf.ServerApiConfig{
		ApiHost:   server.URL,
		ServerId:  1,
		SecretKey: "test-secret",
	})
	_, err := GetServerConfig(t.Context(), client)
	if err == nil || !strings.Contains(err.Error(), "协议配置为空") {
		t.Fatalf("GetServerConfig() error = %v, want empty protocol configuration error", err)
	}
}
