// Package claudecode parses the NDJSON stream produced by the Claude Code CLI
// when invoked with --output-format stream-json. It translates raw stream events
// into StepEvent values that match the audit trail content shapes written by
// the BoundAgent in internal/agent.
//
// This package imports only internal/model to stay below the agent package in
// the dependency graph and avoid circular imports.
package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/rapp992/gleipnir/internal/model"
)

// StepEvent is a single parsed event ready for the audit trail. The Content
// field matches the shape written by BoundAgent so the frontend renders Claude
// Code steps identically to native Anthropic API steps.
type StepEvent struct {
	Type      model.StepType
	Content   any // JSON-marshalled downstream; shape defined by Type
	TokenCost int
}

// rawEvent is the top-level JSON envelope for every NDJSON line. Claude Code
// wraps streaming API sub-events inside "assistant" lines, so we unmarshal
// the rest lazily via RawMessage.
type rawEvent struct {
	Type string          `json:"type"`
	Rest json.RawMessage `json:"-"` // populated by custom unmarshalling below
}

func (e *rawEvent) UnmarshalJSON(data []byte) error {
	// Two-pass: extract type first, keep the full payload for lazy decoding.
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	e.Type = env.Type
	e.Rest = data
	return nil
}

// blockAccumulator tracks an in-progress content block between
// content_block_start and content_block_stop events.
type blockAccumulator struct {
	blockIndex int
	blockType  string          // "text", "tool_use", or "thinking"
	toolName   string          // populated for tool_use blocks
	toolID     string          // populated for tool_use blocks
	textBuf    strings.Builder // accumulates delta text
}

// streamState holds mutable parser state across all events in one stream.
type streamState struct {
	// accumulators tracks open content blocks, keyed by block index.
	accumulators map[int]*blockAccumulator
	// toolIDToName resolves a tool_use_id back to the tool's name so that
	// tool_result events (which reference only an ID) can carry the name.
	toolIDToName map[string]string
	// pendingTokens accumulates output tokens from message_delta events.
	// They are attached to the next result event so they aren't lost.
	pendingTokens int
}

func newStreamState() *streamState {
	return &streamState{
		accumulators: make(map[int]*blockAccumulator),
		toolIDToName: make(map[string]string),
	}
}

// ParseStream reads NDJSON lines from r, parses Claude Code stream-json events,
// and sends StepEvent values on the returned channel. The goroutine exits and
// closes the channel when r reaches EOF, r errors, or ctx is cancelled.
//
// The returned channel has a buffer of 16 so the producer can stay ahead of a
// slow consumer without blocking the read loop.
func ParseStream(ctx context.Context, r io.Reader) <-chan StepEvent {
	ch := make(chan StepEvent, 16)

	go func() {
		defer close(ch)

		scanner := bufio.NewScanner(r)
		// Raise the default 64KB limit to 2MB. Claude Code tool results can be
		// large, and exceeding the default would silently terminate the scan.
		scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

		state := newStreamState()

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if !scanner.Scan() {
				return
			}

			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var ev rawEvent
			if err := json.Unmarshal(line, &ev); err != nil {
				// Truncate log output to keep warning messages readable.
				preview := string(line)
				if len(preview) > 200 {
					preview = preview[:200] + "..."
				}
				slog.WarnContext(ctx, "malformed NDJSON line", "err", err, "line", preview)
				continue
			}

			handleEvent(ctx, ev, state, ch)
		}
	}()

	return ch
}

// handleEvent dispatches one parsed event to the appropriate handler.
func handleEvent(ctx context.Context, ev rawEvent, state *streamState, ch chan<- StepEvent) {
	switch ev.Type {
	case "content_block_start":
		handleContentBlockStart(ctx, ev.Rest, state)
	case "content_block_delta":
		handleContentBlockDelta(ctx, ev.Rest, state)
	case "content_block_stop":
		handleContentBlockStop(ctx, ev.Rest, state, ch)
	case "message_delta":
		handleMessageDelta(ctx, ev.Rest, state)
	case "result":
		handleResult(ctx, ev.Rest, state, ch)
	case "system":
		handleSystem(ctx, ev.Rest, ch)
	case "assistant":
		handleAssistant(ctx, ev.Rest, state, ch)
	case "message_start", "message_stop":
		// Lifecycle markers with no audit content.
	default:
		slog.DebugContext(ctx, "skipping unknown stream event type", "type", ev.Type)
	}
}

// --- content_block_start ---

type contentBlockStartEvent struct {
	Index        int `json:"index"`
	ContentBlock struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Name  string `json:"name"`
	} `json:"content_block"`
}

func handleContentBlockStart(ctx context.Context, data json.RawMessage, state *streamState) {
	var ev contentBlockStartEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		slog.WarnContext(ctx, "failed to parse content_block_start", "err", err)
		return
	}

	acc := &blockAccumulator{
		blockIndex: ev.Index,
		blockType:  ev.ContentBlock.Type,
	}

	if ev.ContentBlock.Type == "tool_use" {
		acc.toolName = ev.ContentBlock.Name
		acc.toolID = ev.ContentBlock.ID
		// Register now so tool_result events that arrive before block_stop can resolve the name.
		state.toolIDToName[acc.toolID] = acc.toolName
	}

	state.accumulators[ev.Index] = acc
}

// --- content_block_delta ---

type contentBlockDeltaEvent struct {
	Index int `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
	} `json:"delta"`
}

func handleContentBlockDelta(ctx context.Context, data json.RawMessage, state *streamState) {
	var ev contentBlockDeltaEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		slog.WarnContext(ctx, "failed to parse content_block_delta", "err", err)
		return
	}

	acc, ok := state.accumulators[ev.Index]
	if !ok {
		slog.WarnContext(ctx, "content_block_delta for unknown block index", "index", ev.Index)
		return
	}

	switch ev.Delta.Type {
	case "text_delta":
		acc.textBuf.WriteString(ev.Delta.Text)
	case "input_json_delta":
		acc.textBuf.WriteString(ev.Delta.PartialJSON)
	case "thinking_delta":
		acc.textBuf.WriteString(ev.Delta.Thinking)
	default:
		slog.DebugContext(ctx, "skipping unknown delta type", "delta_type", ev.Delta.Type)
	}
}

// --- content_block_stop ---

type contentBlockStopEvent struct {
	Index int `json:"index"`
}

func handleContentBlockStop(ctx context.Context, data json.RawMessage, state *streamState, ch chan<- StepEvent) {
	var ev contentBlockStopEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		slog.WarnContext(ctx, "failed to parse content_block_stop", "err", err)
		return
	}

	acc, ok := state.accumulators[ev.Index]
	if !ok {
		slog.WarnContext(ctx, "content_block_stop for unknown block index", "index", ev.Index)
		return
	}
	delete(state.accumulators, ev.Index)

	accumulated := acc.textBuf.String()

	switch acc.blockType {
	case "text":
		if accumulated == "" {
			return
		}
		send(ctx, ch, StepEvent{
			Type:    model.StepTypeThought,
			Content: map[string]string{"text": accumulated},
		})

	case "thinking":
		send(ctx, ch, StepEvent{
			Type:    model.StepTypeThinking,
			Content: map[string]any{"text": accumulated, "redacted": false},
		})

	case "tool_use":
		input := parseToolInput(ctx, accumulated, acc.toolName)
		send(ctx, ch, StepEvent{
			Type: model.StepTypeToolCall,
			Content: map[string]any{
				"tool_name": acc.toolName,
				"server_id": "", // Claude Code does not expose MCP server identity
				"input":     input,
			},
		})
	}
}

// parseToolInput unmarshals accumulated JSON into a map. If the JSON is
// malformed it returns the raw string under an "input" key and logs a warning,
// so the audit trail still records what the agent attempted.
func parseToolInput(ctx context.Context, raw, toolName string) map[string]any {
	var input map[string]any
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		slog.WarnContext(ctx, "tool input JSON is malformed; storing raw string",
			"tool_name", toolName, "err", err)
		return map[string]any{"input": raw}
	}
	return input
}

// --- message_delta ---

type messageDeltaEvent struct {
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func handleMessageDelta(ctx context.Context, data json.RawMessage, state *streamState) {
	var ev messageDeltaEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		slog.WarnContext(ctx, "failed to parse message_delta", "err", err)
		return
	}
	// Accumulate instead of emitting a step: emitting message_delta as a step
	// would produce content that doesn't match any frontend content type.
	state.pendingTokens += ev.Usage.OutputTokens
}

// --- result ---

type resultEvent struct {
	Subtype string `json:"subtype"`
	Error   string `json:"error"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	CostUSD float64 `json:"cost_usd"`
}

func handleResult(ctx context.Context, data json.RawMessage, state *streamState, ch chan<- StepEvent) {
	var ev resultEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		slog.WarnContext(ctx, "failed to parse result event", "err", err)
		return
	}

	totalTokens := ev.Usage.InputTokens + ev.Usage.OutputTokens + state.pendingTokens
	state.pendingTokens = 0

	if ev.Subtype == "error" {
		msg := ev.Error
		if msg == "" {
			msg = "Claude Code exited with an error"
		}
		send(ctx, ch, StepEvent{
			Type: model.StepTypeError,
			Content: model.ErrorStepContent{
				Message: msg,
				Code:    model.ErrorCodeAPIError,
			},
			TokenCost: totalTokens,
		})
		return
	}

	send(ctx, ch, StepEvent{
		Type:      model.StepTypeComplete,
		Content:   map[string]string{"message": "run completed"},
		TokenCost: totalTokens,
	})
}

// --- system ---

type systemEvent struct {
	Subtype string `json:"subtype"`
	Details string `json:"details"`
	Error   string `json:"error"`
}

func handleSystem(ctx context.Context, data json.RawMessage, ch chan<- StepEvent) {
	var ev systemEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		slog.WarnContext(ctx, "failed to parse system event", "err", err)
		return
	}

	if ev.Subtype != "api_retry" {
		slog.DebugContext(ctx, "skipping system event", "subtype", ev.Subtype)
		return
	}

	details := ev.Details
	if details == "" {
		details = ev.Error
	}
	msg := "API retry"
	if details != "" {
		msg = fmt.Sprintf("API retry: %s", details)
	}

	send(ctx, ch, StepEvent{
		Type: model.StepTypeError,
		Content: model.ErrorStepContent{
			Message: msg,
			Code:    model.ErrorCodeAPIError,
		},
	})
}

// --- assistant ---

// assistantEvent wraps a full assistant turn which may contain content blocks
// of various types, including tool_result blocks that reference prior tool calls.
type assistantEvent struct {
	Message struct {
		Content []assistantContentBlock `json:"content"`
	} `json:"message"`
}

type assistantContentBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	Thinking   string          `json:"thinking"`
	ToolUseID  string          `json:"tool_use_id"`
	ToolCallID string          `json:"tool_call_id"` // alternate field name used by some versions
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input"`
	Content    json.RawMessage `json:"content"` // for tool_result: string or array
	IsError    bool            `json:"is_error"`
}

func handleAssistant(ctx context.Context, data json.RawMessage, state *streamState, ch chan<- StepEvent) {
	var ev assistantEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		slog.WarnContext(ctx, "failed to parse assistant event", "err", err)
		return
	}

	for _, block := range ev.Message.Content {
		switch block.Type {
		case "text":
			if block.Text == "" {
				continue
			}
			send(ctx, ch, StepEvent{
				Type:    model.StepTypeThought,
				Content: map[string]string{"text": block.Text},
			})

		case "thinking":
			send(ctx, ch, StepEvent{
				Type:    model.StepTypeThinking,
				Content: map[string]any{"text": block.Thinking, "redacted": false},
			})

		case "tool_use":
			// Register tool name for future tool_result resolution.
			if block.ID != "" && block.Name != "" {
				state.toolIDToName[block.ID] = block.Name
			}
			var input map[string]any
			if err := json.Unmarshal(block.Input, &input); err != nil {
				input = map[string]any{"input": string(block.Input)}
			}
			send(ctx, ch, StepEvent{
				Type: model.StepTypeToolCall,
				Content: map[string]any{
					"tool_name": block.Name,
					"server_id": "",
					"input":     input,
				},
			})

		case "tool_result":
			toolUseID := block.ToolUseID
			if toolUseID == "" {
				toolUseID = block.ToolCallID
			}
			toolName, ok := state.toolIDToName[toolUseID]
			if !ok {
				toolName = "unknown"
			}
			output := extractToolResultOutput(block.Content)
			send(ctx, ch, StepEvent{
				Type: model.StepTypeToolResult,
				Content: map[string]any{
					"tool_name": toolName,
					"output":    output,
					"is_error":  block.IsError,
				},
			})
		}
	}
}

// extractToolResultOutput coerces a tool_result content field to a plain string.
// The content may be a JSON string, a JSON array of content blocks, or absent.
func extractToolResultOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try plain string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Try array of content blocks — concatenate text fields.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}

	// Fall back to the raw JSON bytes as a string.
	return string(raw)
}

// send delivers a StepEvent on ch, abandoning the send if ctx is cancelled.
func send(ctx context.Context, ch chan<- StepEvent, ev StepEvent) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}
