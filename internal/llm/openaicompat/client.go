package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
	"github.com/felag-engineering/gleipnir/internal/llm"
)

// Compile-time assertion that *Client satisfies the LLMClient interface.
var _ llm.LLMClient = (*Client)(nil)

// Client implements llm.LLMClient against the OpenAI Chat Completions API.
// The same client serves OpenAI itself and any OpenAI-compatible backend
// (Ollama, vLLM, OpenRouter, etc.) — the only differences are baseURL and
// apiKey, both set at construction time. See ADR-032.
//
// Client embeds *llm.ProviderAdapter which promotes CreateMessage and
// StreamMessage. The four model/option methods are kept as thin explicit
// forwarders delegating to the compatWire's cache so cache state is never
// split across the adapter and the wire (BLOCKING-1, issue #506).
type Client struct {
	*llm.ProviderAdapter
	w *compatWire
}

// compatWire implements llm.ProviderWire for the OpenAI-compatible provider.
// Named compatWire (not wire) to avoid collision with the existing wire.go
// HTTP-shape file in this package (BLOCKING-2, issue #506).
//
// compatWire carries the state that was previously on Client: the http client,
// base URL, API key, provider name, and the model cache. The model cache state
// (models + modelsUnavailable) lives here so it is never split across the wire
// and the adapter. modelsUnavailable is intentionally unguarded by a mutex —
// preserving the existing behavior exactly.
type compatWire struct {
	httpClient        *http.Client
	baseURL           string
	apiKey            string
	providerName      string // Prometheus label; defaults to "openaicompat" when unset
	models            llm.ModelCache
	modelsUnavailable bool // true when the /models endpoint returned 404 (ADR-032 escape hatch)
}

// Option is a functional option for configuring a Client.
type Option func(*compatWire)

// WithHTTPClient injects a custom *http.Client. Tests pass srv.Client() from
// an httptest.Server to route requests to the test server's TLS stack.
//
// Note: WithTimeout will overwrite the Timeout on whichever httpClient is
// current at the time it runs, including one set by WithHTTPClient. Apply
// WithHTTPClient before WithTimeout if you need both.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *compatWire) {
		c.httpClient = hc
	}
}

// WithTimeout sets the request timeout on the Client's http.Client. If no
// http.Client has been set yet (via WithHTTPClient), a new one is allocated.
func WithTimeout(d time.Duration) Option {
	return func(c *compatWire) {
		if c.httpClient == nil {
			c.httpClient = &http.Client{}
		}
		c.httpClient.Timeout = d
	}
}

// WithProviderName sets the Prometheus provider label for this client.
// When unset, CreateMessage falls back to "openaicompat". The loader sets this
// to the admin-registered provider name so metrics reflect the configured backend.
func WithProviderName(name string) Option {
	return func(c *compatWire) { c.providerName = name }
}

// NewClient constructs a Client for the given base URL and API key.
// baseURL should be the root URL without a trailing slash (e.g.
// "https://api.openai.com/v1"). Functional options may inject a custom
// http.Client or timeout; any nil httpClient is defaulted to a 60-second
// timeout client after options are applied.
func NewClient(baseURL, apiKey string, opts ...Option) *Client {
	w := &compatWire{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		models:  llm.NewModelCache("OpenAI-compatible"),
	}
	for _, opt := range opts {
		opt(w)
	}
	if w.httpClient == nil {
		w.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{ProviderAdapter: llm.NewAdapter(w), w: w}
}

// --- ProviderWire implementation on compatWire ---

// ProviderName returns the Prometheus label, defaulting to "openaicompat".
func (w *compatWire) ProviderName() string {
	if w.providerName != "" {
		return w.providerName
	}
	return "openaicompat"
}

// ClassifyError maps an openaicompat error to the fixed error_type enum.
func (w *compatWire) ClassifyError(err error) string {
	if et, ok := llm.ClassifyContextError(err); ok {
		return et
	}
	var httpErr *llm.HTTPError
	if errors.As(err, &httpErr) {
		return llm.ClassifyHTTPStatus(httpErr.StatusCode)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return metrics.ErrorTypeConnection
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return metrics.ErrorTypeConnection
	}
	return metrics.ErrorTypeConnection
}

// Call executes a synchronous Chat Completions request and returns the
// normalized response. This is the body previously in Client.CreateMessage
// (minus the metrics-defer, which now lives in ProviderAdapter.CreateMessage).
func (w *compatWire) Call(ctx context.Context, req llm.MessageRequest) (*llm.MessageResponse, error) {
	// OpenAI allows [a-zA-Z0-9_-] in tool names; build the mapping once and
	// use it for both outbound sanitization and inbound reversal.
	names := llm.BuildNameMapping(req.Tools, "-")
	wireReq := BuildChatCompletionRequest(req, false, names)

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshalling request: %w", err)
	}

	httpResp, err := w.doRequest(ctx, http.MethodPost, "/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: reading response body: %w", err)
	}

	if httpResp.StatusCode >= 400 {
		return nil, wrapHTTPError(httpResp.StatusCode, raw)
	}

	var wireResp chatResponse
	if umErr := json.Unmarshal(raw, &wireResp); umErr != nil {
		return nil, fmt.Errorf("openai: decoding response: %w", umErr)
	}

	return ParseChatCompletionResponse(&wireResp, names)
}

// Stream sends a streaming Chat Completions request and returns a
// channel that delivers chunks as they arrive. Pre-stream HTTP errors (e.g.
// 401, 429) are returned synchronously; mid-stream errors arrive on the
// channel as MessageChunk{Err: err}. parseSSEStream owns closing resp.Body
// and the out channel, so callers must not close either.
func (w *compatWire) Stream(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	names := llm.BuildNameMapping(req.Tools, "-")
	wireReq := BuildChatCompletionRequest(req, true, names)

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal stream request: %w", err)
	}

	resp, err := w.doRequest(ctx, http.MethodPost, "/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, wrapHTTPError(resp.StatusCode, raw)
	}

	out := make(chan llm.MessageChunk, 16)
	go parseSSEStream(ctx, resp.Body, out, names)
	return out, nil
}

// ListModels returns the models available from the backend. Results are cached
// after the first successful fetch; call InvalidateModelCache to force a refresh.
func (w *compatWire) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	w.models.LoadOnce(func() (map[string]string, error) {
		return w.fetchModels(ctx)
	})
	if w.modelsUnavailable {
		// The backend does not expose a /models endpoint (ADR-032 escape hatch).
		return []llm.ModelInfo{}, nil
	}
	return w.models.ListModels()
}

// ValidateModelName returns nil if name is recognized by the backend, or a
// descriptive error. If the backend has no /models endpoint (404), any
// non-empty name is accepted (ADR-032 escape hatch for unknown backends).
func (w *compatWire) ValidateModelName(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("openai: model name is empty")
	}
	w.models.LoadOnce(func() (map[string]string, error) {
		return w.fetchModels(ctx)
	})
	if w.modelsUnavailable {
		// Unknown backend: we can't validate, so we accept any non-empty name.
		return nil
	}
	return w.models.ValidateModelName(name)
}

// ValidateOptions validates provider-specific options from the policy YAML.
func (w *compatWire) ValidateOptions(options map[string]any) error {
	_, err := parseHints(options)
	return err
}

// InvalidateModelCache clears the cached model list so the next call to
// ListModels or ValidateModelName fetches fresh data from the backend.
func (w *compatWire) InvalidateModelCache() {
	w.models.Invalidate()
	w.modelsUnavailable = false
}

// --- Thin forwarders on Client (zero-value safe, BLOCKING-1) ---
//
// These four methods are declared on the CONCRETE type so they satisfy the
// LLMClient interface without promotion. They forward to the wire's cache
// (which holds models + modelsUnavailable), never to the embedded adapter.
// A real-world Client is always constructed via NewClient, so c.w is non-nil.

// ValidateOptions validates provider-specific options from the policy YAML.
// Accepted keys: temperature, top_p, reasoning_effort, max_output_tokens.
func (c *Client) ValidateOptions(options map[string]any) error {
	return c.w.ValidateOptions(options)
}

// ListModels returns the models available from the backend, with caching.
func (c *Client) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return c.w.ListModels(ctx)
}

// ValidateModelName returns nil if name is recognized by the backend.
func (c *Client) ValidateModelName(ctx context.Context, name string) error {
	return c.w.ValidateModelName(ctx, name)
}

// InvalidateModelCache clears the cached model list.
func (c *Client) InvalidateModelCache() {
	c.w.InvalidateModelCache()
}

// --- Internal helpers on compatWire ---

// fetchModels calls GET /models and returns a map of id → id. The display name
// equals the model ID because OpenAI-compatible backends don't expose human
// readable names — using the id as DisplayName matches the unknown-backend
// convention where we show exactly what you would type in the policy YAML.
func (w *compatWire) fetchModels(ctx context.Context) (map[string]string, error) {
	resp, err := w.doRequest(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// The backend doesn't implement /models. Mark it unavailable so callers
		// skip validation entirely rather than blocking on a missing endpoint.
		w.modelsUnavailable = true
		return map[string]string{}, nil
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: reading models response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, wrapHTTPError(resp.StatusCode, raw)
	}

	var wireResp modelsResponse
	if err := json.Unmarshal(raw, &wireResp); err != nil {
		return nil, fmt.Errorf("openai: decoding models response: %w", err)
	}

	cache := make(map[string]string, len(wireResp.Data))
	for _, entry := range wireResp.Data {
		// id → id: display name equals the model ID for OpenAI-compatible backends
		// because they don't surface human-readable names.
		cache[entry.ID] = entry.ID
	}
	w.modelsUnavailable = false
	return cache, nil
}

// doRequest builds and executes an HTTP request against the backend. On
// network error it returns ctx.Err() directly when the context is done, so
// callers can use errors.Is(err, context.Canceled) without unwrapping.
func (w *compatWire) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, w.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("openai: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		// Prefer the context error so callers can distinguish cancellation from
		// network failures with errors.Is(err, context.Canceled).
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("openai: http request: %w", err)
	}
	return resp, nil
}

// errorEnvelope is used only to parse the error field of an HTTP error
// response. A dedicated type avoids conflating it with the full chatResponse.
type errorEnvelope struct {
	Error *apiError `json:"error,omitempty"`
}

// wrapHTTPError formats an HTTP error response into a descriptive error. If
// the body is a JSON object with an "error" field, the message from that field
// is included; otherwise the raw body is truncated to 256 characters. The
// returned error wraps *llm.HTTPError via %w so the defer in CreateMessage can
// recover the status code via errors.As for metric classification.
func wrapHTTPError(status int, body []byte) error {
	httpErr := &llm.HTTPError{StatusCode: status, Body: string(body)}
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error != nil && env.Error.Message != "" {
		return fmt.Errorf("openai: HTTP %d: %s (type=%s code=%s): %w",
			status, env.Error.Message, env.Error.Type, env.Error.Code, httpErr)
	}

	// Fallback: include the raw body truncated to 256 characters.
	raw := string(body)
	if len(raw) > 256 {
		raw = raw[:256] + "..."
	}
	return fmt.Errorf("openai: HTTP %d: %s: %w", status, raw, httpErr)
}
