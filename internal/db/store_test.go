package db

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
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

// newTestStore opens a fresh in-memory-backed test DB and applies the schema.
// Use this in any test that needs a fully-migrated store.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	applyTestSchema(t, s)
	return s
}

func TestScanOrphanedRuns(t *testing.T) {
	// discardLogger silences log output during tests so scan results are
	// verified via DB state rather than log assertions.
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("zero orphans: returns nil, no steps created", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.ScanOrphanedRuns(context.Background(), discardLogger); err != nil {
			t.Fatalf("ScanOrphanedRuns on empty db: %v", err)
		}
		var count int64
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM run_steps`).Scan(&count); err != nil {
			t.Fatalf("count run_steps: %v", err)
		}
		if count != 0 {
			t.Errorf("expected 0 run_steps, got %d", count)
		}
	})

	t.Run("multiple orphans: running and waiting_for_approval get interrupted, complete is untouched", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r-running", "p1", "running")
		insertRun(t, s, "r-waiting", "p1", "waiting_for_approval")
		insertRun(t, s, "r-complete", "p1", "complete")

		before := time.Now()
		if err := s.ScanOrphanedRuns(context.Background(), discardLogger); err != nil {
			t.Fatalf("ScanOrphanedRuns: %v", err)
		}

		// Both orphaned runs must be interrupted with a completed_at, error, and an error step.
		for _, id := range []string{"r-running", "r-waiting"} {
			var status, completedAt, errField string
			err := s.DB().QueryRow(
				`SELECT status, completed_at, error FROM runs WHERE id = ?`, id,
			).Scan(&status, &completedAt, &errField)
			if err != nil {
				t.Fatalf("query run %s: %v", id, err)
			}
			if status != "interrupted" {
				t.Errorf("run %s: status = %q, want %q", id, status, "interrupted")
			}
			if errField == "" {
				t.Errorf("run %s: error field is empty", id)
			}
			ts, err := time.Parse(time.RFC3339Nano, completedAt)
			if err != nil {
				t.Errorf("run %s: completed_at %q not valid RFC3339Nano: %v", id, completedAt, err)
			} else if ts.Before(before) {
				t.Errorf("run %s: completed_at %v is before test start %v", id, ts, before)
			}

			// Each orphaned run must have exactly one error step with the expected content.
			var stepType, content string
			err = s.DB().QueryRow(
				`SELECT type, content FROM run_steps WHERE run_id = ?`, id,
			).Scan(&stepType, &content)
			if err != nil {
				t.Fatalf("query run_step for %s: %v", id, err)
			}
			if stepType != "error" {
				t.Errorf("run %s: step type = %q, want %q", id, stepType, "error")
			}
			var payload map[string]string
			if err := json.Unmarshal([]byte(content), &payload); err != nil {
				t.Errorf("run %s: step content not valid JSON: %v", id, err)
			} else {
				if payload["code"] != "interrupted" {
					t.Errorf("run %s: step content code = %q, want %q", id, payload["code"], "interrupted")
				}
				if payload["message"] == "" {
					t.Errorf("run %s: step content message is empty", id)
				}
			}
		}

		// The complete run must be entirely untouched.
		var status string
		if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r-complete'`).Scan(&status); err != nil {
			t.Fatalf("query r-complete: %v", err)
		}
		if status != "complete" {
			t.Errorf("r-complete: status = %q, want %q (should be untouched)", status, "complete")
		}
		var stepCount int64
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM run_steps WHERE run_id = 'r-complete'`).Scan(&stepCount); err != nil {
			t.Fatalf("count steps for r-complete: %v", err)
		}
		if stepCount != 0 {
			t.Errorf("r-complete: got %d run_steps, want 0", stepCount)
		}
	})

	t.Run("orphaned run with existing steps: error step gets next step_number", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r1", "p1", "running")

		// Pre-insert 2 steps so the error step should get step_number = 2.
		_, err := s.DB().Exec(
			`INSERT INTO run_steps(id, run_id, step_number, type, content, created_at)
			 VALUES ('s1', 'r1', 0, 'thought', '{}', '2024-01-01T00:00:00Z'),
			        ('s2', 'r1', 1, 'tool_call', '{}', '2024-01-01T00:00:00Z')`,
		)
		if err != nil {
			t.Fatalf("insert existing steps: %v", err)
		}

		if err := s.ScanOrphanedRuns(context.Background(), discardLogger); err != nil {
			t.Fatalf("ScanOrphanedRuns: %v", err)
		}

		var stepNumber int64
		err = s.DB().QueryRow(
			`SELECT step_number FROM run_steps WHERE run_id = 'r1' AND type = 'error'`,
		).Scan(&stepNumber)
		if err != nil {
			t.Fatalf("query error step: %v", err)
		}
		if stepNumber != 2 {
			t.Errorf("error step step_number = %d, want 2", stepNumber)
		}
	})

	t.Run("partial failure: second run still gets marked when first fails", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r1", "p1", "running")
		insertRun(t, s, "r2", "p1", "running")

		// Install a trigger that makes any run_step INSERT for r1 fail. This
		// simulates a DB error mid-scan without needing mocks or concurrency.
		_, err := s.DB().Exec(`
			CREATE TRIGGER fail_r1_step
			BEFORE INSERT ON run_steps
			WHEN NEW.run_id = 'r1'
			BEGIN SELECT RAISE(FAIL, 'injected failure for test'); END
		`)
		if err != nil {
			t.Fatalf("create trigger: %v", err)
		}

		// ScanOrphanedRuns must return nil even though r1 fails — errors are logged.
		if err := s.ScanOrphanedRuns(context.Background(), discardLogger); err != nil {
			t.Fatalf("ScanOrphanedRuns returned error, want nil: %v", err)
		}

		// r1 is NOT interrupted because its step insertion failed and the run was skipped.
		var r1Status string
		if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r1'`).Scan(&r1Status); err != nil {
			t.Fatalf("query r1: %v", err)
		}
		if r1Status != "running" {
			t.Errorf("r1: status = %q, want %q (step insertion should have failed)", r1Status, "running")
		}

		// r2 is interrupted despite r1's failure.
		var r2Status string
		if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'r2'`).Scan(&r2Status); err != nil {
			t.Fatalf("query r2: %v", err)
		}
		if r2Status != "interrupted" {
			t.Errorf("r2: status = %q, want %q (should be marked even after r1 failure)", r2Status, "interrupted")
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

func applyTestSchema(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("apply test schema: %v", err)
	}
}

func TestMigrate(t *testing.T) {
	newStore := func(t *testing.T) *Store {
		t.Helper()
		s, err := Open(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}

	t.Run("clean state", func(t *testing.T) {
		s := newStore(t)
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		// SELECT version = 1 in the WHERE clause means the predicate is already
		// the assertion; use COUNT so the check fails visibly if the row is absent.
		var count int
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 1`).Scan(&count); err != nil {
			t.Fatalf("query schema_migrations: %v", err)
		}
		if count != 1 {
			t.Errorf("schema_migrations: version 1 row not found")
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		s := newStore(t)
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("first Migrate: %v", err)
		}
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("second Migrate: %v", err)
		}
	})

	t.Run("tables exist", func(t *testing.T) {
		s := newStore(t)
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		want := []string{
			"schema_migrations",
			"mcp_servers",
			"mcp_tools",
			"policies",
			"runs",
			"run_steps",
			"approval_requests",
			"users",
			"sessions",
			"user_roles",
		}
		for _, table := range want {
			var count int
			err := s.DB().QueryRow(
				`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table,
			).Scan(&count)
			if err != nil {
				t.Fatalf("check table %s: %v", table, err)
			}
			if count != 1 {
				t.Errorf("table %q not found in sqlite_master", table)
			}
		}
	})
}

func TestSchemaConstraints(t *testing.T) {
	t.Run("CHECK constraints reject invalid enum values", func(t *testing.T) {
		cases := []struct {
			name  string
			setup func(t *testing.T, s *Store)
			exec  func(s *Store) error
		}{
			{
				name: "policies.trigger_type invalid",
				exec: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO policies(id, name, trigger_type, yaml, created_at, updated_at)
						 VALUES ('p1', 'p', 'invalid_type', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
			{
				name: "runs.status invalid",
				setup: func(t *testing.T, s *Store) {
					insertPolicy(t, s, "p1")
				},
				exec: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
						 VALUES ('r1', 'p1', 'invalid_status', 'webhook', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
			{
				name: "runs.trigger_type invalid",
				setup: func(t *testing.T, s *Store) {
					insertPolicy(t, s, "p1")
				},
				exec: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
						 VALUES ('r1', 'p1', 'pending', 'invalid_trigger', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
			{
				name: "run_steps.type invalid",
				setup: func(t *testing.T, s *Store) {
					insertPolicy(t, s, "p1")
					insertRun(t, s, "r1", "p1", "running")
				},
				exec: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO run_steps(id, run_id, step_number, type, content, created_at)
						 VALUES ('s1', 'r1', 0, 'invalid_type', '{}', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
			{
				name: "approval_requests.status invalid",
				setup: func(t *testing.T, s *Store) {
					insertPolicy(t, s, "p1")
					insertRun(t, s, "r1", "p1", "waiting_for_approval")
				},
				exec: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO approval_requests(id, run_id, tool_name, proposed_input, reasoning_summary, status, expires_at, created_at)
						 VALUES ('a1', 'r1', 'tool', '{}', 'summary', 'invalid_status', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				s := newTestStore(t)
				if tc.setup != nil {
					tc.setup(t, s)
				}
				if err := tc.exec(s); err == nil {
					t.Error("expected error for invalid enum value, got nil")
				}
			})
		}
	})

	t.Run("FOREIGN KEY constraints reject missing parents", func(t *testing.T) {
		cases := []struct {
			name string
			exec func(s *Store) error
		}{
			{
				name: "mcp_tools.server_id nonexistent",
				exec: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO mcp_tools(id, server_id, name, description, input_schema, created_at)
						 VALUES ('t1', 'nonexistent', 'tool', 'desc', '{}', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
			{
				name: "runs.policy_id nonexistent",
				exec: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
						 VALUES ('r1', 'nonexistent', 'pending', 'webhook', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
			{
				name: "run_steps.run_id nonexistent",
				exec: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO run_steps(id, run_id, step_number, type, content, created_at)
						 VALUES ('s1', 'nonexistent', 0, 'thought', '{}', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
			{
				name: "approval_requests.run_id nonexistent",
				exec: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO approval_requests(id, run_id, tool_name, proposed_input, reasoning_summary, status, expires_at, created_at)
						 VALUES ('a1', 'nonexistent', 'tool', '{}', 'summary', 'pending', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				s := newTestStore(t)
				if err := tc.exec(s); err == nil {
					t.Error("expected foreign key error, got nil")
				}
			})
		}
	})

	t.Run("UNIQUE constraints reject duplicates", func(t *testing.T) {
		cases := []struct {
			name   string
			setup  func(t *testing.T, s *Store)
			first  func(s *Store) error
			second func(s *Store) error
		}{
			{
				name: "policies.name",
				first: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO policies(id, name, trigger_type, yaml, created_at, updated_at)
						 VALUES ('p1', 'same-name', 'webhook', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
				second: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO policies(id, name, trigger_type, yaml, created_at, updated_at)
						 VALUES ('p2', 'same-name', 'webhook', '{}', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
			{
				name: "mcp_servers.name",
				first: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO mcp_servers(id, name, url, created_at)
						 VALUES ('s1', 'same-name', 'http://localhost:8080', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
				second: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO mcp_servers(id, name, url, created_at)
						 VALUES ('s2', 'same-name', 'http://localhost:8081', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
			{
				name: "mcp_tools(server_id, name)",
				setup: func(t *testing.T, s *Store) {
					insertMcpServer(t, s, "srv1")
				},
				first: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO mcp_tools(id, server_id, name, description, input_schema, created_at)
						 VALUES ('t1', 'srv1', 'tool', 'desc', '{}', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
				second: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO mcp_tools(id, server_id, name, description, input_schema, created_at)
						 VALUES ('t2', 'srv1', 'tool', 'desc', '{}', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
			{
				name: "run_steps(run_id, step_number)",
				setup: func(t *testing.T, s *Store) {
					insertPolicy(t, s, "p1")
					insertRun(t, s, "r1", "p1", "running")
				},
				first: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO run_steps(id, run_id, step_number, type, content, created_at)
						 VALUES ('s1', 'r1', 0, 'thought', '{}', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
				second: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO run_steps(id, run_id, step_number, type, content, created_at)
						 VALUES ('s2', 'r1', 0, 'thought', '{}', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				s := newTestStore(t)
				if tc.setup != nil {
					tc.setup(t, s)
				}
				if err := tc.first(s); err != nil {
					t.Fatalf("first insert: %v", err)
				}
				if err := tc.second(s); err == nil {
					t.Error("expected unique constraint error, got nil")
				}
			})
		}
	})
}

func TestMigrateAddThinkingStepType(t *testing.T) {
	t.Run("adds thinking to CHECK constraint when absent", func(t *testing.T) {
		s, err := Open(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { s.Close() })

		// Apply the old schema without 'thinking' in the CHECK constraint.
		oldSchema := `
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations VALUES (1, '2024-01-01T00:00:00Z');
CREATE TABLE policies (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, trigger_type TEXT NOT NULL, yaml TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE runs (id TEXT PRIMARY KEY, policy_id TEXT NOT NULL REFERENCES policies(id) ON DELETE CASCADE, status TEXT NOT NULL, trigger_type TEXT NOT NULL, trigger_payload TEXT NOT NULL, started_at TEXT NOT NULL, completed_at TEXT, token_cost INTEGER NOT NULL DEFAULT 0, error TEXT, thread_id TEXT, created_at TEXT NOT NULL);
CREATE TABLE run_steps (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_number INTEGER NOT NULL,
    type TEXT NOT NULL CHECK(type IN ('capability_snapshot','thought','tool_call','tool_result','approval_request','feedback_request','feedback_response','error','complete')),
    content TEXT NOT NULL,
    token_cost INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    UNIQUE(run_id, step_number)
);
CREATE INDEX idx_run_steps_run_step ON run_steps(run_id, step_number);
`
		if _, err := s.db.Exec(oldSchema); err != nil {
			t.Fatalf("apply old schema: %v", err)
		}

		// Run the inline migration.
		if err := s.migrateAddThinkingStepType(context.Background()); err != nil {
			t.Fatalf("migrateAddThinkingStepType: %v", err)
		}

		// Insert a policy and run so we can insert a run_step.
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r1", "p1", "running")

		// Verify that inserting a 'thinking' step now succeeds.
		_, err = s.db.Exec(
			`INSERT INTO run_steps(id, run_id, step_number, type, content, created_at)
			 VALUES ('s1', 'r1', 0, 'thinking', '{}', '2024-01-01T00:00:00Z')`,
		)
		if err != nil {
			t.Errorf("expected thinking insert to succeed after migration, got: %v", err)
		}
	})

	t.Run("idempotent when thinking already present", func(t *testing.T) {
		s := newTestStore(t) // schema already includes 'thinking'

		if err := s.migrateAddThinkingStepType(context.Background()); err != nil {
			t.Fatalf("first migrateAddThinkingStepType: %v", err)
		}
		if err := s.migrateAddThinkingStepType(context.Background()); err != nil {
			t.Fatalf("second migrateAddThinkingStepType: %v", err)
		}
	})
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

func insertMcpServer(t *testing.T, s *Store, id string) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO mcp_servers(id, name, url, created_at) VALUES (?, ?, ?, ?)`,
		id, "server-"+id, "http://localhost:8080", "2024-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insertMcpServer %s: %v", id, err)
	}
}
