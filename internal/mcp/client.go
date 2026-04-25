// Package mcp implements the MCP HTTP transport client and tool registry.
// This package must not import internal/execution/agent (package boundary, ADR-001).
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// maxRedirects is the maximum number of HTTP redirects the MCP client will follow.
	maxRedirects = 10

	// mcpProtocolVersion is the MCP protocol version sent during the initialize handshake.
	mcpProtocolVersion = "2024-11-05"
)

// Tool is a tool discovered from an MCP server.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage // JSON Schema object
}

// ToolResult is the response from a tool invocation.
type ToolResult struct {
	Output  json.RawMessage
	IsError bool
}

// JSON-RPC 2.0 wire types used for MCP HTTP transport.

type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC error response from an MCP server.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *JSONRPCError) Error() string {
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// HTTPStatusError represents a non-OK HTTP status code from an MCP server.
type HTTPStatusError struct {
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("mcp server returned status %d", e.StatusCode)
}

type toolsListResult struct {
	Tools []toolWire `json:"tools"`
}

type toolWire struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type toolsCallResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Client calls a single MCP server over HTTP transport.
type Client struct {
	serverURL   string
	serverName  string       // Prometheus label; set by registry.newClient, empty for direct NewClient callers
	authHeaders []AuthHeader // static headers injected on every outbound request
	httpClient  *http.Client
	mu          sync.Mutex
	sessionID   string
}

// ClientOption configures a Client. Options are applied sequentially after
// the default Client is constructed, so order matters when combining options
// (e.g. WithHTTPClient followed by WithTimeout sets the timeout on the
// supplied client, not the default one).
type ClientOption func(*Client)

// WithHTTPClient replaces the Client's HTTP client entirely. This replaces
// the default CheckRedirect policy as well; the caller is responsible for
// their own redirect policy when using this option.
func WithHTTPClient(c *http.Client) ClientOption {
	return func(cl *Client) {
		cl.httpClient = c
	}
}

// WithTimeout sets the Timeout on whatever httpClient exists at the time this
// option is applied. When combined with WithHTTPClient, place WithTimeout
// after WithHTTPClient so the timeout is set on the supplied client.
func WithTimeout(d time.Duration) ClientOption {
	return func(cl *Client) {
		cl.httpClient.Timeout = d
	}
}

// WithAuthHeaders configures static headers to be injected on every outbound
// request. Headers are applied before Mcp-Session-Id so the client-managed
// session header always takes precedence.
func WithAuthHeaders(hs []AuthHeader) ClientOption {
	return func(cl *Client) {
		cl.authHeaders = hs
	}
}

// NewClient returns a Client targeting serverURL. Optional ClientOptions are
// applied in order after the default Client is constructed.
func NewClient(serverURL string, opts ...ClientOption) *Client {
	c := &Client{
		serverURL: serverURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if err := checkRedirectTarget(req.URL); err != nil {
					return err
				}
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// initializeResult holds the fields we care about from the MCP initialize response.
type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// initialize performs the MCP handshake and returns the session ID assigned by
// the server. Callers must include this ID as "Mcp-Session-Id" on all
// subsequent requests to the same server.
//
// The streamable-HTTP transport requires:
//  1. POST initialize → server replies with Mcp-Session-Id header
//  2. POST notifications/initialized (no response body expected)
//
// Only after that will the server accept method calls like tools/list.
func (c *Client) initialize(ctx context.Context) (string, error) {
	initBody, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "gleipnir",
				"version": "0.1.0",
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal initialize: %w", err)
	}

	resp, err := c.postRaw(ctx, initBody, "")
	if err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}
	defer resp.Body.Close()

	sessionID := resp.Header.Get("Mcp-Session-Id")

	var initEnvelope jsonrpcResponse
	if err := decodeResponse(resp, &initEnvelope); err != nil {
		return "", fmt.Errorf("decode initialize response: %w", err)
	}
	if initEnvelope.Error != nil {
		return "", fmt.Errorf("initialize error: %w", initEnvelope.Error)
	}
	// Notify the server that initialisation is complete (fire-and-forget; the
	// server sends no response to notifications).
	notifyBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	})
	if err != nil {
		return "", fmt.Errorf("marshal notifications/initialized: %w", err)
	}
	nresp, err := c.postRaw(ctx, notifyBody, sessionID)
	if err != nil {
		return "", fmt.Errorf("notifications/initialized: %w", err)
	}
	drainResponseBody(nresp.Body)

	return sessionID, nil
}

// ensureSession returns the cached session ID, initializing if necessary.
// Uses double-checked locking so that the HTTP round-trip to initialize is not
// done under the mutex — concurrent callers unblock immediately once any one
// of them stores a valid session.
func (c *Client) ensureSession(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.sessionID != "" {
		sid := c.sessionID
		c.mu.Unlock()
		return sid, nil
	}
	c.mu.Unlock()

	sid, err := c.initialize(ctx)
	if err != nil {
		return "", fmt.Errorf("ensure session: %w", err)
	}

	c.mu.Lock()
	if c.sessionID != "" {
		// Another goroutine stored a valid session while we were initializing.
		sid = c.sessionID
	} else {
		c.sessionID = sid
	}
	c.mu.Unlock()
	return sid, nil
}

// resetSession clears the cached session ID so the next call re-initializes.
func (c *Client) resetSession() {
	c.mu.Lock()
	c.sessionID = ""
	c.mu.Unlock()
}

// callWithSession sends body to the server, automatically handling session
// initialization and a single re-init retry on HTTP 401.
func (c *Client) callWithSession(ctx context.Context, body []byte) (*http.Response, error) {
	sid, err := c.ensureSession(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := c.postRaw(ctx, body, sid)
	if err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusUnauthorized {
			// Session expired — re-initialize once and retry.
			c.resetSession()
			sid, err = c.ensureSession(ctx)
			if err != nil {
				return nil, err
			}
			return c.postRaw(ctx, body, sid)
		}
		return nil, err
	}
	return resp, nil
}

// DiscoverTools calls the MCP server's tool list endpoint and returns all
// available tools. Used during server registration to populate mcp_tools.
func (c *Client) DiscoverTools(ctx context.Context) ([]Tool, error) {
	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
		Params:  struct{}{},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal tools/list request: %w", err)
	}

	resp, err := c.callWithSession(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("post tools/list: %w", err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := decodeResponse(resp, &envelope); err != nil {
		return nil, fmt.Errorf("decode tools/list response: %w", err)
	}
	if envelope.Error != nil {
		return nil, envelope.Error
	}

	var result toolsListResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tools/list result: %w", err)
	}

	tools := make([]Tool, len(result.Tools))
	for i, tw := range result.Tools {
		tools[i] = Tool{
			Name:        tw.Name,
			Description: tw.Description,
			InputSchema: tw.InputSchema,
		}
	}
	return tools, nil
}

// CallTool invokes a named tool on the MCP server with the given input.
// The input must be a JSON-serialisable value matching the tool's inputSchema.
func (c *Client) CallTool(ctx context.Context, name string, input map[string]any) (res ToolResult, err error) {
	start := time.Now()
	defer func() {
		mcpCallDurationSeconds.
			WithLabelValues(c.serverName, name).
			Observe(time.Since(start).Seconds())
		if err != nil {
			mcpErrorsTotal.
				WithLabelValues(c.serverName, ClassifyMCPErrorType(err)).
				Inc()
		}
	}()

	var body []byte
	body, err = json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/call",
		Params: struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}{
			Name:      name,
			Arguments: input,
		},
	})
	if err != nil {
		err = fmt.Errorf("marshal tools/call request: %w", err)
		return
	}

	var resp *http.Response
	resp, err = c.callWithSession(ctx, body)
	if err != nil {
		err = fmt.Errorf("post tools/call: %w", err)
		return
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if decErr := decodeResponse(resp, &envelope); decErr != nil {
		err = fmt.Errorf("decode tools/call response: %w", decErr)
		return
	}
	if envelope.Error != nil {
		err = envelope.Error
		return
	}

	var result toolsCallResult
	if umErr := json.Unmarshal(envelope.Result, &result); umErr != nil {
		err = fmt.Errorf("unmarshal tools/call result: %w", umErr)
		return
	}

	var output []byte
	output, err = json.Marshal(result.Content)
	if err != nil {
		err = fmt.Errorf("marshal content array: %w", err)
		return
	}

	res = ToolResult{
		Output:  output,
		IsError: result.IsError,
	}
	return
}

// decodeResponse decodes a JSON-RPC response from resp.Body into dst.
// The MCP streamable-HTTP transport may return either plain JSON or an SSE
// stream (Content-Type: text/event-stream). In SSE mode each response is a
// "data: <json>" line; we extract the first such line and decode it.
func decodeResponse(resp *http.Response, dst any) error {
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				return json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), dst)
			}
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read SSE stream: %w", err)
		}
		return fmt.Errorf("no data line found in SSE response")
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// postRaw sends a JSON-RPC request body to c.serverURL and returns the HTTP
// response. sessionID is included as "Mcp-Session-Id" when non-empty.
// It returns an error for non-2xx status codes.
//
// Header injection order:
//  1. Content-Type and Accept (transport requirements)
//  2. c.authHeaders (operator-configured, applied in registration order)
//  3. Mcp-Session-Id (client-managed, always wins — set last)
func (c *Client) postRaw(ctx context.Context, body []byte, sessionID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// MCP streamable-HTTP transport requires the client to accept both JSON
	// (for single-response calls) and SSE (for streaming responses).
	req.Header.Set("Accept", "application/json, text/event-stream")
	// Inject operator-configured auth headers before the session header so that
	// Mcp-Session-Id always takes precedence if an operator mistakenly
	// configures a header with that name.
	for _, h := range c.authHeaders {
		req.Header.Set(h.Name, h.Value)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		// Drain and close the body so the connection can be reused.
		drainResponseBody(resp.Body)
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode}
	}

	return resp, nil
}

// drainResponseBody reads any remaining data from rc and closes it.
// This ensures the underlying TCP connection can be reused by the HTTP
// transport's connection pool.
func drainResponseBody(rc io.ReadCloser) {
	io.Copy(io.Discard, rc) //nolint:errcheck
	rc.Close()
}
