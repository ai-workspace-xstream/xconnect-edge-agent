package agentmode

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/telemetry"
)

type BillingClient struct {
	baseURL *url.URL
	http    *http.Client
}

func NewBillingClient(baseURL string, timeout time.Duration) (*BillingClient, error) {
	trimmedURL := strings.TrimSpace(baseURL)
	if trimmedURL == "" {
		return nil, fmt.Errorf("billing base url is required")
	}
	parsed, err := url.Parse(trimmedURL)
	if err != nil {
		return nil, fmt.Errorf("parse billing base url: %w", err)
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &BillingClient{
		baseURL: parsed,
		http: telemetry.WrapHTTPClient(&http.Client{
			Timeout: timeout,
		}),
	}, nil
}

func (c *BillingClient) TriggerCollectAndRate(ctx context.Context) error {
	return c.trigger(ctx, "/v1/jobs/collect-and-rate")
}

func (c *BillingClient) TriggerReconcile(ctx context.Context) error {
	return c.trigger(ctx, "/v1/jobs/reconcile")
}

func (c *BillingClient) trigger(ctx context.Context, path string) error {
	endpoint, err := url.JoinPath(c.baseURL.String(), path)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	return fmt.Errorf("billing returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
