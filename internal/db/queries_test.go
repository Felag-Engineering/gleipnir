package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestCountActiveRuns(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	t.Run("empty db returns 0", func(t *testing.T) {
		s := newTestStore(t)
		got, err := s.CountActiveRuns(ctx)
		if err != nil {
			t.Fatalf("CountActiveRuns: %v", err)
		}
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})

	t.Run("counts pending running and waiting_for_approval", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")
		for _, row := range []struct {
			id     string
			status string
		}{
			{"r-pending", "pending"},
			{"r-running", "running"},
			{"r-waiting", "waiting_for_approval"},
			{"r-complete", "complete"},
			{"r-failed", "failed"},
		} {
			_, err := s.DB().ExecContext(ctx,
				`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
				 VALUES (?, 'pol1', ?, 'webhook', '{}', ?, ?)`,
				row.id, row.status, now, now,
			)
			if err != nil {
				t.Fatalf("insert run %s: %v", row.id, err)
			}
		}
		got, err := s.CountActiveRuns(ctx)
		if err != nil {
			t.Fatalf("CountActiveRuns: %v", err)
		}
		if got != 3 {
			t.Errorf("got %d, want 3", got)
		}
	})
}

func TestSumTokensLast24Hours(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("no runs returns 0", func(t *testing.T) {
		s := newTestStore(t)
		since := now.Add(-24 * time.Hour).Format(time.RFC3339Nano)
		tokens, err := s.SumTokensLast24Hours(ctx, since)
		if err != nil {
			t.Fatalf("SumTokensLast24Hours: %v", err)
		}
		if tokens != 0 {
			t.Errorf("got %d, want 0", tokens)
		}
	})

	t.Run("runs within 24h are summed", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")
		recent := now.Add(-1 * time.Hour).Format(time.RFC3339Nano)
		for _, row := range []struct {
			id   string
			cost int64
		}{
			{"run1", 1000},
			{"run2", 500},
		} {
			_, err := s.DB().ExecContext(ctx,
				`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at, token_cost)
				 VALUES (?, 'pol1', 'complete', 'webhook', '{}', ?, ?, ?)`,
				row.id, recent, recent, row.cost,
			)
			if err != nil {
				t.Fatalf("insert run %s: %v", row.id, err)
			}
		}
		since := now.Add(-24 * time.Hour).Format(time.RFC3339Nano)
		tokens, err := s.SumTokensLast24Hours(ctx, since)
		if err != nil {
			t.Fatalf("SumTokensLast24Hours: %v", err)
		}
		if tokens != 1500 {
			t.Errorf("got %d, want 1500", tokens)
		}
	})

	t.Run("runs older than 24h are excluded", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")
		old := "2020-01-01T00:00:00Z"
		_, err := s.DB().ExecContext(ctx,
			`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at, token_cost)
			 VALUES ('run-old', 'pol1', 'complete', 'webhook', '{}', ?, ?, 9999)`,
			old, old,
		)
		if err != nil {
			t.Fatalf("insert old run: %v", err)
		}
		since := now.Add(-24 * time.Hour).Format(time.RFC3339Nano)
		tokens, err := s.SumTokensLast24Hours(ctx, since)
		if err != nil {
			t.Fatalf("SumTokensLast24Hours: %v", err)
		}
		if tokens != 0 {
			t.Errorf("got %d, want 0 (old run should be excluded)", tokens)
		}
	})

	t.Run("multiple runs for same policy all counted", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")
		recent := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
		for i, id := range []string{"r1", "r2", "r3"} {
			_, err := s.DB().ExecContext(ctx,
				`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at, token_cost)
				 VALUES (?, 'pol1', 'complete', 'webhook', '{}', ?, ?, ?)`,
				id, recent, recent, (i+1)*100,
			)
			if err != nil {
				t.Fatalf("insert run %s: %v", id, err)
			}
		}
		since := now.Add(-24 * time.Hour).Format(time.RFC3339Nano)
		tokens, err := s.SumTokensLast24Hours(ctx, since)
		if err != nil {
			t.Fatalf("SumTokensLast24Hours: %v", err)
		}
		if tokens != 600 { // 100+200+300
			t.Errorf("got %d, want 600", tokens)
		}
	})
}

func TestCountPolicies(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	t.Run("empty db returns 0", func(t *testing.T) {
		s := newTestStore(t)
		got, err := s.CountPolicies(ctx)
		if err != nil {
			t.Fatalf("CountPolicies: %v", err)
		}
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})

	t.Run("returns count of inserted policies", func(t *testing.T) {
		s := newTestStore(t)
		for _, id := range []string{"pol1", "pol2"} {
			if _, err := s.CreatePolicy(ctx, CreatePolicyParams{
				ID:          id,
				Name:        id,
				TriggerType: "webhook",
				Yaml:        "trigger: webhook",
				CreatedAt:   now,
				UpdatedAt:   now,
			}); err != nil {
				t.Fatalf("CreatePolicy %s: %v", id, err)
			}
		}
		got, err := s.CountPolicies(ctx)
		if err != nil {
			t.Fatalf("CountPolicies: %v", err)
		}
		if got != 2 {
			t.Errorf("got %d, want 2", got)
		}
	})
}

func TestCountPendingApprovalRequests(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	futureExpiry := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)

	t.Run("empty db returns 0", func(t *testing.T) {
		s := newTestStore(t)
		got, err := s.CountPendingApprovalRequests(ctx)
		if err != nil {
			t.Fatalf("CountPendingApprovalRequests: %v", err)
		}
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})

	t.Run("counts only pending not approved", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")
		insertRun(t, s, "run1", "pol1", "waiting_for_approval")

		// Insert one pending and one approved request.
		if _, err := s.CreateApprovalRequest(ctx, CreateApprovalRequestParams{
			ID:               "ar1",
			RunID:            "run1",
			ToolName:         "bash",
			ProposedInput:    `{}`,
			ReasoningSummary: "pending one",
			ExpiresAt:        futureExpiry,
			CreatedAt:        now,
		}); err != nil {
			t.Fatalf("CreateApprovalRequest ar1: %v", err)
		}
		if _, err := s.CreateApprovalRequest(ctx, CreateApprovalRequestParams{
			ID:               "ar2",
			RunID:            "run1",
			ToolName:         "curl",
			ProposedInput:    `{}`,
			ReasoningSummary: "will be approved",
			ExpiresAt:        futureExpiry,
			CreatedAt:        now,
		}); err != nil {
			t.Fatalf("CreateApprovalRequest ar2: %v", err)
		}
		decidedAt := now
		note := "ok"
		if err := s.UpdateApprovalRequestStatus(ctx, UpdateApprovalRequestStatusParams{
			Status:    "approved",
			DecidedAt: &decidedAt,
			Note:      &note,
			ID:        "ar2",
		}); err != nil {
			t.Fatalf("UpdateApprovalRequestStatus: %v", err)
		}

		got, err := s.CountPendingApprovalRequests(ctx)
		if err != nil {
			t.Fatalf("CountPendingApprovalRequests: %v", err)
		}
		if got != 1 {
			t.Errorf("got %d, want 1", got)
		}
	})
}

func TestHasScheduledRunSince(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("no runs returns 0", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")
		since := now.Add(-time.Minute).Format(time.RFC3339Nano)
		got, err := s.HasScheduledRunSince(ctx, HasScheduledRunSinceParams{
			PolicyID: "pol1",
			Since:    since,
		})
		if err != nil {
			t.Fatalf("HasScheduledRunSince: %v", err)
		}
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})

	t.Run("run created after since returns 1", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")
		runAt := now.Format(time.RFC3339Nano)
		_, err := s.DB().ExecContext(ctx,
			`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
			 VALUES ('run1', 'pol1', 'complete', 'scheduled', '{}', ?, ?)`,
			runAt, runAt,
		)
		if err != nil {
			t.Fatalf("insert scheduled run: %v", err)
		}
		since := now.Add(-time.Minute).Format(time.RFC3339Nano)
		got, err := s.HasScheduledRunSince(ctx, HasScheduledRunSinceParams{
			PolicyID: "pol1",
			Since:    since,
		})
		if err != nil {
			t.Fatalf("HasScheduledRunSince: %v", err)
		}
		if got != 1 {
			t.Errorf("got %d, want 1", got)
		}
	})

	t.Run("run created before since returns 0", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")
		oldAt := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
		_, err := s.DB().ExecContext(ctx,
			`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
			 VALUES ('run1', 'pol1', 'complete', 'scheduled', '{}', ?, ?)`,
			oldAt, oldAt,
		)
		if err != nil {
			t.Fatalf("insert old scheduled run: %v", err)
		}
		since := now.Add(-time.Minute).Format(time.RFC3339Nano)
		got, err := s.HasScheduledRunSince(ctx, HasScheduledRunSinceParams{
			PolicyID: "pol1",
			Since:    since,
		})
		if err != nil {
			t.Fatalf("HasScheduledRunSince: %v", err)
		}
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})

	t.Run("webhook run after since is not counted", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")
		runAt := now.Format(time.RFC3339Nano)
		_, err := s.DB().ExecContext(ctx,
			`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
			 VALUES ('run1', 'pol1', 'complete', 'webhook', '{}', ?, ?)`,
			runAt, runAt,
		)
		if err != nil {
			t.Fatalf("insert webhook run: %v", err)
		}
		since := now.Add(-time.Minute).Format(time.RFC3339Nano)
		got, err := s.HasScheduledRunSince(ctx, HasScheduledRunSinceParams{
			PolicyID: "pol1",
			Since:    since,
		})
		if err != nil {
			t.Fatalf("HasScheduledRunSince: %v", err)
		}
		if got != 0 {
			t.Errorf("got %d, want 0 (webhook runs must not trigger dedup)", got)
		}
	})
}

func TestMCPServerQueries(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	srv, err := s.CreateMCPServer(ctx, CreateMCPServerParams{
		ID:        "srv1",
		Name:      "server-one",
		Url:       "http://localhost:8080",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}
	if srv.ID != "srv1" || srv.Name != "server-one" || srv.Url != "http://localhost:8080" {
		t.Errorf("CreateMCPServer fields mismatch: %+v", srv)
	}

	if srv.LastDiscoveredAt != nil {
		t.Errorf("CreateMCPServer: last_discovered_at = %v, want nil", srv.LastDiscoveredAt)
	}

	got, err := s.GetMCPServer(ctx, "srv1")
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	if got.ID != srv.ID || got.Name != srv.Name {
		t.Errorf("GetMCPServer mismatch: got %+v, want %+v", got, srv)
	}

	discoveredAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.UpdateMCPServerLastDiscovered(ctx, UpdateMCPServerLastDiscoveredParams{
		LastDiscoveredAt: &discoveredAt,
		ID:               "srv1",
	}); err != nil {
		t.Fatalf("UpdateMCPServerLastDiscovered: %v", err)
	}
	got, err = s.GetMCPServer(ctx, "srv1")
	if err != nil {
		t.Fatalf("GetMCPServer after UpdateMCPServerLastDiscovered: %v", err)
	}
	if got.LastDiscoveredAt == nil || *got.LastDiscoveredAt != discoveredAt {
		t.Errorf("LastDiscoveredAt after update: got %v, want %q", got.LastDiscoveredAt, discoveredAt)
	}

	if _, err := s.CreateMCPServer(ctx, CreateMCPServerParams{
		ID:        "srv2",
		Name:      "server-two",
		Url:       "http://localhost:8081",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateMCPServer srv2: %v", err)
	}

	servers, err := s.ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("ListMCPServers: %v", err)
	}
	if len(servers) != 2 {
		t.Errorf("ListMCPServers: got %d, want 2", len(servers))
	}

	if err := s.DeleteMCPServer(ctx, "srv1"); err != nil {
		t.Fatalf("DeleteMCPServer: %v", err)
	}

	servers, err = s.ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("ListMCPServers after delete: %v", err)
	}
	if len(servers) != 1 {
		t.Errorf("ListMCPServers after delete: got %d, want 1", len(servers))
	}

	_, err = s.GetMCPServer(ctx, "srv1")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetMCPServer deleted: got %v, want sql.ErrNoRows", err)
	}
}

func TestMCPToolQueries(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	insertMcpServer(t, s, "srv1")

	tool, err := s.UpsertMCPTool(ctx, UpsertMCPToolParams{
		ID:             "tool1",
		ServerID:       "srv1",
		Name:           "alpha",
		Description:    "first tool",
		InputSchema:    "{}",
		CapabilityRole: "tool",
		CreatedAt:      now,
	})
	if err != nil {
		t.Fatalf("UpsertMCPTool: %v", err)
	}
	if tool.ID != "tool1" || tool.CapabilityRole != "tool" {
		t.Errorf("UpsertMCPTool fields mismatch: %+v", tool)
	}

	got, err := s.GetMCPTool(ctx, "tool1")
	if err != nil {
		t.Fatalf("GetMCPTool: %v", err)
	}
	if got.ID != "tool1" || got.Name != "alpha" {
		t.Errorf("GetMCPTool mismatch: %+v", got)
	}

	if _, err := s.UpsertMCPTool(ctx, UpsertMCPToolParams{
		ID:             "tool2",
		ServerID:       "srv1",
		Name:           "beta",
		Description:    "second tool",
		InputSchema:    "{}",
		CapabilityRole: "tool",
		CreatedAt:      now,
	}); err != nil {
		t.Fatalf("UpsertMCPTool tool2: %v", err)
	}

	tools, err := s.ListMCPToolsByServer(ctx, "srv1")
	if err != nil {
		t.Fatalf("ListMCPToolsByServer: %v", err)
	}
	if len(tools) != 2 {
		t.Errorf("ListMCPToolsByServer: got %d, want 2", len(tools))
	}
	// ordered by name ASC: alpha, beta
	if tools[0].Name != "alpha" || tools[1].Name != "beta" {
		t.Errorf("ListMCPToolsByServer order wrong: %v, %v", tools[0].Name, tools[1].Name)
	}

	if err := s.UpdateMCPToolCapabilityRole(ctx, UpdateMCPToolCapabilityRoleParams{
		CapabilityRole: "feedback",
		ID:             "tool1",
	}); err != nil {
		t.Fatalf("UpdateMCPToolCapabilityRole: %v", err)
	}

	got, err = s.GetMCPTool(ctx, "tool1")
	if err != nil {
		t.Fatalf("GetMCPTool after update: %v", err)
	}
	if got.CapabilityRole != "feedback" {
		t.Errorf("CapabilityRole after update: got %q, want %q", got.CapabilityRole, "feedback")
	}

	// Conflict/update path: upsert same server+name — should update, not insert.
	upserted, err := s.UpsertMCPTool(ctx, UpsertMCPToolParams{
		ID:             "tool3",
		ServerID:       "srv1",
		Name:           "alpha", // same server+name as tool1
		Description:    "updated desc",
		InputSchema:    `{"type":"object"}`,
		CapabilityRole: "tool",
		CreatedAt:      now,
	})
	if err != nil {
		t.Fatalf("UpsertMCPTool conflict path: %v", err)
	}
	// RETURNING * must reflect the updated columns, not the attempted insert values.
	if upserted.Description != "updated desc" || upserted.InputSchema != `{"type":"object"}` || upserted.CapabilityRole != "tool" {
		t.Errorf("UpsertMCPTool conflict path: returned row wrong: %+v", upserted)
	}

	tools, err = s.ListMCPToolsByServer(ctx, "srv1")
	if err != nil {
		t.Fatalf("ListMCPToolsByServer after conflict upsert: %v", err)
	}
	if len(tools) != 2 {
		t.Errorf("ListMCPToolsByServer after conflict upsert: got %d, want 2", len(tools))
	}

	// GetMCPToolByServerAndName is the only join query in the layer; verify it resolves correctly.
	byName, err := s.GetMCPToolByServerAndName(ctx, GetMCPToolByServerAndNameParams{
		ServerName: "server-srv1",
		ToolName:   "beta",
	})
	if err != nil {
		t.Fatalf("GetMCPToolByServerAndName: %v", err)
	}
	if byName.ID != "tool2" || byName.ServerID != "srv1" {
		t.Errorf("GetMCPToolByServerAndName: got %+v", byName)
	}

	_, err = s.GetMCPToolByServerAndName(ctx, GetMCPToolByServerAndNameParams{
		ServerName: "server-srv1",
		ToolName:   "nonexistent",
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetMCPToolByServerAndName missing: got %v, want sql.ErrNoRows", err)
	}

	if err := s.DeleteMCPToolsByServer(ctx, "srv1"); err != nil {
		t.Fatalf("DeleteMCPToolsByServer: %v", err)
	}

	tools, err = s.ListMCPToolsByServer(ctx, "srv1")
	if err != nil {
		t.Fatalf("ListMCPToolsByServer after delete: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("ListMCPToolsByServer after delete: got %d, want 0", len(tools))
	}
}

func TestPolicyQueries(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	p, err := s.CreatePolicy(ctx, CreatePolicyParams{
		ID:          "pol1",
		Name:        "my-policy",
		TriggerType: "webhook",
		Yaml:        "trigger: webhook",
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	if p.ID != "pol1" || p.Name != "my-policy" || p.TriggerType != "webhook" {
		t.Errorf("CreatePolicy fields mismatch: %+v", p)
	}

	got, err := s.GetPolicy(ctx, "pol1")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if got.ID != "pol1" || got.Yaml != "trigger: webhook" {
		t.Errorf("GetPolicy mismatch: %+v", got)
	}

	byName, err := s.GetPolicyByName(ctx, "my-policy")
	if err != nil {
		t.Fatalf("GetPolicyByName: %v", err)
	}
	if byName.ID != "pol1" {
		t.Errorf("GetPolicyByName: got ID %q, want %q", byName.ID, "pol1")
	}

	if _, err := s.CreatePolicy(ctx, CreatePolicyParams{
		ID:          "pol2",
		Name:        "other-policy",
		TriggerType: "webhook",
		Yaml:        "trigger: webhook",
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("CreatePolicy pol2: %v", err)
	}

	policies, err := s.ListPolicies(ctx)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(policies) != 2 {
		t.Errorf("ListPolicies: got %d, want 2", len(policies))
	}

	later := time.Now().UTC().Add(time.Millisecond).Format(time.RFC3339Nano)
	updated, err := s.UpdatePolicy(ctx, UpdatePolicyParams{
		Name:        "policy-one",
		TriggerType: "webhook",
		Yaml:        "trigger: webhook\nversion: 2",
		UpdatedAt:   later,
		ID:          "pol1",
	})
	if err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	if updated.Yaml != "trigger: webhook\nversion: 2" {
		t.Errorf("UpdatePolicy returned wrong yaml: %q", updated.Yaml)
	}

	confirmed, err := s.GetPolicy(ctx, "pol1")
	if err != nil {
		t.Fatalf("GetPolicy after update: %v", err)
	}
	if confirmed.Yaml != "trigger: webhook\nversion: 2" || confirmed.UpdatedAt != later {
		t.Errorf("GetPolicy after update: yaml=%q updated_at=%q", confirmed.Yaml, confirmed.UpdatedAt)
	}

	if err := s.DeletePolicy(ctx, "pol1"); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}

	_, err = s.GetPolicy(ctx, "pol1")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetPolicy after delete: got %v, want sql.ErrNoRows", err)
	}

	policies, err = s.ListPolicies(ctx)
	if err != nil {
		t.Fatalf("ListPolicies after delete: %v", err)
	}
	if len(policies) != 1 {
		t.Errorf("ListPolicies after delete: got %d, want 1", len(policies))
	}
}

func TestRunQueries(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	insertPolicy(t, s, "pol1")

	run, err := s.CreateRun(ctx, CreateRunParams{
		ID:             "run1",
		PolicyID:       "pol1",
		TriggerType:    "webhook",
		TriggerPayload: `{"event":"push"}`,
		StartedAt:      now,
		CreatedAt:      now,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.Status != "pending" {
		t.Errorf("CreateRun: status = %q, want %q", run.Status, "pending")
	}
	if run.TokenCost != 0 {
		t.Errorf("CreateRun: token_cost = %d, want 0", run.TokenCost)
	}
	if run.CompletedAt != nil {
		t.Errorf("CreateRun: completed_at = %v, want nil", run.CompletedAt)
	}
	if run.Error != nil {
		t.Errorf("CreateRun: error = %v, want nil", run.Error)
	}
	if run.ThreadID != nil {
		t.Errorf("CreateRun: thread_id = %v, want nil", run.ThreadID)
	}

	got, err := s.GetRun(ctx, "run1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ID != "run1" || got.PolicyID != "pol1" {
		t.Errorf("GetRun mismatch: %+v", got)
	}

	if _, err := s.CreateRun(ctx, CreateRunParams{
		ID:             "run2",
		PolicyID:       "pol1",
		TriggerType:    "webhook",
		TriggerPayload: `{}`,
		StartedAt:      now,
		CreatedAt:      now,
	}); err != nil {
		t.Fatalf("CreateRun run2: %v", err)
	}

	runs, err := s.ListRunsByPolicy(ctx, "pol1")
	if err != nil {
		t.Fatalf("ListRunsByPolicy: %v", err)
	}
	if len(runs) != 2 {
		t.Errorf("ListRunsByPolicy: got %d, want 2", len(runs))
	}

	if err := s.UpdateRunStatus(ctx, UpdateRunStatusParams{
		Status:      "running",
		CompletedAt: nil,
		ID:          "run1",
	}); err != nil {
		t.Fatalf("UpdateRunStatus running: %v", err)
	}

	got, err = s.GetRun(ctx, "run1")
	if err != nil {
		t.Fatalf("GetRun after status update: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("status after update: got %q, want %q", got.Status, "running")
	}

	for range 2 {
		if err := s.IncrementRunTokenCost(ctx, IncrementRunTokenCostParams{
			TokenCost: 500,
			ID:        "run1",
		}); err != nil {
			t.Fatalf("IncrementRunTokenCost: %v", err)
		}
	}

	got, err = s.GetRun(ctx, "run1")
	if err != nil {
		t.Fatalf("GetRun after token increment: %v", err)
	}
	if got.TokenCost != 1000 {
		t.Errorf("token_cost: got %d, want 1000", got.TokenCost)
	}

	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.UpdateRunStatus(ctx, UpdateRunStatusParams{
		Status:      "complete",
		CompletedAt: &completedAt,
		ID:          "run1",
	}); err != nil {
		t.Fatalf("UpdateRunStatus complete: %v", err)
	}

	// UpdateRunError sets status, error message, and completed_at atomically.
	// Use a dedicated run so run2 stays 'pending' for the ListRunsByStatus check below.
	insertRun(t, s, "run-fail", "pol1", "running")
	errMsg := "tool returned non-zero exit code"
	if err := s.UpdateRunError(ctx, UpdateRunErrorParams{
		Status:      "failed",
		Error:       &errMsg,
		CompletedAt: &completedAt,
		ID:          "run-fail",
	}); err != nil {
		t.Fatalf("UpdateRunError: %v", err)
	}
	failedRun, err := s.GetRun(ctx, "run-fail")
	if err != nil {
		t.Fatalf("GetRun after UpdateRunError: %v", err)
	}
	if failedRun.Status != "failed" {
		t.Errorf("UpdateRunError: status = %q, want %q", failedRun.Status, "failed")
	}
	if failedRun.Error == nil || *failedRun.Error != errMsg {
		t.Errorf("UpdateRunError: error = %v, want %q", failedRun.Error, errMsg)
	}
	if failedRun.CompletedAt == nil {
		t.Error("UpdateRunError: completed_at is nil")
	}

	completeRuns, err := s.ListRunsByStatus(ctx, "complete")
	if err != nil {
		t.Fatalf("ListRunsByStatus complete: %v", err)
	}
	found := false
	for _, r := range completeRuns {
		if r.ID == "run1" {
			found = true
		}
	}
	if !found {
		t.Error("run1 not found in ListRunsByStatus('complete')")
	}

	pendingRuns, err := s.ListRunsByStatus(ctx, "pending")
	if err != nil {
		t.Fatalf("ListRunsByStatus pending: %v", err)
	}
	foundFirst := false
	foundSecond := false
	for _, r := range pendingRuns {
		if r.ID == "run1" {
			foundFirst = true
		}
		if r.ID == "run2" {
			foundSecond = true
		}
	}
	if foundFirst {
		t.Error("run1 (complete) should not appear in ListRunsByStatus('pending')")
	}
	if !foundSecond {
		t.Error("run2 not found in ListRunsByStatus('pending')")
	}

	// UpdateRunThreadID is written once when the first Slack notification is posted.
	threadID := "1234567890.123456"
	if err := s.UpdateRunThreadID(ctx, UpdateRunThreadIDParams{
		ThreadID: &threadID,
		ID:       "run1",
	}); err != nil {
		t.Fatalf("UpdateRunThreadID: %v", err)
	}
	got, err = s.GetRun(ctx, "run1")
	if err != nil {
		t.Fatalf("GetRun after UpdateRunThreadID: %v", err)
	}
	if got.ThreadID == nil || *got.ThreadID != threadID {
		t.Errorf("ThreadID after update: got %v, want %q", got.ThreadID, threadID)
	}
}

func TestListPoliciesWithLatestRunQuery(t *testing.T) {
	ctx := context.Background()

	t.Run("empty db returns empty slice", func(t *testing.T) {
		s := newTestStore(t)
		rows, err := s.ListPoliciesWithLatestRun(ctx)
		if err != nil {
			t.Fatalf("ListPoliciesWithLatestRun: %v", err)
		}
		if len(rows) != 0 {
			t.Errorf("got %d rows, want 0", len(rows))
		}
	})

	t.Run("two policies with no runs: RunID nil", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")
		insertPolicy(t, s, "pol2")

		rows, err := s.ListPoliciesWithLatestRun(ctx)
		if err != nil {
			t.Fatalf("ListPoliciesWithLatestRun: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("got %d rows, want 2", len(rows))
		}
		for _, row := range rows {
			if row.RunID != nil {
				t.Errorf("policy %s: RunID = %v, want nil", row.ID, row.RunID)
			}
		}
	})

	t.Run("one policy with three runs: returns newest run", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")

		// Insert runs with explicit created_at timestamps to control ordering.
		for _, run := range []struct {
			id        string
			createdAt string
		}{
			{"run-old", "2024-01-01T00:00:00Z"},
			{"run-mid", "2024-06-01T00:00:00Z"},
			{"run-new", "2025-01-01T00:00:00Z"},
		} {
			_, err := s.DB().Exec(
				`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
				 VALUES (?, 'pol1', 'complete', 'webhook', '{}', ?, ?)`,
				run.id, run.createdAt, run.createdAt,
			)
			if err != nil {
				t.Fatalf("insert run %s: %v", run.id, err)
			}
		}

		rows, err := s.ListPoliciesWithLatestRun(ctx)
		if err != nil {
			t.Fatalf("ListPoliciesWithLatestRun: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(rows))
		}
		row := rows[0]
		if row.RunID == nil {
			t.Fatal("RunID is nil, want run-new")
		}
		if *row.RunID != "run-new" {
			t.Errorf("RunID = %q, want %q", *row.RunID, "run-new")
		}
	})

	t.Run("two policies each with a run: both returned with correct run data", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "pol1")
		insertPolicy(t, s, "pol2")
		insertRun(t, s, "run1", "pol1", "complete")
		insertRun(t, s, "run2", "pol2", "running")

		rows, err := s.ListPoliciesWithLatestRun(ctx)
		if err != nil {
			t.Fatalf("ListPoliciesWithLatestRun: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("got %d rows, want 2", len(rows))
		}

		byPolicy := make(map[string]ListPoliciesWithLatestRunRow, 2)
		for _, row := range rows {
			byPolicy[row.ID] = row
		}

		r1, ok := byPolicy["pol1"]
		if !ok {
			t.Fatal("pol1 not in results")
		}
		if r1.RunID == nil || *r1.RunID != "run1" {
			t.Errorf("pol1 RunID = %v, want run1", r1.RunID)
		}
		if r1.RunStatus == nil || *r1.RunStatus != "complete" {
			t.Errorf("pol1 RunStatus = %v, want complete", r1.RunStatus)
		}

		r2, ok := byPolicy["pol2"]
		if !ok {
			t.Fatal("pol2 not in results")
		}
		if r2.RunID == nil || *r2.RunID != "run2" {
			t.Errorf("pol2 RunID = %v, want run2", r2.RunID)
		}
		if r2.RunStatus == nil || *r2.RunStatus != "running" {
			t.Errorf("pol2 RunStatus = %v, want running", r2.RunStatus)
		}
	})
}

func TestRunStepQueries(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	insertPolicy(t, s, "pol1")
	insertRun(t, s, "run1", "pol1", "running")

	_, err := s.GetLatestRunStep(ctx, "run1")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetLatestRunStep empty: got %v, want sql.ErrNoRows", err)
	}

	step1, err := s.CreateRunStep(ctx, CreateRunStepParams{
		ID:         "step1",
		RunID:      "run1",
		StepNumber: 0,
		Type:       "thought",
		Content:    `{"text":"thinking"}`,
		TokenCost:  100,
		CreatedAt:  now,
	})
	if err != nil {
		t.Fatalf("CreateRunStep 1: %v", err)
	}
	if step1.StepNumber != 0 || step1.Type != "thought" {
		t.Errorf("CreateRunStep 1 fields: %+v", step1)
	}

	if _, err := s.CreateRunStep(ctx, CreateRunStepParams{
		ID:         "step2",
		RunID:      "run1",
		StepNumber: 1,
		Type:       "tool_call",
		Content:    `{"tool":"bash"}`,
		TokenCost:  50,
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("CreateRunStep 2: %v", err)
	}

	if _, err := s.CreateRunStep(ctx, CreateRunStepParams{
		ID:         "step3",
		RunID:      "run1",
		StepNumber: 2,
		Type:       "tool_result",
		Content:    `{"result":"ok"}`,
		TokenCost:  0,
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("CreateRunStep 3: %v", err)
	}

	steps, err := s.ListRunSteps(ctx, "run1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	if len(steps) != 3 {
		t.Errorf("ListRunSteps: got %d, want 3", len(steps))
	}
	for i, step := range steps {
		want := int64(i)
		if step.StepNumber != want {
			t.Errorf("ListRunSteps[%d].StepNumber = %d, want %d", i, step.StepNumber, want)
		}
	}

	latest, err := s.GetLatestRunStep(ctx, "run1")
	if err != nil {
		t.Fatalf("GetLatestRunStep: %v", err)
	}
	if latest.StepNumber != 2 || latest.Type != "tool_result" {
		t.Errorf("GetLatestRunStep: step_number=%d type=%q", latest.StepNumber, latest.Type)
	}

	count, err := s.CountRunSteps(ctx, "run1")
	if err != nil {
		t.Fatalf("CountRunSteps: %v", err)
	}
	if count != 3 {
		t.Errorf("CountRunSteps: got %d, want 3", count)
	}

	// Duplicate step_number must fail (UNIQUE constraint)
	_, err = s.CreateRunStep(ctx, CreateRunStepParams{
		ID:         "step4",
		RunID:      "run1",
		StepNumber: 0, // duplicate
		Type:       "thought",
		Content:    `{}`,
		TokenCost:  0,
		CreatedAt:  now,
	})
	if err == nil {
		t.Error("CreateRunStep duplicate step_number: expected constraint error, got nil")
	}
}

func TestApprovalRequestQueries(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	insertPolicy(t, s, "pol1")
	insertRun(t, s, "run1", "pol1", "waiting_for_approval")

	futureExpiry := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	ar, err := s.CreateApprovalRequest(ctx, CreateApprovalRequestParams{
		ID:               "ar1",
		RunID:            "run1",
		ToolName:         "bash",
		ProposedInput:    `{"cmd":"ls"}`,
		ReasoningSummary: "list files",
		ExpiresAt:        futureExpiry,
		CreatedAt:        now,
	})
	if err != nil {
		t.Fatalf("CreateApprovalRequest: %v", err)
	}
	if ar.Status != "pending" {
		t.Errorf("CreateApprovalRequest: status = %q, want %q", ar.Status, "pending")
	}

	got, err := s.GetApprovalRequest(ctx, "ar1")
	if err != nil {
		t.Fatalf("GetApprovalRequest: %v", err)
	}
	if got.ID != "ar1" || got.ToolName != "bash" {
		t.Errorf("GetApprovalRequest mismatch: %+v", got)
	}

	// Second request with a clearly past expiry (fixed timestamp, not relative).
	// Using a fixed value avoids relying on sub-millisecond ordering between
	// the test's "now" capture and the expiry calculation.
	const distantPast = "2020-01-01T00:00:00Z"
	if _, err := s.CreateApprovalRequest(ctx, CreateApprovalRequestParams{
		ID:               "ar2",
		RunID:            "run1",
		ToolName:         "curl",
		ProposedInput:    `{"url":"http://example.com"}`,
		ReasoningSummary: "fetch data",
		ExpiresAt:        distantPast,
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("CreateApprovalRequest ar2: %v", err)
	}

	pending, err := s.ListPendingApprovalRequests(ctx)
	if err != nil {
		t.Fatalf("ListPendingApprovalRequests: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("ListPendingApprovalRequests: got %d, want 2", len(pending))
	}

	// ListExpiredApprovalRequests: cutoff=now returns only ar2 (distantPast < now < futureExpiry).
	cutoff := time.Now().UTC().Format(time.RFC3339Nano)
	expired, err := s.ListExpiredApprovalRequests(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListExpiredApprovalRequests: %v", err)
	}
	if len(expired) != 1 || expired[0].ID != "ar2" {
		t.Errorf("ListExpiredApprovalRequests: got %v, want [ar2]", expired)
	}

	decidedAt := time.Now().UTC().Format(time.RFC3339Nano)
	note := "looks good"
	if err := s.UpdateApprovalRequestStatus(ctx, UpdateApprovalRequestStatusParams{
		Status:    "approved",
		DecidedAt: &decidedAt,
		Note:      &note,
		ID:        "ar1",
	}); err != nil {
		t.Fatalf("UpdateApprovalRequestStatus: %v", err)
	}

	approved, err := s.GetApprovalRequest(ctx, "ar1")
	if err != nil {
		t.Fatalf("GetApprovalRequest after approve: %v", err)
	}
	if approved.Status != "approved" {
		t.Errorf("status after approve: got %q, want %q", approved.Status, "approved")
	}
	if approved.DecidedAt == nil {
		t.Error("decided_at is nil after approve")
	}

	pending, err = s.ListPendingApprovalRequests(ctx)
	if err != nil {
		t.Fatalf("ListPendingApprovalRequests after approve: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("ListPendingApprovalRequests after approve: got %d, want 1", len(pending))
	}
	if pending[0].ID != "ar2" {
		t.Errorf("remaining pending: got %q, want %q", pending[0].ID, "ar2")
	}

	// GetPendingApprovalRequestsByRun returns only pending requests for the given run.
	insertRun(t, s, "run2", "pol1", "waiting_for_approval")
	if _, err := s.CreateApprovalRequest(ctx, CreateApprovalRequestParams{
		ID:               "ar3",
		RunID:            "run2",
		ToolName:         "grep",
		ProposedInput:    `{"pattern":"secret"}`,
		ReasoningSummary: "search files",
		ExpiresAt:        futureExpiry,
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("CreateApprovalRequest ar3: %v", err)
	}

	byRun1, err := s.GetPendingApprovalRequestsByRun(ctx, "run1")
	if err != nil {
		t.Fatalf("GetPendingApprovalRequestsByRun run1: %v", err)
	}
	// ar1 is approved, ar2 is still pending — only ar2 belongs to run1
	if len(byRun1) != 1 || byRun1[0].ID != "ar2" {
		t.Errorf("GetPendingApprovalRequestsByRun run1: got %v, want [ar2]", byRun1)
	}

	byRun2, err := s.GetPendingApprovalRequestsByRun(ctx, "run2")
	if err != nil {
		t.Fatalf("GetPendingApprovalRequestsByRun run2: %v", err)
	}
	if len(byRun2) != 1 || byRun2[0].ID != "ar3" {
		t.Errorf("GetPendingApprovalRequestsByRun run2: got %v, want [ar3]", byRun2)
	}

	// rejected transition: status, decided_at, and note are all recorded.
	decidedAt2 := time.Now().UTC().Format(time.RFC3339Nano)
	note2 := "too risky"
	if err := s.UpdateApprovalRequestStatus(ctx, UpdateApprovalRequestStatusParams{
		Status:    "rejected",
		DecidedAt: &decidedAt2,
		Note:      &note2,
		ID:        "ar2",
	}); err != nil {
		t.Fatalf("UpdateApprovalRequestStatus rejected: %v", err)
	}
	rejectedAR, err := s.GetApprovalRequest(ctx, "ar2")
	if err != nil {
		t.Fatalf("GetApprovalRequest after reject: %v", err)
	}
	if rejectedAR.Status != "rejected" {
		t.Errorf("rejected transition: status = %q, want %q", rejectedAR.Status, "rejected")
	}
	if rejectedAR.Note == nil || *rejectedAR.Note != note2 {
		t.Errorf("rejected transition: note = %v, want %q", rejectedAR.Note, note2)
	}

	// timeout transition: note may be nil (no human decision).
	insertRun(t, s, "run3", "pol1", "waiting_for_approval")
	futureExpiry2 := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := s.CreateApprovalRequest(ctx, CreateApprovalRequestParams{
		ID:               "ar4",
		RunID:            "run3",
		ToolName:         "wget",
		ProposedInput:    `{}`,
		ReasoningSummary: "download file",
		ExpiresAt:        futureExpiry2,
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("CreateApprovalRequest ar4: %v", err)
	}
	decidedAt3 := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.UpdateApprovalRequestStatus(ctx, UpdateApprovalRequestStatusParams{
		Status:    "timeout",
		DecidedAt: &decidedAt3,
		Note:      nil,
		ID:        "ar4",
	}); err != nil {
		t.Fatalf("UpdateApprovalRequestStatus timeout: %v", err)
	}
	timedOut, err := s.GetApprovalRequest(ctx, "ar4")
	if err != nil {
		t.Fatalf("GetApprovalRequest after timeout: %v", err)
	}
	if timedOut.Status != "timeout" {
		t.Errorf("timeout transition: status = %q, want %q", timedOut.Status, "timeout")
	}
	if timedOut.Note != nil {
		t.Errorf("timeout transition: note = %v, want nil", timedOut.Note)
	}
}
