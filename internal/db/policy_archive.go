package db

import (
	"context"
	"database/sql"
	"fmt"
)

// ArchivePolicy soft-deletes a policy and, in the same transaction, drops the
// rows that only make sense for a live agent: its queued triggers and its poll
// state. Runs and everything hanging off them are left untouched — that
// history is the point of archiving (#1052). The webhook secret is cleared by
// the ArchivePolicy query itself.
//
// Returns sql.ErrNoRows when the policy does not exist or is already archived.
func (s *Store) ArchivePolicy(ctx context.Context, id, deletedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin archive policy tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.Queries().WithTx(tx)

	n, err := qtx.ArchivePolicy(ctx, ArchivePolicyParams{ID: id, DeletedAt: &deletedAt, UpdatedAt: deletedAt})
	if err != nil {
		return fmt.Errorf("archive policy: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	if err := qtx.DeleteQueuedTriggersByPolicy(ctx, id); err != nil {
		return fmt.Errorf("delete queued triggers: %w", err)
	}
	if err := qtx.DeletePollState(ctx, id); err != nil {
		return fmt.Errorf("delete poll state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit archive policy: %w", err)
	}
	return nil
}
