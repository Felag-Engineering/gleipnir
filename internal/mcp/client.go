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
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/infra/version"
)

const (
	// maxRedirects is the maximum number of HTTP redirects the MCP client will follow.
	maxRedirects = 10

	// ProtocolVersionLegacy is the version Gleipnir sends in the legacy
	// initialize handshake and the pin used for servers that do not answer
	// server/discover.
	ProtocolVersionLegacy = "2024-11-05"
	// ProtocolVersion20260728 is the modern MCP revision Gleipnir speaks.
	ProtocolVersion20260728 = "2026-07-28"
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
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *JSONRPCError) Error() string {
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// maxErrorBodyBytes caps how much of a non-2xx response body HTTPStatusError
// retains. Era detection (see discover.go) needs to read 400/404 bodies to
// distinguish a modern server's JSON-RPC error from a legacy server's opaque
// 4xx; this bound keeps a misbehaving server from ballooning memory.
const maxErrorBodyBytes = 64 << 10

// HTTPStatusError represents a non-OK HTTP status code from an MCP server.
type HTTPStatusError struct {
	StatusCode int
	// Body is up to maxErrorBodyBytes of the response body. Retained because
	// era detection must distinguish a modern server's JSON-RPC error
	// 400/404 from a legacy server's opaque 4xx.
	//
	// WARNING: Body is untrusted, attacker-controlled content from the
	// remote server and may contain secrets or arbitrary HTML/text. Error()
	// deliberately does not include it. Do not add a %#v verb — or any other
	// formatting path that would print every exported field — anywhere this
	// type is logged; Go's default %#v struct formatting would dump Body.
	Body []byte
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

	protocolVersion   string // pinned per-server version; "" = unpinned/legacy
	negotiatedVersion string // legacy initialize result; guarded by mu
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

// WithProtocolVersion pins the MCP protocol version negotiated for this
// server. "" (the default) means "not yet probed" and keeps the legacy
// request shaping; a pin matching a version in supportedProtocolVersions
// switches every request through sendRPC to the stateless 2026-07-28
// transport instead (see isModernProtocol).
func WithProtocolVersion(v string) ClientOption {
	return func(cl *Client) {
		cl.protocolVersion = v
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

// initializeResult is the subset of the legacy initialize response body we
// read. The server's negotiated protocolVersion is recorded as the legacy
// pin (see negotiatedLegacyVersion).
type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// initialize performs the MCP handshake and returns the session ID assigned
// by the server, plus the protocolVersion the server negotiated (empty if
// the result could not be parsed). Callers must include the session ID as
// "Mcp-Session-Id" on all subsequent requests to the same server.
//
// The streamable-HTTP transport requires:
//  1. POST initialize → server replies with Mcp-Session-Id header
//  2. POST notifications/initialized (no response body expected)
//
// Only after that will the server accept method calls like tools/list.
func (c *Client) initialize(ctx context.Context) (sessionID, negotiated string, err error) {
	initBody, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": ProtocolVersionLegacy,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "gleipnir",
				"version": version.Version,
			},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("marshal initialize: %w", err)
	}

	resp, err := c.postRaw(ctx, initBody, "")
	if err != nil {
		return "", "", fmt.Errorf("initialize: %w", err)
	}
	defer resp.Body.Close()

	sessionID = resp.Header.Get("Mcp-Session-Id")

	var initEnvelope jsonrpcResponse
	if err := decodeResponse(resp, &initEnvelope); err != nil {
		return "", "", fmt.Errorf("decode initialize response: %w", err)
	}
	if initEnvelope.Error != nil {
		return "", "", fmt.Errorf("initialize error: %w", initEnvelope.Error)
	}
	if len(initEnvelope.Result) > 0 {
		var result initializeResult
		if err := json.Unmarshal(initEnvelope.Result, &result); err != nil {
			// A server that answers initialize with an odd result shape is
			// still usable — the session ID is what matters for tool
			// traffic. Log and continue with an empty negotiated version.
			slog.Debug("failed to parse initialize result; continuing without negotiated version",
				"server_name", c.serverName, "err", err)
		} else {
			negotiated = result.ProtocolVersion
		}
	}
	// Notify the server that initialisation is complete (fire-and-forget; the
	// server sends no response to notifications).
	notifyBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	})
	if err != nil {
		return "", "", fmt.Errorf("marshal notifications/initialized: %w", err)
	}
	nresp, err := c.postRaw(ctx, notifyBody, sessionID)
	if err != nil {
		return "", "", fmt.Errorf("notifications/initialized: %w", err)
	}
	drainResponseBody(nresp.Body)

	return sessionID, negotiated, nil
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

	sid, negotiated, err := c.initialize(ctx)
	if err != nil {
		return "", fmt.Errorf("ensure session: %w", err)
	}

	c.mu.Lock()
	if c.sessionID != "" {
		// Another goroutine stored a valid session while we were initializing.
		sid = c.sessionID
	} else {
		c.sessionID = sid
		c.negotiatedVersion = negotiated
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

// negotiatedLegacyVersion returns the protocolVersion the server reported in
// the most recent legacy initialize handshake, or "".
func (c *Client) negotiatedLegacyVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.negotiatedVersion
}

// isModernProtocol reports whether this client speaks the 2026-07-28
// stateless transport. Only an explicit pin of a version in
// supportedProtocolVersions counts: "" (never probed) and every legacy
// pin keep the legacy session shaping, so a server that has not been
// probed can never be silently re-shaped.
//
// No lock: protocolVersion is written once by WithProtocolVersion during
// NewClient and never mutated afterwards (unlike sessionID /
// negotiatedVersion, which mu guards).
//
// A legacy server cannot reach this branch by lying about its version:
// sanitizedLegacyVersion (discover.go) only ever emits an allowlisted
// pre-2026 token, and knownLegacyProtocolVersions is disjoint from
// supportedProtocolVersions by construction
// (TestLegacyAllowlistDisjointFromModernVersions).
func (c *Client) isModernProtocol() bool {
	for _, v := range supportedProtocolVersions {
		if c.protocolVersion == v {
			return true
		}
	}
	return false
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

// sendRPC dispatches one JSON-RPC request under whichever transport the
// pinned protocol version calls for.
//
// Modern (2026-07-28): stateless. MCP-Protocol-Version, Mcp-Method and (when
// the method targets a named entity) Mcp-Name are bound to the request;
// there is no session and therefore no 401 re-init retry — a 401 on a
// stateless transport is an auth failure, not a stale session, and retrying
// it would double every auth failure.
//
// Legacy: unchanged, byte for byte — callWithSession keeps owning the
// handshake and the single 401 re-init retry.
//
// rpcName is "" for methods that address no named entity (tools/list,
// server/discover); see the Mcp-Name decision in Key decisions.
func (c *Client) sendRPC(ctx context.Context, body []byte, method, rpcName string) (*http.Response, error) {
	if !c.isModernProtocol() {
		return c.callWithSession(ctx, body)
	}
	return c.post(ctx, body, postOptions{
		protocolVersion: c.protocolVersion,
		rpcMethod:       method,
		rpcName:         rpcName,
	})
}

// methodToolsList / methodToolsCall are written once so the JSON-RPC body's
// "method" and the Mcp-Method header cannot drift (same single-variable rule
// ProbeProtocolVersion applies to requestedVersion, discover.go).
const (
	methodToolsList = "tools/list"
	methodToolsCall = "tools/call"
)

// DiscoverTools calls the MCP server's tool list endpoint and returns all
// available tools. Used during server registration to populate mcp_tools.
func (c *Client) DiscoverTools(ctx context.Context) ([]Tool, error) {
	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  methodToolsList,
		Params:  struct{}{},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal tools/list request: %w", err)
	}

	resp, err := c.sendRPC(ctx, body, methodToolsList, "")
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
		tools[i] = Tool(tw)
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
		Method:  methodToolsCall,
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
	resp, err = c.sendRPC(ctx, body, methodToolsCall, name)
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

// maxJSONRPCPayloadBytes bounds how much of a 2xx response body
// readJSONRPCPayload will ever buffer. Legitimate tools/list and
// server/discover responses are expected to be well under this — the plan
// deliberately left this path uncapped for #737 because tool catalogs are
// legitimately large, but Finding 4 (security review, #737 cycle 2)
// confirmed an uncapped read buffered 33,554,476 bytes from a single crafted
// 2xx response. This cap is a memory-exhaustion backstop, not a realistic
// content-size limit — it is far larger than maxErrorBodyBytes because error
// bodies are expected to be small diagnostic text while a tool catalog is
// not.
const maxJSONRPCPayloadBytes = 32 << 20 // 32 MiB

// readJSONRPCPayload returns the raw JSON-RPC payload bytes from resp,
// transparently unwrapping the first "data: " line when the server replies
// with an SSE stream.
func readJSONRPCPayload(resp *http.Response) ([]byte, error) {
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				return []byte(strings.TrimPrefix(line, "data: ")), nil
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read SSE stream: %w", err)
		}
		return nil, fmt.Errorf("no data line found in SSE response")
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxJSONRPCPayloadBytes))
}

// decodeResponse decodes a JSON-RPC response from resp.Body into dst.
// The MCP streamable-HTTP transport may return either plain JSON or an SSE
// stream (Content-Type: text/event-stream). In SSE mode each response is a
// "data: <json>" line; we extract the first such line and decode it.
func decodeResponse(resp *http.Response, dst any) error {
	payload, err := readJSONRPCPayload(resp)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, dst)
}

// postOptions carries the per-request client-managed transport headers.
// The zero value is today's legacy shaping.
type postOptions struct {
	sessionID       string // Mcp-Session-Id      (legacy transport)
	protocolVersion string // MCP-Protocol-Version (2026-07-28 transport)
	rpcMethod       string // Mcp-Method           (2026-07-28 transport)
	rpcName         string // Mcp-Name             (2026-07-28 transport; "" when the method targets no named entity)
}

// post sends a JSON-RPC request body to c.serverURL and returns the HTTP
// response. It returns an error for non-2xx status codes.
//
// Header injection order:
//  1. Content-Type and Accept (transport requirements)
//  2. c.authHeaders (operator-configured, applied in registration order)
//  3. all client-managed headers (session, protocol version, method, name),
//     each only when non-empty — set last so they always win, even if an
//     operator configures a colliding auth header.
func (c *Client) post(ctx context.Context, body []byte, o postOptions) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// MCP streamable-HTTP transport requires the client to accept both JSON
	// (for single-response calls) and SSE (for streaming responses).
	req.Header.Set("Accept", "application/json, text/event-stream")
	// Inject operator-configured auth headers before the client-managed
	// headers so that the client-managed values always take precedence if an
	// operator mistakenly configures a header with a colliding name.
	for _, h := range c.authHeaders {
		req.Header.Set(h.Name, h.Value)
	}
	if o.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", o.sessionID)
	}
	if o.protocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", o.protocolVersion)
	}
	if o.rpcMethod != "" {
		req.Header.Set("Mcp-Method", o.rpcMethod)
	}
	if o.rpcName != "" {
		// o.rpcName is a tool name that ultimately originates from the remote
		// MCP server (DiscoverTools takes it verbatim) and is not
		// charset-constrained upstream — a hostile server could hand us a
		// name containing CRLF. We do not sanitize it here: net/http's
		// outbound Transport.roundTrip runs httpguts.ValidHeaderFieldValue on
		// every header value and refuses to send a request carrying CTL
		// bytes, failing the call closed rather than smuggling a header.
		req.Header.Set("Mcp-Name", o.rpcName)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		// Capture a bounded prefix of the body before draining, so era
		// detection (discover.go) can inspect a 400/404 body, then still
		// drain fully so the connection can be reused.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		drainResponseBody(resp.Body)
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Body: errBody}
	}

	return resp, nil
}

// postRaw is a backward-compatible wrapper over post for the legacy
// session-based transport. sessionID is included as "Mcp-Session-Id" when
// non-empty.
func (c *Client) postRaw(ctx context.Context, body []byte, sessionID string) (*http.Response, error) {
	return c.post(ctx, body, postOptions{sessionID: sessionID})
}

// drainResponseBody reads any remaining data from rc and closes it.
// This ensures the underlying TCP connection can be reused by the HTTP
// transport's connection pool.
func drainResponseBody(rc io.ReadCloser) {
	io.Copy(io.Discard, rc) //nolint:errcheck
	rc.Close()
}
