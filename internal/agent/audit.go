package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
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
	queries *db.Queries
	queue   chan writeRequest
	done    chan struct{}
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
	return nil
}

// loop is the single writer goroutine. It pulls from the queue and writes
// each step to the DB, assigning sequential step numbers per run.
// Uses context.Background() for DB calls so that drain completes even after
// caller context cancellation.
func (w *AuditWriter) loop() {
	defer close(w.done)

	// counters tracks the next step_number for each run. Because loop() is
	// the only goroutine writing this map, no mutex is needed.
	counters := make(map[string]int64)

	for req := range w.queue {
		counters[req.step.RunID]++
		stepNum := counters[req.step.RunID]

		content, err := json.Marshal(req.step.Content)
		if err != nil {
			req.resp <- fmt.Errorf("marshal step content: %w", err)
			continue
		}

		_, err = w.queries.CreateRunStep(context.Background(), db.CreateRunStepParams{
			ID:         model.NewULID(),
			RunID:      req.step.RunID,
			StepNumber: stepNum,
			Type:       req.step.Type.String(),
			Content:    string(content),
			TokenCost:  int64(req.step.TokenCost),
			CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			req.resp <- fmt.Errorf("create run step: %w", err)
			continue
		}

		if req.step.TokenCost > 0 {
			err = w.queries.IncrementRunTokenCost(context.Background(), db.IncrementRunTokenCostParams{
				TokenCost: int64(req.step.TokenCost),
				ID:        req.step.RunID,
			})
			if err != nil {
				req.resp <- fmt.Errorf("increment token cost: %w", err)
				continue
			}
		}

		req.resp <- nil
	}
}
