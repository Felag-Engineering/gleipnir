package channelext

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// timeNow is the clock TTL logic reads; tests swap it rather than waiting on
// wall-clock time.
var timeNow = time.Now

// TaskStatus is a Tasks-extension task's status as this package reports it
// on the wire. Only four values are ever produced: input_required is not
// among them because a channel wait has exactly one open state, working.
type TaskStatus string

const (
	TaskWorking   TaskStatus = "working"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

// Terminal reports whether s is a state a task cannot leave.
func (s TaskStatus) Terminal() bool {
	return s != TaskWorking
}

var (
	// ErrTaskNotFound reports an unknown (or malformed) task ID.
	ErrTaskNotFound = errors.New("channelext: task not found")

	// ErrTaskTerminal reports that a task was already terminal when a
	// transition was attempted. The first terminal write wins; every later
	// one gets this error and must not be treated as having changed anything.
	ErrTaskTerminal = errors.New("channelext: task already terminal")

	// ErrInvalidResolution reports a Resolution carrying neither OptionID nor
	// Content. Contract §8.2: recording it would read a non-answer as an
	// answer.
	ErrInvalidResolution = errors.New("channelext: resolution carries neither optionId nor content")

	// ErrTaskExpired reports an answer that arrived after the task's TTL. The
	// store records the expiry (failed, never completed) and returns this; it
	// wraps ErrTaskTerminal because the answer changed nothing.
	ErrTaskExpired = fmt.Errorf("%w: ttl expired before the answer arrived", ErrTaskTerminal)
)

// expiredMessage is the statusMessage of a task that ran out its TTL.
const expiredMessage = "task ttl expired before the recipient answered"

// Resolution is what a human did, and the result payload of a completed task
// (contract §8.2). The JSON tags are the wire shape.
type Resolution struct {
	// OptionID is the chosen option, for a pick-one ask.
	OptionID string `json:"optionId,omitempty"`

	// Content is the submitted form payload, for a schema ask.
	Content json.RawMessage `json:"content,omitempty"`

	// ActorExternalID is the channel's claim about who acted. The host
	// verifies it against its identity directory before an approval counts.
	ActorExternalID string `json:"actorExternalId,omitempty"`
}

// validate refuses a resolution that records no decision.
func (r Resolution) validate() error {
	if r.OptionID == "" && len(r.Content) == 0 {
		return ErrInvalidResolution
	}
	return nil
}

// Task is one durable channel/request wait.
type Task struct {
	ID            string      `json:"id"`
	Status        TaskStatus  `json:"status"`
	StatusMessage string      `json:"statusMessage,omitempty"`
	Result        *Resolution `json:"result,omitempty"`
	CreatedAt     time.Time   `json:"createdAt"`

	// TTL is how long the task may stay working, measured from CreatedAt.
	TTL time.Duration `json:"ttl"`
}

// expiresAt is the instant a still-working task is considered expired.
func (t Task) expiresAt() time.Time { return t.CreatedAt.Add(t.TTL) }

// NewTask is the input to TaskStore.Create. The store assigns the ID and the
// initial working status.
type NewTask struct {
	CreatedAt time.Time
	TTL       time.Duration
}

// TaskStore is the durable record of channel/request waits. A plugin holds
// it to complete a task when its human answers (see Extension.Tasks).
//
// Complete, Cancel and Fail are compare-and-swap: only a working task can
// transition, the first terminal write wins, and every later one returns
// ErrTaskTerminal. On ErrTaskTerminal the returned Task is the state that
// won, so a caller can report it without a second, racy Get.
type TaskStore interface {
	Create(ctx context.Context, n NewTask) (Task, error)
	Get(ctx context.Context, id string) (Task, error)
	Complete(ctx context.Context, id string, r Resolution) (Task, error)
	Cancel(ctx context.Context, id string) (Task, error)

	// Fail drives a working task to failed with message. It is how TTL expiry
	// and a failed delivery end a wait: a terminal non-completed state, never
	// a fabricated answer (contract §10).
	Fail(ctx context.Context, id, message string) (Task, error)
}

// taskIDPattern is the only shape newTaskID produces. The file store also
// uses it to reject a path-traversing ID before it becomes a filename.
var taskIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// newTaskID returns an unguessable ID: the ID is the only thing standing
// between a caller and someone else's task.
func newTaskID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("channelext: generate task id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Transition helpers shared by both stores so the CAS rules cannot drift
// between them. Each takes the current task and returns the updated one, or
// the unchanged task with the reason it refused.

func completed(t Task, r Resolution) (Task, error) {
	if err := r.validate(); err != nil {
		return t, err
	}
	if t.Status.Terminal() {
		return t, ErrTaskTerminal
	}
	if !timeNow().Before(t.expiresAt()) {
		// A late answer must not complete a wait the host may already have
		// given up on, even if no tasks/get has observed the expiry yet. The
		// expiry itself is the terminal write (the store persists it).
		t.Status = TaskFailed
		t.StatusMessage = expiredMessage
		return t, ErrTaskExpired
	}
	t.Status = TaskCompleted
	t.Result = &r
	return t, nil
}

func terminated(t Task, status TaskStatus, message string) (Task, error) {
	if t.Status.Terminal() {
		return t, ErrTaskTerminal
	}
	t.Status = status
	t.StatusMessage = message
	return t, nil
}
