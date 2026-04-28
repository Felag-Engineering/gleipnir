package google

import (
	"context"
	"errors"
	"iter"
	"testing"

	"google.golang.org/genai"

	"github.com/felag-engineering/gleipnir/internal/llm"
)

// makeStreamSeq builds an iter.Seq2 directly from a slice of responses and an
// optional trailing error. Used in tests that call consumeStream directly.
func makeStreamSeq(responses []*genai.GenerateContentResponse, err error) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		for _, resp := range responses {
			if !yield(resp, nil) {
				return
			}
		}
		if err != nil {
			yield(nil, err)
		}
	}
}

// drainChunks collects all chunks from a channel.
func drainChunks(t *testing.T, ch <-chan llm.MessageChunk) []llm.MessageChunk {
	t.Helper()
	var out []llm.MessageChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

// emptyNames returns a ToolNameMapping with empty maps (no name remapping).
func emptyNames() llm.ToolNameMapping {
	return llm.ToolNameMapping{
		SanitizedToOriginal: map[string]string{},
		OriginalToSanitized: map[string]string{},
	}
}

func TestConsumeStream_MultipleTextChunks(t *testing.T) {
	seq := makeStreamSeq([]*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "Hello"}}}}}},
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: " World"}}}}}},
		{
			Candidates: []*genai.Candidate{{
				Content:      &genai.Content{Parts: []*genai.Part{{Text: "!"}}},
				FinishReason: genai.FinishReasonStop,
			}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     10,
				CandidatesTokenCount: 5,
			},
		},
	}, nil)

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), seq, out, emptyNames())
	chunks := drainChunks(t, out)

	// Expect: 3 text chunks + 1 final chunk.
	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %d", len(chunks))
	}
	wantTexts := []string{"Hello", " World", "!"}
	for i, want := range wantTexts {
		if chunks[i].Text == nil || *chunks[i].Text != want {
			t.Errorf("chunks[%d].Text = %v, want %q", i, chunks[i].Text, want)
		}
	}

	final := chunks[3]
	if final.StopReason == nil || *final.StopReason != llm.StopReasonEndTurn {
		t.Errorf("final.StopReason = %v, want EndTurn", final.StopReason)
	}
	if final.Usage == nil || final.Usage.InputTokens != 10 || final.Usage.OutputTokens != 5 {
		t.Errorf("final.Usage = %v, want {10, 5}", final.Usage)
	}
}

func TestConsumeStream_TextThenToolCall(t *testing.T) {
	thoughtSig := []byte("sig-bytes")
	seq := makeStreamSeq([]*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "thinking"}}}}}},
		{
			Candidates: []*genai.Candidate{{
				Content: &genai.Content{Parts: []*genai.Part{
					{
						FunctionCall:     &genai.FunctionCall{ID: "fc-1", Name: "fs_read", Args: map[string]any{"path": "/tmp"}},
						ThoughtSignature: thoughtSig,
					},
				}},
				FinishReason: genai.FinishReasonStop,
			}},
		},
	}, nil)

	names := llm.ToolNameMapping{
		SanitizedToOriginal: map[string]string{"fs_read": "fs.read"},
		OriginalToSanitized: map[string]string{"fs.read": "fs_read"},
	}

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), seq, out, names)
	chunks := drainChunks(t, out)

	// Expect: 1 text + 1 tool call + 1 final.
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	if chunks[0].Text == nil || *chunks[0].Text != "thinking" {
		t.Errorf("chunks[0].Text = %v, want %q", chunks[0].Text, "thinking")
	}

	tc := chunks[1].ToolCall
	if tc == nil {
		t.Fatal("chunks[1].ToolCall is nil")
	}
	if tc.Name != "fs.read" {
		t.Errorf("ToolCall.Name = %q, want %q", tc.Name, "fs.read")
	}
	if string(tc.ProviderMetadata["google.thought_signature"]) != string(thoughtSig) {
		t.Errorf("ProviderMetadata[google.thought_signature] = %v, want %v", tc.ProviderMetadata["google.thought_signature"], thoughtSig)
	}

	final := chunks[2]
	if final.StopReason == nil || *final.StopReason != llm.StopReasonToolUse {
		t.Errorf("final.StopReason = %v, want ToolUse", final.StopReason)
	}
}

func TestConsumeStream_ThinkingPart(t *testing.T) {
	seq := makeStreamSeq([]*genai.GenerateContentResponse{
		{
			Candidates: []*genai.Candidate{{
				Content: &genai.Content{Parts: []*genai.Part{
					{Text: "my reasoning", Thought: true},
				}},
				FinishReason: genai.FinishReasonStop,
			}},
		},
	}, nil)

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), seq, out, emptyNames())
	chunks := drainChunks(t, out)

	// Expect: 1 thinking chunk + 1 final chunk.
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}

	thinking := chunks[0].Thinking
	if thinking == nil {
		t.Fatal("chunks[0].Thinking is nil")
	}
	if thinking.Provider != "google" {
		t.Errorf("Thinking.Provider = %q, want %q", thinking.Provider, "google")
	}
	if thinking.Text != "my reasoning" {
		t.Errorf("Thinking.Text = %q, want %q", thinking.Text, "my reasoning")
	}
	if thinking.Redacted {
		t.Error("Thinking.Redacted = true, want false")
	}
}

func TestConsumeStream_ThinkingPartEmptyTextSkipped(t *testing.T) {
	// Gemini routinely emits Thought-flagged parts with empty Text around
	// tool calls. These must not surface as empty Thinking chunks in the
	// audit trail.
	seq := makeStreamSeq([]*genai.GenerateContentResponse{
		{
			Candidates: []*genai.Candidate{{
				Content: &genai.Content{Parts: []*genai.Part{
					{Text: "", Thought: true},
					{FunctionCall: &genai.FunctionCall{ID: "fc-1", Name: "noop", Args: nil}},
					{Text: "", Thought: true},
				}},
				FinishReason: genai.FinishReasonStop,
			}},
		},
	}, nil)

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), seq, out, emptyNames())
	chunks := drainChunks(t, out)

	for i, c := range chunks {
		if c.Thinking != nil {
			t.Errorf("chunks[%d] is an empty Thinking chunk; expected to be skipped", i)
		}
	}

	// Expect: 1 tool call + 1 final chunk (both empty thoughts dropped).
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0].ToolCall == nil {
		t.Errorf("chunks[0].ToolCall is nil; want tool call")
	}
}

func TestConsumeStream_FunctionCallEmptyID(t *testing.T) {
	seq := makeStreamSeq([]*genai.GenerateContentResponse{
		{
			Candidates: []*genai.Candidate{{
				Content: &genai.Content{Parts: []*genai.Part{
					// ID is empty — buildToolCallBlockFromPart must generate a UUID.
					{FunctionCall: &genai.FunctionCall{ID: "", Name: "noop", Args: nil}},
				}},
				FinishReason: genai.FinishReasonStop,
			}},
		},
	}, nil)

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), seq, out, emptyNames())
	chunks := drainChunks(t, out)

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	tc := chunks[0].ToolCall
	if tc == nil {
		t.Fatal("chunks[0].ToolCall is nil")
	}
	if tc.ID == "" {
		t.Error("ToolCallBlock.ID is empty; expected a generated UUID")
	}
}

func TestConsumeStream_FinishReasonMaxTokens(t *testing.T) {
	seq := makeStreamSeq([]*genai.GenerateContentResponse{
		{
			Candidates: []*genai.Candidate{{
				Content:      &genai.Content{Parts: []*genai.Part{{Text: "truncated"}}},
				FinishReason: genai.FinishReasonMaxTokens,
			}},
		},
	}, nil)

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), seq, out, emptyNames())
	chunks := drainChunks(t, out)

	final := chunks[len(chunks)-1]
	if final.StopReason == nil || *final.StopReason != llm.StopReasonMaxTokens {
		t.Errorf("final.StopReason = %v, want MaxTokens", final.StopReason)
	}
}

func TestConsumeStream_FinishReasonSafety(t *testing.T) {
	seq := makeStreamSeq([]*genai.GenerateContentResponse{
		{
			Candidates: []*genai.Candidate{{
				Content:      &genai.Content{Parts: []*genai.Part{{Text: "blocked"}}},
				FinishReason: genai.FinishReasonSafety,
			}},
		},
	}, nil)

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), seq, out, emptyNames())
	chunks := drainChunks(t, out)

	final := chunks[len(chunks)-1]
	if final.StopReason == nil || *final.StopReason != llm.StopReasonError {
		t.Errorf("final.StopReason = %v, want Error", final.StopReason)
	}
}

func TestConsumeStream_StreamError(t *testing.T) {
	streamErr := errors.New("boom")
	seq := makeStreamSeq(nil, streamErr)

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), seq, out, emptyNames())
	chunks := drainChunks(t, out)

	// The final chunk should carry the wrapped error.
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk (the error chunk), got none")
	}
	last := chunks[len(chunks)-1]
	if last.Err == nil {
		t.Fatal("expected final chunk to have Err, got nil")
	}
	if !errors.Is(last.Err, streamErr) {
		t.Errorf("Err = %v, want to wrap %v", last.Err, streamErr)
	}
}

func TestConsumeStream_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	seq := makeStreamSeq([]*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "hi"}}}}}},
	}, nil)

	out := make(chan llm.MessageChunk, 32)
	consumeStream(ctx, seq, out, emptyNames())
	chunks := drainChunks(t, out)

	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk (the error chunk), got none")
	}
	if chunks[0].Err == nil {
		t.Fatal("expected Err chunk on cancelled context, got nil Err")
	}
	if !errors.Is(chunks[0].Err, context.Canceled) {
		t.Errorf("Err = %v, want context.Canceled", chunks[0].Err)
	}
}

func TestConsumeStream_ChannelClosed(t *testing.T) {
	seq := makeStreamSeq([]*genai.GenerateContentResponse{
		{
			Candidates: []*genai.Candidate{{
				Content:      &genai.Content{Parts: []*genai.Part{{Text: "done"}}},
				FinishReason: genai.FinishReasonStop,
			}},
		},
	}, nil)

	out := make(chan llm.MessageChunk, 32)
	consumeStream(context.Background(), seq, out, emptyNames())

	// Drain all chunks.
	for range out {
	}

	// Channel must be closed after drain.
	_, ok := <-out
	if ok {
		t.Error("expected channel to be closed, but received a value")
	}
}
