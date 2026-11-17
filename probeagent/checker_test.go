package probeagent

import (
	"context"
	"net"
	"testing"
)

func TestCheckTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	results := Check(context.Background(), &Task{
		TimeoutSeconds: 2,
		Endpoints: []Endpoint{{
			Protocol: "shadowsocks",
			Kind:     "tcp",
			Address:  "127.0.0.1",
			Port:     uint16(port),
		}},
	})
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("Check() = %#v, want successful TCP result", results)
	}
}

func TestCheckRejectsUnknownKind(t *testing.T) {
	results := Check(context.Background(), &Task{
		TimeoutSeconds: 1,
		Endpoints: []Endpoint{{
			Protocol: "unknown",
			Kind:     "unsupported",
			Address:  "127.0.0.1",
			Port:     1,
		}},
	})
	if len(results) != 1 || results[0].Success || results[0].Error == "" {
		t.Fatalf("Check() = %#v, want failed unsupported result", results)
	}
}
