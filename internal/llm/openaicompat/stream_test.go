package openaicompat

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/llm"
)

func loadStreamFixture(t *testing.T, name string) io.ReadCloser {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return io.NopCloser(strings.NewReader(string(raw)))
}

func collectChunks(t *testing.T, ch <-chan llm.MessageChunk) []llm.MessageChunk {
	t.Helper()
	var out []llm.MessageChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

func TestParseSSEStream_TextOnly(t *testing.T) {
	body := loadStreamFixture(t, "stream_chunks_text.txt")
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch, llm.ToolNameMapping{})
	chunks := collectChunks(t, ch)

	var text strings.Builder
	var sawStop bool
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("unexpected error chunk: %v", c.Err)
		}
		if c.Text != nil {
			text.WriteString(*c.Text)
		}
		if c.StopReason != nil {
			sawStop = true
			if *c.StopReason != llm.StopReasonEndTurn {
				t.Errorf("stop reason: got %v, want EndTurn", *c.StopReason)
			}
		}
	}
	if text.String() != "Hello world" {
		t.Errorf("assembled text: %q", text.String())
	}
	if !sawStop {
		t.Error("no stop chunk emitted")
	}
}

func TestParseSSEStream_ToolCallsAreEmittedComplete(t *testing.T) {
	body := loadStreamFixture(t, "stream_chunks_with_tool_calls.txt")
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch, llm.ToolNameMapping{})
	chunks := collectChunks(t, ch)

	var toolCallChunks int
	var finalCall *llm.ToolCallBlock
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("unexpected error: %v", c.Err)
		}
		if c.ToolCall != nil {
			toolCallChunks++
			finalCall = c.ToolCall
		}
	}
	if toolCallChunks != 1 {
		t.Fatalf("want exactly 1 tool-call chunk, got %d", toolCallChunks)
	}
	if finalCall.ID != "call_abc" {
		t.Errorf("id: %q", finalCall.ID)
	}
	if finalCall.Name != "get_weather" {
		t.Errorf("name: %q", finalCall.Name)
	}
	if string(finalCall.Input) != `{"city":"SF"}` {
		t.Errorf("arguments not reassembled: %q", finalCall.Input)
	}
}

func TestParseSSEStream_UsageChunkPopulatesFinalUsage(t *testing.T) {
	body := loadStreamFixture(t, "stream_chunks_with_usage.txt")
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch, llm.ToolNameMapping{})
	chunks := collectChunks(t, ch)

	var gotUsage *llm.TokenUsage
	for _, c := range chunks {
		if c.Usage != nil {
			gotUsage = c.Usage
		}
	}
	if gotUsage == nil {
		t.Fatal("no usage chunk emitted")
	}
	if gotUsage.InputTokens != 7 || gotUsage.OutputTokens != 1 {
		t.Errorf("usage: %+v", gotUsage)
	}
}

func TestParseSSEStream_NoDoneTerminatorIsError(t *testing.T) {
	body := io.NopCloser(strings.NewReader(
		`data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n",
	))
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch, llm.ToolNameMapping{})
	chunks := collectChunks(t, ch)

	// Stream ends without [DONE] → final chunk should carry an error.
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	last := chunks[len(chunks)-1]
	if last.Err == nil {
		t.Errorf("expected error on incomplete stream, got %+v", last)
	}
}

func TestParseSSEStream_MalformedJSONIsError(t *testing.T) {
	body := io.NopCloser(strings.NewReader(
		`data: {not-valid-json` + "\n\n" + `data: [DONE]` + "\n\n",
	))
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch, llm.ToolNameMapping{})
	chunks := collectChunks(t, ch)
	var sawErr bool
	for _, c := range chunks {
		if c.Err != nil {
			sawErr = true
			break
		}
	}
	if !sawErr {
		t.Error("expected error chunk for malformed JSON")
	}
}

func TestParseSSEStream_ContextCancellation(t *testing.T) {
	// A slow reader that blocks until its context is cancelled or Close is called.
	// The Read method selects on ctx.Done() so that context cancellation unblocks
	// the scanner even before body.Close() runs — without this, scanner.Scan()
	// would block forever and parseSSEStream would never reach the ctx.Done()
	// check at the top of the loop. (Deviation from plan noted in implementation
	// summary.)
	ctx, cancel := context.WithCancel(context.Background())
	slow := &blockingReader{done: ctx.Done()}
	out := make(chan llm.MessageChunk, 4)

	go parseSSEStream(ctx, slow, out, llm.ToolNameMapping{})
	cancel()

	// Expect the channel to close; expect a context-related error on the last chunk.
	var chunks []llm.MessageChunk
	for c := range out {
		chunks = append(chunks, c)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk (the cancellation error)")
	}
	last := chunks[len(chunks)-1]
	if !errors.Is(last.Err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", last.Err)
	}
}

// blockingReader.Read blocks until done is closed (e.g. ctx cancellation)
// or Close is called. This ensures context cancellation unblocks the scanner
// without relying on body.Close() being called first.
type blockingReader struct {
	done <-chan struct{}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.done
	return 0, io.EOF
}

func (b *blockingReader) Close() error {
	return nil
}

func TestParseSSEStream_ChannelClosedExactlyOnce(t *testing.T) {
	body := loadStreamFixture(t, "stream_chunks_text.txt")
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch, llm.ToolNameMapping{})
	for range ch {
	}
	// Second receive on a closed channel must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("second receive panicked: %v", r)
		}
	}()
	_, ok := <-ch
	if ok {
		t.Error("channel should be closed")
	}
}
