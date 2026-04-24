package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/infra/event"
	"github.com/rapp992/gleipnir/internal/infra/logctx"
	"github.com/rapp992/gleipnir/internal/model"
)

// Step is a single entry in the run's reasoning trace.
type Step struct {
	RunID     string
	Type      model.StepType
	Content   any // will be JSON-marshalled; shape defined by StepType
	TokenCost int
}

// AuditWriter serialises run step writes through an internal queue to avoid
// SQLite write contention under concurrent runs (ADR-003).
// It must be closed after the run completes to flush the queue.
type AuditWriter struct {
	queries   *db.Queries
	queue     chan writeRequest
	done      chan struct{}
	closeOnce sync.Once
	publisher event.Publisher
	// drainErr accumulates all errors encountered by loop(); read after <-w.done in Close().
	drainErr error
}

type writeRequest struct {
	step Step
	resp chan error
}

// Option configures an AuditWriter.
type Option func(*AuditWriter)

// WithQueueDepth sets the capacity of the internal write queue.
// The default is 256.
func WithQueueDepth(n int) Option {
	return func(w *AuditWriter) {
		w.queue = make(chan writeRequest, n)
	}
}

// WithPublisher injects a Publisher that receives run.step_added events after
// each step is successfully written to the DB.
func WithPublisher(p event.Publisher) Option {
	return func(w *AuditWriter) {
		w.publisher = p
	}
}

// NewAuditWriter creates an AuditWriter and starts the background write loop.
func NewAuditWriter(queries *db.Queries, opts ...Option) *AuditWriter {
	w := &AuditWriter{
		queries: queries,
		queue:   make(chan writeRequest, 256),
		done:    make(chan struct{}),
	}
	for _, opt := range opts {
		opt(w)
	}
	go w.loop()
	return w
}

// Write enqueues a step for writing. It blocks until the step is written or
// ctx is cancelled. Writes are ordered by arrival; step_number is assigned
// sequentially within the run.
func (w *AuditWriter) Write(ctx context.Context, step Step) error {
	auditQueueDepth.Inc()
	resp := make(chan error, 1)
	select {
	case w.queue <- writeRequest{step: step, resp: resp}:
	case <-ctx.Done():
		auditQueueDepth.Dec() // never enqueued — undo the Inc
		return ctx.Err()
	}
	select {
	case err := <-resp:
		return err
	case <-ctx.Done():
		// Do NOT Dec here — the item was enqueued; loop() will Dec when it dequeues.
		return ctx.Err()
	}
}

// Close drains the queue and stops the background loop.
// Must be called after the run completes. Safe to call multiple times —
// subsequent calls are no-ops and wait for the drain started by the first call.
func (w *AuditWriter) Close() error {
	w.closeOnce.Do(func() {
		close(w.queue)
	})
	<-w.done
	return w.drainErr
}

// logAuditError writes an audit step on error paths where the write is best-effort.
// The primary error is already being returned to the caller, so a failed audit write
// is logged rather than propagated.
//
// Safe to call with a cancelled context — if ctx is already done, a background
// context is substituted so the DB write still lands. This is the single place
// that enforces the "audit writes must survive cancellation" invariant for error
// steps; callers should always pass their original ctx.
func logAuditError(ctx context.Context, w *AuditWriter, step Step) {
	writeCtx := ctx
	if ctx.Err() != nil {
		writeCtx = context.Background()
	}
	if err := w.Write(writeCtx, step); err != nil {
		// Use logctx.Logger for correlation IDs when the original ctx carries them.
		// When writeCtx is context.Background() (cancelled original ctx), the logger
		// falls back to slog.Default() and we keep the explicit run_id as a fallback.
		logctx.Logger(writeCtx).WarnContext(writeCtx, "audit write failed on error path", "step_type", step.Type.String(), "run_id", step.RunID, "err", err)
	}
}

// loop is the single writer goroutine. It pulls from the queue and writes
// each step to the DB, assigning sequential step numbers per run.
// Uses context.Background() for DB calls so that drain completes even after
// caller context cancellation — this ensures in-flight audit writes drain
// completely even during graceful shutdown when the run context is cancelled.
func (w *AuditWriter) loop() {
	defer close(w.done)

	// counters tracks the next step_number (0-indexed) for each run. Because
	// loop() is the only goroutine writing this map, no mutex is needed.
	counters := make(map[string]int64)

	for req := range w.queue {
		auditQueueDepth.Dec()
		stepNum := counters[req.step.RunID]
		counters[req.step.RunID]++

		content, err := json.Marshal(req.step.Content)
		if err != nil {
			e := fmt.Errorf("marshal step content: %w", err)
			req.resp <- e
			w.drainErr = errors.Join(w.drainErr, e)
			continue
		}

		stepID := model.NewULID()
		_, err = w.queries.CreateRunStep(context.Background(), db.CreateRunStepParams{
			ID:         stepID,
			RunID:      req.step.RunID,
			StepNumber: stepNum,
			Type:       req.step.Type.String(),
			Content:    string(content),
			TokenCost:  int64(req.step.TokenCost),
			CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			e := fmt.Errorf("create run step: %w", err)
			req.resp <- e
			w.drainErr = errors.Join(w.drainErr, e)
			continue
		}

		runStepsTotal.WithLabelValues(req.step.Type.String()).Inc()

		if w.publisher != nil {
			data, err := json.Marshal(map[string]any{
				"run_id":      req.step.RunID,
				"step_id":     stepID,
				"step_number": stepNum,
				"type":        req.step.Type,
			})
			if err != nil {
				e := fmt.Errorf("marshal publish payload: %w", err)
				req.resp <- e
				w.drainErr = errors.Join(w.drainErr, e)
				continue
			}
			w.publisher.Publish("run.step_added", data)
		}

		if req.step.TokenCost > 0 {
			err = w.queries.IncrementRunTokenCost(context.Background(), db.IncrementRunTokenCostParams{
				TokenCost: int64(req.step.TokenCost),
				ID:        req.step.RunID,
			})
			if err != nil {
				e := fmt.Errorf("increment token cost: %w", err)
				req.resp <- e
				w.drainErr = errors.Join(w.drainErr, e)
				continue
			}
		}

		req.resp <- nil
	}
}
