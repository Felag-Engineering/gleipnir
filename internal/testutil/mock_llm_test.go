package testutil_test

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/testutil"
)

func TestMockLLMClient_ScriptedSequence(t *testing.T) {
	resp1 := testutil.MakeLLMTextResponse("first", llm.StopReasonEndTurn, 10, 5)
	resp2 := testutil.MakeLLMTextResponse("second", llm.StopReasonEndTurn, 8, 4)

	client := testutil.NewMockLLMClient(resp1, resp2)

	got1, err := client.CreateMessage(context.Background(), llm.MessageRequest{})
	if err != nil {
		t.Fatalf("call 1: unexpected error: %v", err)
	}
	if got1 != resp1 {
		t.Errorf("call 1: got %v, want %v", got1, resp1)
	}

	got2, err := client.CreateMessage(context.Background(), llm.MessageRequest{})
	if err != nil {
		t.Fatalf("call 2: unexpected error: %v", err)
	}
	if got2 != resp2 {
		t.Errorf("call 2: got %v, want %v", got2, resp2)
	}
}

func TestMockLLMClient_ExhaustionError(t *testing.T) {
	client := testutil.NewMockLLMClient(
		testutil.MakeLLMTextResponse("only one", llm.StopReasonEndTurn, 10, 5),
	)

	if _, err := client.CreateMessage(context.Background(), llm.MessageRequest{}); err != nil {
		t.Fatalf("call 1: unexpected error: %v", err)
	}

	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{})
	if err == nil {
		t.Fatal("call 2: expected error when responses exhausted, got nil")
	}
}

func TestMockLLMClient_RequestCapture(t *testing.T) {
	client := testutil.NewMockLLMClient(
		testutil.MakeLLMTextResponse("a", llm.StopReasonEndTurn, 10, 5),
		testutil.MakeLLMTextResponse("b", llm.StopReasonEndTurn, 10, 5),
	)

	req1 := llm.MessageRequest{SystemPrompt: "prompt-one"}
	req2 := llm.MessageRequest{SystemPrompt: "prompt-two"}

	if _, err := client.CreateMessage(context.Background(), req1); err != nil {
		t.Fatalf("call 1: %v", err)
	}
	if _, err := client.CreateMessage(context.Background(), req2); err != nil {
		t.Fatalf("call 2: %v", err)
	}

	reqs := client.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Requests() len = %d, want 2", len(reqs))
	}
	if reqs[0].SystemPrompt != "prompt-one" {
		t.Errorf("reqs[0].SystemPrompt = %q, want %q", reqs[0].SystemPrompt, "prompt-one")
	}
	if reqs[1].SystemPrompt != "prompt-two" {
		t.Errorf("reqs[1].SystemPrompt = %q, want %q", reqs[1].SystemPrompt, "prompt-two")
	}
}

func TestBlockingLLMClient_CancelledContext(t *testing.T) {
	client, transport := testutil.NewBlockingLLMClient()
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := client.CreateMessage(ctx, llm.MessageRequest{})
		errCh <- err
	}()

	// Wait until CreateMessage has been entered before cancelling.
	for transport.Calls() < 1 {
		runtime.Gosched()
	}
	cancel()

	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got error %v, want context.Canceled", err)
	}
}

func TestErrorLLMClient_AlwaysErrors(t *testing.T) {
	sentinel := errors.New("injected error")
	client := testutil.NewErrorLLMClient(sentinel)

	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{})
	if !errors.Is(err, sentinel) {
		t.Errorf("CreateMessage: got %v, want %v", err, sentinel)
	}

	_, err = client.StreamMessage(context.Background(), llm.MessageRequest{})
	if !errors.Is(err, sentinel) {
		t.Errorf("StreamMessage: got %v, want %v", err, sentinel)
	}
}

func TestMakeTextResponse(t *testing.T) {
	resp := testutil.MakeTextResponse("hello world")
	if len(resp.Text) == 0 || resp.Text[0].Text != "hello world" {
		t.Errorf("Text = %v, want [{hello world}]", resp.Text)
	}
	if resp.StopReason != llm.StopReasonEndTurn {
		t.Errorf("StopReason = %v, want EndTurn", resp.StopReason)
	}
	if resp.Usage.InputTokens == 0 || resp.Usage.OutputTokens == 0 {
		t.Errorf("Usage = %+v, want non-zero", resp.Usage)
	}
}

func TestMakeToolCallResponse(t *testing.T) {
	input := map[string]any{"key": "value"}
	resp := testutil.MakeToolCallResponse("my-server.do_thing", "tc-1", input)

	if len(resp.ToolCalls) == 0 {
		t.Fatal("ToolCalls is empty")
	}
	tc := resp.ToolCalls[0]
	if tc.Name != "my-server.do_thing" {
		t.Errorf("Name = %q, want %q", tc.Name, "my-server.do_thing")
	}
	if tc.ID != "tc-1" {
		t.Errorf("ID = %q, want %q", tc.ID, "tc-1")
	}
	if resp.StopReason != llm.StopReasonToolUse {
		t.Errorf("StopReason = %v, want ToolUse", resp.StopReason)
	}
}
