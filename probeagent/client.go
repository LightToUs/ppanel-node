package probeagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

type Endpoint struct {
	Protocol             string `json:"protocol"`
	Kind                 string `json:"kind"`
	Address              string `json:"address"`
	Port                 uint16 `json:"port"`
	Credential           string `json:"credential,omitempty"`
	Security             string `json:"security,omitempty"`
	SNI                  string `json:"sni,omitempty"`
	AllowInsecure        bool   `json:"allow_insecure,omitempty"`
	Fingerprint          string `json:"fingerprint,omitempty"`
	RealityPublicKey     string `json:"reality_public_key,omitempty"`
	RealityShortID       string `json:"reality_short_id,omitempty"`
	Transport            string `json:"transport,omitempty"`
	Host                 string `json:"host,omitempty"`
	Path                 string `json:"path,omitempty"`
	ServiceName          string `json:"service_name,omitempty"`
	Flow                 string `json:"flow,omitempty"`
	Cipher               string `json:"cipher,omitempty"`
	ServerKey            string `json:"server_key,omitempty"`
	Obfs                 string `json:"obfs,omitempty"`
	ObfsPassword         string `json:"obfs_password,omitempty"`
	CongestionController string `json:"congestion_controller,omitempty"`
	ReduceRTT            bool   `json:"reduce_rtt,omitempty"`
}

type Task struct {
	TaskID         int64      `json:"task_id"`
	LeaseToken     string     `json:"lease_token"`
	LeaseExpiresAt int64      `json:"lease_expires_at"`
	TimeoutSeconds int        `json:"timeout_seconds"`
	Endpoints      []Endpoint `json:"endpoints"`
}

type CheckResult struct {
	Protocol  string `json:"protocol"`
	Kind      string `json:"kind"`
	Success   bool   `json:"success"`
	LatencyMS uint32 `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

type completion struct {
	TaskID     int64         `json:"task_id"`
	LeaseToken string        `json:"lease_token"`
	Status     string        `json:"status"`
	Checks     []CheckResult `json:"checks"`
}

type response[T any] struct {
	Code uint32 `json:"code"`
	Msg  string `json:"msg"`
	Data T      `json:"data"`
}

type Client struct {
	http *resty.Client
}

func NewClient(apiHost, token string) (*Client, error) {
	apiHost = strings.TrimRight(strings.TrimSpace(apiHost), "/")
	token = strings.TrimSpace(token)
	if apiHost == "" {
		return nil, errors.New("probe API host is required")
	}
	if token == "" {
		return nil, errors.New("probe agent token is required")
	}
	client := resty.New().
		SetBaseURL(apiHost).
		SetHeader("Authorization", "Bearer "+token).
		SetHeader("User-Agent", "ppnode-connectivity-probe/1").
		SetTimeout(20 * time.Second).
		SetRetryCount(0)
	return &Client{http: client}, nil
}

func (c *Client) Claim(ctx context.Context) (*Task, error) {
	var envelope response[*Task]
	resp, err := c.http.R().
		SetContext(ctx).
		SetResult(&envelope).
		Post("/v1/probe/connectivity/claim")
	if err != nil {
		return nil, fmt.Errorf("claim connectivity task: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("claim connectivity task: HTTP %d: %s", resp.StatusCode(), resp.String())
	}
	if envelope.Code != 0 && envelope.Code != 200 {
		return nil, fmt.Errorf("claim connectivity task: %s", envelope.Msg)
	}
	return envelope.Data, nil
}

func (c *Client) Complete(
	ctx context.Context,
	task *Task,
	checks []CheckResult,
) error {
	status := "passed"
	for _, check := range checks {
		if !check.Success {
			status = "failed"
			break
		}
	}
	body := completion{
		TaskID:     task.TaskID,
		LeaseToken: task.LeaseToken,
		Status:     status,
		Checks:     checks,
	}
	var envelope response[any]
	resp, err := c.http.R().
		SetContext(ctx).
		SetBody(body).
		SetResult(&envelope).
		Post("/v1/probe/connectivity/complete")
	if err != nil {
		return fmt.Errorf("complete connectivity task: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("complete connectivity task: HTTP %d: %s", resp.StatusCode(), resp.String())
	}
	if envelope.Code != 0 && envelope.Code != 200 {
		return fmt.Errorf("complete connectivity task: %s", envelope.Msg)
	}
	return nil
}
