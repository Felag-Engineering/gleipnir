// Package openai implements an LLMClient for the premium OpenAI provider using
// the official openai-go SDK targeting the Responses API. This package is for
// OpenAI's own API only; admin-managed OpenAI-compatible backends use the
// separate internal/llm/openaicompat package. See ADR-033.
package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
	"github.com/rapp992/gleipnir/internal/llm"
)

// Compile-time check that *Client satisfies the LLMClient interface.
var _ llm.LLMClient = (*Client)(nil)

// Client implements llm.LLMClient against the OpenAI Responses API via the
// official openai-go SDK. Unlike the compat client, it uses the stateless
// Responses API which provides native reasoning tokens and a typed surface.
type Client struct {
	sdk *openaisdk.Client
}

// NewClient constructs a Client for the given API key. The variadic opts are
// forwarded to the SDK constructor, allowing callers to inject options such as
// option.WithHTTPClient + option.WithBaseURL for tests without exposing the
// SDK client directly.
func NewClient(apiKey string, opts ...option.RequestOption) *Client {
	allOpts := append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	sdk := openaisdk.NewClient(allOpts...)
	return &Client{sdk: &sdk}
}

// CreateMessage sends a single synchronous Responses API request and returns
// the normalized response.
func (c *Client) CreateMessage(ctx context.Context, req llm.MessageRequest) (*llm.MessageResponse, error) {
	hints, _ := req.Hints.(*OpenAIHints)

	tools, names, err := buildTools(req.Tools)
	if err != nil {
		return nil, fmt.Errorf("openai: building tools: %w", err)
	}

	params := c.buildParams(req, hints, tools, names)

	resp, err := c.sdk.Responses.New(ctx, params)
	if err != nil {
		return nil, wrapSDKError(err)
	}

	return translateResponse(resp, names)
}

// StreamMessage sends a streaming Responses API request and returns a channel
// that delivers chunks as they arrive. Pre-stream errors are returned
// synchronously. Mid-stream errors arrive as MessageChunk{Err: err}.
func (c *Client) StreamMessage(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	hints, _ := req.Hints.(*OpenAIHints)

	tools, names, err := buildTools(req.Tools)
	if err != nil {
		return nil, fmt.Errorf("openai: building tools: %w", err)
	}

	params := c.buildParams(req, hints, tools, names)

	stream := c.sdk.Responses.NewStreaming(ctx, params)

	out := make(chan llm.MessageChunk, 16)
	go consumeStream(ctx, stream, out, names)
	return out, nil
}

// buildParams constructs the ResponseNewParams from a MessageRequest. Shared
// between CreateMessage and StreamMessage.
func (c *Client) buildParams(
	req llm.MessageRequest,
	hints *OpenAIHints,
	tools []responses.ToolUnionParam,
	names llm.ToolNameMapping,
) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(req.Model),
		Tools: tools,
	}

	// Only reasoning models accept reasoning.encrypted_content; non-reasoning
	// models reject it with a 400 error. The flag lives on the curated model
	// entry so the data and the behavior stay in sync.
	if curatedModelIsReasoning(req.Model) {
		params.Include = []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent}
	}

	input := buildInput(req, names)
	if len(input) > 0 {
		params.Input = responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		}
	}

	if req.SystemPrompt != "" {
		params.Instructions = openaisdk.String(req.SystemPrompt)
	}

	// MaxOutputTokens: explicit per-call limit takes precedence over hints.
	maxOut := int64(req.MaxTokens)
	if hints != nil {
		if hints.MaxOutputTokens != nil && maxOut == 0 {
			maxOut = *hints.MaxOutputTokens
		}
		if hints.Temperature != nil {
			params.Temperature = openaisdk.Float(*hints.Temperature)
		}
		if hints.TopP != nil {
			params.TopP = openaisdk.Float(*hints.TopP)
		}
		if hints.ReasoningEffort != nil {
			params.Reasoning = shared.ReasoningParam{
				Effort: shared.ReasoningEffort(*hints.ReasoningEffort),
			}
		}
	}
	if maxOut > 0 {
		params.MaxOutputTokens = openaisdk.Int(maxOut)
	}

	return params
}

// ValidateOptions validates provider-specific options from the policy YAML.
// Accepted keys: temperature, top_p, reasoning_effort, max_output_tokens.
func (c *Client) ValidateOptions(options map[string]any) error {
	_, err := parseHints(options)
	return err
}

// ListModels returns a defensive copy of the curated OpenAI model list.
// No network call is made — this never panics even on a zero-value client.
func (c *Client) ListModels(_ context.Context) ([]llm.ModelInfo, error) {
	result := make([]llm.ModelInfo, len(curatedModels))
	copy(result, curatedModels)
	return result, nil
}

// ValidateModelName returns nil if name is in the curated model list, or a
// descriptive error if not. No network call is made.
func (c *Client) ValidateModelName(_ context.Context, name string) error {
	if name == "" {
		return errors.New("openai: model name is empty")
	}
	if _, ok := curatedModelsByName[name]; ok {
		return nil
	}
	names := make([]string, len(curatedModels))
	for i, m := range curatedModels {
		names[i] = m.DisplayName
	}
	return fmt.Errorf("unknown OpenAI model %q; available models: %s", name, strings.Join(names, ", "))
}

// InvalidateModelCache is a no-op: the curated model list is static and
// requires no cache invalidation. The method exists to satisfy the LLMClient
// interface so the provider registry's /api/v1/models/refresh path works.
func (c *Client) InvalidateModelCache() {}

// curatedModelIsReasoning reports whether model is marked IsReasoning in the
// curated model list. Unknown models return false.
func curatedModelIsReasoning(model string) bool {
	return curatedModelsByName[model].IsReasoning
}

// wrapSDKError wraps an openai-go SDK error with HTTP status context so callers
// can produce meaningful log messages.
func wrapSDKError(err error) error {
	var apiErr *openaisdk.Error
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("openai: API error: %w", err)
	}
	switch {
	case apiErr.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("openai: rate limited (HTTP 429): %w", err)
	case apiErr.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("openai: authentication failed (HTTP 401): %w", err)
	case apiErr.StatusCode >= http.StatusInternalServerError:
		return fmt.Errorf("openai: server error (HTTP %d): %w", apiErr.StatusCode, err)
	default:
		return fmt.Errorf("openai: unexpected API error (HTTP %d): %w", apiErr.StatusCode, err)
	}
}
