package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/rapp992/gleipnir/internal/llm"
	"google.golang.org/genai"
)

// sanitizeToolName replaces any character outside [a-zA-Z0-9_] with '_' and
// truncates to 128 characters. The Gemini API rejects tool names containing
// dots, hyphens, or other special characters.
func sanitizeToolName(name string) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, name)
	if len(sanitized) > 128 {
		sanitized = sanitized[:128]
	}
	return sanitized
}

// toolNameMapping holds both directions of the MCP-name ↔ wire-name mapping.
type toolNameMapping struct {
	SanitizedToOriginal map[string]string
	OriginalToSanitized map[string]string
}

// contentGenerator abstracts genai.Models.GenerateContent for test injection.
// genai.Models has a value receiver on GenerateContent, so *genai.Models
// satisfies this interface because value-receiver methods are in the pointer's
// method set.
type contentGenerator interface {
	GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// modelLister abstracts genai.Models.List for test injection.
type modelLister interface {
	List(ctx context.Context, config *genai.ListModelsConfig) (genai.Page[genai.Model], error)
}

// Compile-time check that GeminiClient satisfies the LLMClient interface.
var _ llm.LLMClient = (*GeminiClient)(nil)

// GeminiClient implements llm.LLMClient using the Google Gemini API.
type GeminiClient struct {
	generator contentGenerator
	lister    modelLister

	modelMu     sync.RWMutex
	modelCache  map[string]string // model name → display name
	modelErr    error
	modelLoaded bool
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
	return &GeminiClient{generator: client.Models, lister: client.Models}, nil
}

// newClientWithGenerator constructs a GeminiClient with an injected generator and lister.
// Used in tests to avoid real API calls.
func newClientWithGenerator(gen contentGenerator, lister modelLister) *GeminiClient {
	return &GeminiClient{generator: gen, lister: lister}
}

// CreateMessage sends a single synchronous request to the Gemini API and
// returns the normalized response.
func (c *GeminiClient) CreateMessage(ctx context.Context, req llm.MessageRequest) (*llm.MessageResponse, error) {
	hints, _ := req.Hints.(*GeminiHints)

	// Build name mapping before translating history so tool names in
	// ToolCallBlock/ToolResultBlock use the sanitized wire-format names.
	names := buildNameMapping(req.Tools)
	contents := buildContents(req.History, names)
	config := buildConfig(req, hints, names)

	resp, err := c.generator.GenerateContent(ctx, req.Model, contents, config)
	if err != nil {
		return nil, wrapSDKError(err)
	}

	return translateResponse(resp, names)
}

// StreamMessage wraps CreateMessage and emits the complete response as a single
// MessageChunk on a buffered channel. The channel is closed immediately after
// the chunk is sent. This is a v1.0 stub; real streaming will be added later.
func (c *GeminiClient) StreamMessage(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	resp, err := c.CreateMessage(ctx, req)
	if err != nil {
		return nil, err
	}
	return llm.StubStreamFromResponse(resp), nil
}

var validOptions = map[string]bool{
	"thinking_level":   true,
	"enable_grounding": true,
}

var validThinkingLevels = []string{"low", "medium", "high"}

// ValidateOptions validates provider-specific options from the policy YAML.
// Accepted keys: "thinking_level" (string, one of "low", "medium", "high"), "enable_grounding" (bool).
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

	if v, ok := options["thinking_level"]; ok {
		s, isString := v.(string)
		if !isString {
			errs = append(errs, fmt.Sprintf("option \"thinking_level\": expected string, got %T", v))
		} else {
			validLevel := false
			for _, level := range validThinkingLevels {
				if s == level {
					validLevel = true
					break
				}
			}
			if !validLevel {
				errs = append(errs, fmt.Sprintf("option \"thinking_level\": must be one of %q, got %q", validThinkingLevels, s))
			}
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

// fetchModels populates the model cache from the API if not already loaded.
func (c *GeminiClient) fetchModels(ctx context.Context) {
	c.modelMu.Lock()
	defer c.modelMu.Unlock()

	if c.modelLoaded {
		return
	}

	cache := make(map[string]string)
	page, err := c.lister.List(ctx, nil)
	if err != nil {
		c.modelErr = fmt.Errorf("google: fetching available models: %w", err)
		c.modelLoaded = true
		return
	}

	for _, m := range page.Items {
		// The API returns names like "models/gemini-2.0-flash".
		// Strip the "models/" prefix so users write just "gemini-2.0-flash" in policy YAML.
		name := strings.TrimPrefix(m.Name, "models/")
		displayName := m.DisplayName
		if displayName == "" {
			displayName = name
		}
		cache[name] = displayName
	}

	// Paginate through all pages.
	for page.NextPageToken != "" {
		page, err = page.Next(ctx)
		if err != nil {
			c.modelErr = fmt.Errorf("google: fetching available models (pagination): %w", err)
			c.modelLoaded = true
			return
		}
		for _, m := range page.Items {
			name := strings.TrimPrefix(m.Name, "models/")
			displayName := m.DisplayName
			if displayName == "" {
				displayName = name
			}
			cache[name] = displayName
		}
	}

	c.modelCache = cache
	c.modelErr = nil
	c.modelLoaded = true
}

// ValidateModelName returns nil if modelName is recognized by the Gemini API,
// or a descriptive error if not. The model list is fetched from the API on the
// first call and cached until InvalidateModelCache is called.
func (c *GeminiClient) ValidateModelName(ctx context.Context, modelName string) error {
	c.fetchModels(ctx)

	c.modelMu.RLock()
	defer c.modelMu.RUnlock()

	if c.modelErr != nil {
		return fmt.Errorf("could not validate model name: %w", c.modelErr)
	}

	if _, ok := c.modelCache[modelName]; ok {
		return nil
	}

	known := make([]string, 0, len(c.modelCache))
	for name := range c.modelCache {
		known = append(known, name)
	}
	sort.Strings(known)
	return fmt.Errorf("unknown Google model %q; known models: %s", modelName, strings.Join(known, ", "))
}

// ListModels returns the models available from the Gemini API. Results are
// cached; call InvalidateModelCache to force a refresh on the next call.
func (c *GeminiClient) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	c.fetchModels(ctx)

	c.modelMu.RLock()
	defer c.modelMu.RUnlock()

	if c.modelErr != nil {
		return nil, c.modelErr
	}

	models := make([]llm.ModelInfo, 0, len(c.modelCache))
	for name, displayName := range c.modelCache {
		models = append(models, llm.ModelInfo{Name: name, DisplayName: displayName})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models, nil
}

// InvalidateModelCache clears the cached model list so the next call to
// ListModels or ValidateModelName fetches fresh data from the API.
func (c *GeminiClient) InvalidateModelCache() {
	c.modelMu.Lock()
	defer c.modelMu.Unlock()
	c.modelCache = nil
	c.modelErr = nil
	c.modelLoaded = false
}

// buildNameMapping creates the bidirectional mapping between original MCP tool
// names (which may contain dots/hyphens) and the sanitized wire-format names
// that the Gemini API accepts.
func buildNameMapping(tools []llm.ToolDefinition) toolNameMapping {
	names := toolNameMapping{
		SanitizedToOriginal: make(map[string]string, len(tools)),
		OriginalToSanitized: make(map[string]string, len(tools)),
	}
	for _, t := range tools {
		sanitized := sanitizeToolName(t.Name)
		names.SanitizedToOriginal[sanitized] = t.Name
		names.OriginalToSanitized[t.Name] = sanitized
	}
	return names
}

// buildContents translates the provider-neutral conversation history into
// genai Content structs. It performs a two-pass approach: first collecting
// a callID→name map from all ToolCallBlocks, then translating each turn.
// Tool names in ToolCallBlock and ToolResultBlock are mapped to their
// sanitized wire-format equivalents using the provided name mapping.
func buildContents(history []llm.ConversationTurn, names toolNameMapping) []*genai.Content {
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
			case llm.TextBlock:
				parts = append(parts, &genai.Part{Text: b.Text})
			case llm.ToolCallBlock:
				var argsMap map[string]any
				if len(b.Input) > 0 {
					_ = json.Unmarshal(b.Input, &argsMap)
				}
				wireName := b.Name
				if mapped, ok := names.OriginalToSanitized[b.Name]; ok {
					wireName = mapped
				}
				parts = append(parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   b.ID,
						Name: wireName,
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
func buildConfig(req llm.MessageRequest, hints *GeminiHints, names toolNameMapping) *genai.GenerateContentConfig {
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
// FunctionDeclarations. Tool names are sanitized to comply with Gemini's
// naming requirements (alphanumeric + underscore only).
func buildTools(tools []llm.ToolDefinition) []*genai.Tool {
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		decl := &genai.FunctionDeclaration{
			Name:        sanitizeToolName(t.Name),
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
//
// Known gap: part.ThoughtSignature bytes are discarded here. When full thinking
// continuity is implemented, ThoughtSignature must be echoed back in subsequent
// requests to maintain the thinking chain. A follow-up issue is needed.
func translateResponse(resp *genai.GenerateContentResponse, names toolNameMapping) (*llm.MessageResponse, error) {
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
		if part.Thought {
			result.Thinking = append(result.Thinking, llm.ThinkingBlock{
				Text:     part.Text,
				Redacted: false,
			})
			continue
		}
		if part.FunctionCall != nil {
			id := part.FunctionCall.ID
			if id == "" {
				id = uuid.NewString()
			}
			argsJSON, _ := json.Marshal(part.FunctionCall.Args)
			// Reverse-map from sanitized wire name to original MCP name.
			originalName := part.FunctionCall.Name
			if mapped, ok := names.SanitizedToOriginal[part.FunctionCall.Name]; ok {
				originalName = mapped
			}
			result.ToolCalls = append(result.ToolCalls, llm.ToolCallBlock{
				ID:    id,
				Name:  originalName,
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
