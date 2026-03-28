package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/rapp992/gleipnir/internal/llm"
	"google.golang.org/genai"
)

// contentGenerator abstracts genai.Models.GenerateContent for test injection.
// genai.Models has a value receiver on GenerateContent, so *genai.Models
// satisfies this interface because value-receiver methods are in the pointer's
// method set.
type contentGenerator interface {
	GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
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
func (c *GeminiClient) CreateMessage(ctx context.Context, req llm.MessageRequest) (*llm.MessageResponse, error) {
	hints, _ := req.Hints.(*GeminiHints)

	contents := buildContents(req.History)
	config := buildConfig(req, hints)

	resp, err := c.generator.GenerateContent(ctx, req.Model, contents, config)
	if err != nil {
		return nil, wrapSDKError(err)
	}

	return translateResponse(resp)
}

// StreamMessage wraps CreateMessage and emits the complete response as a single
// MessageChunk on a buffered channel. The channel is closed immediately after
// the chunk is sent. This is a v1.0 stub; real streaming will be added later.
func (c *GeminiClient) StreamMessage(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	resp, err := c.CreateMessage(ctx, req)
	if err != nil {
		return nil, err
	}

	var chunk llm.MessageChunk

	if len(resp.Text) > 0 {
		parts := make([]string, len(resp.Text))
		for i, tb := range resp.Text {
			parts[i] = tb.Text
		}
		joined := strings.Join(parts, "")
		chunk.Text = &joined
	}

	if len(resp.ToolCalls) > 0 {
		chunk.ToolCall = &resp.ToolCalls[0]
	}

	chunk.StopReason = &resp.StopReason
	chunk.Usage = &resp.Usage

	ch := make(chan llm.MessageChunk, 1)
	ch <- chunk
	close(ch)
	return ch, nil
}

var validOptions = map[string]bool{
	"thinking_budget":  true,
	"enable_grounding": true,
}

// ValidateOptions validates provider-specific options from the policy YAML.
// Accepted keys: "thinking_budget" (positive int), "enable_grounding" (bool).
// All errors are collected before returning so the caller sees every problem at once.
func (c *GeminiClient) ValidateOptions(options map[string]any) error {
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

	if v, ok := options["thinking_budget"]; ok {
		switch val := v.(type) {
		case int:
			if val <= 0 {
				errs = append(errs, fmt.Sprintf("option \"thinking_budget\": must be positive, got %d", val))
			}
		case int32:
			if val <= 0 {
				errs = append(errs, fmt.Sprintf("option \"thinking_budget\": must be positive, got %d", val))
			}
		case int64:
			if val <= 0 {
				errs = append(errs, fmt.Sprintf("option \"thinking_budget\": must be positive, got %d", val))
			}
		case float64:
			if val != float64(int64(val)) {
				errs = append(errs, fmt.Sprintf("option \"thinking_budget\": must be a whole number, got %v", val))
			} else if val <= 0 {
				errs = append(errs, fmt.Sprintf("option \"thinking_budget\": must be positive, got %d", int(val)))
			}
		default:
			errs = append(errs, fmt.Sprintf("option \"thinking_budget\": expected numeric, got %T", v))
		}
	}

	if v, ok := options["enable_grounding"]; ok {
		if _, isBool := v.(bool); !isBool {
			errs = append(errs, fmt.Sprintf("option \"enable_grounding\": expected bool, got %T", v))
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// ValidateModelName always returns nil. Gemini model list API is different
// from Anthropic's; validation is deferred to a follow-up issue.
func (c *GeminiClient) ValidateModelName(_ context.Context, _ string) error {
	return nil
}

// buildContents translates the provider-neutral conversation history into
// genai Content structs. It performs a two-pass approach: first collecting
// a callID→name map from all ToolCallBlocks, then translating each turn.
func buildContents(history []llm.ConversationTurn) []*genai.Content {
	// First pass: build callID→tool name map for ToolResultBlock resolution.
	callIDToName := make(map[string]string)
	for _, turn := range history {
		for _, cb := range turn.Content {
			if tc, ok := cb.(llm.ToolCallBlock); ok {
				callIDToName[tc.ID] = tc.Name
			}
		}
	}

	contents := make([]*genai.Content, 0, len(history))
	for _, turn := range history {
		parts := make([]*genai.Part, 0, len(turn.Content))
		for _, cb := range turn.Content {
			switch b := cb.(type) {
			case llm.TextBlock:
				parts = append(parts, &genai.Part{Text: b.Text})
			case llm.ToolCallBlock:
				var argsMap map[string]any
				if len(b.Input) > 0 {
					_ = json.Unmarshal(b.Input, &argsMap)
				}
				parts = append(parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   b.ID,
						Name: b.Name,
						Args: argsMap,
					},
				})
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
func buildConfig(req llm.MessageRequest, hints *GeminiHints) *genai.GenerateContentConfig {
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
			config.ThinkingConfig = &genai.ThinkingConfig{ThinkingBudget: hints.ThinkingBudget}
		}
	}

	return config
}

// buildTools groups all ToolDefinitions into a single genai.Tool with all
// FunctionDeclarations. Gemini expects all functions grouped under one Tool.
func buildTools(tools []llm.ToolDefinition) []*genai.Tool {
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		decl := &genai.FunctionDeclaration{
			Name:        t.Name,
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
// MessageResponse. Thought parts (part.Thought == true) are filtered out to
// prevent internal reasoning from leaking into the visible response.
//
// Known gap: part.ThoughtSignature bytes are discarded here. When full thinking
// support is implemented, ThoughtSignature must be echoed back in subsequent
// requests to maintain the thinking chain. A follow-up issue is needed.
func translateResponse(resp *genai.GenerateContentResponse) (*llm.MessageResponse, error) {
	var result llm.MessageResponse

	if resp.UsageMetadata != nil {
		result.Usage = llm.TokenUsage{
			InputTokens:  int(resp.UsageMetadata.PromptTokenCount),
			OutputTokens: int(resp.UsageMetadata.CandidatesTokenCount),
		}
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return &result, nil
	}

	candidate := resp.Candidates[0]

	for _, part := range candidate.Content.Parts {
		if part.Thought {
			// Skip thinking/reasoning parts — analogous to how the Anthropic
			// client skips ThinkingBlock in its translateResponse.
			continue
		}
		if part.FunctionCall != nil {
			id := part.FunctionCall.ID
			if id == "" {
				id = uuid.NewString()
			}
			argsJSON, _ := json.Marshal(part.FunctionCall.Args)
			result.ToolCalls = append(result.ToolCalls, llm.ToolCallBlock{
				ID:    id,
				Name:  part.FunctionCall.Name,
				Input: json.RawMessage(argsJSON),
			})
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
