package openai

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openai/openai-go/responses"
	"github.com/rapp992/gleipnir/internal/llm"
)

// fakeStream implements streamIterator for tests.
type fakeStream struct {
	events []responses.ResponseStreamEventUnion
	idx    int
	err    error
}

func newFakeStream(events ...responses.ResponseStreamEventUnion) *fakeStream {
	return &fakeStream{events: events}
}

func (f *fakeStream) Next() bool {
	if f.err != nil || f.idx >= len(f.events) {
		return false
	}
	f.idx++
	return true
}

func (f *fakeStream) Current() responses.ResponseStreamEventUnion {
	return f.events[f.idx-1]
}

func (f *fakeStream) Err() error { return f.err }

// makeTextDeltaEvent builds a ResponseStreamEventUnion for a text delta.
func makeTextDeltaEvent(itemID, delta string) responses.ResponseStreamEventUnion {
	raw := json.RawMessage(`{"type":"response.output_text.delta","item_id":"` + itemID + `","output_index":0,"content_index":0,"delta":"` + delta + `","sequence_number":1,"logprobs":[]}`)
	var evt responses.ResponseStreamEventUnion
	_ = json.Unmarshal(raw, &evt)
	return evt
}

// makeToolArgsDeltaEvent builds an event for function call argument deltas.
func makeToolArgsDeltaEvent(itemID, delta string) responses.ResponseStreamEventUnion {
	raw := json.RawMessage(`{"type":"response.function_call_arguments.delta","item_id":"` + itemID + `","output_index":0,"delta":"` + delta + `","sequence_number":2}`)
	var evt responses.ResponseStreamEventUnion
	_ = json.Unmarshal(raw, &evt)
	return evt
}

// makeOutputItemDoneEvent builds a done event for a function_call item.
func makeOutputItemDoneEvent(callID, name, args string) responses.ResponseStreamEventUnion {
	payload := map[string]any{
		"type":            "response.output_item.done",
		"output_index":    0,
		"sequence_number": 3,
		"item": map[string]any{
			"id":        "fc_" + callID,
			"type":      "function_call",
			"call_id":   callID,
			"name":      name,
			"arguments": args,
			"status":    "completed",
		},
	}
	raw, _ := json.Marshal(payload)
	var evt responses.ResponseStreamEventUnion
	_ = json.Unmarshal(raw, &evt)
	return evt
}

// makeCompletedEvent builds a response.completed event.
func makeCompletedEvent(hasToolCall bool) responses.ResponseStreamEventUnion {
	output := `[]`
	if hasToolCall {
		output = `[{"id":"fc_001","type":"function_call","call_id":"call_001","name":"get_data","arguments":"{}","status":"completed"}]`
	} else {
		output = `[{"id":"msg_001","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done","annotations":[]}]}]`
	}
	raw := json.RawMessage(`{"type":"response.completed","sequence_number":10,"response":{"id":"resp_001","object":"response","created_at":1700000000,"model":"gpt-4o","status":"completed","output":` + output + `,"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`)
	var evt responses.ResponseStreamEventUnion
	_ = json.Unmarshal(raw, &evt)
	return evt
}

func collectChunks(t *testing.T, ch <-chan llm.MessageChunk) []llm.MessageChunk {
	t.Helper()
	var out []llm.MessageChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

func TestConsumeStream_TextDeltas(t *testing.T) {
	stream := newFakeStream(
		makeTextDeltaEvent("msg_001", "Hello"),
		makeTextDeltaEvent("msg_001", " world"),
		makeCompletedEvent(false),
	)

	out := make(chan llm.MessageChunk, 16)
	go consumeStream(context.Background(), stream, out, llm.ToolNameMapping{})
	chunks := collectChunks(t, out)

	var text string
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("unexpected error chunk: %v", c.Err)
		}
		if c.Text != nil {
			text += *c.Text
		}
	}
	if text != "Hello world" {
		t.Errorf("assembled text = %q; want %q", text, "Hello world")
	}
}

func TestConsumeStream_ToolCallEmitted(t *testing.T) {
	stream := newFakeStream(
		makeToolArgsDeltaEvent("fc_001", `{"city`),
		makeToolArgsDeltaEvent("fc_001", `":"SF"}`),
		makeOutputItemDoneEvent("call_abc", "get_weather", `{"city":"SF"}`),
		makeCompletedEvent(true),
	)

	out := make(chan llm.MessageChunk, 16)
	go consumeStream(context.Background(), stream, out, llm.ToolNameMapping{})
	chunks := collectChunks(t, out)

	var toolChunk *llm.ToolCallBlock
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("unexpected error chunk: %v", c.Err)
		}
		if c.ToolCall != nil {
			toolChunk = c.ToolCall
		}
	}
	if toolChunk == nil {
		t.Fatal("expected a tool call chunk")
	}
	if toolChunk.ID != "call_abc" {
		t.Errorf("call_id = %q; want call_abc", toolChunk.ID)
	}
	if toolChunk.Name != "get_weather" {
		t.Errorf("name = %q; want get_weather", toolChunk.Name)
	}
}

func TestConsumeStream_StopReasonEmitted(t *testing.T) {
	stream := newFakeStream(makeCompletedEvent(false))

	out := make(chan llm.MessageChunk, 16)
	go consumeStream(context.Background(), stream, out, llm.ToolNameMapping{})
	chunks := collectChunks(t, out)

	var sawStop bool
	for _, c := range chunks {
		if c.StopReason != nil {
			sawStop = true
			if *c.StopReason != llm.StopReasonEndTurn {
				t.Errorf("StopReason = %v; want EndTurn", *c.StopReason)
			}
		}
	}
	if !sawStop {
		t.Error("expected a stop chunk")
	}
}

func TestConsumeStream_UsageEmitted(t *testing.T) {
	stream := newFakeStream(makeCompletedEvent(false))

	out := make(chan llm.MessageChunk, 16)
	go consumeStream(context.Background(), stream, out, llm.ToolNameMapping{})
	chunks := collectChunks(t, out)

	var usage *llm.TokenUsage
	for _, c := range chunks {
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	if usage == nil {
		t.Fatal("expected usage chunk")
	}
	if usage.InputTokens != 5 || usage.OutputTokens != 3 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestConsumeStream_StreamError(t *testing.T) {
	stream := &fakeStream{err: errors.New("network error")}

	out := make(chan llm.MessageChunk, 16)
	go consumeStream(context.Background(), stream, out, llm.ToolNameMapping{})
	chunks := collectChunks(t, out)

	var sawErr bool
	for _, c := range chunks {
		if c.Err != nil {
			sawErr = true
		}
	}
	if !sawErr {
		t.Error("expected error chunk")
	}
}

func TestConsumeStream_ContextCancellation(t *testing.T) {
	// Pre-cancel the context. The select at the top of the for-loop in
	// consumeStream should detect it on the first iteration and emit an error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Provide one event so the loop body runs at least once and hits the select.
	stream := newFakeStream(makeTextDeltaEvent("msg_001", "hi"))

	out := make(chan llm.MessageChunk, 4)
	go consumeStream(ctx, stream, out, llm.ToolNameMapping{})

	var chunks []llm.MessageChunk
	for c := range out {
		chunks = append(chunks, c)
	}
	// We expect at least the cancellation error chunk.
	var sawCancel bool
	for _, c := range chunks {
		if errors.Is(c.Err, context.Canceled) {
			sawCancel = true
		}
	}
	if !sawCancel {
		t.Errorf("expected context.Canceled in chunks, got: %+v", chunks)
	}
}

func TestConsumeStream_ChannelClosedExactlyOnce(t *testing.T) {
	stream := newFakeStream(makeCompletedEvent(false))
	out := make(chan llm.MessageChunk, 16)
	go consumeStream(context.Background(), stream, out, llm.ToolNameMapping{})
	for range out {
	}
	// Second receive on closed channel must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("second receive panicked: %v", r)
		}
	}()
	_, ok := <-out
	if ok {
		t.Error("channel should be closed")
	}
}

// makeReasoningOutputItemDoneEvent builds a ResponseOutputItemDoneEvent with a
// reasoning item. Used to test that consumeStream emits ThinkingBlock chunks.
func makeReasoningOutputItemDoneEvent(id, summaryText, encryptedContent string) responses.ResponseStreamEventUnion {
	summary := ""
	if summaryText != "" {
		summary = `,"summary":[{"type":"summary_text","text":"` + summaryText + `"}]`
	} else {
		summary = `,"summary":[]`
	}
	encField := ""
	if encryptedContent != "" {
		encField = `,"encrypted_content":"` + encryptedContent + `"`
	}
	payload := `{"type":"response.output_item.done","output_index":0,"sequence_number":5,"item":{"id":"` + id + `","type":"reasoning","status":"completed"` + summary + encField + `}}`
	var evt responses.ResponseStreamEventUnion
	_ = json.Unmarshal([]byte(payload), &evt)
	return evt
}

func TestConsumeStream_ReasoningItemEmitted(t *testing.T) {
	stream := newFakeStream(
		makeReasoningOutputItemDoneEvent("rs_001", "I should reason about this", "enc_token_abc"),
		makeCompletedEvent(false),
	)

	out := make(chan llm.MessageChunk, 16)
	go consumeStream(context.Background(), stream, out, llm.ToolNameMapping{})
	chunks := collectChunks(t, out)

	var thinkChunk *llm.ThinkingBlock
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("unexpected error: %v", c.Err)
		}
		if c.Thinking != nil {
			thinkChunk = c.Thinking
		}
	}
	if thinkChunk == nil {
		t.Fatal("expected a thinking chunk from reasoning item")
	}
	var state openaiThinkingState
	if err := json.Unmarshal(thinkChunk.ProviderState, &state); err != nil {
		t.Fatalf("unmarshal ProviderState: %v", err)
	}
	if state.ID != "rs_001" {
		t.Errorf("state.ID = %q, want rs_001", state.ID)
	}
	if thinkChunk.Text != "I should reason about this" {
		t.Errorf("Text = %q, want 'I should reason about this'", thinkChunk.Text)
	}
	if state.EncryptedContent != "enc_token_abc" {
		t.Errorf("state.EncryptedContent = %q, want enc_token_abc", state.EncryptedContent)
	}
}

func TestConsumeStream_ReasoningItemNoEncryptedContent(t *testing.T) {
	// Even without encrypted_content, a reasoning item with summary text should
	// emit a ThinkingBlock chunk.
	stream := newFakeStream(
		makeReasoningOutputItemDoneEvent("rs_002", "summary only", ""),
		makeCompletedEvent(false),
	)

	out := make(chan llm.MessageChunk, 16)
	go consumeStream(context.Background(), stream, out, llm.ToolNameMapping{})
	chunks := collectChunks(t, out)

	var thinkChunk *llm.ThinkingBlock
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("unexpected error: %v", c.Err)
		}
		if c.Thinking != nil {
			thinkChunk = c.Thinking
		}
	}
	if thinkChunk == nil {
		t.Fatal("expected a thinking chunk from reasoning item with summary text")
	}
	if thinkChunk.Text != "summary only" {
		t.Errorf("Text = %q, want 'summary only'", thinkChunk.Text)
	}
	var state openaiThinkingState
	if err := json.Unmarshal(thinkChunk.ProviderState, &state); err != nil {
		t.Fatalf("unmarshal ProviderState: %v", err)
	}
	if state.EncryptedContent != "" {
		t.Errorf("state.EncryptedContent should be empty, got %q", state.EncryptedContent)
	}
}
