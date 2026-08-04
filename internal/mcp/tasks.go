// Tasks-extension client: tasks/get, tasks/update, tasks/cancel
// (io.modelcontextprotocol/tasks, SEP-2663, spec §6.4). A durable task is how
// both a long-running MRTR tool call and a channel Request resolve — Amendment
// 1 folded channel Request onto the same task lifecycle — so nothing here is
// tool-call-specific; the caller addresses a task purely by its server-issued
// taskId.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const (
	methodTasksGet    = "tasks/get"
	methodTasksUpdate = "tasks/update"
	methodTasksCancel = "tasks/cancel"
)

// TaskStatusValue is a Tasks-extension task's status (spec §6.4, SEP-2663).
// Unlike ToolResult.ResultType (client.go), this package DOES compare against
// every value here: PollScheduler branches on Terminal() to decide whether a
// task is done polling, and on the specific value to pick the mcp_tasks
// terminal status to persist.
type TaskStatusValue string

const (
	TaskStatusWorking       TaskStatusValue = "working"
	TaskStatusInputRequired TaskStatusValue = "input_required"
	TaskStatusCompleted     TaskStatusValue = "completed"
	TaskStatusFailed        TaskStatusValue = "failed"
	TaskStatusCancelled     TaskStatusValue = "cancelled"
)

// Terminal reports whether s is one of the three states a Tasks-extension
// task cannot leave once reached.
func (s TaskStatusValue) Terminal() bool {
	switch s {
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
		return true
	default:
		return false
	}
}

// maxTaskStatusLen bounds a server-controlled status string before it is
// stored in a TaskStatus — same bounded-untrusted-value discipline as
// maxResultTypeLen (client.go). Every value this package recognizes is well
// under this, so it is headroom, not a realistic limit: an unrecognized value
// of sane length still passes through verbatim rather than being rejected —
// PollScheduler treats an unrecognized non-terminal-shaped value as "not yet
// terminal, keep polling" rather than failing the task outright.
const maxTaskStatusLen = 32

// maxTaskStatusMessageLen bounds a server-controlled statusMessage string —
// same discipline as maxServerInfoFieldLen (meta.go), sized generously enough
// for a real human-readable failure reason (TaskFailedError.Message reads
// from this field).
const maxTaskStatusMessageLen = 256

// minTaskPollInterval floors a server-declared pollIntervalMs so a
// misconfigured or hostile server cannot make PollScheduler hammer it in a
// tight loop; 0 (poll immediately) still floors up to this value rather than
// being honored literally.
const minTaskPollInterval = 1 * time.Second

// maxTaskPollInterval ceilings a server-declared pollIntervalMs. Generous
// relative to defaultTaskPollInterval (tasks_scheduler.go) — this only
// protects against an absurd or overflowing int64 ms value, the same overflow
// defense maxCacheHintTTL documents (cache.go), not a product decision about
// how slow polling should realistically be.
const maxTaskPollInterval = 10 * time.Minute

// maxTaskTTL ceilings a server-declared ttlMs. A human-in-the-loop wait
// legitimately runs much longer than a tool-catalog cache entry (cf.
// maxCacheHintTTL's 60s), so this is set generously (24h) — it exists purely
// to prevent the ms→time.Duration overflow an unbounded hostile int64 could
// cause, not to second-guess a server's real TTL policy.
const maxTaskTTL = 24 * time.Hour

// TaskStatus is the decoded response of tasks/get, tasks/update, or
// tasks/cancel (spec §6.4, SEP-2663).
type TaskStatus struct {
	// TaskID echoes the server's task identifier.
	TaskID string

	// Status is the task's current status, bounded to maxTaskStatusLen.
	// Unrecognized values pass through verbatim rather than being rejected —
	// only a missing/malformed status (which decodeTaskStatusValue reports as
	// "") fails the call; see callTasksMethod.
	Status TaskStatusValue

	// StatusMessage is the server's untrusted, human-readable explanation of
	// Status — most useful when Status is TaskStatusFailed. Bounded to
	// maxTaskStatusMessageLen; empty when the server sent none.
	StatusMessage string

	// Result is the task's terminal payload, present once Status.Terminal()
	// (absent/nil on a "working"/"input_required" poll). Never interpreted by
	// this package.
	Result json.RawMessage

	// PollInterval is the server-declared poll cadence (spec §6.4 "honor
	// server-declared poll interval"), clamped to
	// [minTaskPollInterval, maxTaskPollInterval]. Zero means the server sent
	// no hint — the caller falls back to its own default.
	PollInterval time.Duration

	// TTL is the server-declared time remaining before the task should be
	// considered expired if it has not reached a terminal state by then
	// (spec §6.3: "server-side TTLs are weather"), clamped to
	// [0, maxTaskTTL]. Zero means the server sent no hint.
	TTL time.Duration
}

// tasksGetParams is the params object for tasks/get.
type tasksGetParams struct {
	TaskID string         `json:"taskId"`
	Meta   map[string]any `json:"_meta,omitempty"`
}

// tasksCancelParams is the params object for tasks/cancel.
type tasksCancelParams struct {
	TaskID string         `json:"taskId"`
	Meta   map[string]any `json:"_meta,omitempty"`
}

// tasksUpdateParams is the params object for tasks/update — delivers the
// operator's answer to a task sitting in input_required. InputResponses reuses
// inputResponseWire (inputrequired.go): a Tasks-extension "please answer this
// task" is elicitation-shaped in exactly the same way an MRTR retry's
// inputResponses is, so the wire entry shape is shared rather than duplicated.
type tasksUpdateParams struct {
	TaskID         string              `json:"taskId"`
	InputResponses []inputResponseWire `json:"inputResponses"`
	Meta           map[string]any      `json:"_meta,omitempty"`
}

// taskResultWire is the wire shape shared by tasks/get, tasks/update, and
// tasks/cancel responses. Status and StatusMessage are json.RawMessage —
// same tolerant-decode discipline as toolsCallResult.ResultType (client.go) —
// so a non-compliant server sending a non-string value cannot fail the whole
// json.Unmarshal; decodeTaskStatusValue and the StatusMessage decode below
// each absorb that case instead.
type taskResultWire struct {
	TaskID         string          `json:"taskId"`
	Status         json.RawMessage `json:"status"`
	StatusMessage  json.RawMessage `json:"statusMessage,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	PollIntervalMs json.RawMessage `json:"pollIntervalMs,omitempty"`
	TtlMs          json.RawMessage `json:"ttlMs,omitempty"`
}

// decodeTaskStatusValue extracts a taskResultWire.Status value, tolerating a
// non-compliant server the same way decodeResultType does: a missing or
// non-string value decodes to "", which callTasksMethod treats as a
// structural decode error (unlike resultType, an absent Tasks-extension
// status has no legacy meaning to preserve — every conforming response to a
// method this package chose to call MUST carry one).
func decodeTaskStatusValue(raw json.RawMessage) TaskStatusValue {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return TaskStatusValue(truncateForLog(s, maxTaskStatusLen))
}

// decodeTaskStatusMessage extracts taskResultWire.StatusMessage, returning ""
// on any absent/malformed/non-string value — same tolerant posture as
// parseServerInfo (meta.go).
func decodeTaskStatusMessage(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return truncateForLog(s, maxTaskStatusMessageLen)
}

// parseTaskDurationMs decodes a millisecond duration hint (pollIntervalMs or
// ttlMs), clamping to [floor, ceiling]. Returns 0 — "no usable hint" — when
// raw is absent, JSON null, fails to unmarshal into an int64, or is negative;
// same tolerant-decode discipline as parseCacheHint (cache.go). The clamp is
// applied to the integer millisecond value before the multiply, for the same
// ms→time.Duration overflow reason maxCacheHintTTLMs documents.
func parseTaskDurationMs(raw json.RawMessage, floor, ceiling time.Duration) time.Duration {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var ms int64
	if err := json.Unmarshal(raw, &ms); err != nil || ms < 0 {
		return 0
	}
	floorMs := int64(floor / time.Millisecond)
	ceilingMs := int64(ceiling / time.Millisecond)
	if ms < floorMs {
		ms = floorMs
	}
	if ms > ceilingMs {
		ms = ceilingMs
	}
	return time.Duration(ms) * time.Millisecond
}

// callTasksMethod sends one Tasks-extension JSON-RPC request and decodes its
// taskResultWire response. Tasks is a 2026-07-28 extension (spec §6.4): a
// legacy-pinned or never-probed client has no session that could possibly
// understand it, so this refuses to send the request at all rather than
// letting a legacy server answer -32601 for a method it has never heard of —
// the same "gated like every other modern feature, never assumed present"
// rule x-mcp-header (client.go) and the MRTR retry fields (CallOptions)
// already follow.
//
// rpcName is set to taskID: tasks/get, tasks/update, and tasks/cancel each
// name exactly one task, matching the Mcp-Name convention tools/call uses for
// the tool it names (sendRPC's doc).
func (c *Client) callTasksMethod(ctx context.Context, method, taskID string, params any) (TaskStatus, error) {
	if !c.isModernProtocol() {
		return TaskStatus{}, fmt.Errorf("%s: requires the 2026-07-28 transport, server is pinned to %q", method, c.protocolVersion)
	}

	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return TaskStatus{}, fmt.Errorf("marshal %s request: %w", method, err)
	}

	resp, err := c.sendRPC(ctx, body, method, taskID, nil)
	if err != nil {
		return TaskStatus{}, fmt.Errorf("post %s: %w", method, err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := decodeResponse(resp, &envelope); err != nil {
		return TaskStatus{}, fmt.Errorf("decode %s response: %w", method, err)
	}
	if envelope.Error != nil {
		return TaskStatus{}, envelope.Error
	}

	var wire taskResultWire
	if err := json.Unmarshal(envelope.Result, &wire); err != nil {
		return TaskStatus{}, fmt.Errorf("unmarshal %s result: %w", method, err)
	}

	status := decodeTaskStatusValue(wire.Status)
	if status == "" {
		return TaskStatus{}, fmt.Errorf("%s: server returned no usable task status", method)
	}

	return TaskStatus{
		TaskID:        wire.TaskID,
		Status:        status,
		StatusMessage: decodeTaskStatusMessage(wire.StatusMessage),
		Result:        wire.Result,
		PollInterval:  parseTaskDurationMs(wire.PollIntervalMs, minTaskPollInterval, maxTaskPollInterval),
		TTL:           parseTaskDurationMs(wire.TtlMs, 0, maxTaskTTL),
	}, nil
}

// GetTask polls a Tasks-extension task's current status (tasks/get).
func (c *Client) GetTask(ctx context.Context, taskID string) (TaskStatus, error) {
	return c.callTasksMethod(ctx, methodTasksGet, taskID, tasksGetParams{
		TaskID: taskID,
		Meta:   c.requestMeta(ClientCapabilities{}),
	})
}

// CancelTask asks the server to terminate a Tasks-extension task
// (tasks/cancel). The returned TaskStatus reflects whatever terminal state
// the server actually settled on — a server may answer "failed" instead of
// "cancelled" if cancellation raced completion — so callers must read
// TaskStatus.Status rather than assume TaskStatusCancelled.
func (c *Client) CancelTask(ctx context.Context, taskID string) (TaskStatus, error) {
	return c.callTasksMethod(ctx, methodTasksCancel, taskID, tasksCancelParams{
		TaskID: taskID,
		Meta:   c.requestMeta(ClientCapabilities{}),
	})
}

// UpdateTask delivers the operator's answer to a task sitting in
// input_required (tasks/update) — the Tasks-extension sibling of an MRTR
// retry's inputResponses (CallOptions.InputResponses), addressed by taskId
// instead of re-issuing the original tools/call.
func (c *Client) UpdateTask(ctx context.Context, taskID string, responses []InputResponse) (TaskStatus, error) {
	wireResponses := make([]inputResponseWire, len(responses))
	for i, r := range responses {
		wireResponses[i] = inputResponseWire(r)
	}
	return c.callTasksMethod(ctx, methodTasksUpdate, taskID, tasksUpdateParams{
		TaskID:         taskID,
		InputResponses: wireResponses,
		Meta:           c.requestMeta(ClientCapabilities{}),
	})
}
