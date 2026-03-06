package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOpen(t *testing.T) {
	cases := []struct {
		name    string
		path    func(t *testing.T) string
		wantErr bool
	}{
		{
			name: "clean state",
			path: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "test.db")
			},
		},
		{
			name: "reopen existing file",
			path: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "test.db")
				s, err := Open(p)
				if err != nil {
					t.Fatalf("initial Open: %v", err)
				}
				s.Close()
				return p
			},
		},
		{
			// sql.Open is lazy and doesn't touch the filesystem until the first
			// operation. Open forces a real connection via PRAGMA calls, which
			// surfaces the error when the parent directory doesn't exist.
			name: "nonexistent parent directory",
			path: func(t *testing.T) string {
				return "/nonexistent/dir/test.db"
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(tc.path(t))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			defer s.Close()
			checkPragmas(t, s)
		})
	}
}

func TestMarkInterrupted(t *testing.T) {
	newStore := func(t *testing.T) *Store {
		t.Helper()
		s, err := Open(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		applyTestSchema(t, s)
		return s
	}

	t.Run("updates running and waiting_for_approval", func(t *testing.T) {
		s := newStore(t)
		insertPolicy(t, s, "p1")

		// These two statuses must be marked interrupted.
		insertRun(t, s, "r-running", "p1", "running")
		insertRun(t, s, "r-waiting", "p1", "waiting_for_approval")
		// All other statuses must be left untouched.
		insertRun(t, s, "r-pending", "p1", "pending")
		insertRun(t, s, "r-complete", "p1", "complete")
		insertRun(t, s, "r-failed", "p1", "failed")
		insertRun(t, s, "r-interrupted", "p1", "interrupted")

		before := time.Now()
		if err := s.MarkInterrupted(context.Background()); err != nil {
			t.Fatalf("MarkInterrupted: %v", err)
		}

		for _, id := range []string{"r-running", "r-waiting"} {
			var status, completedAt string
			err := s.DB().QueryRow(`SELECT status, completed_at FROM runs WHERE id = ?`, id).
				Scan(&status, &completedAt)
			if err != nil {
				t.Fatalf("query run %s: %v", id, err)
			}
			if status != "interrupted" {
				t.Errorf("run %s: status = %q, want %q", id, status, "interrupted")
			}
			ts, err := time.Parse(time.RFC3339Nano, completedAt)
			if err != nil {
				t.Errorf("run %s: completed_at %q not valid RFC3339Nano: %v", id, completedAt, err)
			} else if ts.Before(before) {
				t.Errorf("run %s: completed_at %v is before test start %v", id, ts, before)
			}
		}

		unchanged := map[string]string{
			"r-pending":     "pending",
			"r-complete":    "complete",
			"r-failed":      "failed",
			"r-interrupted": "interrupted",
		}
		for id, want := range unchanged {
			var got string
			if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = ?`, id).Scan(&got); err != nil {
				t.Fatalf("query run %s: %v", id, err)
			}
			if got != want {
				t.Errorf("run %s: status = %q, want %q (should be untouched)", id, got, want)
			}
		}
	})

	t.Run("no-op when no active runs", func(t *testing.T) {
		s := newStore(t)
		if err := s.MarkInterrupted(context.Background()); err != nil {
			t.Fatalf("MarkInterrupted on empty db: %v", err)
		}
	})
}

// checkPragmas asserts that WAL mode and foreign-key enforcement are active.
func checkPragmas(t *testing.T, s *Store) {
	t.Helper()

	var journalMode string
	if err := s.DB().QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	var foreignKeys int
	if err := s.DB().QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
}

// applyTestSchema creates the minimal tables needed for MarkInterrupted tests.
// Switch to s.Migrate() once issue #2 implements it.
func applyTestSchema(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.DB().Exec(`
		CREATE TABLE policies (
			id           TEXT PRIMARY KEY,
			name         TEXT NOT NULL UNIQUE,
			trigger_type TEXT NOT NULL,
			yaml         TEXT NOT NULL,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		);
		CREATE TABLE runs (
			id              TEXT PRIMARY KEY,
			policy_id       TEXT NOT NULL REFERENCES policies(id),
			status          TEXT NOT NULL CHECK(status IN (
								'pending','running','waiting_for_approval',
								'complete','failed','interrupted'
							)),
			trigger_type    TEXT NOT NULL,
			trigger_payload TEXT NOT NULL,
			started_at      TEXT NOT NULL,
			completed_at    TEXT,
			token_cost      INTEGER NOT NULL DEFAULT 0,
			error           TEXT,
			thread_id       TEXT,
			created_at      TEXT NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("apply test schema: %v", err)
	}
}

func insertPolicy(t *testing.T, s *Store, id string) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO policies(id, name, trigger_type, yaml, created_at, updated_at)
		 VALUES (?, ?, 'webhook', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		id, "policy-"+id,
	)
	if err != nil {
		t.Fatalf("insertPolicy %s: %v", id, err)
	}
}

func insertRun(t *testing.T, s *Store, id, policyID, status string) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
		 VALUES (?, ?, ?, 'webhook', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		id, policyID, status,
	)
	if err != nil {
		t.Fatalf("insertRun %s: %v", id, err)
	}
}
