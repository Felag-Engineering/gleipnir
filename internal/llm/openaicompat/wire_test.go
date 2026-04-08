package openaicompat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestChatResponseFixturesUnmarshal(t *testing.T) {
	cases := []struct {
		file       string
		wantFinish string
		wantTools  int
		wantText   string
	}{
		{"chat_response_text_only.json", "stop", 0, "Hello from OpenAI."},
		{"chat_response_with_tool_calls.json", "tool_calls", 1, ""},
		{"chat_response_parallel_tool_calls.json", "tool_calls", 2, ""},
		{"chat_response_o_series_with_reasoning_tokens.json", "stop", 0, "42"},
		{"chat_response_finish_length.json", "length", 0, "truncated..."},
		{"chat_response_finish_content_filter.json", "content_filter", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var resp chatResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(resp.Choices) != 1 {
				t.Fatalf("want 1 choice, got %d", len(resp.Choices))
			}
			choice := resp.Choices[0]
			if choice.FinishReason != tc.wantFinish {
				t.Errorf("finish_reason: got %q, want %q", choice.FinishReason, tc.wantFinish)
			}
			if got := len(choice.Message.ToolCalls); got != tc.wantTools {
				t.Errorf("tool_calls: got %d, want %d", got, tc.wantTools)
			}
			if tc.wantText != "" {
				if choice.Message.Content == nil || *choice.Message.Content != tc.wantText {
					t.Errorf("content: got %v, want %q", choice.Message.Content, tc.wantText)
				}
			}
		})
	}
}

func TestErrorResponseFixturesUnmarshal(t *testing.T) {
	for _, file := range []string{
		"chat_response_error_401.json",
		"chat_response_error_429.json",
		"chat_response_error_500.json",
	} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", file))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var resp chatResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.Error == nil || resp.Error.Message == "" {
				t.Errorf("want non-empty error message, got %+v", resp.Error)
			}
		})
	}
}

func TestModelsResponseFixtureUnmarshal(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "models_response.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var resp modelsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 3 {
		t.Errorf("want 3 entries, got %d", len(resp.Data))
	}
	wantIDs := map[string]bool{"gpt-4o": true, "gpt-4o-mini": true, "o3-mini": true}
	for _, e := range resp.Data {
		if !wantIDs[e.ID] {
			t.Errorf("unexpected id %q", e.ID)
		}
	}
}

func TestReasoningTokensFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "chat_response_o_series_with_reasoning_tokens.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var resp chatResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Usage == nil || resp.Usage.CompletionTokensDetails == nil {
		t.Fatalf("want completion_tokens_details, got %+v", resp.Usage)
	}
	if got := resp.Usage.CompletionTokensDetails.ReasoningTokens; got != 120 {
		t.Errorf("reasoning_tokens: got %d, want 120", got)
	}
}
