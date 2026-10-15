package channelext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

const (
	methodChannelRequest = "channel/request"
	methodTasksGet       = "tasks/get"
	methodTasksCancel    = "tasks/cancel"
)

// Defaults for the task envelope. Human waits are slow, so the poll cadence is
// relaxed and the TTL generous; the host clamps both on its side.
const (
	DefaultPollInterval = 5 * time.Second
	DefaultTaskTTL      = time.Hour
)

// Status messages for tasks the SDK itself ends.
const (
	deliveryFailedMessage = "the request could not be delivered"
	cancelReason          = "cancelled by the host via tasks/cancel"
)

// Option is an optional Extension capability, passed to New.
type Option func(*Extension) error

// RequestFunc asks the human a question (contract §8). The task already
// exists in the store, in working, when it is called; taskID is the handle the
// plugin later passes to TaskStore.Complete when the human answers.
//
// It should deliver the question and return — not wait for the answer. ctx is
// the HTTP request's context and ends when channel/request returns, so work
// that outlives the call must not use it. A returned error fails the task and
// the call.
type RequestFunc func(ctx context.Context, taskID string, req Request) error

// CancelFunc is told that the host cancelled a task (replacing the v1
// RequestTerminated close-the-loop RPC), so the plugin can retract the
// question it showed the human. It runs only when the cancel actually won; a
// cancel that lost to a completion does not call it. The task is already
// terminal when it runs, so a failure here has nothing left to undo and is the
// plugin's to log.
type CancelFunc func(ctx context.Context, taskID, reason string)

// Request is a decoded channel/request (contract §8).
type Request struct {
	Target  Target
	Message string

	// RequestedSchema is the JSON Schema of a form; empty for a pick-one.
	RequestedSchema json.RawMessage

	// Options are the choices offered; empty for a form.
	Options []RequestOption

	// Kind is the host's optional "permission"/"information" rendering hint.
	// It is not an enforcement point: the assurance gate runs host-side.
	Kind string
}

// RequestOption is one choice a human may pick.
type RequestOption struct {
	ID    string
	Label string
}

// WithRequest registers channel/request, tasks/get and tasks/cancel, making
// the extension able to ask questions. Without it the extension is
// notify-only and the composed server answers channel/request with
// method-not-found (contract worked example C).
func WithRequest(fn RequestFunc) Option {
	return func(e *Extension) error {
		if fn == nil {
			return fmt.Errorf("channelext: request handler is nil")
		}
		e.request = fn
		return nil
	}
}

// WithTaskStore sets where tasks are kept. The default is an in-memory store
// that forgets every wait on restart; a plugin that must survive one passes
// NewFileTaskStore on its instance volume.
func WithTaskStore(store TaskStore) Option {
	return func(e *Extension) error {
		if store == nil {
			return fmt.Errorf("channelext: task store is nil")
		}
		e.tasks = store
		return nil
	}
}

// WithOnCancel registers the hook run when a host cancel wins (see
// CancelFunc). It requires WithRequest.
func WithOnCancel(fn CancelFunc) Option {
	return func(e *Extension) error {
		if fn == nil {
			return fmt.Errorf("channelext: cancel hook is nil")
		}
		e.onCancel = fn
		return nil
	}
}

// WithTaskTiming overrides the pollIntervalMs and ttlMs the extension
// advertises, and the TTL it enforces. Both must be positive.
func WithTaskTiming(pollInterval, ttl time.Duration) Option {
	return func(e *Extension) error {
		if pollInterval <= 0 || ttl <= 0 {
			return fmt.Errorf("channelext: task timing must be positive, got poll %s ttl %s", pollInterval, ttl)
		}
		e.pollInterval = pollInterval
		e.taskTTL = ttl
		return nil
	}
}

// requestParamsWire is the wire shape of channel/request's params (§8).
type requestParamsWire struct {
	Target          targetWire      `json:"target"`
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requestedSchema,omitempty"`
	Options         []optionWire    `json:"options,omitempty"`
	Kind            string          `json:"kind,omitempty"`
	Meta            json.RawMessage `json:"_meta,omitempty"`
}

type optionWire struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// decodeRequest decodes and validates a channel/request params object.
func decodeRequest(raw json.RawMessage) (Request, error) {
	var wire requestParamsWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Request{}, fmt.Errorf("channel/request: invalid params: %w", err)
	}
	target, err := decodeTarget(wire.Target)
	if err != nil {
		return Request{}, fmt.Errorf("channel/request: %w", err)
	}
	if wire.Message == "" {
		return Request{}, fmt.Errorf("channel/request: message is empty; there is nothing to ask")
	}
	hasSchema := len(wire.RequestedSchema) > 0 && string(wire.RequestedSchema) != "null"
	if len(wire.Options) == 0 && !hasSchema {
		// Neither is not a question: the task would stay open forever waiting
		// for an answer the human has no way to give (§8).
		return Request{}, fmt.Errorf("channel/request: request carries neither options nor requestedSchema; use channel/notify for a message that needs no answer")
	}

	req := Request{Target: target, Message: wire.Message, Kind: wire.Kind}
	if hasSchema {
		req.RequestedSchema = wire.RequestedSchema
	}
	seen := make(map[string]bool, len(wire.Options))
	for _, o := range wire.Options {
		if o.ID == "" {
			return Request{}, fmt.Errorf("channel/request: an option has an empty id")
		}
		if seen[o.ID] {
			return Request{}, fmt.Errorf("channel/request: duplicate option id %q", o.ID)
		}
		seen[o.ID] = true
		req.Options = append(req.Options, RequestOption{ID: o.ID, Label: o.Label})
	}
	return req, nil
}

// taskParamsWire is the params of tasks/get and tasks/cancel.
type taskParamsWire struct {
	TaskID string `json:"taskId"`
}

func decodeTaskID(raw json.RawMessage, method string) (string, error) {
	var wire taskParamsWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return "", fmt.Errorf("%s: invalid params: %w", method, err)
	}
	if wire.TaskID == "" {
		return "", fmt.Errorf("%s: taskId is required", method)
	}
	return wire.TaskID, nil
}

// handleRequest serves channel/request (contract §8).
func (e *Extension) handleRequest(w http.ResponseWriter, r *http.Request, req mcpserver.Request) {
	decoded, err := decodeRequest(req.Params)
	if err != nil {
		mcpserver.WriteError(w, req.ID, http.StatusBadRequest, mcpserver.ErrCodeInvalidParams, err.Error(), nil)
		return
	}

	ctx := r.Context()
	task, err := e.tasks.Create(ctx, NewTask{CreatedAt: timeNow(), TTL: e.taskTTL})
	if err != nil {
		e.writeInternalError(w, req.ID, methodChannelRequest, err)
		return
	}

	if err := e.request(ctx, task.ID, decoded); err != nil {
		// A question nobody was shown must not leave a task open until its
		// TTL. The failure is terminal and the host sees an error, not a
		// handle to a wait that can never be answered.
		if _, failErr := e.tasks.Fail(ctx, task.ID, deliveryFailedMessage); failErr != nil && !errors.Is(failErr, ErrTaskTerminal) {
			err = fmt.Errorf("%w (and recording the failure: %v)", err, failErr)
		}
		e.writeInternalError(w, req.ID, methodChannelRequest, err)
		return
	}

	// The plugin may have completed the task already (an instant answer), so
	// report the stored state rather than assuming working.
	current, err := e.tasks.Get(ctx, task.ID)
	if err != nil {
		e.writeInternalError(w, req.ID, methodChannelRequest, err)
		return
	}
	mcpserver.WriteResult(w, req.ID, e.envelope(current))
}

// handleTasksGet serves tasks/get.
func (e *Extension) handleTasksGet(w http.ResponseWriter, r *http.Request, req mcpserver.Request) {
	id, err := decodeTaskID(req.Params, methodTasksGet)
	if err != nil {
		mcpserver.WriteError(w, req.ID, http.StatusBadRequest, mcpserver.ErrCodeInvalidParams, err.Error(), nil)
		return
	}

	task, err := e.tasks.Get(r.Context(), id)
	if err != nil {
		e.writeTaskError(w, req.ID, methodTasksGet, err)
		return
	}

	if !task.Status.Terminal() && !timeNow().Before(task.expiresAt()) {
		// Expiry is applied on observation, through the same CAS as every
		// other terminal write, so an answer that lands at the deadline and an
		// expiry cannot both win.
		task, err = e.tasks.Fail(r.Context(), id, expiredMessage)
		if err != nil && !errors.Is(err, ErrTaskTerminal) {
			e.writeTaskError(w, req.ID, methodTasksGet, err)
			return
		}
	}
	mcpserver.WriteResult(w, req.ID, e.envelope(task))
}

// handleTasksCancel serves tasks/cancel.
func (e *Extension) handleTasksCancel(w http.ResponseWriter, r *http.Request, req mcpserver.Request) {
	id, err := decodeTaskID(req.Params, methodTasksCancel)
	if err != nil {
		mcpserver.WriteError(w, req.ID, http.StatusBadRequest, mcpserver.ErrCodeInvalidParams, err.Error(), nil)
		return
	}

	task, err := e.tasks.Cancel(r.Context(), id)
	switch {
	case err == nil:
		if e.onCancel != nil {
			e.onCancel(r.Context(), id, cancelReason)
		}
	case errors.Is(err, ErrTaskTerminal):
		// Lost to an earlier terminal write: report what won. A terminal task
		// is not resurrected and the plugin is not told to retract a question
		// that was already answered or ended.
	default:
		e.writeTaskError(w, req.ID, methodTasksCancel, err)
		return
	}
	mcpserver.WriteResult(w, req.ID, e.envelope(task))
}

// taskEnvelopeWire is the Tasks-extension result shape (§8). ttlMs is the
// time remaining, which is how the host reads it.
type taskEnvelopeWire struct {
	TaskID         string      `json:"taskId"`
	Status         TaskStatus  `json:"status"`
	StatusMessage  string      `json:"statusMessage,omitempty"`
	Result         *Resolution `json:"result,omitempty"`
	PollIntervalMs int64       `json:"pollIntervalMs"`
	TTLMs          int64       `json:"ttlMs"`
}

func (e *Extension) envelope(t Task) taskEnvelopeWire {
	remaining := t.expiresAt().Sub(timeNow())
	if remaining < 0 {
		remaining = 0
	}
	return taskEnvelopeWire{
		TaskID:         t.ID,
		Status:         t.Status,
		StatusMessage:  t.StatusMessage,
		Result:         t.Result,
		PollIntervalMs: e.pollInterval.Milliseconds(),
		TTLMs:          remaining.Milliseconds(),
	}
}

func (e *Extension) writeTaskError(w http.ResponseWriter, id json.RawMessage, method string, err error) {
	if errors.Is(err, ErrTaskNotFound) {
		mcpserver.WriteError(w, id, http.StatusBadRequest, mcpserver.ErrCodeInvalidParams, method+": unknown task", nil)
		return
	}
	e.writeInternalError(w, id, method, err)
}

func (e *Extension) writeInternalError(w http.ResponseWriter, id json.RawMessage, method string, err error) {
	mcpserver.WriteError(w, id, http.StatusInternalServerError, errCodeInternal, method+": "+err.Error(), nil)
}
