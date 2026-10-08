package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// purgeBusyTimeoutMS is how long the purge waits for the live server's write
// lock before failing. db.Open sets no busy_timeout.
const purgeBusyTimeoutMS = 10000

var timeNow = func() time.Time { return time.Now() }

// defaultPurgeStatuses is every terminal run status.
var defaultPurgeStatuses = []string{
	string(model.RunStatusComplete),
	string(model.RunStatusFailed),
	string(model.RunStatusInterrupted),
}

// PurgeOptions configures PurgeRuns.
type PurgeOptions struct {
	// OlderThan is the age threshold; its original text (e.g. "90d") is
	// echoed in the summary line.
	OlderThan string
	// Statuses are the run statuses eligible for deletion. All must be terminal.
	Statuses []string
	DryRun   bool
}

// parseOlderThan accepts a Go duration ("36h") or a whole number of days
// ("90d"). time.ParseDuration has no day unit. The result must be positive so
// that a zero value can never select every run.
func parseOlderThan(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("--older-than is required")
	}
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("invalid --older-than %q: want a Go duration or whole days such as 90d", s)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid --older-than %q: want a Go duration or whole days such as 90d", s)
		}
		d = parsed
	}
	if d <= 0 {
		return 0, fmt.Errorf("--older-than must be greater than zero, got %q", s)
	}
	return d, nil
}

// validatePurgeStatuses rejects unknown and non-terminal statuses so that an
// active run can never be deleted, and drops duplicates.
func validatePurgeStatuses(statuses []string) ([]string, error) {
	if len(statuses) == 0 {
		return nil, fmt.Errorf("--status must name at least one status")
	}
	seen := make(map[string]bool, len(statuses))
	var out []string
	for _, s := range statuses {
		s = strings.TrimSpace(s)
		status := model.RunStatus(s)
		if !status.Valid() {
			return nil, fmt.Errorf("unknown run status %q", s)
		}
		if !model.IsTerminalStatus(status) {
			return nil, fmt.Errorf("refusing to purge non-terminal status %q: only complete, failed and interrupted runs can be purged", s)
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}

// PurgeRuns deletes (or, with DryRun, counts) terminal runs whose
// COALESCE(completed_at, started_at) is older than opts.OlderThan, along with
// their dependent rows, in one write transaction. Returns a shell exit code:
//
//	0 — success (including nothing to purge)
//	1 — invalid input or unexpected error (nothing is deleted)
func PurgeRuns(ctx context.Context, dbPath string, opts PurgeOptions, out, errOut io.Writer) int {
	age, err := parseOlderThan(opts.OlderThan)
	if err != nil {
		fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}
	statuses, err := validatePurgeStatuses(opts.Statuses)
	if err != nil {
		fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}
	// Run timestamps are RFC3339 UTC strings, which compare correctly as text.
	cutoff := timeNow().UTC().Add(-age).Format(time.RFC3339)

	store, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(errOut, "error: open db: %v\n", err)
		return 1
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		fmt.Fprintf(errOut, "error: migrate db: %v\n", err)
		return 1
	}

	// db.Open uses a single connection, so this pragma sticks for the
	// statements below.
	if _, err := store.DB().ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", purgeBusyTimeoutMS)); err != nil {
		fmt.Fprintf(errOut, "error: set busy_timeout: %v\n", err)
		return 1
	}

	conn, err := store.DB().Conn(ctx)
	if err != nil {
		fmt.Fprintf(errOut, "error: acquire connection: %v\n", err)
		return 1
	}
	defer conn.Close()

	// BEGIN IMMEDIATE takes the write lock up front. A deferred transaction
	// that reads first and then writes can fail with SQLITE_BUSY_SNAPSHOT
	// against a live server instead of waiting on busy_timeout. A dry run only
	// reads, so it uses a plain deferred transaction.
	begin := "BEGIN IMMEDIATE"
	if opts.DryRun {
		begin = "BEGIN"
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		fmt.Fprintf(errOut, "error: begin transaction: %v\n", err)
		return 1
	}
	committed := false
	defer func() {
		if !committed {
			// Best effort: closing the connection ends the transaction anyway.
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	q := db.New(conn)
	runs, err := q.CountPurgeableRuns(ctx, db.CountPurgeableRunsParams{Statuses: statuses, Cutoff: &cutoff})
	if err != nil {
		fmt.Fprintf(errOut, "error: count runs: %v\n", err)
		return 1
	}
	steps, err := q.CountPurgeableRunSteps(ctx, db.CountPurgeableRunStepsParams{Statuses: statuses, Cutoff: &cutoff})
	if err != nil {
		fmt.Fprintf(errOut, "error: count steps: %v\n", err)
		return 1
	}

	if opts.DryRun {
		fmt.Fprintf(out, "dry run: would delete %d runs and %d steps older than %s\n", runs, steps, opts.OlderThan)
		return 0
	}

	deleted, err := q.DeletePurgeableRuns(ctx, db.DeletePurgeableRunsParams{Statuses: statuses, Cutoff: &cutoff})
	if err != nil {
		fmt.Fprintf(errOut, "error: delete runs: %v\n", err)
		return 1
	}
	if deleted != runs {
		fmt.Fprintf(errOut, "error: deleted %d runs but counted %d; rolling back\n", deleted, runs)
		return 1
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		fmt.Fprintf(errOut, "error: commit: %v\n", err)
		return 1
	}
	committed = true

	fmt.Fprintf(out, "deleted %d runs and %d steps older than %s\n", deleted, steps, opts.OlderThan)
	return 0
}
