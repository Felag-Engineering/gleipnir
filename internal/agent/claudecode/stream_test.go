package claudecode

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
)

// drain collects all events from ch until it is closed, with a 2-second timeout
// to prevent tests from hanging on bugs that leave the channel open.
func drain(t *testing.T, ch <-chan StepEvent) []StepEvent {
	t.Helper()
	var events []StepEvent
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-deadline:
			t.Fatal("drain timed out waiting for channel to close")
			return events
		}
	}
}

// contentMap asserts that ev.Content is a map and returns it.
func contentMap(t *testing.T, ev StepEvent) map[string]any {
	t.Helper()
	m, ok := ev.Content.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any content, got %T: %v", ev.Content, ev.Content)
	}
	return m
}

// strField returns the string value of key in m, failing the test if absent or wrong type.
func strField(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("content map missing key %q; map: %v", key, m)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("content[%q] expected string, got %T: %v", key, v, v)
	}
	return s
}

// TestParseStream_SingleEvents covers the standard event types individually.
func TestParseStream_SingleEvents(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		assert func(t *testing.T, events []StepEvent)
	}{
		{
			name: "text_delta accumulation",
			input: lines(
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","id":"","name":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello, "}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
				`{"type":"content_block_stop","index":0}`,
			),
			assert: func(t *testing.T, events []StepEvent) {
				if len(events) != 1 {
					t.Fatalf("want 1 event, got %d", len(events))
				}
				ev := events[0]
				if ev.Type != model.StepTypeThought {
					t.Errorf("want StepTypeThought, got %s", ev.Type)
				}
				m, ok := ev.Content.(map[string]string)
				if !ok {
					t.Fatalf("content should be map[string]string, got %T", ev.Content)
				}
				if m["text"] != "Hello, world" {
					t.Errorf("want %q, got %q", "Hello, world", m["text"])
				}
			},
		},
		{
			name: "tool_use lifecycle",
			input: lines(
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"my_tool"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"key\":"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"val\""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"}"}}`,
				`{"type":"content_block_stop","index":0}`,
			),
			assert: func(t *testing.T, events []StepEvent) {
				if len(events) != 1 {
					t.Fatalf("want 1 event, got %d", len(events))
				}
				ev := events[0]
				if ev.Type != model.StepTypeToolCall {
					t.Errorf("want StepTypeToolCall, got %s", ev.Type)
				}
				m := contentMap(t, ev)
				if strField(t, m, "tool_name") != "my_tool" {
					t.Errorf("tool_name mismatch")
				}
				if strField(t, m, "server_id") != "" {
					t.Errorf("server_id should be empty string for Claude Code")
				}
				input, ok := m["input"].(map[string]any)
				if !ok {
					t.Fatalf("input should be map[string]any, got %T", m["input"])
				}
				if input["key"] != "val" {
					t.Errorf("parsed input[key] want %q, got %v", "val", input["key"])
				}
			},
		},
		{
			name: "thinking block",
			input: lines(
				`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","id":"","name":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"deep thought"}}`,
				`{"type":"content_block_stop","index":0}`,
			),
			assert: func(t *testing.T, events []StepEvent) {
				if len(events) != 1 {
					t.Fatalf("want 1 event, got %d", len(events))
				}
				ev := events[0]
				if ev.Type != model.StepTypeThinking {
					t.Errorf("want StepTypeThinking, got %s", ev.Type)
				}
				m := contentMap(t, ev)
				if strField(t, m, "text") != "deep thought" {
					t.Errorf("thinking text mismatch")
				}
				redacted, ok := m["redacted"].(bool)
				if !ok || redacted {
					t.Errorf("redacted should be false bool, got %v (%T)", m["redacted"], m["redacted"])
				}
			},
		},
		{
			name:  "result event success",
			input: `{"type":"result","subtype":"success","usage":{"input_tokens":100,"output_tokens":200},"cost_usd":0.01}`,
			assert: func(t *testing.T, events []StepEvent) {
				if len(events) != 1 {
					t.Fatalf("want 1 event, got %d", len(events))
				}
				ev := events[0]
				if ev.Type != model.StepTypeComplete {
					t.Errorf("want StepTypeComplete, got %s", ev.Type)
				}
				m, ok := ev.Content.(map[string]string)
				if !ok {
					t.Fatalf("content should be map[string]string, got %T", ev.Content)
				}
				if m["message"] == "" {
					t.Error("message should not be empty")
				}
				if ev.TokenCost != 300 {
					t.Errorf("want TokenCost=300, got %d", ev.TokenCost)
				}
			},
		},
		{
			name:  "result event error",
			input: `{"type":"result","subtype":"error","error":"context deadline exceeded","usage":{"input_tokens":0,"output_tokens":0}}`,
			assert: func(t *testing.T, events []StepEvent) {
				if len(events) != 1 {
					t.Fatalf("want 1 event, got %d", len(events))
				}
				ev := events[0]
				if ev.Type != model.StepTypeError {
					t.Errorf("want StepTypeError, got %s", ev.Type)
				}
				content, ok := ev.Content.(model.ErrorStepContent)
				if !ok {
					t.Fatalf("content should be model.ErrorStepContent, got %T", ev.Content)
				}
				if content.Message == "" {
					t.Error("message should not be empty")
				}
				if content.Code == "" {
					t.Error("code should not be empty")
				}
			},
		},
		{
			name:  "system api_retry",
			input: `{"type":"system","subtype":"api_retry","details":"rate limit hit"}`,
			assert: func(t *testing.T, events []StepEvent) {
				if len(events) != 1 {
					t.Fatalf("want 1 event, got %d", len(events))
				}
				ev := events[0]
				if ev.Type != model.StepTypeError {
					t.Errorf("want StepTypeError, got %s", ev.Type)
				}
				content, ok := ev.Content.(model.ErrorStepContent)
				if !ok {
					t.Fatalf("content should be model.ErrorStepContent, got %T", ev.Content)
				}
				if !strings.Contains(strings.ToLower(content.Message), "retry") {
					t.Errorf("message should contain 'retry', got: %s", content.Message)
				}
				if content.Code == "" {
					t.Error("code should not be empty")
				}
			},
		},
		{
			name: "assistant with tool_result",
			// First register the tool name via content_block_start, then deliver the assistant event.
			input: lines(
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu1","name":"read_file"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"assistant","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":"file contents here","is_error":false}]}}`,
			),
			assert: func(t *testing.T, events []StepEvent) {
				// Two events: one empty tool_call from the block_stop (tool_use with empty input)
				// and one tool_result from the assistant event.
				var toolResultEvents []StepEvent
				for _, ev := range events {
					if ev.Type == model.StepTypeToolResult {
						toolResultEvents = append(toolResultEvents, ev)
					}
				}
				if len(toolResultEvents) != 1 {
					t.Fatalf("want 1 tool_result event, got %d total events: %v", len(toolResultEvents), events)
				}
				ev := toolResultEvents[0]
				m := contentMap(t, ev)
				if strField(t, m, "tool_name") != "read_file" {
					t.Errorf("tool_name mismatch")
				}
				if strField(t, m, "output") != "file contents here" {
					t.Errorf("output mismatch: %s", strField(t, m, "output"))
				}
				isError, ok := m["is_error"].(bool)
				if !ok {
					t.Fatalf("is_error should be bool, got %T", m["is_error"])
				}
				if isError {
					t.Error("is_error should be false")
				}
			},
		},
		{
			name:  "message_delta does not emit step",
			input: `{"type":"message_delta","usage":{"output_tokens":150}}`,
			assert: func(t *testing.T, events []StepEvent) {
				if len(events) != 0 {
					t.Errorf("message_delta should not emit steps, got %d", len(events))
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := ParseStream(context.Background(), strings.NewReader(tc.input))
			events := drain(t, ch)
			tc.assert(t, events)
		})
	}
}

func TestParseStream_MalformedLine(t *testing.T) {
	// A bad JSON line followed by a valid event sequence. The parser must
	// skip the bad line and still emit the event from the valid sequence.
	input := lines(
		`not json at all`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":0}`,
	)

	ch := ParseStream(context.Background(), strings.NewReader(input))
	events := drain(t, ch)

	if len(events) != 1 {
		t.Fatalf("want 1 event after malformed line, got %d", len(events))
	}
	if events[0].Type != model.StepTypeThought {
		t.Errorf("want StepTypeThought, got %s", events[0].Type)
	}
}

func TestParseStream_ContextCancellation(t *testing.T) {
	// The read end of an io.Pipe blocks until data is written or the pipe is closed.
	// Cancel the context immediately and verify the channel closes promptly.
	pr, _ := io.Pipe()
	defer pr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before any reads happen

	ch := ParseStream(ctx, pr)

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed, got an event")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("channel did not close within 1 second after context cancellation")
	}
}

func TestParseStream_UnknownEventType(t *testing.T) {
	input := `{"type":"unknown_future_event","data":"foo"}` + "\n"
	ch := ParseStream(context.Background(), strings.NewReader(input))
	events := drain(t, ch)

	if len(events) != 0 {
		t.Errorf("unknown event type should produce 0 events, got %d", len(events))
	}
}

func TestParseStream_EmptyInput(t *testing.T) {
	ch := ParseStream(context.Background(), strings.NewReader(""))
	events := drain(t, ch)

	if len(events) != 0 {
		t.Errorf("empty input should produce 0 events, got %d", len(events))
	}
}

func TestParseStream_MultipleBlocks(t *testing.T) {
	// Block 0 (text) starts, then block 1 (tool_use) starts, then block 0 stops,
	// then block 1 stops. Verifies the accumulator map handles interleaving.
	input := lines(
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu2","name":"write_file"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"thinking..."}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/tmp/out\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_stop","index":1}`,
	)

	ch := ParseStream(context.Background(), strings.NewReader(input))
	events := drain(t, ch)

	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d: %v", len(events), events)
	}

	// First stop (index 0) produces thought; second stop (index 1) produces tool_call.
	if events[0].Type != model.StepTypeThought {
		t.Errorf("first event should be thought, got %s", events[0].Type)
	}
	if events[1].Type != model.StepTypeToolCall {
		t.Errorf("second event should be tool_call, got %s", events[1].Type)
	}

	// Verify tool_call content shape.
	m := contentMap(t, events[1])
	if strField(t, m, "tool_name") != "write_file" {
		t.Errorf("tool_name mismatch")
	}
	if strField(t, m, "server_id") != "" {
		t.Errorf("server_id should be empty for Claude Code")
	}
}

func TestParseStream_LargeLine(t *testing.T) {
	// Generate >64KB of text to verify the 2MB scanner buffer handles it.
	bigText := strings.Repeat("x", 80*1024)

	// We need to embed the large text inside a JSON string literal — escape it.
	// Since bigText contains only 'x' chars, no escaping is needed.
	input := lines(
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+bigText+`"}}`,
		`{"type":"content_block_stop","index":0}`,
	)

	ch := ParseStream(context.Background(), strings.NewReader(input))
	events := drain(t, ch)

	if len(events) != 1 {
		t.Fatalf("large line: want 1 event, got %d", len(events))
	}
	m, ok := events[0].Content.(map[string]string)
	if !ok {
		t.Fatalf("content should be map[string]string, got %T", events[0].Content)
	}
	if len(m["text"]) != len(bigText) {
		t.Errorf("text length: want %d, got %d", len(bigText), len(m["text"]))
	}
}

func TestParseStream_MessageDeltaTokensAttachToResult(t *testing.T) {
	// message_delta contributes 100 tokens; result contributes 50 more.
	// The single StepTypeComplete event should carry the total 150.
	input := lines(
		`{"type":"message_delta","usage":{"output_tokens":100}}`,
		`{"type":"result","subtype":"success","usage":{"input_tokens":0,"output_tokens":50},"cost_usd":0.001}`,
	)

	ch := ParseStream(context.Background(), strings.NewReader(input))
	events := drain(t, ch)

	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].Type != model.StepTypeComplete {
		t.Errorf("want StepTypeComplete, got %s", events[0].Type)
	}
	if events[0].TokenCost != 150 {
		t.Errorf("want TokenCost=150, got %d", events[0].TokenCost)
	}
}

func TestParseStream_ToolResultUnknownToolID(t *testing.T) {
	// An assistant event referencing a tool_use_id that was never registered.
	// The tool_name should fall back to "unknown".
	input := `{"type":"assistant","message":{"content":[{"type":"tool_result","tool_use_id":"nope","content":"output","is_error":false}]}}` + "\n"

	ch := ParseStream(context.Background(), strings.NewReader(input))
	events := drain(t, ch)

	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	m := contentMap(t, events[0])
	if strField(t, m, "tool_name") != "unknown" {
		t.Errorf("unresolved tool_id should fall back to 'unknown', got %q", strField(t, m, "tool_name"))
	}
}

// lines joins NDJSON lines with newline separators.
func lines(ll ...string) string {
	return strings.Join(ll, "\n") + "\n"
}
