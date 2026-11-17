package probeagent

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	quic "github.com/apernet/quic-go"
	"github.com/lighttous/ppanel-node/api/panel"
	nodecore "github.com/lighttous/ppanel-node/core"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet/reality"
)

func Check(ctx context.Context, task *Task) []CheckResult {
	timeout := time.Duration(task.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 8 * time.Second
	}
	results := make([]CheckResult, len(task.Endpoints))
	var wait sync.WaitGroup
	wait.Add(len(task.Endpoints))
	for i := range task.Endpoints {
		go func(index int) {
			defer wait.Done()
			endpoint := task.Endpoints[index]
			checkCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			startedAt := time.Now()
			err := checkEndpoint(checkCtx, endpoint, timeout)
			results[index] = CheckResult{
				Protocol:  endpoint.Protocol,
				Kind:      endpoint.Kind,
				Success:   err == nil,
				LatencyMS: durationMilliseconds(time.Since(startedAt)),
			}
			if err != nil {
				results[index].Error = truncateError(err.Error())
			}
		}(i)
	}
	wait.Wait()
	return results
}

func checkEndpoint(ctx context.Context, endpoint Endpoint, timeout time.Duration) error {
	if strings.TrimSpace(endpoint.Address) == "" || endpoint.Port == 0 {
		return errors.New("invalid probe endpoint")
	}
	switch endpoint.Kind {
	case "tcp":
		return checkTCP(ctx, endpoint)
	case "tls":
		return checkTLS(ctx, endpoint, timeout)
	case "quic":
		return checkQUIC(ctx, endpoint)
	case "reality":
		return checkReality(ctx, endpoint)
	case "xray":
		return checkXrayProtocol(ctx, endpoint, timeout)
	default:
		return fmt.Errorf("unsupported probe kind %q", endpoint.Kind)
	}
}

func checkXrayProtocol(ctx context.Context, endpoint Endpoint, timeout time.Duration) error {
	if strings.TrimSpace(endpoint.Credential) == "" {
		return errors.New("protocol probe credential is missing")
	}
	protocol := strings.ToLower(strings.TrimSpace(endpoint.Protocol))
	security := strings.ToLower(strings.TrimSpace(endpointSecurity(endpoint)))
	outbound := panel.Outbound{
		Name:                 "connectivity-probe",
		Protocol:             protocol,
		Address:              strings.Trim(endpoint.Address, "[]"),
		Port:                 int(endpoint.Port),
		Password:             endpoint.Credential,
		UUID:                 endpoint.Credential,
		Cipher:               endpoint.Cipher,
		ServerKey:            endpoint.ServerKey,
		Security:             security,
		SNI:                  endpoint.SNI,
		AllowInsecure:        endpoint.AllowInsecure,
		Fingerprint:          endpoint.Fingerprint,
		Transport:            endpoint.Transport,
		Host:                 endpoint.Host,
		Path:                 endpoint.Path,
		ServiceName:          endpoint.ServiceName,
		Flow:                 endpoint.Flow,
		CongestionController: endpoint.CongestionController,
		ReduceRTT:            endpoint.ReduceRTT,
		RealityPublicKey:     endpoint.RealityPublicKey,
		RealityShortID:       endpoint.RealityShortID,
		Obfs:                 endpoint.Obfs,
		ObfsPassword:         endpoint.ObfsPassword,
	}
	client, err := nodecore.NewProbeClient(outbound)
	if err != nil {
		return err
	}
	defer client.Close()

	const verifyHost = "cp.cloudflare.com"
	conn, err := client.Dial(ctx, verifyHost, 80)
	if err != nil {
		return fmt.Errorf("dial through %s proxy: %w", protocol, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if err = conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set protocol probe deadline: %w", err)
	}
	if _, err = fmt.Fprintf(
		conn,
		"GET /generate_204 HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
		verifyHost,
	); err != nil {
		return fmt.Errorf("write protocol probe request: %w", err)
	}
	statusLine, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read protocol probe response: %w", err)
	}
	if !strings.HasPrefix(statusLine, "HTTP/") {
		return fmt.Errorf("protocol probe returned an invalid HTTP response: %q", strings.TrimSpace(statusLine))
	}
	return nil
}

func endpointSecurity(endpoint Endpoint) string {
	security := strings.ToLower(strings.TrimSpace(endpoint.Security))
	if security != "" {
		return security
	}
	if endpoint.RealityPublicKey != "" {
		return "reality"
	}
	if protocol := strings.ToLower(strings.TrimSpace(endpoint.Protocol)); protocol == "hysteria" ||
		protocol == "hysteria2" ||
		protocol == "hy2" ||
		protocol == "tuic" {
		return "tls"
	}
	return ""
}

func checkTCP(ctx context.Context, endpoint Endpoint) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", endpointAddress(endpoint))
	if err != nil {
		return err
	}
	return conn.Close()
}

func checkTLS(ctx context.Context, endpoint Endpoint, timeout time.Duration) error {
	serverName := strings.TrimSpace(endpoint.SNI)
	if serverName == "" {
		serverName = strings.Trim(endpoint.Address, "[]")
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", endpointAddress(endpoint), &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		InsecureSkipVerify: endpoint.AllowInsecure,
	})
	if err != nil {
		return err
	}
	return conn.Close()
}

func checkQUIC(ctx context.Context, endpoint Endpoint) error {
	serverName := strings.TrimSpace(endpoint.SNI)
	if serverName == "" {
		serverName = strings.Trim(endpoint.Address, "[]")
	}
	conn, err := quic.DialAddr(ctx, endpointAddress(endpoint), &tls.Config{
		MinVersion:         tls.VersionTLS13,
		ServerName:         serverName,
		InsecureSkipVerify: endpoint.AllowInsecure,
		NextProtos:         []string{"h3"},
	}, &quic.Config{
		HandshakeIdleTimeout: 5 * time.Second,
		MaxIdleTimeout:       5 * time.Second,
	})
	if err != nil {
		return err
	}
	return conn.CloseWithError(0, "probe complete")
}

func checkReality(ctx context.Context, endpoint Endpoint) error {
	publicKey, err := base64.RawURLEncoding.DecodeString(endpoint.RealityPublicKey)
	if err != nil || len(publicKey) != 32 {
		return errors.New("invalid Reality public key")
	}
	if len(endpoint.RealityShortID) > 16 {
		return errors.New("invalid Reality short ID")
	}
	shortID := make([]byte, 8)
	if _, err = hex.Decode(shortID, []byte(endpoint.RealityShortID)); err != nil {
		return errors.New("invalid Reality short ID")
	}
	var dialer net.Dialer
	rawConn, err := dialer.DialContext(ctx, "tcp", endpointAddress(endpoint))
	if err != nil {
		return err
	}
	config := &reality.Config{
		Fingerprint: "chrome",
		ServerName:  strings.TrimSpace(endpoint.SNI),
		PublicKey:   publicKey,
		ShortId:     shortID,
		SpiderX:     "/",
	}
	dest := xnet.TCPDestination(
		xnet.ParseAddress(endpoint.Address),
		xnet.Port(endpoint.Port),
	)
	conn, err := reality.UClient(rawConn, config, ctx, dest)
	if err != nil {
		_ = rawConn.Close()
		return err
	}
	defer conn.Close()
	realityConn, ok := conn.(*reality.UConn)
	if !ok || !realityConn.Verified {
		return errors.New("Reality handshake fell back to an unverified TLS destination")
	}
	return nil
}

func endpointAddress(endpoint Endpoint) string {
	return net.JoinHostPort(
		strings.Trim(endpoint.Address, "[]"),
		strconv.FormatUint(uint64(endpoint.Port), 10),
	)
}

func durationMilliseconds(value time.Duration) uint32 {
	milliseconds := value.Milliseconds()
	if milliseconds < 0 {
		return 0
	}
	if milliseconds > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(milliseconds)
}

func truncateError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 1024 {
		return value
	}
	return value[:1024]
}
