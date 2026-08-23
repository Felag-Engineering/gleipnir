package hostclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// InstanceTokenEnvVar is the environment variable the host writes before
// exec'ing (or, once the container substrate goes live, before starting) a
// plugin, carrying the per-generation bearer token this client attaches to
// every host-endpoint request.
//
// This is deliberately a duplicated string literal, not an import of
// plugin-sdk/serve.InstanceTokenEnvVar (which holds the same value today).
// Importing serve would drag its go-plugin/gRPC dependency graph into
// hostclient's, which is exactly the zero-protobuf property this package
// exists to hold — one string constant is a small price for that guarantee.
// serve's gRPC transport (and this duplication along with it) is retired at
// the #883 cutover; until then both constants must be kept equal by hand.
const InstanceTokenEnvVar = "GLEIPNIR_INSTANCE_TOKEN"

// HostEndpointURLEnvVar is the environment variable carrying the host
// endpoint's base URL for the calling instance's own per-instance network.
// Nothing sets it yet — the reconciler injects it at container create once
// the substrate goes live (internal/plugin/reconciler; "BUILT BUT NOT LIVE"
// per the root CLAUDE.md) — so today this is the name a plugin author wires
// up by hand for local testing, and the name the reconciler will populate
// automatically once instances run as containers.
const HostEndpointURLEnvVar = "GLEIPNIR_HOST_ENDPOINT_URL"

// ProtocolVersion is the MCP protocol version this client speaks. The host
// endpoint is modern-only (internal/plugin/hostendpoint refuses the legacy
// handshake outright), so there is exactly one value to negotiate.
const ProtocolVersion = "2026-07-28"

// Client is a typed caller for the host endpoint. It holds no per-call
// state — every method is safe to call concurrently from multiple
// goroutines, matching the concurrency contract a generated gRPC client
// gave authors before this package existed.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// Option configures a Client at construction time.
type Option func(*clientConfig)

type clientConfig struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// WithBaseURL overrides the host endpoint URL that would otherwise be read
// from HostEndpointURLEnvVar. Primarily for tests, where the endpoint is an
// httptest.Server rather than a reconciler-provisioned network address.
func WithBaseURL(url string) Option {
	return func(c *clientConfig) { c.baseURL = url }
}

// WithToken overrides the bearer token that would otherwise be read from
// InstanceTokenEnvVar.
func WithToken(token string) Option {
	return func(c *clientConfig) { c.token = token }
}

// WithHTTPClient overrides the *http.Client used for host-endpoint requests.
// Defaults to http.DefaultClient.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *clientConfig) { c.httpClient = hc }
}

// New constructs a Client. The base URL and token default to
// HostEndpointURLEnvVar and InstanceTokenEnvVar respectively; an author
// should not have to know either environment variable's name to use this
// package, only that New() picks them up automatically inside a plugin
// process the host launched.
//
// New fails clearly when the base URL or token is empty after applying
// options — a plugin that cannot identify itself to the host should refuse
// to start making host calls rather than send unauthenticated requests the
// host will reject one at a time.
func New(opts ...Option) (*Client, error) {
	cfg := &clientConfig{
		baseURL:    os.Getenv(HostEndpointURLEnvVar),
		token:      os.Getenv(InstanceTokenEnvVar),
		httpClient: http.DefaultClient,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.baseURL == "" {
		return nil, fmt.Errorf("hostclient: no host endpoint URL: set %s or pass WithBaseURL", HostEndpointURLEnvVar)
	}
	if cfg.token == "" {
		return nil, fmt.Errorf("hostclient: no instance token: set %s or pass WithToken", InstanceTokenEnvVar)
	}
	return &Client{baseURL: cfg.baseURL, token: cfg.token, httpClient: cfg.httpClient}, nil
}

// jsonrpcRequest is the wire shape every request takes: one JSON-RPC call
// per POST, no session, no batching — the stateless 2026-07-28 streamable
// HTTP profile the host endpoint speaks.
type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// jsonrpcResponse is the wire shape every response takes, whether it carries
// a tool result or a JSON-RPC-level error.
type jsonrpcResponse struct {
	Result json.RawMessage  `json:"result"`
	Error  *jsonrpcErrorObj `json:"error"`
}

type jsonrpcErrorObj struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// doRequest sends one JSON-RPC request and returns its raw result, or a
// *JSONRPCError for a transport-level failure. mcpName is attached as the
// Mcp-Name header when non-empty (tools/call only — server/discover does not
// carry one, per internal/mcp's transport rules).
func (c *Client) doRequest(ctx context.Context, method, mcpName string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(jsonrpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("hostclient: %s: marshal request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("hostclient: %s: build request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	req.Header.Set("Mcp-Method", method)
	if mcpName != "" {
		req.Header.Set("Mcp-Name", mcpName)
	}
	if callID, ok := CallIDFromContext(ctx); ok {
		req.Header.Set(CallIDHeader, callID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hostclient: %s: do request: %w", method, err)
	}
	defer resp.Body.Close()

	var env jsonrpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("hostclient: %s: decode response (HTTP %d): %w", method, resp.StatusCode, err)
	}
	if env.Error != nil {
		return nil, &JSONRPCError{Code: env.Error.Code, Message: env.Error.Message, Data: env.Error.Data}
	}
	return env.Result, nil
}

// toolResultEnvelope is the standard tools/call result shape (spec §8):
// content[0].text holds either the JSON-encoded result (isError=false) or a
// "code: message" string (isError=true).
type toolResultEnvelope struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// callTool invokes one host/* tool by name and decodes its result into out.
// out may be nil for tools whose result an author has no use for (none of
// the eleven host methods currently qualify, but the seam costs nothing to
// keep general).
func (c *Client) callTool(ctx context.Context, name string, args, out any) error {
	argsRaw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("hostclient: %s: marshal arguments: %w", name, err)
	}
	params := map[string]any{"name": name, "arguments": json.RawMessage(argsRaw)}

	resultRaw, err := c.doRequest(ctx, "tools/call", name, params)
	if err != nil {
		return err
	}

	var envelope toolResultEnvelope
	if err := json.Unmarshal(resultRaw, &envelope); err != nil {
		return fmt.Errorf("hostclient: %s: decode tool result: %w", name, err)
	}
	text := ""
	if len(envelope.Content) > 0 {
		text = envelope.Content[0].Text
	}
	if envelope.IsError {
		return parseHostError(text)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("hostclient: %s: decode result: %w", name, err)
	}
	return nil
}
