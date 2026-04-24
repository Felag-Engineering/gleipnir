package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/infra/metrics"
	"github.com/rapp992/gleipnir/internal/llm"
	"google.golang.org/genai"
)

// contentGenerator abstracts genai.Models methods for test injection.
// genai.Models has value receivers on both methods, so *genai.Models satisfies
// this interface because value-receiver methods are in the pointer's method set.
type contentGenerator interface {
	GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
	GenerateContentStream(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error]
}

// Compile-time check that GeminiClient satisfies the LLMClient interface.
var _ llm.LLMClient = (*GeminiClient)(nil)

// GeminiClient implements llm.LLMClient using the Google Gemini API.
type GeminiClient struct {
	generator contentGenerator
}

// NewClient constructs a GeminiClient with the given API key.
func NewClient(ctx context.Context, apiKey string) (*GeminiClient, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("google: creating client: %w", err)
	}
	return &GeminiClient{generator: client.Models}, nil
}

// newClientWithGenerator constructs a GeminiClient with an injected generator.
// Used in tests to avoid real API calls.
func newClientWithGenerator(gen contentGenerator) *GeminiClient {
	return &GeminiClient{generator: gen}
}

// CreateMessage sends a single synchronous request to the Gemini API and
// returns the normalized response.
func (c *GeminiClient) CreateMessage(ctx context.Context, req llm.MessageRequest) (resp *llm.MessageResponse, err error) {
	start := time.Now()
	defer func() {
		llm.ObserveRequestDuration("google", req.Model, time.Since(start))
		if err != nil {
			llm.RecordError("google", classifyGoogleError(err))
			return
		}
		if resp != nil {
			llm.RecordTokens("google", req.Model, resp.Usage)
		}
	}()

	hints, _ := req.Hints.(*GeminiHints)

	// Build name mapping before translating history so tool names in
	// ToolCallBlock/ToolResultBlock use the sanitized wire-format names.
	names := llm.BuildNameMapping(req.Tools, "")
	contents := buildContents(req.History, names)
	config := buildConfig(req, hints, names)

	sdkResp, sdkErr := c.generator.GenerateContent(ctx, req.Model, contents, config)
	if sdkErr != nil {
		err = wrapSDKError(sdkErr)
		return
	}

	resp, err = translateResponse(sdkResp, names)
	return
}

// classifyGoogleError maps a Gemini SDK error to the fixed error_type enum.
// genai.APIError is a value type, so the errors.As target is a pointer-to-value.
func classifyGoogleError(err error) string {
	if et, ok := llm.ClassifyContextError(err); ok {
		return et
	}
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		return llm.ClassifyHTTPStatus(apiErr.Code)
	}
	return metrics.ErrorTypeConnection
}

// StreamMessage opens a streaming request to the Gemini API and emits
// llm.MessageChunk values on the returned channel as responses arrive. The
// channel is closed when the stream ends. Errors arrive as Err chunks rather
// than synchronous returns because GenerateContentStream is a lazy iterator.
func (c *GeminiClient) StreamMessage(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	hints, _ := req.Hints.(*GeminiHints)
	names := llm.BuildNameMapping(req.Tools, "")
	contents := buildContents(req.History, names)
	config := buildConfig(req, hints, names)

	seq := c.generator.GenerateContentStream(ctx, req.Model, contents, config)
	out := make(chan llm.MessageChunk, 16)
	go consumeStream(ctx, seq, out, names)
	return out, nil
}

// ValidateOptions validates provider-specific options from the policy YAML.
// Accepted keys: "thinking_level" (string), "thinking_budget" (int), "enable_grounding" (bool).
// Delegates to parseHints, which is the single source of truth for validation.
func (c *GeminiClient) ValidateOptions(options map[string]any) error {
	_, err := parseHints(options)
	return err
}

// ValidateModelName returns nil if modelName is in the curated model list, or
// a descriptive error if not. No network call is made.
func (c *GeminiClient) ValidateModelName(_ context.Context, modelName string) error {
	for _, m := range curatedModels {
		if m.Name == modelName {
			return nil
		}
	}
	names := make([]string, len(curatedModels))
	for i, m := range curatedModels {
		names[i] = m.DisplayName
	}
	return fmt.Errorf("unknown Google model %q; available models: %s", modelName, strings.Join(names, ", "))
}

// ListModels returns a defensive copy of the curated Gemini model list.
// No network call is made — this never panics even on a zero-value client.
func (c *GeminiClient) ListModels(_ context.Context) ([]llm.ModelInfo, error) {
	result := make([]llm.ModelInfo, len(curatedModels))
	copy(result, curatedModels)
	return result, nil
}

// InvalidateModelCache is a no-op: the curated model list is static and
// requires no cache invalidation. The method exists to satisfy the LLMClient
// interface so the provider registry's /api/v1/models/refresh path works.
func (c *GeminiClient) InvalidateModelCache() {}

// buildContents translates the provider-neutral conversation history into
// genai Content structs. It performs a two-pass approach: first collecting
// a callID→name map from all ToolCallBlocks, then translating each turn.
// Tool names in ToolCallBlock and ToolResultBlock are mapped to their
// sanitized wire-format equivalents using the provided name mapping.
func buildContents(history []llm.ConversationTurn, names llm.ToolNameMapping) []*genai.Content {
	// First pass: build callID→tool name map for ToolResultBlock resolution.
	callIDToName := make(map[string]string)
	for _, turn := range history {
		for _, cb := range turn.Content {
			if tc, ok := cb.(llm.ToolCallBlock); ok {
				wireName := tc.Name
				if mapped, ok := names.OriginalToSanitized[tc.Name]; ok {
					wireName = mapped
				}
				callIDToName[tc.ID] = wireName
			}
		}
	}

	contents := make([]*genai.Content, 0, len(history))
	for _, turn := range history {
		parts := make([]*genai.Part, 0, len(turn.Content))
		for _, cb := range turn.Content {
			switch b := cb.(type) {
			case llm.ThinkingBlock:
				// Google thinking continuity uses ProviderMetadata on ToolCallBlock
				// (thought_signature), not ThinkingBlock. Skip.
				continue
			case llm.TextBlock:
				parts = append(parts, &genai.Part{Text: b.Text})
			case llm.ToolCallBlock:
				var argsMap map[string]any
				if len(b.Input) > 0 {
					if err := json.Unmarshal(b.Input, &argsMap); err != nil {
						// Sending nil args is safer than failing the whole request.
						slog.Warn("google: failed to unmarshal tool call args", "tool", b.Name, "err", err)
					}
				}
				wireName := b.Name
				if mapped, ok := names.OriginalToSanitized[b.Name]; ok {
					wireName = mapped
				}
				part := &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   b.ID,
						Name: wireName,
						Args: argsMap,
					},
				}
				if sig := b.ProviderMetadata["google.thought_signature"]; len(sig) > 0 {
					part.ThoughtSignature = sig
				}
				parts = append(parts, part)
			case llm.ToolResultBlock:
				name, ok := callIDToName[b.ToolCallID]
				if !ok {
					// Defensive fallback: use call ID as name if not found in history.
					name = b.ToolCallID
				}
				var response map[string]any
				if b.IsError {
					response = map[string]any{"error": b.Content}
				} else {
					response = map[string]any{"output": b.Content}
				}
				parts = append(parts, &genai.Part{
					FunctionResponse: &genai.FunctionResponse{
						ID:       b.ToolCallID,
						Name:     name,
						Response: response,
					},
				})
			}
		}
		contents = append(contents, &genai.Content{
			Role:  mapRoleToGenai(turn.Role),
			Parts: parts,
		})
	}
	return contents
}

// mapRoleToGenai maps the provider-neutral Role to the Gemini API role string.
func mapRoleToGenai(role llm.Role) string {
	switch role {
	case llm.RoleAssistant:
		return "model"
	default:
		return "user"
	}
}

// buildConfig constructs the GenerateContentConfig for a request.
func buildConfig(req llm.MessageRequest, hints *GeminiHints, names llm.ToolNameMapping) *genai.GenerateContentConfig {
	config := &genai.GenerateContentConfig{}

	if req.MaxTokens > 0 {
		config.MaxOutputTokens = int32(req.MaxTokens)
	}

	if req.SystemPrompt != "" {
		// Role is intentionally omitted; the SDK treats SystemInstruction as
		// the system role implicitly.
		config.SystemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: req.SystemPrompt}},
		}
	}

	if len(req.Tools) > 0 {
		config.Tools = buildTools(req.Tools)
	}

	if hints != nil {
		if hints.EnableGrounding != nil && *hints.EnableGrounding {
			config.Tools = append(config.Tools, &genai.Tool{GoogleSearch: &genai.GoogleSearch{}})
		}
		if hints.ThinkingBudget != nil {
			if config.ThinkingConfig == nil {
				config.ThinkingConfig = &genai.ThinkingConfig{}
			}
			config.ThinkingConfig.ThinkingBudget = hints.ThinkingBudget
		}
		if hints.ThinkingLevel != nil {
			if config.ThinkingConfig == nil {
				config.ThinkingConfig = &genai.ThinkingConfig{}
			}
			if level, ok := thinkingLevelToGenai[*hints.ThinkingLevel]; ok {
				config.ThinkingConfig.ThinkingLevel = level
			}
		}
	}

	return config
}

// buildTools groups all ToolDefinitions into a single genai.Tool with all
// FunctionDeclarations. Tool names are sanitized to comply with Gemini's
// naming requirements (alphanumeric + underscore only).
func buildTools(tools []llm.ToolDefinition) []*genai.Tool {
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		decl := &genai.FunctionDeclaration{
			Name:        llm.SanitizeToolName(t.Name, ""),
			Description: t.Description,
		}
		if len(t.InputSchema) > 0 {
			var schemaMap map[string]any
			if err := json.Unmarshal(t.InputSchema, &schemaMap); err == nil {
				if schema, err := translateJSONSchemaToGenaiSchema(schemaMap); err == nil {
					decl.Parameters = schema
				}
			}
		}
		decls = append(decls, decl)
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

// translateResponse converts a Gemini API response into the provider-neutral
// MessageResponse. Thought parts (part.Thought == true) are captured as
// ThinkingBlocks for audit purposes and excluded from the visible text response.
func translateResponse(resp *genai.GenerateContentResponse, names llm.ToolNameMapping) (*llm.MessageResponse, error) {
	var result llm.MessageResponse

	if resp.UsageMetadata != nil {
		result.Usage = llm.TokenUsage{
			InputTokens:    int(resp.UsageMetadata.PromptTokenCount),
			OutputTokens:   int(resp.UsageMetadata.CandidatesTokenCount),
			ThinkingTokens: int(resp.UsageMetadata.ThoughtsTokenCount),
		}
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return &result, nil
	}

	candidate := resp.Candidates[0]

	for _, part := range candidate.Content.Parts {
		if part.Thought && part.Text != "" {
			result.Thinking = append(result.Thinking, llm.ThinkingBlock{
				Provider: "google",
				Text:     part.Text,
				Redacted: false,
			})
			continue
		}
		if part.FunctionCall != nil {
			block, err := buildToolCallBlockFromPart(part, names)
			if err != nil {
				return nil, fmt.Errorf("google: building tool call block: %w", err)
			}
			result.ToolCalls = append(result.ToolCalls, block)
		} else if part.Text != "" {
			result.Text = append(result.Text, llm.TextBlock{Text: part.Text})
		}
	}

	switch candidate.FinishReason {
	case genai.FinishReasonStop:
		if len(result.ToolCalls) > 0 {
			result.StopReason = llm.StopReasonToolUse
		} else {
			result.StopReason = llm.StopReasonEndTurn
		}
	case genai.FinishReasonMaxTokens:
		result.StopReason = llm.StopReasonMaxTokens
	case genai.FinishReasonSafety,
		genai.FinishReasonMalformedFunctionCall,
		genai.FinishReasonRecitation,
		genai.FinishReasonProhibitedContent:
		result.StopReason = llm.StopReasonError
	default:
		result.StopReason = llm.StopReasonUnknown
	}

	return &result, nil
}

// wrapSDKError translates genai SDK errors into descriptive errors with
// HTTP status context. Non-SDK errors (network failures, timeouts) are wrapped
// generically.
func wrapSDKError(err error) error {
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("google: %w", err)
	}
	switch {
	case apiErr.Code == 429:
		return fmt.Errorf("google: rate limited (HTTP 429): %w", err)
	case apiErr.Code == 401 || apiErr.Code == 403:
		return fmt.Errorf("google: authentication/permission error (HTTP %d): %w", apiErr.Code, err)
	case apiErr.Code >= 500:
		return fmt.Errorf("google: server error (HTTP %d): %w", apiErr.Code, err)
	default:
		return fmt.Errorf("google: API error (HTTP %d): %w", apiErr.Code, err)
	}
}
