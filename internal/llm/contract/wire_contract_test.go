// Package contract_test provides a cross-wire contract suite parameterized over
// all four real ProviderWire implementations (anthropic, google, openai,
// openaicompat). It asserts the provider-agnostic invariants that the individual
// package-local tests leave implicit or incomplete:
//
//   - Continuity-state round-trip: the opaque state needed for multi-turn
//     continuity survives a full response→history→outbound-request cycle (the
//     token is parsed out of a response and then re-emitted on the wire when fed
//     back through CreateMessage).
//   - Tool-name round-trip: dots and other MCP-namespace characters survive
//     sanitization on the way out and reverse-mapping on the way back.
//   - Stop-reason normalization: provider-native stop strings map to the
//     neutral llm.StopReason enum.
//   - Usage extraction: InputTokens / OutputTokens (and ThinkingTokens where
//     applicable) land in the right fields.
//
// The continuity-state carrier differs per provider (BLOCKING-3):
//
//   - anthropic / openai: ThinkingBlock.ProviderState (opaque JSON round-trip).
//   - google: ToolCallBlock.ProviderMetadata["google.thought_signature"]
//     (thought bytes attached to a FunctionCall part).
//   - openaicompat: REQUEST-direction ThinkingBlocks (history fed back through
//     CreateMessage) are DROPPED — Chat Completions has no reasoning round-trip,
//     so sending a ThinkingBlock in history must not echo it back in the outbound
//     chatRequest. RESPONSE-direction reasoning_content from thinking-capable
//     backends (LM Studio, Ollama ≥0.5, vLLM, llama.cpp) IS surfaced as
//     llm.ThinkingBlocks on the way in — see TestContract_ReasoningContent_OpenAICompat.
package contract_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
	openaisdk_option "github.com/openai/openai-go/option"
	"google.golang.org/genai"

	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/llm/anthropic"
	"github.com/felag-engineering/gleipnir/internal/llm/google"
	"github.com/felag-engineering/gleipnir/internal/llm/openai"
	"github.com/felag-engineering/gleipnir/internal/llm/openaicompat"
)

// --- Response body builders ---

func anthropicResponse(stopReason, content string, inputTokens, outputTokens int) string {
	return fmt.Sprintf(
		`{"id":"msg_x","type":"message","role":"assistant","model":"claude-sonnet-4-6",`+
			`"content":[%s],"stop_reason":%q,`+
			`"usage":{"input_tokens":%d,"output_tokens":%d}}`,
		content, stopReason, inputTokens, outputTokens,
	)
}

func anthropicTextBody(inputTokens, outputTokens int) string {
	return anthropicResponse("end_turn", `{"type":"text","text":"hello world"}`, inputTokens, outputTokens)
}

func anthropicToolCallBody() string {
	return anthropicResponse("tool_use",
		`{"type":"tool_use","id":"tu_1","name":"my_server_do_thing","input":{"q":"x"}}`, 10, 5)
}

func anthropicThinkingBody() string {
	return anthropicResponse("end_turn",
		`{"type":"thinking","thinking":"I think...","signature":"sig-abc"}`, 20, 10)
}

func anthropicMaxTokensBody() string {
	return anthropicResponse("max_tokens", `{"type":"text","text":"truncated"}`, 5, 100)
}

func openaiResponse(status, outputItem string, inputTokens, outputTokens, reasoningTokens int) string {
	return fmt.Sprintf(
		`{"id":"resp_x","object":"response","created_at":1700000000,"model":"gpt-5","status":%q,`+
			`"output":[%s],`+
			`"usage":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d,`+
			`"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":%d}}}`,
		status, outputItem, inputTokens, outputTokens, inputTokens+outputTokens, reasoningTokens,
	)
}

func openaiTextBody(inputTokens, outputTokens int) string {
	msg := `{"id":"m1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello world","annotations":[]}]}`
	return openaiResponse("completed", msg, inputTokens, outputTokens, 0)
}

func openaiToolCallBody() string {
	fc := `{"id":"fc1","type":"function_call","call_id":"call_x","name":"my_server_do_thing","arguments":"{\"q\":\"x\"}","status":"completed"}`
	return openaiResponse("completed", fc, 10, 8, 0)
}

func openaiThinkingBody() string {
	rs := `{"id":"rs1","type":"reasoning","encrypted_content":"enc_xyz","summary":[{"type":"summary_text","text":"thinking"}],"status":"completed"}`
	return openaiResponse("completed", rs, 5, 20, 15)
}

func openaiMaxTokensBody() string {
	msg := `{"id":"m2","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"truncated","annotations":[]}]}`
	return openaiResponse("incomplete", msg, 5, 100, 0)
}

func compatResponse(finishReason, content string, promptTokens, completionTokens int) string {
	return fmt.Sprintf(
		`{"id":"chatcmpl-x","choices":[{"index":0,"message":%s,"finish_reason":%q}],`+
			`"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`,
		content, finishReason, promptTokens, completionTokens,
	)
}

func compatTextBody(inputTokens, outputTokens int) string {
	return compatResponse("stop", `{"role":"assistant","content":"hello world"}`, inputTokens, outputTokens)
}

func compatToolCallBody() string {
	tc := `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"my_server_do_thing","arguments":"{\"q\":\"x\"}"}}]}`
	return compatResponse("tool_calls", tc, 10, 5)
}

func compatMaxTokensBody() string {
	return compatResponse("length", `{"role":"assistant","content":"truncated"}`, 5, 100)
}

// --- Client constructors backed by httptest servers ---

func newAnthropicClientJSON(t *testing.T, body string) llm.LLMClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return anthropic.NewClient("test-key",
		option.WithBaseURL(srv.URL),
		option.WithMaxRetries(0),
	)
}

func newOpenAIClientJSON(t *testing.T, body string) llm.LLMClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return openai.NewClient("test-key",
		openaisdk_option.WithHTTPClient(srv.Client()),
		openaisdk_option.WithBaseURL(srv.URL),
	)
}

func newCompatClientJSON(t *testing.T, body string) llm.LLMClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return openaicompat.NewClient(srv.URL, "test-key", openaicompat.WithHTTPClient(srv.Client()))
}

// captureServer returns an httptest server that always responds with respBody
// and records the body of the most recent request it received into *lastReq.
// The continuity round-trip tests use it to inspect what each wire re-emits when
// a ProviderState-bearing ThinkingBlock is fed back through CreateMessage.
func captureServer(t *testing.T, respBody string, lastReq *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*lastReq = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(respBody)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --- Google fake generator ---

// fakeGenerator satisfies the unexported google.contentGenerator interface
// structurally: same method signatures, no need to name the interface.
// See BLOCKING-3 in the plan — Google continuity uses ProviderMetadata, not
// ThinkingBlock.ProviderState; the fakeGenerator drives the translateResponse
// path via google.NewClientWithGenerator.
type fakeGenerator struct {
	response *genai.GenerateContentResponse
	err      error

	// lastContents records the contents of the most recent GenerateContent call,
	// so the continuity round-trip test can inspect what buildContents re-emitted.
	lastContents []*genai.Content

	// lastConfig records the config of the most recent GenerateContent
	// call, so the schema-translation contract can inspect the
	// *genai.Schema buildTools produced for each tool.
	lastConfig *genai.GenerateContentConfig
}

func (f *fakeGenerator) GenerateContent(_ context.Context, _ string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	f.lastContents = contents
	f.lastConfig = config
	return f.response, f.err
}

func (f *fakeGenerator) GenerateContentStream(_ context.Context, _ string, _ []*genai.Content, _ *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		yield(f.response, f.err)
	}
}

func newGoogleClient(gen *fakeGenerator) llm.LLMClient {
	return google.NewClientWithGenerator(gen)
}

// --- Shared test helpers ---

func minimalHistory() []llm.ConversationTurn {
	return []llm.ConversationTurn{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hello"}}},
	}
}

func minimalRequest() llm.MessageRequest {
	return llm.MessageRequest{
		Model:   "test-model",
		History: minimalHistory(),
	}
}

// requestWithTool returns a request carrying a single tool named
// "my_server.do_thing" with the given InputSchema. Pass nil for the tests
// that only exercise the tool NAME round-trip and never look at the schema —
// nil is exactly today's zero value, so those call sites are unchanged in
// behaviour.
func requestWithTool(schema json.RawMessage) llm.MessageRequest {
	return llm.MessageRequest{
		Model:   "test-model",
		History: minimalHistory(),
		Tools:   []llm.ToolDefinition{{Name: "my_server.do_thing", Description: "test", InputSchema: schema}},
	}
}

// requestWithFeatureRichTool returns a request whose tool InputSchema uses a
// nested "oneOf", a "format" annotation, and a "$defs"/"$ref" pair — the
// constructs a restricted SchemaFeatureSet would gate. Used by
// TestContract_ToolSchemaPassthrough_FullSupport to prove the shared
// TranslateForFeatures pass strips nothing when a wire declares full support.
func requestWithFeatureRichTool() llm.MessageRequest {
	return requestWithTool(json.RawMessage(`{"type":"object","properties":{"target":{"oneOf":[` +
		`{"type":"string","format":"date-time"},{"$ref":"#/$defs/Other"}]}},` +
		`"$defs":{"Other":{"type":"string"}}}`))
}

// --- Contract test: stop-reason normalization ---

// TestContract_StopReasonNormalization verifies that each provider maps its
// native stop strings to the shared llm.StopReason enum values. This is the
// primary invariant that lets the agent runtime be provider-agnostic.
func TestContract_StopReasonNormalization(t *testing.T) {
	type stopCase struct {
		name       string
		makeClient func() llm.LLMClient
		req        llm.MessageRequest
		want       llm.StopReason
	}

	cases := []stopCase{
		// Anthropic
		{
			name:       "anthropic/end_turn",
			makeClient: func() llm.LLMClient { return newAnthropicClientJSON(t, anthropicTextBody(5, 3)) },
			req:        minimalRequest(),
			want:       llm.StopReasonEndTurn,
		},
		{
			name:       "anthropic/tool_use",
			makeClient: func() llm.LLMClient { return newAnthropicClientJSON(t, anthropicToolCallBody()) },
			req:        requestWithTool(nil),
			want:       llm.StopReasonToolUse,
		},
		{
			name:       "anthropic/max_tokens",
			makeClient: func() llm.LLMClient { return newAnthropicClientJSON(t, anthropicMaxTokensBody()) },
			req:        minimalRequest(),
			want:       llm.StopReasonMaxTokens,
		},
		// OpenAI Responses API
		{
			name:       "openai/end_turn",
			makeClient: func() llm.LLMClient { return newOpenAIClientJSON(t, openaiTextBody(5, 3)) },
			req:        minimalRequest(),
			want:       llm.StopReasonEndTurn,
		},
		{
			name:       "openai/tool_use",
			makeClient: func() llm.LLMClient { return newOpenAIClientJSON(t, openaiToolCallBody()) },
			req:        requestWithTool(nil),
			want:       llm.StopReasonToolUse,
		},
		{
			name:       "openai/max_tokens",
			makeClient: func() llm.LLMClient { return newOpenAIClientJSON(t, openaiMaxTokensBody()) },
			req:        minimalRequest(),
			want:       llm.StopReasonMaxTokens,
		},
		// OpenAI Chat Completions compatible
		{
			name:       "openaicompat/end_turn",
			makeClient: func() llm.LLMClient { return newCompatClientJSON(t, compatTextBody(5, 3)) },
			req:        minimalRequest(),
			want:       llm.StopReasonEndTurn,
		},
		{
			name:       "openaicompat/tool_use",
			makeClient: func() llm.LLMClient { return newCompatClientJSON(t, compatToolCallBody()) },
			req:        requestWithTool(nil),
			want:       llm.StopReasonToolUse,
		},
		{
			name:       "openaicompat/max_tokens",
			makeClient: func() llm.LLMClient { return newCompatClientJSON(t, compatMaxTokensBody()) },
			req:        minimalRequest(),
			want:       llm.StopReasonMaxTokens,
		},
		// Google (via fakeGenerator)
		{
			name: "google/end_turn",
			makeClient: func() llm.LLMClient {
				return newGoogleClient(&fakeGenerator{
					response: &genai.GenerateContentResponse{
						Candidates: []*genai.Candidate{{
							Content:      &genai.Content{Parts: []*genai.Part{{Text: "hello"}}},
							FinishReason: genai.FinishReasonStop,
						}},
						UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3},
					},
				})
			},
			req:  minimalRequest(),
			want: llm.StopReasonEndTurn,
		},
		{
			name: "google/tool_use",
			makeClient: func() llm.LLMClient {
				return newGoogleClient(&fakeGenerator{
					response: &genai.GenerateContentResponse{
						Candidates: []*genai.Candidate{{
							Content: &genai.Content{Parts: []*genai.Part{{
								FunctionCall: &genai.FunctionCall{ID: "fc-1", Name: "my_server_do_thing", Args: map[string]any{"q": "x"}},
							}}},
							FinishReason: genai.FinishReasonStop,
						}},
						UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5},
					},
				})
			},
			req:  requestWithTool(nil),
			want: llm.StopReasonToolUse,
		},
		{
			name: "google/max_tokens",
			makeClient: func() llm.LLMClient {
				return newGoogleClient(&fakeGenerator{
					response: &genai.GenerateContentResponse{
						Candidates: []*genai.Candidate{{
							Content:      &genai.Content{Parts: []*genai.Part{{Text: "truncated"}}},
							FinishReason: genai.FinishReasonMaxTokens,
						}},
						UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 100},
					},
				})
			},
			req:  minimalRequest(),
			want: llm.StopReasonMaxTokens,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.makeClient()
			resp, err := client.CreateMessage(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
			if resp.StopReason != tc.want {
				t.Errorf("StopReason = %v, want %v", resp.StopReason, tc.want)
			}
		})
	}
}

// --- Contract test: usage extraction ---

// TestContract_UsageExtraction verifies that InputTokens, OutputTokens, and
// (where available) ThinkingTokens are populated correctly across all providers.
func TestContract_UsageExtraction(t *testing.T) {
	type usageCase struct {
		name         string
		makeClient   func() llm.LLMClient
		wantInput    int
		wantOutput   int
		wantThinking int
	}

	cases := []usageCase{
		{
			name:       "anthropic",
			makeClient: func() llm.LLMClient { return newAnthropicClientJSON(t, anthropicTextBody(42, 17)) },
			wantInput:  42,
			wantOutput: 17,
		},
		{
			name:       "openai",
			makeClient: func() llm.LLMClient { return newOpenAIClientJSON(t, openaiTextBody(42, 17)) },
			wantInput:  42,
			wantOutput: 17,
		},
		{
			name:         "openai_thinking_tokens",
			makeClient:   func() llm.LLMClient { return newOpenAIClientJSON(t, openaiThinkingBody()) },
			wantInput:    5,
			wantOutput:   20,
			wantThinking: 15,
		},
		{
			name:       "openaicompat",
			makeClient: func() llm.LLMClient { return newCompatClientJSON(t, compatTextBody(42, 17)) },
			wantInput:  42,
			wantOutput: 17,
		},
		{
			name: "google",
			makeClient: func() llm.LLMClient {
				return newGoogleClient(&fakeGenerator{
					response: &genai.GenerateContentResponse{
						Candidates: []*genai.Candidate{{
							Content:      &genai.Content{Parts: []*genai.Part{{Text: "hi"}}},
							FinishReason: genai.FinishReasonStop,
						}},
						UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
							PromptTokenCount:     42,
							CandidatesTokenCount: 17,
						},
					},
				})
			},
			wantInput:  42,
			wantOutput: 17,
		},
		{
			name: "google_thinking_tokens",
			makeClient: func() llm.LLMClient {
				return newGoogleClient(&fakeGenerator{
					response: &genai.GenerateContentResponse{
						Candidates: []*genai.Candidate{{
							Content:      &genai.Content{Parts: []*genai.Part{{Text: "answer"}}},
							FinishReason: genai.FinishReasonStop,
						}},
						UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
							PromptTokenCount:     10,
							CandidatesTokenCount: 5,
							ThoughtsTokenCount:   30,
						},
					},
				})
			},
			wantInput:    10,
			wantOutput:   5,
			wantThinking: 30,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.makeClient()
			resp, err := client.CreateMessage(context.Background(), minimalRequest())
			if err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
			if resp.Usage.InputTokens != tc.wantInput {
				t.Errorf("InputTokens = %d, want %d", resp.Usage.InputTokens, tc.wantInput)
			}
			if resp.Usage.OutputTokens != tc.wantOutput {
				t.Errorf("OutputTokens = %d, want %d", resp.Usage.OutputTokens, tc.wantOutput)
			}
			if tc.wantThinking != 0 && resp.Usage.ThinkingTokens != tc.wantThinking {
				t.Errorf("ThinkingTokens = %d, want %d", resp.Usage.ThinkingTokens, tc.wantThinking)
			}
		})
	}
}

// --- Contract test: tool-name round-trip ---

// TestContract_ToolNameRoundTrip verifies that tool names containing '.' (the
// MCP namespace separator) survive sanitization on the outbound request and
// reverse-mapping on the inbound tool-call response. The tool name returned in
// ToolCalls must not be empty.
//
// The fixture response returns the sanitized wire name ("my_server_do_thing" —
// the '.' is replaced with '_' by every provider, since '.' is never in any
// provider's allowedExtra set). The reverse-map must convert it back to the
// original MCP name "my_server.do_thing"; the contract asserts that exact
// round-trip so a broken reverse-map surfaces here, not just in per-provider
// unit tests.
func TestContract_ToolNameRoundTrip(t *testing.T) {
	type toolCase struct {
		name       string
		makeClient func() llm.LLMClient
	}

	cases := []toolCase{
		{
			name:       "anthropic",
			makeClient: func() llm.LLMClient { return newAnthropicClientJSON(t, anthropicToolCallBody()) },
		},
		{
			name:       "openai",
			makeClient: func() llm.LLMClient { return newOpenAIClientJSON(t, openaiToolCallBody()) },
		},
		{
			name:       "openaicompat",
			makeClient: func() llm.LLMClient { return newCompatClientJSON(t, compatToolCallBody()) },
		},
		{
			name: "google",
			makeClient: func() llm.LLMClient {
				return newGoogleClient(&fakeGenerator{
					response: &genai.GenerateContentResponse{
						Candidates: []*genai.Candidate{{
							Content: &genai.Content{Parts: []*genai.Part{{
								FunctionCall: &genai.FunctionCall{ID: "fc-1", Name: "my_server_do_thing", Args: map[string]any{"q": "x"}},
							}}},
							FinishReason: genai.FinishReasonStop,
						}},
						UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5},
					},
				})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.makeClient()
			resp, err := client.CreateMessage(context.Background(), requestWithTool(nil))
			if err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
			if len(resp.ToolCalls) == 0 {
				t.Fatal("expected at least one tool call in response")
			}
			if got := resp.ToolCalls[0].Name; got != "my_server.do_thing" {
				t.Errorf("tool name not reverse-mapped to original: got %q, want %q", got, "my_server.do_thing")
			}
		})
	}
}

// --- Contract test: tool-schema passthrough on full-support declaration ---

// TestContract_ToolSchemaPassthrough_FullSupport verifies that
// ProviderAdapter.prepareRequest's shared TranslateForFeatures pass strips
// nothing from a tool's InputSchema when the wire declares full JSON Schema
// support (issue #736's DoD): the outbound HTTP body must still contain the
// nested "oneOf" and the "format" value verbatim.
//
// Google is deliberately EXCLUDED from this table: translateJSONSchemaToGenaiSchema
// (google/schema.go) drops unknown keywords before the SDK call as
// pre-existing wire behavior unrelated to the shared pass, so an outbound
// byte-identity assertion is not meaningful there. Google's full-support
// declaration is covered by its package-local TestWire_SchemaFeatures_Full and
// by the TranslateForFeatures unit tests in internal/llm.
func TestContract_ToolSchemaPassthrough_FullSupport(t *testing.T) {
	type schemaCase struct {
		name       string
		respBody   string
		makeClient func(t *testing.T, srv *httptest.Server) llm.LLMClient
	}

	cases := []schemaCase{
		{
			name:     "anthropic",
			respBody: anthropicTextBody(5, 3),
			makeClient: func(t *testing.T, srv *httptest.Server) llm.LLMClient {
				return anthropic.NewClient("test-key", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
			},
		},
		{
			name:     "openai",
			respBody: openaiTextBody(5, 3),
			makeClient: func(t *testing.T, srv *httptest.Server) llm.LLMClient {
				return openai.NewClient("test-key", openaisdk_option.WithHTTPClient(srv.Client()), openaisdk_option.WithBaseURL(srv.URL))
			},
		},
		{
			name:     "openaicompat",
			respBody: compatTextBody(5, 3),
			makeClient: func(t *testing.T, srv *httptest.Server) llm.LLMClient {
				return openaicompat.NewClient(srv.URL, "test-key", openaicompat.WithHTTPClient(srv.Client()))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var lastReq string
			srv := captureServer(t, tc.respBody, &lastReq)
			client := tc.makeClient(t, srv)

			if _, err := client.CreateMessage(context.Background(), requestWithFeatureRichTool()); err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
			if !strings.Contains(lastReq, "oneOf") {
				t.Errorf("outbound request must forward \"oneOf\" verbatim on a full-support declaration; body=%s", lastReq)
			}
			if !strings.Contains(lastReq, "date-time") {
				t.Errorf("outbound request must forward the format value \"date-time\" verbatim; body=%s", lastReq)
			}
		})
	}
}

// --- Contract test: continuity-state round-trip (per-provider carrier differs) ---
//
// Each test drives the full cycle through the exported Client + a real transport
// stub: parse the continuity token out of a response, feed it back through a
// second CreateMessage, and assert it re-appears on the outbound wire (or, for
// openaicompat, is correctly dropped). The carrier differs per provider, so these
// are separate tests rather than one table.

// TestContract_ContinuityState_Anthropic asserts the full continuity round-trip
// for Anthropic: the response carries ProviderState with a "signature" field
// (response direction), and feeding that ThinkingBlock back through CreateMessage
// re-emits the signature on the outbound wire (request direction) — the opaque
// token Anthropic requires for extended-thinking continuity.
func TestContract_ContinuityState_Anthropic(t *testing.T) {
	var lastReq string
	srv := captureServer(t, anthropicThinkingBody(), &lastReq)
	client := anthropic.NewClient("test-key", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))

	// Response direction: the thinking block carries ProviderState with a signature.
	resp, err := client.CreateMessage(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("CreateMessage (response direction): %v", err)
	}
	if len(resp.Thinking) == 0 {
		t.Fatal("expected at least one ThinkingBlock")
	}

	tb := resp.Thinking[0]
	if tb.Provider != "anthropic" {
		t.Errorf("ThinkingBlock.Provider = %q, want %q", tb.Provider, "anthropic")
	}
	if len(tb.ProviderState) == 0 {
		t.Fatal("ThinkingBlock.ProviderState must not be empty for Anthropic thinking blocks")
	}
	var state struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(tb.ProviderState, &state); err != nil {
		t.Fatalf("unmarshal ProviderState: %v", err)
	}
	if state.Signature == "" {
		t.Fatal("ThinkingBlock.ProviderState must contain a non-empty 'signature' field")
	}

	// Request direction: feed the thinking block back and assert the signature
	// re-appears on the outbound wire — the round-trip that continuity depends on.
	if _, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model: "claude-sonnet-4-6",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hello"}}},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{tb, llm.TextBlock{Text: "answer"}}},
		},
	}); err != nil {
		t.Fatalf("CreateMessage (request direction): %v", err)
	}
	if !strings.Contains(lastReq, state.Signature) {
		t.Errorf("outbound request must echo the thinking signature %q for continuity; body=%s", state.Signature, lastReq)
	}
}

// TestContract_ContinuityState_OpenAI asserts the full continuity round-trip for
// OpenAI: the response carries ProviderState with an "encrypted_content" field
// (response direction), and feeding that reasoning item back through
// CreateMessage re-emits encrypted_content on the outbound wire (request
// direction) — the opaque token OpenAI's Responses API requires to replay the
// reasoning item on subsequent turns.
func TestContract_ContinuityState_OpenAI(t *testing.T) {
	var lastReq string
	srv := captureServer(t, openaiThinkingBody(), &lastReq)
	client := openai.NewClient("test-key",
		openaisdk_option.WithHTTPClient(srv.Client()),
		openaisdk_option.WithBaseURL(srv.URL),
	)

	// Response direction: the reasoning item carries ProviderState with encrypted_content.
	resp, err := client.CreateMessage(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("CreateMessage (response direction): %v", err)
	}
	if len(resp.Thinking) == 0 {
		t.Fatal("expected at least one ThinkingBlock")
	}

	tb := resp.Thinking[0]
	if tb.Provider != "openai" {
		t.Errorf("ThinkingBlock.Provider = %q, want %q", tb.Provider, "openai")
	}
	if len(tb.ProviderState) == 0 {
		t.Fatal("ThinkingBlock.ProviderState must not be empty for OpenAI reasoning items")
	}
	var state struct {
		EncryptedContent string `json:"encrypted_content"`
	}
	if err := json.Unmarshal(tb.ProviderState, &state); err != nil {
		t.Fatalf("unmarshal ProviderState: %v", err)
	}
	if state.EncryptedContent == "" {
		t.Fatal("ThinkingBlock.ProviderState must contain a non-empty 'encrypted_content' field")
	}

	// Request direction: feed the reasoning item back and assert encrypted_content
	// re-appears on the outbound wire — the round-trip that continuity depends on.
	if _, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gpt-5",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hello"}}},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{tb, llm.TextBlock{Text: "answer"}}},
		},
	}); err != nil {
		t.Fatalf("CreateMessage (request direction): %v", err)
	}
	if !strings.Contains(lastReq, state.EncryptedContent) {
		t.Errorf("outbound request must echo encrypted_content %q for continuity; body=%s", state.EncryptedContent, lastReq)
	}
}

// TestContract_ContinuityState_Google asserts the full continuity round-trip for
// Google. Google has no ThinkingBlock.ProviderState — the thought bytes ride
// ToolCallBlock.ProviderMetadata["google.thought_signature"], attached to a
// FunctionCall part. The response direction surfaces them into the metadata map,
// and feeding the tool call back through CreateMessage re-attaches them as
// part.ThoughtSignature on the outbound contents.
func TestContract_ContinuityState_Google(t *testing.T) {
	sig := []byte{0xde, 0xad, 0xbe, 0xef}
	gen := &fakeGenerator{
		response: &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{{
				Content: &genai.Content{Parts: []*genai.Part{{
					FunctionCall:     &genai.FunctionCall{ID: "fc-1", Name: "do_thing", Args: map[string]any{"q": "x"}},
					ThoughtSignature: sig,
				}}},
				FinishReason: genai.FinishReasonStop,
			}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5},
		},
	}
	client := newGoogleClient(gen)

	// Response direction: the thought signature surfaces into ProviderMetadata.
	resp, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model:   "gemini-2.0-flash",
		History: minimalHistory(),
		Tools:   []llm.ToolDefinition{{Name: "do_thing", Description: "test"}},
	})
	if err != nil {
		t.Fatalf("CreateMessage (response direction): %v", err)
	}
	if len(resp.ToolCalls) == 0 {
		t.Fatal("expected at least one ToolCallBlock")
	}

	gotSig := resp.ToolCalls[0].ProviderMetadata["google.thought_signature"]
	if len(gotSig) == 0 {
		t.Fatal("ToolCallBlock.ProviderMetadata['google.thought_signature'] must not be empty when ThoughtSignature is set")
	}
	if string(gotSig) != string(sig) {
		t.Errorf("thought_signature bytes = %v, want %v", gotSig, sig)
	}

	// Request direction: feed the tool call back and assert buildContents
	// re-attaches the signature as part.ThoughtSignature on the outbound contents.
	if _, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gemini-2.0-flash",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hello"}}},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{resp.ToolCalls[0]}},
		},
		Tools: []llm.ToolDefinition{{Name: "do_thing", Description: "test"}},
	}); err != nil {
		t.Fatalf("CreateMessage (request direction): %v", err)
	}
	var echoed []byte
	for _, c := range gen.lastContents {
		for _, p := range c.Parts {
			if len(p.ThoughtSignature) > 0 {
				echoed = p.ThoughtSignature
			}
		}
	}
	if string(echoed) != string(sig) {
		t.Errorf("buildContents must re-attach thought_signature on the outbound part; got %v, want %v", echoed, sig)
	}
}

// TestContract_ReasoningContent_OpenAICompat verifies that reasoning_content
// returned by an OpenAI-compatible backend in a sync response is surfaced as a
// ThinkingBlock in the normalized llm.MessageResponse. This is the response
// direction; the request-direction drop is asserted by
// TestContract_ContinuityState_OpenAICompat below.
func TestContract_ReasoningContent_OpenAICompat(t *testing.T) {
	const reasoningSentinel = "<reasoning-sentinel>"
	msgJSON := `{"role":"assistant","content":"answer","reasoning_content":"` + reasoningSentinel + `"}`
	body := compatResponse("stop", msgJSON, 5, 3)
	client := newCompatClientJSON(t, body)

	resp, err := client.CreateMessage(context.Background(), llm.MessageRequest{Model: "gpt-4"})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if len(resp.Thinking) != 1 {
		t.Fatalf("want 1 ThinkingBlock, got %d", len(resp.Thinking))
	}
	if resp.Thinking[0].Text != reasoningSentinel {
		t.Errorf("ThinkingBlock.Text = %q, want %q", resp.Thinking[0].Text, reasoningSentinel)
	}
}

// TestContract_ContinuityState_OpenAICompat asserts that ThinkingBlocks in
// conversation history are DROPPED by the Chat Completions wire. Chat
// Completions has no reasoning round-trip — the openaicompat provider must
// silently omit them rather than forwarding them as unrecognized content.
//
// We capture the outbound HTTP request body and verify it contains no
// "thinking" markers from the injected ThinkingBlock.
func TestContract_ContinuityState_OpenAICompat(t *testing.T) {
	var lastReq string
	srv := captureServer(t, compatTextBody(5, 3), &lastReq)
	client := openaicompat.NewClient(srv.URL, "test-key", openaicompat.WithHTTPClient(srv.Client()))

	providerState, _ := json.Marshal(map[string]string{"signature": "sig-sentinel"})
	history := []llm.ConversationTurn{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hello"}}},
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				llm.ThinkingBlock{
					Provider:      "anthropic",
					Text:          "internal-reasoning-sentinel",
					ProviderState: providerState,
				},
				llm.TextBlock{Text: "I reasoned about this"},
			},
		},
	}

	resp, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model:   "gpt-4",
		History: history,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if len(resp.Text) == 0 {
		t.Fatal("expected non-empty text response")
	}

	if strings.Contains(lastReq, "sig-sentinel") {
		t.Error("outbound request must not echo ThinkingBlock.ProviderState (thinking dropped for compat wire)")
	}
	if strings.Contains(lastReq, "internal-reasoning-sentinel") {
		t.Error("outbound request must not echo ThinkingBlock.Text (thinking dropped for compat wire)")
	}
}
