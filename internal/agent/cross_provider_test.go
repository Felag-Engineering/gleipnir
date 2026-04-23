package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/llm/anthropic"
	googlellm "github.com/rapp992/gleipnir/internal/llm/google"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// TestCrossProvider_StructuralParity verifies that the same policy run through
// two different provider names (anthropic, google) produces an identical audit
// trail step type sequence. Both entries use MockLLMClient — provider name is
// recorded in the capability_snapshot step but does not affect routing here.
func TestCrossProvider_StructuralParity(t *testing.T) {
	t.Log("Both sub-tests use MockLLMClient which returns scripted IDs. These tests validate the provider-agnostic architecture path, not provider-specific behaviors like Gemini's synthetic UUID generation (which occurs in the real GeminiClient.translateResponse, not exercised here).")

	mcpSrv := makeToolCallServer(t, json.RawMessage(`[{"type":"text","text":"result data"}]`), false)

	providerSequences := make(map[string][]string)

	providers := []struct {
		name      string
		provider  string
		modelName string
	}{
		{name: "anthropic", provider: "anthropic", modelName: "claude-sonnet-4-6"},
		{name: "google", provider: "google", modelName: "gemini-2.5-flash"},
	}

	for _, tc := range providers {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

			pol := minimalPolicy()
			pol.Agent.ModelConfig.Provider = tc.provider
			pol.Agent.ModelConfig.Name = tc.modelName

			mockClient := testutil.NewMockLLMClient(
				testutil.MakeToolCallResponse("my-server.read_data", "call-1", nil),
				testutil.MakeTextResponse("Done."),
			)

			tools := []mcp.ResolvedTool{toolForRun(mcpSrv.URL, "my-server", "read_data")}

			ba, err := New(Config{
				LLMClient:    mockClient,
				Tools:        tools,
				Policy:       pol,
				Audit:        NewAuditWriter(s.Queries()),
				StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.DB(), s.Queries()),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			if err := ba.Run(context.Background(), "r1", "do stuff"); err != nil {
				t.Fatalf("Run: %v", err)
			}

			steps, err := s.ListRunSteps(context.Background(), "r1")
			if err != nil {
				t.Fatalf("ListRunSteps: %v", err)
			}

			types := make([]string, len(steps))
			for i, st := range steps {
				types[i] = st.Type
			}

			want := []string{
				string(model.StepTypeCapabilitySnapshot),
				string(model.StepTypeToolCall),
				string(model.StepTypeToolResult),
				string(model.StepTypeThought),
				string(model.StepTypeComplete),
			}
			if !reflect.DeepEqual(types, want) {
				t.Errorf("provider=%s: step types = %v, want %v", tc.provider, types, want)
			}

			// Verify capability snapshot content includes provider and model fields.
			type snapshotContent struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}
			var snap snapshotContent
			if err := json.Unmarshal([]byte(steps[0].Content), &snap); err != nil {
				t.Fatalf("provider=%s: unmarshal snapshot content: %v", tc.provider, err)
			}
			if snap.Provider != tc.provider {
				t.Errorf("provider=%s: snapshot provider = %q, want %q", tc.provider, snap.Provider, tc.provider)
			}
			if snap.Model != tc.modelName {
				t.Errorf("provider=%s: snapshot model = %q, want %q", tc.provider, snap.Model, tc.modelName)
			}

			providerSequences[tc.provider] = types
		})
	}

	if seq1, seq2 := providerSequences["anthropic"], providerSequences["google"]; !reflect.DeepEqual(seq1, seq2) {
		t.Errorf("cross-provider step sequences differ:\n  anthropic: %v\n  google:    %v", seq1, seq2)
	}
}

// TestCrossProvider_OptionsValidation verifies that ValidateOptions rejects
// invalid options for both the Anthropic and Gemini providers, and that the
// ProviderRegistry correctly delegates validation to the right client.
func TestCrossProvider_OptionsValidation(t *testing.T) {
	// Sub-group 1: Direct provider validation.
	t.Run("direct", func(t *testing.T) {
		tests := []struct {
			name          string
			client        llm.LLMClient
			options       map[string]any
			wantErrSubstr string
		}{
			{
				name:          "anthropic_invalid_option",
				client:        anthropic.NewClient("fake-key"),
				options:       map[string]any{"unknown_key": true},
				wantErrSubstr: "unknown option",
			},
			{
				name:          "anthropic_bad_type",
				client:        anthropic.NewClient("fake-key"),
				options:       map[string]any{"max_tokens": "not_a_number"},
				wantErrSubstr: "expected numeric",
			},
			{
				name:          "google_invalid_option",
				client:        &googlellm.GeminiClient{},
				options:       map[string]any{"bad_key": 42},
				wantErrSubstr: "unknown option",
			},
			{
				name:          "google_bad_type",
				client:        &googlellm.GeminiClient{},
				options:       map[string]any{"thinking_level": 123},
				wantErrSubstr: "must be a string",
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				err := tc.client.ValidateOptions(tc.options)
				if err == nil {
					t.Fatalf("ValidateOptions(%v) returned nil, want error containing %q", tc.options, tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("ValidateOptions error = %q, want substring %q", err.Error(), tc.wantErrSubstr)
				}
			})
		}
	})

	// Sub-group 2: Registry delegation verifies ProviderRegistry.ValidateProviderOptions
	// correctly routes to the registered client without any network I/O.
	t.Run("registry", func(t *testing.T) {
		registry := llm.NewProviderRegistry()
		registry.Register("anthropic", anthropic.NewClient("fake-key"))
		registry.Register("google", &googlellm.GeminiClient{})

		tests := []struct {
			name          string
			provider      string
			options       map[string]any
			wantErrSubstr string
		}{
			{
				name:          "registry_delegates_anthropic",
				provider:      "anthropic",
				options:       map[string]any{"unknown_key": true},
				wantErrSubstr: "unknown option",
			},
			{
				name:          "registry_delegates_google",
				provider:      "google",
				options:       map[string]any{"bad_key": 42},
				wantErrSubstr: "unknown option",
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				err := registry.ValidateProviderOptions(tc.provider, tc.options)
				if err == nil {
					t.Fatalf("ValidateProviderOptions(%q, %v) returned nil, want error containing %q", tc.provider, tc.options, tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("ValidateProviderOptions error = %q, want substring %q", err.Error(), tc.wantErrSubstr)
				}
			})
		}
	})
}

// TestCrossProvider_MultiToolCallBatching verifies that when the LLM returns
// multiple tool calls in a single response, the agent dispatches each one and
// records interleaved tool_call/tool_result pairs in the audit trail.
func TestCrossProvider_MultiToolCallBatching(t *testing.T) {
	t.Log("Both sub-tests use MockLLMClient. Gemini synthetic UUID generation is not exercised here — these tests validate provider-agnostic batching behavior.")

	// makeToolCallServer handles any tools/call request with a fixed response,
	// so a single server can back all three registered tools.
	mcpSrv := makeToolCallServer(t, json.RawMessage(`[{"type":"text","text":"ok"}]`), false)

	providerSequences := make(map[string][]string)

	providers := []struct {
		name     string
		provider string
	}{
		{name: "anthropic", provider: "anthropic"},
		{name: "google", provider: "google"},
	}

	for _, tc := range providers {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

			pol := minimalPolicy()
			pol.Agent.ModelConfig.Provider = tc.provider

			// Register 3 tools on the same MCP server. The agent dispatch
			// loop looks up tools by "serverName.toolName" key.
			tools := []mcp.ResolvedTool{
				toolForRun(mcpSrv.URL, "srv", "tool_a"),
				toolForRun(mcpSrv.URL, "srv", "tool_b"),
				toolForRun(mcpSrv.URL, "srv", "tool_c"),
			}

			mockClient := testutil.NewMockLLMClient(
				testutil.MakeMultiToolCallResponse([]testutil.MockToolCall{
					{ID: "call-a", Name: "srv.tool_a", Input: nil},
					{ID: "call-b", Name: "srv.tool_b", Input: nil},
					{ID: "call-c", Name: "srv.tool_c", Input: nil},
				}),
				testutil.MakeTextResponse("Done."),
			)

			ba, err := New(Config{
				LLMClient:    mockClient,
				Tools:        tools,
				Policy:       pol,
				Audit:        NewAuditWriter(s.Queries()),
				StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.DB(), s.Queries()),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			if err := ba.Run(context.Background(), "r1", "use all tools"); err != nil {
				t.Fatalf("Run: %v", err)
			}

			steps, err := s.ListRunSteps(context.Background(), "r1")
			if err != nil {
				t.Fatalf("ListRunSteps: %v", err)
			}

			types := make([]string, len(steps))
			for i, st := range steps {
				types[i] = st.Type
			}

			want := []string{
				string(model.StepTypeCapabilitySnapshot),
				string(model.StepTypeToolCall),
				string(model.StepTypeToolResult),
				string(model.StepTypeToolCall),
				string(model.StepTypeToolResult),
				string(model.StepTypeToolCall),
				string(model.StepTypeToolResult),
				string(model.StepTypeThought),
				string(model.StepTypeComplete),
			}
			if !reflect.DeepEqual(types, want) {
				t.Errorf("provider=%s: step types = %v, want %v", tc.provider, types, want)
			}

			var toolResultCount int
			for _, typ := range types {
				if typ == string(model.StepTypeToolResult) {
					toolResultCount++
				}
			}
			if toolResultCount != 3 {
				t.Errorf("provider=%s: tool_result count = %d, want 3", tc.provider, toolResultCount)
			}

			providerSequences[tc.provider] = types
		})
	}

	if seq1, seq2 := providerSequences["anthropic"], providerSequences["google"]; !reflect.DeepEqual(seq1, seq2) {
		t.Errorf("cross-provider step sequences differ:\n  anthropic: %v\n  google:    %v", seq1, seq2)
	}
}

// TestToolResultBlocksBeforeTextInUserTurn verifies that when the agent builds
// a user turn containing tool results and a time text block, the tool_result
// blocks appear before any text blocks. The Anthropic API requires this ordering
// and returns a 400 if a text block precedes tool_result blocks in a user message.
func TestToolResultBlocksBeforeTextInUserTurn(t *testing.T) {
	mcpSrv := makeToolCallServer(t, json.RawMessage(`[{"type":"text","text":"ok"}]`), false)

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	pol := minimalPolicy()

	tools := []mcp.ResolvedTool{
		toolForRun(mcpSrv.URL, "srv", "tool_a"),
		toolForRun(mcpSrv.URL, "srv", "tool_b"),
	}

	mockClient := testutil.NewMockLLMClient(
		testutil.MakeMultiToolCallResponse([]testutil.MockToolCall{
			{ID: "call-a", Name: "srv.tool_a", Input: nil},
			{ID: "call-b", Name: "srv.tool_b", Input: nil},
		}),
		testutil.MakeTextResponse("Done."),
	)

	ba, err := New(Config{
		LLMClient:    mockClient,
		Tools:        tools,
		Policy:       pol,
		Audit:        NewAuditWriter(s.Queries()),
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.DB(), s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := ba.Run(context.Background(), "r1", "use tools"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The second CreateMessage call should have the tool results turn in its history.
	reqs := mockClient.Requests()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 API calls, got %d", len(reqs))
	}

	// History on the second call: [user(trigger), assistant(tool_use), user(tool_results+time)]
	history := reqs[1].History
	if len(history) < 3 {
		t.Fatalf("expected at least 3 turns in second request history, got %d", len(history))
	}

	// The third turn (index 2) is the user turn with tool results and the time block.
	userTurn := history[2]
	if userTurn.Role != llm.RoleUser {
		t.Fatalf("expected user turn at index 2, got %s", userTurn.Role)
	}

	// Verify: all ToolResultBlocks must appear before any TextBlock.
	seenText := false
	for i, block := range userTurn.Content {
		switch block.(type) {
		case llm.TextBlock:
			seenText = true
		case llm.ToolResultBlock:
			if seenText {
				t.Errorf("ToolResultBlock at index %d appears after a TextBlock — "+
					"Anthropic API requires tool_result blocks before text blocks in user messages", i)
			}
		}
	}
}
