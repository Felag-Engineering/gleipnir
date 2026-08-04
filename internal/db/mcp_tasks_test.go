package db

import (
	"context"
	"testing"
	"time"
)

func createTestMCPTask(t *testing.T, s *Store, id, runID, serverID, kind, status string) McpTask {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	pollInterval := int64(2000)
	serverTTL := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	task, err := s.Queries().CreateMCPTask(context.Background(), CreateMCPTaskParams{
		ID:             id,
		RunID:          runID,
		ServerID:       serverID,
		TaskID:         "server-task-" + id,
		Kind:           kind,
		PollIntervalMs: &pollInterval,
		ServerTtl:      &serverTTL,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		t.Fatalf("CreateMCPTask %s: %v", id, err)
	}
	if status == "working" {
		return task
	}

	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	switch status {
	case "expired":
		if _, err := s.Queries().ExpireMCPTask(context.Background(), ExpireMCPTaskParams{
			UpdatedAt: updatedAt,
			ID:        id,
		}); err != nil {
			t.Fatalf("ExpireMCPTask %s: %v", id, err)
		}
	default:
		// Any other status is treated as a terminal ResolveMCPTask transition
		// (complete, failed, cancelled, or input_required as an intermediate state).
		result := `{"outcome":"ok"}`
		if _, err := s.Queries().ResolveMCPTask(context.Background(), ResolveMCPTaskParams{
			Status:    status,
			Result:    &result,
			UpdatedAt: updatedAt,
			ID:        id,
		}); err != nil {
			t.Fatalf("ResolveMCPTask %s: %v", id, err)
		}
	}
	got, err := s.Queries().GetMCPTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMCPTask %s: %v", id, err)
	}
	return got
}

func TestCreateAndGetMCPTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")
	insertMcpServer(t, s, "srv1")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	pollInterval := int64(5000)
	created, err := s.Queries().CreateMCPTask(ctx, CreateMCPTaskParams{
		ID:             "task1",
		RunID:          "r1",
		ServerID:       "srv1",
		TaskID:         "remote-task-1",
		Kind:           "tool_call",
		PollIntervalMs: &pollInterval,
		ServerTtl:      nil,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		t.Fatalf("CreateMCPTask: %v", err)
	}
	if created.Status != "working" {
		t.Errorf("created status = %q, want working", created.Status)
	}
	if created.Kind != "tool_call" {
		t.Errorf("created kind = %q, want tool_call", created.Kind)
	}

	got, err := s.Queries().GetMCPTask(ctx, "task1")
	if err != nil {
		t.Fatalf("GetMCPTask: %v", err)
	}
	if got.TaskID != "remote-task-1" {
		t.Errorf("got task_id = %q, want remote-task-1", got.TaskID)
	}
	if got.Result != nil {
		t.Errorf("got result = %v, want nil before resolution", got.Result)
	}
}

func TestCreateMCPTaskChannelRequestKind(t *testing.T) {
	// mcp_tasks is deliberately reused by the channel-Request-as-task path
	// (spec sec 6.4); the channel_request kind must round-trip.
	ctx := context.Background()
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")
	insertMcpServer(t, s, "srv1")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	created, err := s.Queries().CreateMCPTask(ctx, CreateMCPTaskParams{
		ID:        "task-channel",
		RunID:     "r1",
		ServerID:  "srv1",
		TaskID:    "remote-task-channel",
		Kind:      "channel_request",
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateMCPTask: %v", err)
	}
	if created.Kind != "channel_request" {
		t.Errorf("kind = %q, want channel_request", created.Kind)
	}
}

func TestResolveMCPTask(t *testing.T) {
	ctx := context.Background()

	t.Run("working task resolves to complete with result", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r1", "p1", "running")
		insertMcpServer(t, s, "srv1")
		createTestMCPTask(t, s, "task1", "r1", "srv1", "tool_call", "working")

		result := `{"outcome":"paid"}`
		updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
		rows, err := s.Queries().ResolveMCPTask(ctx, ResolveMCPTaskParams{
			Status:    "complete",
			Result:    &result,
			UpdatedAt: updatedAt,
			ID:        "task1",
		})
		if err != nil {
			t.Fatalf("ResolveMCPTask: %v", err)
		}
		if rows != 1 {
			t.Fatalf("rows affected = %d, want 1", rows)
		}

		got, err := s.Queries().GetMCPTask(ctx, "task1")
		if err != nil {
			t.Fatalf("GetMCPTask: %v", err)
		}
		if got.Status != "complete" {
			t.Errorf("status = %q, want complete", got.Status)
		}
		if got.Result == nil || *got.Result != result {
			t.Errorf("result = %v, want %q", got.Result, result)
		}
	})

	t.Run("input_required task can also resolve to a terminal status", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r1", "p1", "running")
		insertMcpServer(t, s, "srv1")
		createTestMCPTask(t, s, "task1", "r1", "srv1", "tool_call", "input_required")

		result := `{"outcome":"failed"}`
		updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
		rows, err := s.Queries().ResolveMCPTask(ctx, ResolveMCPTaskParams{
			Status:    "failed",
			Result:    &result,
			UpdatedAt: updatedAt,
			ID:        "task1",
		})
		if err != nil {
			t.Fatalf("ResolveMCPTask: %v", err)
		}
		if rows != 1 {
			t.Fatalf("rows affected = %d, want 1", rows)
		}
	})

	t.Run("double resolve is a no-op on the second call", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r1", "p1", "running")
		insertMcpServer(t, s, "srv1")
		createTestMCPTask(t, s, "task1", "r1", "srv1", "tool_call", "working")

		result := "{}"
		updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := s.Queries().ResolveMCPTask(ctx, ResolveMCPTaskParams{
			Status:    "complete",
			Result:    &result,
			UpdatedAt: updatedAt,
			ID:        "task1",
		}); err != nil {
			t.Fatalf("first ResolveMCPTask: %v", err)
		}

		rows, err := s.Queries().ResolveMCPTask(ctx, ResolveMCPTaskParams{
			Status:    "failed",
			Result:    &result,
			UpdatedAt: updatedAt,
			ID:        "task1",
		})
		if err != nil {
			t.Fatalf("second ResolveMCPTask: %v", err)
		}
		if rows != 0 {
			t.Errorf("rows affected on second resolve = %d, want 0", rows)
		}

		got, err := s.Queries().GetMCPTask(ctx, "task1")
		if err != nil {
			t.Fatalf("GetMCPTask: %v", err)
		}
		if got.Status != "complete" {
			t.Errorf("status after double-resolve = %q, want complete (first write preserved)", got.Status)
		}
	})
}

func TestExpireMCPTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")
	insertMcpServer(t, s, "srv1")
	createTestMCPTask(t, s, "task1", "r1", "srv1", "tool_call", "working")

	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.Queries().ExpireMCPTask(ctx, ExpireMCPTaskParams{
		UpdatedAt: updatedAt,
		ID:        "task1",
	})
	if err != nil {
		t.Fatalf("ExpireMCPTask: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows affected = %d, want 1", rows)
	}

	got, err := s.Queries().GetMCPTask(ctx, "task1")
	if err != nil {
		t.Fatalf("GetMCPTask: %v", err)
	}
	if got.Status != "expired" {
		t.Errorf("status = %q, want expired", got.Status)
	}

	// An already-expired task cannot be expired again.
	rows, err = s.Queries().ExpireMCPTask(ctx, ExpireMCPTaskParams{
		UpdatedAt: updatedAt,
		ID:        "task1",
	})
	if err != nil {
		t.Fatalf("second ExpireMCPTask: %v", err)
	}
	if rows != 0 {
		t.Errorf("rows affected on already-expired task = %d, want 0", rows)
	}
}

func TestListResumableMCPTasks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")
	insertMcpServer(t, s, "srv1")

	createTestMCPTask(t, s, "task-working", "r1", "srv1", "tool_call", "working")
	createTestMCPTask(t, s, "task-input-required", "r1", "srv1", "tool_call", "input_required")
	createTestMCPTask(t, s, "task-complete", "r1", "srv1", "tool_call", "complete")
	createTestMCPTask(t, s, "task-failed", "r1", "srv1", "channel_request", "failed")
	createTestMCPTask(t, s, "task-cancelled", "r1", "srv1", "channel_request", "cancelled")
	createTestMCPTask(t, s, "task-expired", "r1", "srv1", "tool_call", "expired")

	resumable, err := s.Queries().ListResumableMCPTasks(ctx)
	if err != nil {
		t.Fatalf("ListResumableMCPTasks: %v", err)
	}
	if len(resumable) != 2 {
		t.Fatalf("resumable count = %d, want 2", len(resumable))
	}
	ids := map[string]bool{}
	for _, task := range resumable {
		ids[task.ID] = true
	}
	if !ids["task-working"] || !ids["task-input-required"] {
		t.Errorf("resumable ids = %v, want task-working and task-input-required", ids)
	}
}

func TestMCPTaskUniqueServerAndTaskID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")
	insertMcpServer(t, s, "srv1")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.Queries().CreateMCPTask(ctx, CreateMCPTaskParams{
		ID:        "task1",
		RunID:     "r1",
		ServerID:  "srv1",
		TaskID:    "same-remote-id",
		Kind:      "tool_call",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("first CreateMCPTask: %v", err)
	}

	_, err := s.Queries().CreateMCPTask(ctx, CreateMCPTaskParams{
		ID:        "task2",
		RunID:     "r1",
		ServerID:  "srv1",
		TaskID:    "same-remote-id",
		Kind:      "tool_call",
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err == nil {
		t.Fatal("expected UNIQUE(server_id, task_id) violation, got nil")
	}
}

func TestMCPTaskCascadesOnRunDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "running")
	insertMcpServer(t, s, "srv1")
	createTestMCPTask(t, s, "task1", "r1", "srv1", "tool_call", "working")

	if _, err := s.DB().ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, "r1"); err != nil {
		t.Fatalf("delete run: %v", err)
	}

	_, err := s.Queries().GetMCPTask(ctx, "task1")
	if err == nil {
		t.Fatal("expected error fetching an mcp_tasks row whose run was deleted (ON DELETE CASCADE)")
	}
}
