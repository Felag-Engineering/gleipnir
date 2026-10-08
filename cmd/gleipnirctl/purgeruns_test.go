package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

var purgeNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func fixPurgeClock(t *testing.T) {
	t.Helper()
	orig := timeNow
	timeNow = func() time.Time { return purgeNow }
	t.Cleanup(func() { timeNow = orig })
}

func daysAgo(n int) string {
	return purgeNow.Add(-time.Duration(n) * 24 * time.Hour).Format(time.RFC3339Nano)
}

// seedPurgeRun inserts a run with completed_at set to completedAt (nil for
// none), started 1h earlier than created, plus stepCount steps.
func seedPurgeRun(t *testing.T, s *db.Store, id string, status model.RunStatus, startedAt string, completedAt *string, stepCount int) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, completed_at, created_at)
		 VALUES (?, 'p1', ?, 'webhook', '{}', ?, ?, ?)`,
		id, string(status), startedAt, completedAt, startedAt,
	)
	if err != nil {
		t.Fatalf("insert run %s: %v", id, err)
	}
	for i := 0; i < stepCount; i++ {
		testutil.InsertRunStep(t, s, id+"-step-"+string(rune('a'+i)), id, int64(i))
	}
}

func countRows(t *testing.T, s *db.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// seedPurgeFixture creates a store with these runs (policy p1):
//
//	old-complete   complete,    completed 100d ago, 2 steps, with dependents
//	old-failed     failed,      completed 100d ago, 1 step
//	old-interrupt  interrupted, never completed, started 100d ago, 1 step
//	new-complete   complete,    completed 10d ago,  1 step
//	old-running    running,     started 100d ago,   1 step
//	old-waiting    waiting_for_approval, started 100d ago
func seedPurgeFixture(t *testing.T) (*db.Store, string) {
	t.Helper()
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy", "manual", "name: policy")
	testutil.InsertMcpServer(t, s, "srv1", "srv", "http://localhost:1")

	old, recent := daysAgo(100), daysAgo(10)
	seedPurgeRun(t, s, "old-complete", model.RunStatusComplete, old, &old, 2)
	seedPurgeRun(t, s, "old-failed", model.RunStatusFailed, old, &old, 1)
	seedPurgeRun(t, s, "old-interrupt", model.RunStatusInterrupted, old, nil, 1)
	seedPurgeRun(t, s, "new-complete", model.RunStatusComplete, recent, &recent, 1)
	seedPurgeRun(t, s, "old-running", model.RunStatusRunning, old, nil, 1)
	seedPurgeRun(t, s, "old-waiting", model.RunStatusWaitingForApproval, old, nil, 0)

	testutil.InsertApprovalRequest(t, s, "appr1", "old-complete", "tool")
	mustExec(t, s, `INSERT INTO feedback_requests(id, run_id, tool_name, proposed_input, message, status, created_at)
		VALUES ('fb1', 'old-complete', 'tool', '{}', 'msg', 'pending', ?)`, old)
	mustExec(t, s, `INSERT INTO tool_input_requests(id, run_id, server_id, tool_name, call_args, request_state, request_payload, elicitation_kind, status, expires_at, created_at)
		VALUES ('tir1', 'old-complete', 'srv1', 'tool', '{}', 's', '{}', 'information', 'pending', ?, ?)`, old, old)
	mustExec(t, s, `INSERT INTO plugin_audit_events(event_type, severity, payload_json, created_at, run_id)
		VALUES ('tool_permission_request', 'info', '{}', ?, 'old-complete')`, old)

	path := storePath(t, s)
	s.Close()
	return s, path
}

func mustExec(t *testing.T, s *db.Store, query string, args ...any) {
	t.Helper()
	if _, err := s.DB().Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func runPurge(t *testing.T, path string, opts PurgeOptions) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = PurgeRuns(context.Background(), path, opts, &out, &errOut)
	return code, out.String(), errOut.String()
}

func remainingRunIDs(t *testing.T, path string) map[string]bool {
	t.Helper()
	s, err := db.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	rows, err := s.DB().Query(`SELECT id FROM runs`)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids[id] = true
	}
	return ids
}

func TestPurgeRuns_DryRunDeletesNothing(t *testing.T) {
	fixPurgeClock(t)
	_, path := seedPurgeFixture(t)

	code, stdout, stderr := runPurge(t, path, PurgeOptions{OlderThan: "90d", Statuses: defaultPurgeStatuses, DryRun: true})
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	want := "dry run: would delete 3 runs and 4 steps older than 90d\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if got := len(remainingRunIDs(t, path)); got != 6 {
		t.Errorf("dry run left %d runs, want 6", got)
	}
}

func TestPurgeRuns_DeletesTerminalOldRunsAndDependents(t *testing.T) {
	fixPurgeClock(t)
	_, path := seedPurgeFixture(t)

	code, stdout, stderr := runPurge(t, path, PurgeOptions{OlderThan: "90d", Statuses: defaultPurgeStatuses})
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	if want := "deleted 3 runs and 4 steps older than 90d\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	remaining := remainingRunIDs(t, path)
	for _, id := range []string{"new-complete", "old-running", "old-waiting"} {
		if !remaining[id] {
			t.Errorf("run %s should have survived", id)
		}
	}
	if len(remaining) != 3 {
		t.Errorf("remaining runs = %v, want exactly the 3 kept runs", remaining)
	}

	s, err := db.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	for _, table := range []string{"approval_requests", "feedback_requests", "tool_input_requests"} {
		if n := countRows(t, s, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Errorf("%s has %d rows, want 0 after cascade", table, n)
		}
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM run_steps WHERE run_id IN ('old-complete','old-failed','old-interrupt')`); n != 0 {
		t.Errorf("%d steps of purged runs remain", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM run_steps`); n != 2 {
		t.Errorf("run_steps total = %d, want 2 (new-complete, old-running)", n)
	}

	// Oversight records outlive the run; only the link is cleared.
	if n := countRows(t, s, `SELECT COUNT(*) FROM plugin_audit_events WHERE run_id IS NULL`); n != 1 {
		t.Errorf("plugin_audit_events with NULL run_id = %d, want 1 (preserved)", n)
	}
}

func TestPurgeRuns_StatusFilter(t *testing.T) {
	tests := []struct {
		name     string
		statuses []string
		wantOut  string
		wantGone []string
	}{
		{"failed only", []string{"failed"}, "deleted 1 runs and 1 steps older than 90d\n", []string{"old-failed"}},
		{"interrupted only", []string{"interrupted"}, "deleted 1 runs and 1 steps older than 90d\n", []string{"old-interrupt"}},
		{"complete and failed", []string{"complete", "failed"}, "deleted 2 runs and 3 steps older than 90d\n", []string{"old-complete", "old-failed"}},
		{"duplicates collapse", []string{"failed", "failed"}, "deleted 1 runs and 1 steps older than 90d\n", []string{"old-failed"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixPurgeClock(t)
			_, path := seedPurgeFixture(t)

			code, stdout, stderr := runPurge(t, path, PurgeOptions{OlderThan: "90d", Statuses: tc.statuses})
			if code != 0 {
				t.Fatalf("exit %d; stderr: %s", code, stderr)
			}
			if stdout != tc.wantOut {
				t.Errorf("stdout = %q, want %q", stdout, tc.wantOut)
			}
			remaining := remainingRunIDs(t, path)
			for _, id := range tc.wantGone {
				if remaining[id] {
					t.Errorf("run %s should have been deleted", id)
				}
			}
			if len(remaining) != 6-len(tc.wantGone) {
				t.Errorf("remaining = %v", remaining)
			}
		})
	}
}

func TestPurgeRuns_RejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		opts    PurgeOptions
		wantErr string
	}{
		{"running status", PurgeOptions{OlderThan: "90d", Statuses: []string{"running"}}, "non-terminal"},
		{"pending status", PurgeOptions{OlderThan: "90d", Statuses: []string{"complete", "pending"}}, "non-terminal"},
		{"waiting_for_approval status", PurgeOptions{OlderThan: "90d", Statuses: []string{"waiting_for_approval"}}, "non-terminal"},
		{"waiting_for_feedback status", PurgeOptions{OlderThan: "90d", Statuses: []string{"waiting_for_feedback"}}, "non-terminal"},
		{"unknown status", PurgeOptions{OlderThan: "90d", Statuses: []string{"bogus"}}, "unknown run status"},
		{"no statuses", PurgeOptions{OlderThan: "90d"}, "at least one status"},
		{"missing older-than", PurgeOptions{Statuses: defaultPurgeStatuses}, "--older-than is required"},
		{"zero days", PurgeOptions{OlderThan: "0d", Statuses: defaultPurgeStatuses}, "greater than zero"},
		{"zero duration", PurgeOptions{OlderThan: "0s", Statuses: defaultPurgeStatuses}, "greater than zero"},
		{"negative", PurgeOptions{OlderThan: "-5d", Statuses: defaultPurgeStatuses}, "greater than zero"},
		{"garbage", PurgeOptions{OlderThan: "soon", Statuses: defaultPurgeStatuses}, "invalid --older-than"},
		{"fractional days", PurgeOptions{OlderThan: "1.5d", Statuses: defaultPurgeStatuses}, "invalid --older-than"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixPurgeClock(t)
			_, path := seedPurgeFixture(t)

			code, stdout, stderr := runPurge(t, path, tc.opts)
			if code != 1 {
				t.Fatalf("exit %d, want 1", code)
			}
			if !strings.Contains(stderr, tc.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.wantErr)
			}
			if stdout != "" {
				t.Errorf("unexpected stdout %q", stdout)
			}
			if got := len(remainingRunIDs(t, path)); got != 6 {
				t.Errorf("rejected purge left %d runs, want 6", got)
			}
		})
	}
}

func TestPurgeRuns_NothingToPurge(t *testing.T) {
	fixPurgeClock(t)
	_, path := seedPurgeFixture(t)

	code, stdout, stderr := runPurge(t, path, PurgeOptions{OlderThan: "1000d", Statuses: defaultPurgeStatuses})
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	if want := "deleted 0 runs and 0 steps older than 1000d\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if got := len(remainingRunIDs(t, path)); got != 6 {
		t.Errorf("%d runs remain, want 6", got)
	}
}

func TestPurgeRuns_AgeUsesCompletedAtThenStartedAt(t *testing.T) {
	fixPurgeClock(t)
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy", "manual", "name: policy")
	// Started long ago but completed recently: not old.
	recent := daysAgo(1)
	seedPurgeRun(t, s, "long-running-recent-finish", model.RunStatusComplete, daysAgo(200), &recent, 0)
	// No completed_at: falls back to started_at.
	seedPurgeRun(t, s, "interrupted-old", model.RunStatusInterrupted, daysAgo(200), nil, 0)
	path := storePath(t, s)
	s.Close()

	code, _, stderr := runPurge(t, path, PurgeOptions{OlderThan: "30d", Statuses: defaultPurgeStatuses})
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	remaining := remainingRunIDs(t, path)
	if !remaining["long-running-recent-finish"] || remaining["interrupted-old"] {
		t.Errorf("remaining = %v", remaining)
	}
}

func TestParseOlderThan(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"90d", 90 * 24 * time.Hour},
		{"1d", 24 * time.Hour},
		{"36h", 36 * time.Hour},
		{"90m", 90 * time.Minute},
		{"1h30m", 90 * time.Minute},
	}
	for _, tc := range tests {
		got, err := parseOlderThan(tc.in)
		if err != nil {
			t.Errorf("parseOlderThan(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseOlderThan(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestPurgeRuns_CommandRequiresOlderThan(t *testing.T) {
	cmd := newPurgeRunsCmd()
	if f := cmd.Flags().Lookup("older-than"); f == nil || f.DefValue != "" {
		t.Fatalf("older-than flag must exist with no default, got %+v", f)
	}
	if f := cmd.Flags().Lookup("status"); f == nil || f.DefValue != "[complete,failed,interrupted]" {
		t.Fatalf("unexpected status default: %+v", f)
	}
}

func TestPurgeRuns_BadDBPathReturns1(t *testing.T) {
	code, _, stderr := runPurge(t, "/nonexistent-dir/x/gleipnir.db", PurgeOptions{OlderThan: "90d", Statuses: defaultPurgeStatuses})
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("stderr missing error: %q", stderr)
	}
}
