package agentmode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
)

// ClientOptions configures the HTTP client used to communicate with the
// controller.
type ClientOptions struct {
	ResourceClass      string
	TeamID             string
	Timeout            time.Duration
	InsecureSkipVerify bool
	UserAgent          string
	AgentID            string
}

// Client issues authenticated requests against the controller.
type Client struct {
	scope     agentproto.UserScope
	baseURL   *url.URL
	token     string
	http      *http.Client
	userAgent string
	agentID   string
}

// NewClient constructs a client for the provided controller URL and token.
func NewClient(baseURL, token string, opts ClientOptions) (*Client, error) {
	if err := (config.Agent{ResourceClass: opts.ResourceClass, TeamID: opts.TeamID}).ValidateUserScope(); err != nil {
		return nil, err
	}
	trimmedURL := strings.TrimSpace(baseURL)
	if trimmedURL == "" {
		return nil, errors.New("controller url is required")
	}
	parsed, err := url.Parse(trimmedURL)
	if err != nil {
		return nil, fmt.Errorf("parse controller url: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("controller token is required")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	transport := http.DefaultTransport
	if t, ok := transport.(*http.Transport); ok {
		clone := t.Clone()
		if opts.InsecureSkipVerify {
			if clone.TLSClientConfig == nil {
				clone.TLSClientConfig = &tls.Config{}
			}
			clone.TLSClientConfig.InsecureSkipVerify = true
		}
		transport = clone
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}

	userAgent := strings.TrimSpace(opts.UserAgent)
	if userAgent == "" {
		userAgent = "xcontrol-agent"
	}

	return &Client{
		baseURL:   parsed,
		scope:     agentproto.UserScope{ResourceClass: opts.ResourceClass, TeamID: opts.TeamID},
		token:     token,
		http:      client,
		userAgent: userAgent,
		agentID:   strings.TrimSpace(opts.AgentID),
	}, nil
}

// ListClients fetches the current set of Xray clients from the controller.
func (c *Client) ListClients(ctx context.Context) (agentproto.ClientListResponse, error) {
	paths := []string{"/api/agent-server/v1/users"}
	var lastErr error

	for _, path := range paths {
		endpoint, err := url.JoinPath(c.baseURL.String(), path)
		if err != nil {
			return agentproto.ClientListResponse{}, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return agentproto.ClientListResponse{}, err
		}
		c.applyHeaders(req)
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
			_ = resp.Body.Close()
			err = fmt.Errorf("controller returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
			if resp.StatusCode == http.StatusNotFound {
				lastErr = err
				continue
			}
			return agentproto.ClientListResponse{}, err
		}

		var payload agentproto.ClientListResponse
		err = json.NewDecoder(resp.Body).Decode(&payload)
		_ = resp.Body.Close()
		if err != nil {
			return agentproto.ClientListResponse{}, fmt.Errorf("decode client list: %w", err)
		}
		if payload.EnrollmentStatus != "" {
			// Administrator assignment is authoritative for managed team nodes.
			// Pending/revoked snapshots must be empty and applied to remove old users.
			valid := c.scope.ResourceClass == "team" && payload.Scope != nil && payload.Scope.ResourceClass == "team"
			switch payload.EnrollmentStatus {
			case "approved":
				valid = valid && payload.Scope.TeamID != ""
			case "pending", "rejected", "revoked":
				valid = valid && len(payload.Clients) == 0 && payload.Total == 0 && payload.Scope.TeamID == ""
			default:
				valid = false
			}
			if !valid {
				return agentproto.ClientListResponse{}, errors.New("invalid controller team enrollment snapshot; refusing Xray synchronization")
			}
		} else if c.scope.ResourceClass != "" && (payload.Scope == nil || *payload.Scope != c.scope) {
			if payload.Scope == nil {
				return agentproto.ClientListResponse{}, fmt.Errorf("controller user scope missing; expected resourceClass=%q teamId=%q; refusing Xray synchronization", c.scope.ResourceClass, c.scope.TeamID)
			}
			return agentproto.ClientListResponse{}, fmt.Errorf("controller user scope mismatched; expected resourceClass=%q teamId=%q, received resourceClass=%q teamId=%q; check agent resourceClass and enrolled controller credential scope (pool name does not select scope); refusing Xray synchronization", c.scope.ResourceClass, c.scope.TeamID, payload.Scope.ResourceClass, payload.Scope.TeamID)
		}
		return payload, nil
	}

	if lastErr != nil {
		return agentproto.ClientListResponse{}, lastErr
	}
	return agentproto.ClientListResponse{}, errors.New("controller users endpoint is unavailable")
}

// WatchUserConfigEvents keeps a server-sent event stream open and invokes
// onChange whenever the controller reports a new desired-user revision.
func (c *Client) WatchUserConfigEvents(ctx context.Context, onChange func(string)) error {
	endpoint, err := url.JoinPath(c.baseURL.String(), "/api/agent-server/v1/users/events")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	req.Header.Set("Accept", "text/event-stream")

	streamClient := *c.http
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return fmt.Errorf("controller event stream returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		revision := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if revision != "" && onChange != nil {
			onChange(revision)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read controller event stream: %w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if resp.Header.Get("X-Agent-Event-Mode") == "poll" {
		return nil
	}
	return errors.New("controller event stream closed")
}

// ReportStatus submits the agent status report to the controller.
func (c *Client) ReportStatus(ctx context.Context, report agentproto.StatusReport) error {
	buf, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode status report: %w", err)
	}

	paths := []string{"/api/agent-server/v1/status"}
	var lastErr error

	for _, path := range paths {
		endpoint, err := url.JoinPath(c.baseURL.String(), path)
		if err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
		if err != nil {
			return err
		}
		c.applyHeaders(req)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
			_ = resp.Body.Close()
			err = fmt.Errorf("controller returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
			if resp.StatusCode == http.StatusNotFound {
				lastErr = err
				continue
			}
			return err
		}

		_ = resp.Body.Close()
		return nil
	}

	if lastErr != nil {
		return lastErr
	}
	return errors.New("controller status endpoint is unavailable")
}

func (c *Client) applyHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Service-Token", c.token)
	if c.agentID != "" {
		req.Header.Set("X-Agent-ID", c.agentID)
	}
	// Expectations only: Accounts must authorize from the enrolled agent credential.
	if c.scope.ResourceClass != "" {
		req.Header.Set("X-Agent-Resource-Class", c.scope.ResourceClass)
	}
	if c.scope.TeamID != "" {
		req.Header.Set("X-Agent-Team-ID", c.scope.TeamID)
	}
	req.Header.Set("User-Agent", c.userAgent)
}
