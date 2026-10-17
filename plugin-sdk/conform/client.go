package conform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// protocolVersion is the transport revision the runner speaks, and the one a
// managed plugin must (TP-PROTO-01).
const protocolVersion = "2026-07-28"

// _meta keys every 2026-07-28 request carries. Duplicated from mcpserver's
// wire.go rather than imported: they are wire contract, and the runner should
// fail a server that disagrees with the spec even if the SDK's own server
// were to drift.
const (
	metaKeyProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
)

const (
	requestTimeout = 30 * time.Second
	// maxResponseBytes bounds one response body. A conformance target is
	// someone's work in progress, and a runaway response should fail a check,
	// not exhaust the runner.
	maxResponseBytes = 16 << 20
	// maxToolPages bounds tools/list pagination against a server that hands
	// back a cursor forever.
	maxToolPages = 100
)

// rpcError is a JSON-RPC error object returned by the server.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// rpcResponse is one decoded response. Exactly one of Result and Err is set
// when the body was a JSON-RPC response; HTTPStatus is always set.
type rpcResponse struct {
	HTTPStatus int
	Result     json.RawMessage
	Err        *rpcError
}

// client speaks the stateless 2026-07-28 streamable HTTP transport: one
// JSON-RPC request per POST, no initialize, no session.
type client struct {
	baseURL string
	header  http.Header
	http    *http.Client
}

func newClient(t Target) *client {
	hc := t.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: requestTimeout}
	}
	return &client{baseURL: t.BaseURL, header: t.Header, http: hc}
}

// call sends one request. mcpName is the Mcp-Name header (tools/call only).
//
// A non-2xx status is not by itself an error here: a server is free to send a
// JSON-RPC error with a 4xx status (the SDK's server does for an unknown
// tool), and the checks need the error object, not just the status. The
// returned error is for a request that got no JSON-RPC answer at all.
func (c *client) call(ctx context.Context, method, mcpName string, params map[string]any) (rpcResponse, error) {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{
		metaKeyProtocolVersion:    protocolVersion,
		metaKeyClientCapabilities: map[string]any{},
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return rpcResponse{}, fmt.Errorf("%s: marshal request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return rpcResponse{}, fmt.Errorf("%s: build request: %w", method, err)
	}
	for name, values := range c.header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	req.Header.Set("Mcp-Method", method)
	if mcpName != "" {
		req.Header.Set("Mcp-Name", mcpName)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return rpcResponse{}, fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return rpcResponse{}, fmt.Errorf("%s: read response (HTTP %d): %w", method, resp.StatusCode, err)
	}
	if len(raw) > maxResponseBytes {
		return rpcResponse{}, fmt.Errorf("%s: response over %d bytes", method, maxResponseBytes)
	}

	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || (env.Error == nil && env.Result == nil) {
		return rpcResponse{HTTPStatus: resp.StatusCode},
			fmt.Errorf("%s: HTTP %d with no JSON-RPC response body", method, resp.StatusCode)
	}
	return rpcResponse{HTTPStatus: resp.StatusCode, Result: env.Result, Err: env.Error}, nil
}

// result calls method and decodes a successful result into out. A JSON-RPC
// error is returned as *rpcError.
func (c *client) result(ctx context.Context, method string, params map[string]any, out any) error {
	resp, err := c.call(ctx, method, "", params)
	if err != nil {
		return err
	}
	if resp.Err != nil {
		return fmt.Errorf("%s: %w", method, resp.Err)
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("%s: decode result: %w", method, err)
	}
	return nil
}

// tool is one tools/list entry, with the schema kept as raw bytes so the
// checks see exactly what the server sent.
type tool struct {
	Name        string          `json:"name"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Session is one run's view of the server. Discovery requests are made once
// and shared by every check that needs them.
type Session struct {
	target Target
	client *client

	discoverOnce sync.Once
	discover     discoverResult
	discoverErr  error

	toolsOnce sync.Once
	tools     []tool
	toolsErr  error
}

func newSession(t Target) *Session {
	return &Session{target: t, client: newClient(t)}
}

type discoverResult struct {
	SupportedVersions []string `json:"supportedVersions"`
}

// serverDiscover returns the server/discover result.
func (s *Session) serverDiscover(ctx context.Context) (discoverResult, error) {
	s.discoverOnce.Do(func() {
		s.discoverErr = s.client.result(ctx, "server/discover", nil, &s.discover)
	})
	return s.discover, s.discoverErr
}

// toolsList returns every tool across all tools/list pages.
func (s *Session) toolsList(ctx context.Context) ([]tool, error) {
	s.toolsOnce.Do(func() {
		s.tools, s.toolsErr = s.fetchTools(ctx)
	})
	return s.tools, s.toolsErr
}

func (s *Session) fetchTools(ctx context.Context) ([]tool, error) {
	var all []tool
	cursor := ""
	for page := 0; page < maxToolPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res struct {
			Tools      *[]tool `json:"tools"`
			NextCursor string  `json:"nextCursor"`
		}
		if err := s.client.result(ctx, "tools/list", params, &res); err != nil {
			return nil, err
		}
		if res.Tools == nil {
			return nil, fmt.Errorf("tools/list: result has no tools array")
		}
		all = append(all, *res.Tools...)
		if res.NextCursor == "" {
			return all, nil
		}
		cursor = res.NextCursor
	}
	return nil, fmt.Errorf("tools/list: still paginating after %d pages", maxToolPages)
}

// toolsOrSkip is toolsList for a check that depends on discovery: a failed
// discovery is TP-DISC-01's failure, so dependents skip rather than repeat it.
func (s *Session) toolsOrSkip(ctx context.Context) ([]tool, error) {
	tools, err := s.toolsList(ctx)
	if err != nil {
		return nil, skipf("tools/list failed (see TP-DISC-01)")
	}
	return tools, nil
}
