package arcade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const (
	defaultBaseURL = "https://api.arcade.dev"

	// statusWaitSeconds is the long-poll window passed to Arcade's
	// /v1/auth/status endpoint. We keep this well below
	// GLEIPNIR_HTTP_WRITE_TIMEOUT (default 15s) so the Go HTTP server
	// never kills the response writer mid-poll. The frontend re-issues
	// the wait endpoint until the response reaches a terminal status.
	statusWaitSeconds = 10

	// maxErrorBodyBytes caps how many bytes of an error response body are
	// included in error strings to avoid flooding logs with large payloads.
	maxErrorBodyBytes = 1024
)

// Option is a functional option for constructing a Client.
type Option func(*Client)

// WithBaseURL overrides the Arcade API base URL. Primarily used in tests
// to point the client at an httptest.Server.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		c.baseURL = u
	}
}

// Client calls Arcade's pre-authorization REST API.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
}

// NewClient constructs a Client. If httpClient is nil, http.DefaultClient is used.
// The baseURL defaults to https://api.arcade.dev; use WithBaseURL to override.
func NewClient(httpClient *http.Client, apiKey string, opts ...Option) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	c := &Client{
		httpClient: httpClient,
		baseURL:    defaultBaseURL,
		apiKey:     apiKey,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// AuthResponse is the response shape from both the authorize and status
// Arcade endpoints.
type AuthResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`        // "pending" | "completed" | "failed"
	URL    string `json:"url,omitempty"` // populated when Status == "pending"
}

// Authorize calls POST /v1/auth/authorize to pre-authorize a (userID, toolName) pair.
// Returns AuthCompleted when the grant already exists (idempotent) or AuthPending
// with a one-time OAuth URL when the user must click through.
func (c *Client) Authorize(ctx context.Context, toolName, userID string) (*AuthResponse, error) {
	body, err := json.Marshal(map[string]string{
		"tool_name": toolName,
		"user_id":   userID,
	})
	if err != nil {
		return nil, fmt.Errorf("arcade: authorize: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/auth/authorize", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("arcade: authorize: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	return c.doRequest(req, "authorize")
}

// WaitForCompletion calls GET /v1/auth/status?id={authID}&wait=10 once and
// returns whatever Arcade returns — which may still be "pending" if the user
// has not completed the OAuth flow. The caller (handler / frontend loop) is
// responsible for re-calling until the status reaches a terminal value.
//
// A single bounded request per call keeps each HTTP response comfortably
// under GLEIPNIR_HTTP_WRITE_TIMEOUT (ADR-040).
func (c *Client) WaitForCompletion(ctx context.Context, authID string) (*AuthResponse, error) {
	reqURL := fmt.Sprintf("%s/v1/auth/status?id=%s&wait=%d",
		c.baseURL, url.QueryEscape(authID), statusWaitSeconds)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("arcade: wait: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	return c.doRequest(req, "wait")
}

// doRequest executes an HTTP request and decodes the JSON body into an AuthResponse.
// Non-2xx responses are returned as errors with up to 1KB of the response body
// included for debugging.
func (c *Client) doRequest(req *http.Request, op string) (*AuthResponse, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("arcade: %s: %w", op, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("arcade: %s: read response body: %w", op, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("arcade: %s: unexpected status %d: %s", op, resp.StatusCode, string(bodyBytes))
	}

	var result AuthResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("arcade: %s: decode response: %w", op, err)
	}
	return &result, nil
}
