package agent

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
)

// Step is a single entry in the run's reasoning trace.
type Step struct {
	RunID      string
	Type       model.StepType
	Content    any // will be JSON-marshalled; shape defined by StepType
	TokenCost  int
}

// AuditWriter serialises run step writes through an internal queue to avoid
// SQLite write contention under concurrent runs (ADR-003).
// It must be closed after the run completes to flush the queue.
type AuditWriter struct {
	db     *sql.DB
	queue  chan writeRequest
	done   chan struct{}
	errCh  chan error
}

type writeRequest struct {
	step Step
	resp chan error
}

// NewAuditWriter creates an AuditWriter and starts the background write loop.
func NewAuditWriter(db *sql.DB) *AuditWriter {
	w := &AuditWriter{
		db:    db,
		queue: make(chan writeRequest, 64),
		done:  make(chan struct{}),
		errCh: make(chan error, 1),
	}
	go w.loop()
	return w
}

// Write enqueues a step for writing. It blocks until the step is written or
// ctx is cancelled. Writes are ordered by arrival; step_number is assigned
// sequentially within the run.
func (w *AuditWriter) Write(ctx context.Context, step Step) error {
	resp := make(chan error, 1)
	select {
	case w.queue <- writeRequest{step: step, resp: resp}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-resp:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close drains the queue and stops the background loop.
// Must be called after the run completes.
func (w *AuditWriter) Close() error {
	close(w.queue)
	<-w.done
	select {
	case err := <-w.errCh:
		return err
	default:
		return nil
	}
}

// loop is the single writer goroutine. It pulls from the queue and writes
// each step to the DB, assigning sequential step numbers.
func (w *AuditWriter) loop() {
	defer close(w.done)
	// TODO: maintain a per-run step counter; for each writeRequest:
	//   1. Increment counter, generate ULID
	//   2. JSON-marshal step.Content
	//   3. INSERT into run_steps
	//   4. UPDATE runs.token_cost += step.TokenCost
	//   5. Send nil or error to req.resp
	_ = fmt.Errorf
	_ = time.Now
	for req := range w.queue {
		req.resp <- fmt.Errorf("not implemented")
	}
}
