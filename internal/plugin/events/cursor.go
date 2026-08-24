package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// Store persists the durable events/listen resume cursor for each plugin
// instance (ADR-054, mcp-realignment-spec.md §5).
//
// The extension has no in-band ack (extension doc §7.3): the ack IS the
// cursor sent on the next (re)connect's cursor param — nothing on an open
// events/listen stream ever tells the server "I have this one." That makes
// this store the entire acknowledgement mechanism, which is why every
// implementation writes through on every consumed event rather than
// checkpointing periodically: a periodic checkpoint would silently convert
// the extension's at-least-once delivery into at-most-once for every event
// consumed between checkpoints and a restart. Advance must be called only
// AFTER the dispatcher has consumed the event it acks — advancing on receipt
// has the identical at-most-once failure, just moved earlier.
//
// Write-through has an obvious cost: one DB write per event rather than one
// per batch. The escape hatch, if that ever matters, is batching the
// Advance call at the dispatcher's natural batch boundary (the extension's
// maxBatch hint, §4.1) rather than on a wall-clock timer — a timer-based
// checkpoint reintroduces the same at-most-once window this design avoids,
// just with a bound on its size instead of eliminating it.
type Store interface {
	// Load returns the cursor and last-consumed sequence stored for
	// instanceID, or ("", 0, nil) when there is nothing to resume from —
	// either no cursor has ever been recorded, or the stored cursor was
	// earned under a different scopeHash. A scope mismatch is reported the
	// same way as "no cursor" rather than as an error: a fresh
	// events/listen connect with no cursor param is exactly the right
	// behavior either way. The stored row is left untouched on a scope
	// mismatch so it remains available for diagnosis.
	Load(ctx context.Context, instanceID, scopeHash string) (cursor string, seq uint64, err error)

	// Advance persists cursor/seq as the new resume point for instanceID
	// under scopeHash. Call only after the dispatcher has fully consumed
	// the event that produced seq (see the Store-level doc comment).
	// Refuses to move backwards: if a cursor is already stored for
	// instanceID, seq must be strictly greater than its sequence, or
	// Advance returns an error naming both values — a server buffer
	// replaying an already-acked sequence is a server bug, not a retry to
	// paper over.
	Advance(ctx context.Context, instanceID, scopeHash, cursor string, seq uint64) error

	// Reset clears instanceID's stored cursor, e.g. when its subscription
	// scope changes. The next events/listen connects with no cursor param,
	// paying the redelivery cost plugin_event_dedup absorbs.
	Reset(ctx context.Context, instanceID string) error
}

// Querier is the narrow DB interface used by dbStore. Using an interface
// (not *db.Queries directly) keeps dbStore testable with fakes, mirroring
// internal/plugin/dedup.Querier.
type Querier interface {
	GetEventCursor(ctx context.Context, pluginInstanceID string) (db.PluginEventCursor, error)
	UpsertEventCursor(ctx context.Context, arg db.UpsertEventCursorParams) error
	ResetEventCursor(ctx context.Context, pluginInstanceID string) error
}

// dbStore is a SQLite-backed Store implementation.
type dbStore struct {
	q     Querier
	clock func() time.Time
}

// NewDBStore returns a Store backed by SQLite. The clock defaults to
// time.Now; inject a fake via dbStore.SetClockForTest for tests.
func NewDBStore(q Querier) *dbStore {
	return &dbStore{
		q:     q,
		clock: time.Now,
	}
}

// SetClockForTest replaces the clock on this store. Tests that call this
// must not use t.Parallel() because the clock is a shared mutable field.
func (s *dbStore) SetClockForTest(fn func() time.Time) {
	s.clock = fn
}

func (s *dbStore) Load(ctx context.Context, instanceID, scopeHash string) (string, uint64, error) {
	row, err := s.q.GetEventCursor(ctx, instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("load event cursor: %w", err)
	}
	if row.ScopeHash != scopeHash {
		// Stale-scope cursor: report "no cursor" without touching the row,
		// so a subsequent diagnostic read still sees what was there.
		return "", 0, nil
	}
	return row.Cursor, uint64(row.Sequence), nil
}

func (s *dbStore) Advance(ctx context.Context, instanceID, scopeHash, cursor string, seq uint64) error {
	existing, err := s.q.GetEventCursor(ctx, instanceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("advance event cursor: read existing: %w", err)
	}
	if err == nil && seq <= uint64(existing.Sequence) {
		return fmt.Errorf("advance event cursor: seq %d is not greater than stored seq %d for instance %s", seq, existing.Sequence, instanceID)
	}

	if err := s.q.UpsertEventCursor(ctx, db.UpsertEventCursorParams{
		PluginInstanceID: instanceID,
		Cursor:           cursor,
		Sequence:         int64(seq),
		ScopeHash:        scopeHash,
		UpdatedAt:        s.clock().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return fmt.Errorf("advance event cursor: %w", err)
	}
	return nil
}

func (s *dbStore) Reset(ctx context.Context, instanceID string) error {
	if err := s.q.ResetEventCursor(ctx, instanceID); err != nil {
		return fmt.Errorf("reset event cursor: %w", err)
	}
	return nil
}

// Noop is a Store that never persists a cursor: Load always reports "no
// cursor" and Advance/Reset are no-ops. Used by tests and call sites that
// don't need durable resume (mirrors internal/plugin/dedup.Noop).
type Noop struct{}

func (Noop) Load(context.Context, string, string) (string, uint64, error)  { return "", 0, nil }
func (Noop) Advance(context.Context, string, string, string, uint64) error { return nil }
func (Noop) Reset(context.Context, string) error                           { return nil }
