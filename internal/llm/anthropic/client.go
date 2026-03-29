package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/rapp992/gleipnir/internal/llm"
)

// sanitizeToolName replaces any character outside [a-zA-Z0-9_-] with '_' and
// truncates to 128 characters. The Claude API rejects tool names containing
// dots or other special characters.
func sanitizeToolName(name string) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
	if len(sanitized) > 128 {
		sanitized = sanitized[:128]
	}
	return sanitized
}

// toolNameMapping holds both directions of the MCP-name ↔ wire-name mapping
// produced by buildTools. Named fields prevent callers from confusing the two
// maps, which would otherwise both be map[string]string.
type toolNameMapping struct {
	// SanitizedToOriginal maps Claude-facing wire names back to original MCP
	// names. Used by translateResponse to reverse-map API responses.
	SanitizedToOriginal map[string]string
	// OriginalToSanitized maps original MCP names to Claude-facing wire names.
	// Used by buildMessages to forward-map conversation history.
	OriginalToSanitized map[string]string
}

const defaultMaxTokens = 4096

var validOptions = map[string]bool{
	"enable_prompt_caching": true,
	"max_tokens":            true,
}

// Compile-time check that AnthropicClient satisfies the LLMClient interface.
var _ llm.LLMClient = (*AnthropicClient)(nil)

// AnthropicClient implements llm.LLMClient using the Anthropic Claude API.
type AnthropicClient struct {
	client *anthropic.Client

	modelMu     sync.RWMutex
	modelCache  map[string]bool
	modelErr    error
	modelLoaded bool
}

// NewClient constructs an AnthropicClient with the given API key.
// The variadic opts are forwarded to the SDK constructor, allowing callers
// to inject options such as option.WithBaseURL for tests without exposing
// the SDK client directly.
func NewClient(apiKey string, opts ...option.RequestOption) *AnthropicClient {
	allOpts := append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	c := anthropic.NewClient(allOpts...)
	return &AnthropicClient{client: &c}
}

// NewClientFromEnv constructs an AnthropicClient using only the default SDK
// options (ANTHROPIC_API_KEY env var). Use this in production so the API key
// is read from the environment without an empty string override.
func NewClientFromEnv(opts ...option.RequestOption) *AnthropicClient {
	c := anthropic.NewClient(opts...)
	return &AnthropicClient{client: &c}
}

// CreateMessage sends a single synchronous request to the Anthropic API and
// returns the normalized response.
func (c *AnthropicClient) CreateMessage(ctx context.Context, req llm.MessageRequest) (*llm.MessageResponse, error) {
	hints, _ := req.Hints.(*AnthropicHints)

	maxTokens := resolveMaxTokens(req, hints)
	system := buildSystemBlocks(req, hints)

	tools, nameMap, err := buildTools(req.Tools)
	if err != nil {
		return nil, fmt.Errorf("anthropic: building tools: %w", err)
	}
	messages := buildMessages(req.History, nameMap.OriginalToSanitized)

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: maxTokens,
		System:    system,
		Messages:  messages,
		Tools:     tools,
	}

	resp, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return nil, wrapSDKError(err)
	}

	return translateResponse(resp, nameMap.SanitizedToOriginal), nil
}

// StreamMessage wraps CreateMessage and emits the complete response as a single
// MessageChunk on a buffered channel. The channel is closed immediately after
// the chunk is sent. This is a v1.0 stub; real streaming will be added later.
func (c *AnthropicClient) StreamMessage(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	resp, err := c.CreateMessage(ctx, req)
	if err != nil {
		return nil, err
	}
	return llm.StubStreamFromResponse(resp), nil
}

// ValidateOptions validates provider-specific options from the policy YAML.
// Accepted keys: "enable_prompt_caching" (bool), "max_tokens" (positive int).
// All errors are collected before returning so the caller sees every problem at once.
func (c *AnthropicClient) ValidateOptions(options map[string]any) error {
	if options == nil {
		return nil
	}

	var errs []string

	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if !validOptions[key] {
			errs = append(errs, fmt.Sprintf("unknown option: %s", key))
		}
	}

	if v, ok := options["enable_prompt_caching"]; ok {
		if _, isBool := v.(bool); !isBool {
			errs = append(errs, fmt.Sprintf("option \"enable_prompt_caching\": expected bool, got %T", v))
		}
	}

	if v, ok := options["max_tokens"]; ok {
		switch val := v.(type) {
		case int:
			if val <= 0 {
				errs = append(errs, fmt.Sprintf("option \"max_tokens\": must be positive, got %d", val))
			}
		case int64:
			if val <= 0 {
				errs = append(errs, fmt.Sprintf("option \"max_tokens\": must be positive, got %d", val))
			}
		case float64:
			if val != math.Trunc(val) {
				errs = append(errs, fmt.Sprintf("option \"max_tokens\": must be a whole number, got %v", val))
			} else if val <= 0 {
				errs = append(errs, fmt.Sprintf("option \"max_tokens\": must be positive, got %d", int(val)))
			}
		default:
			errs = append(errs, fmt.Sprintf("option \"max_tokens\": expected numeric, got %T", v))
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// fetchModels calls the Anthropic Models API and populates modelCache.
// It is a no-op if the cache is already loaded.
func (c *AnthropicClient) fetchModels(ctx context.Context) {
	c.modelMu.Lock()
	defer c.modelMu.Unlock()

	if c.modelLoaded {
		return
	}

	pager := c.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	cache := make(map[string]bool)
	for pager.Next() {
		cache[pager.Current().ID] = true
	}
	if err := pager.Err(); err != nil {
		c.modelErr = fmt.Errorf("anthropic: fetching available models: %w", err)
		c.modelLoaded = true
		return
	}
	c.modelCache = cache
	c.modelErr = nil
	c.modelLoaded = true
}

// ValidateModelName returns nil if modelName is recognized by the Anthropic
// Models API, or a descriptive error if not. The model list is fetched from
// the API on the first call and cached until InvalidateModelCache is called.
// If the API call fails, an error describing the failure is returned so the
// caller can treat it as a non-blocking warning.
func (c *AnthropicClient) ValidateModelName(ctx context.Context, modelName string) error {
	c.fetchModels(ctx)

	c.modelMu.RLock()
	defer c.modelMu.RUnlock()

	if c.modelErr != nil {
		return fmt.Errorf("could not validate model name: %w", c.modelErr)
	}

	if c.modelCache[modelName] {
		return nil
	}

	known := make([]string, 0, len(c.modelCache))
	for name := range c.modelCache {
		known = append(known, name)
	}
	sort.Strings(known)
	return fmt.Errorf("unknown Anthropic model %q; known models: %s", modelName, strings.Join(known, ", "))
}

// ListModels returns the models available from the Anthropic API. Results are
// cached; call InvalidateModelCache to force a refresh on the next call.
func (c *AnthropicClient) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	c.fetchModels(ctx)

	c.modelMu.RLock()
	defer c.modelMu.RUnlock()

	if c.modelErr != nil {
		return nil, c.modelErr
	}

	models := make([]llm.ModelInfo, 0, len(c.modelCache))
	for name := range c.modelCache {
		models = append(models, llm.ModelInfo{Name: name, DisplayName: name})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models, nil
}

// InvalidateModelCache clears the cached model list so the next call to
// ListModels or ValidateModelName fetches fresh data from the API.
func (c *AnthropicClient) InvalidateModelCache() {
	c.modelMu.Lock()
	defer c.modelMu.Unlock()
	c.modelCache = nil
	c.modelErr = nil
	c.modelLoaded = false
}

// resolveMaxTokens determines the effective max_tokens for a request.
// Precedence (highest to lowest):
//  1. req.MaxTokens > 0 — explicit per-call limit on the request
//  2. hints.MaxTokens != nil — provider-specific hint
//  3. defaultMaxTokens (4096)
func resolveMaxTokens(req llm.MessageRequest, hints *AnthropicHints) int64 {
	if req.MaxTokens > 0 {
		return int64(req.MaxTokens)
	}
	if hints != nil && hints.MaxTokens != nil {
		return *hints.MaxTokens
	}
	return defaultMaxTokens
}

// buildSystemBlocks constructs the system prompt block slice for the API
// request. Returns nil when no system prompt is set.
func buildSystemBlocks(req llm.MessageRequest, hints *AnthropicHints) []anthropic.TextBlockParam {
	if req.SystemPrompt == "" {
		return nil
	}
	block := anthropic.TextBlockParam{Text: req.SystemPrompt}
	if hints != nil && hints.EnablePromptCaching != nil && *hints.EnablePromptCaching {
		block.CacheControl = anthropic.NewCacheControlEphemeralParam()
	}
	return []anthropic.TextBlockParam{block}
}

// buildMessages translates the provider-neutral conversation history into
// Anthropic MessageParams. originalToSanitized maps original MCP tool names
// to their sanitized wire-format names; when non-nil, ToolCallBlock names are
// looked up in the map so the API receives valid tool names. See issue #413.
func buildMessages(history []llm.ConversationTurn, originalToSanitized map[string]string) []anthropic.MessageParam {
	msgs := make([]anthropic.MessageParam, 0, len(history))
	for _, turn := range history {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(turn.Content))
		for _, cb := range turn.Content {
			switch b := cb.(type) {
			case llm.TextBlock:
				blocks = append(blocks, anthropic.NewTextBlock(b.Text))
			case llm.ToolCallBlock:
				// Look up the sanitized wire name from the map built by buildTools.
				// If the name is not in the map, fall back to sanitizeToolName so
				// the API never receives an invalid name (e.g. dots from MCP names).
				name := b.Name
				if wire, ok := originalToSanitized[name]; ok {
					name = wire
				} else if originalToSanitized != nil {
					slog.Warn("buildMessages: tool name not found in name map; sanitizing as fallback",
						"tool_name", b.Name, "tool_call_id", b.ID)
					name = sanitizeToolName(name)
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(b.ID, b.Input, name))
			case llm.ToolResultBlock:
				blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolCallID, b.Content, b.IsError))
			default:
				slog.Warn("buildMessages: skipping unknown content block type", "type", fmt.Sprintf("%T", cb))
			}
		}
		switch turn.Role {
		case llm.RoleAssistant:
			msgs = append(msgs, anthropic.NewAssistantMessage(blocks...))
		default:
			msgs = append(msgs, anthropic.NewUserMessage(blocks...))
		}
	}
	return msgs
}

// buildTools translates the provider-neutral tool definitions into the
// Anthropic ToolUnionParam slice. It sanitizes tool names for the Claude API
// and returns a toolNameMapping with both directions so translateResponse can
// reverse-map API responses and buildMessages can forward-map conversation
// history. Returns an error if two tools collide after sanitization.
func buildTools(tools []llm.ToolDefinition) ([]anthropic.ToolUnionParam, toolNameMapping, error) {
	result := make([]anthropic.ToolUnionParam, 0, len(tools))
	names := toolNameMapping{
		SanitizedToOriginal: make(map[string]string, len(tools)),
		OriginalToSanitized: make(map[string]string, len(tools)),
	}

	for _, t := range tools {
		sanitized := sanitizeToolName(t.Name)

		// Collision: two distinct original names map to the same sanitized name.
		if existing, conflict := names.SanitizedToOriginal[sanitized]; conflict && existing != t.Name {
			return nil, toolNameMapping{}, fmt.Errorf("tool name collision after sanitization: %q and %q both become %q", existing, t.Name, sanitized)
		}
		names.SanitizedToOriginal[sanitized] = t.Name
		names.OriginalToSanitized[t.Name] = sanitized

		schema, err := buildToolInputSchema(t.InputSchema)
		if err != nil {
			return nil, toolNameMapping{}, fmt.Errorf("building schema for tool %s: %w", t.Name, err)
		}
		tool := anthropic.ToolUnionParamOfTool(schema, sanitized)
		// OfTool is the active variant after ToolUnionParamOfTool; guard is
		// defensive against future SDK union changes.
		if tool.OfTool != nil && t.Description != "" {
			tool.OfTool.Description = param.NewOpt(t.Description)
		}
		result = append(result, tool)
	}
	return result, names, nil
}

// buildToolInputSchema converts a raw JSON schema into a ToolInputSchemaParam.
// This is duplicated from internal/agent/agent.go; a follow-up issue will
// migrate agent.go to use AnthropicClient directly, removing the duplicate.
func buildToolInputSchema(schema json.RawMessage) (anthropic.ToolInputSchemaParam, error) {
	if len(schema) == 0 {
		return anthropic.ToolInputSchemaParam{}, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(schema, &raw); err != nil {
		return anthropic.ToolInputSchemaParam{}, fmt.Errorf("unmarshal schema: %w", err)
	}

	var properties any
	if props, ok := raw["properties"]; ok {
		properties = props
	}

	// json.Unmarshal decodes JSON arrays into []any, not []string,
	// so each element needs individual assertion.
	var required []string
	if req, ok := raw["required"]; ok {
		if reqSlice, ok := req.([]any); ok {
			for _, v := range reqSlice {
				if s, ok := v.(string); ok {
					required = append(required, s)
				}
			}
		}
	}

	// Copy any extra fields (e.g. "additionalProperties", "$schema") so the
	// schema round-trips cleanly.
	extras := make(map[string]any)
	for k, v := range raw {
		if k != "type" && k != "properties" && k != "required" {
			extras[k] = v
		}
	}

	return anthropic.ToolInputSchemaParam{
		Properties:  properties,
		Required:    required,
		ExtraFields: extras,
	}, nil
}

// translateResponse converts an Anthropic API response into the
// provider-neutral MessageResponse. sanitizedToOriginal is the name map
// returned by buildTools; it is used to reverse-map sanitized tool names
// in ToolUseBlock responses back to the original MCP names.
func translateResponse(resp *anthropic.Message, sanitizedToOriginal map[string]string) *llm.MessageResponse {
	var result llm.MessageResponse

	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			result.Text = append(result.Text, llm.TextBlock{Text: b.Text})
		case anthropic.ToolUseBlock:
			// Reverse-map from the sanitized Claude-facing name to the original
			// MCP dot-separated name. If the name is not in the map (unexpected),
			// fall back to the raw name so callers still see something meaningful.
			originalName := b.Name
			if mapped, ok := sanitizedToOriginal[b.Name]; ok {
				originalName = mapped
			}
			result.ToolCalls = append(result.ToolCalls, llm.ToolCallBlock{
				ID:    b.ID,
				Name:  originalName,
				Input: b.Input,
			})
		case anthropic.ThinkingBlock:
			result.Thinking = append(result.Thinking, llm.ThinkingBlock{
				Text:     b.Thinking,
				Redacted: false,
			})
		case anthropic.RedactedThinkingBlock:
			result.Thinking = append(result.Thinking, llm.ThinkingBlock{
				Text:     "[redacted]",
				Redacted: true,
			})
		default:
			slog.Warn("translateResponse: skipping unhandled content block type", "type", fmt.Sprintf("%T", b))
		}
	}

	// Map Anthropic stop reasons to the provider-neutral enum.
	// StopReasonStopSequence, StopReasonPauseTurn, and StopReasonRefusal are
	// not yet mapped — they fall through to StopReasonUnknown.
	switch resp.StopReason {
	case anthropic.StopReasonEndTurn:
		result.StopReason = llm.StopReasonEndTurn
	case anthropic.StopReasonToolUse:
		result.StopReason = llm.StopReasonToolUse
	case anthropic.StopReasonMaxTokens:
		result.StopReason = llm.StopReasonMaxTokens
	default:
		result.StopReason = llm.StopReasonUnknown
	}

	result.Usage = llm.TokenUsage{
		InputTokens:  int(resp.Usage.InputTokens),
		OutputTokens: int(resp.Usage.OutputTokens),
	}

	return &result
}

// wrapSDKError translates Anthropic SDK errors into descriptive errors with
// HTTP status context. Non-SDK errors (network failures, timeouts) are wrapped
// generically.
func wrapSDKError(err error) error {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("anthropic API error: %w", err)
	}
	switch {
	case apiErr.StatusCode == 429:
		return fmt.Errorf("anthropic: rate limited (HTTP 429): %w", err)
	case apiErr.StatusCode == 401:
		return fmt.Errorf("anthropic: authentication failed (HTTP 401): %w", err)
	case apiErr.StatusCode >= 500:
		return fmt.Errorf("anthropic: server error (HTTP %d): %w", apiErr.StatusCode, err)
	default:
		return fmt.Errorf("anthropic: unexpected API error (HTTP %d): %w", apiErr.StatusCode, err)
	}
}
