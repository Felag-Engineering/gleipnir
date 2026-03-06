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
						 VALUES ('s1', 'r1', 1, 'invalid_type', '{}', '2024-01-01T00:00:00Z')`,
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
			{
				name: "mcp_tools.capability_role invalid",
				setup: func(t *testing.T, s *Store) {
					insertMcpServer(t, s, "srv1")
				},
				exec: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO mcp_tools(id, server_id, name, description, input_schema, capability_role, created_at)
						 VALUES ('t1', 'srv1', 'tool', 'desc', '{}', 'invalid_role', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				s := newStore(t)
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
						`INSERT INTO mcp_tools(id, server_id, name, description, input_schema, capability_role, created_at)
						 VALUES ('t1', 'nonexistent', 'tool', 'desc', '{}', 'sensor', '2024-01-01T00:00:00Z')`,
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
						 VALUES ('s1', 'nonexistent', 1, 'thought', '{}', '2024-01-01T00:00:00Z')`,
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
				s := newStore(t)
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
						`INSERT INTO mcp_tools(id, server_id, name, description, input_schema, capability_role, created_at)
						 VALUES ('t1', 'srv1', 'tool', 'desc', '{}', 'sensor', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
				second: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO mcp_tools(id, server_id, name, description, input_schema, capability_role, created_at)
						 VALUES ('t2', 'srv1', 'tool', 'desc', '{}', 'sensor', '2024-01-01T00:00:00Z')`,
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
						 VALUES ('s1', 'r1', 1, 'thought', '{}', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
				second: func(s *Store) error {
					_, err := s.DB().Exec(
						`INSERT INTO run_steps(id, run_id, step_number, type, content, created_at)
						 VALUES ('s2', 'r1', 1, 'thought', '{}', '2024-01-01T00:00:00Z')`,
					)
					return err
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				s := newStore(t)
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
