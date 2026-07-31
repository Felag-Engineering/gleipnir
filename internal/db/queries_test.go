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
		if _, err := s.UpdateApprovalRequestStatus(ctx, UpdateApprovalRequestStatusParams{
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

	if srv.ProtocolVersion != nil {
		t.Errorf("CreateMCPServer: protocol_version = %v, want nil", srv.ProtocolVersion)
	}

	pv := "2026-07-28"
	if err := s.UpdateMCPServerProtocolVersion(ctx, UpdateMCPServerProtocolVersionParams{
		ProtocolVersion: &pv,
		ID:              "srv1",
	}); err != nil {
		t.Fatalf("UpdateMCPServerProtocolVersion: %v", err)
	}
	got, err = s.GetMCPServer(ctx, "srv1")
	if err != nil {
		t.Fatalf("GetMCPServer after UpdateMCPServerProtocolVersion: %v", err)
	}
	if got.ProtocolVersion == nil || *got.ProtocolVersion != pv {
		t.Errorf("ProtocolVersion after update: got %v, want %q", got.ProtocolVersion, pv)
	}

	serversWithVersion, err := s.ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("ListMCPServers after UpdateMCPServerProtocolVersion: %v", err)
	}
	var foundSrv1 bool
	for _, server := range serversWithVersion {
		if server.ID != "srv1" {
			continue
		}
		foundSrv1 = true
		if server.ProtocolVersion == nil || *server.ProtocolVersion != pv {
			t.Errorf("ListMCPServers: srv1 protocol_version = %v, want %q", server.ProtocolVersion, pv)
		}
	}
	if !foundSrv1 {
		t.Fatal("ListMCPServers: srv1 not found")
	}

	if err := s.UpdateMCPServerProtocolVersion(ctx, UpdateMCPServerProtocolVersionParams{
		ProtocolVersion: nil,
		ID:              "srv1",
	}); err != nil {
		t.Fatalf("UpdateMCPServerProtocolVersion (clear): %v", err)
	}
	got, err = s.GetMCPServer(ctx, "srv1")
	if err != nil {
		t.Fatalf("GetMCPServer after clearing ProtocolVersion: %v", err)
	}
	if got.ProtocolVersion != nil {
		t.Errorf("ProtocolVersion after clear: got %v, want nil", got.ProtocolVersion)
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
		ID:          "tool1",
		ServerID:    "srv1",
		Name:        "alpha",
		Description: "first tool",
		InputSchema: "{}",
		CreatedAt:   now,
	})
	if err != nil {
		t.Fatalf("UpsertMCPTool: %v", err)
	}
	if tool.ID != "tool1" {
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
		ID:          "tool2",
		ServerID:    "srv1",
		Name:        "beta",
		Description: "second tool",
		InputSchema: "{}",
		CreatedAt:   now,
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

	// Conflict/update path: upsert same server+name — should update, not insert.
	upserted, err := s.UpsertMCPTool(ctx, UpsertMCPToolParams{
		ID:          "tool3",
		ServerID:    "srv1",
		Name:        "alpha", // same server+name as tool1
		Description: "updated desc",
		InputSchema: `{"type":"object"}`,
		CreatedAt:   now,
	})
	if err != nil {
		t.Fatalf("UpsertMCPTool conflict path: %v", err)
	}
	// RETURNING * must reflect the updated columns, not the attempted insert values.
	if upserted.Description != "updated desc" || upserted.InputSchema != `{"type":"object"}` {
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

func TestMCPToolEnabledQueries(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	insertMcpServer(t, s, "srv2")

	// Newly inserted tools default to enabled = 1.
	tool1, err := s.UpsertMCPTool(ctx, UpsertMCPToolParams{
		ID:          "etool1",
		ServerID:    "srv2",
		Name:        "alpha",
		Description: "first",
		InputSchema: "{}",
		CreatedAt:   now,
	})
	if err != nil {
		t.Fatalf("UpsertMCPTool: %v", err)
	}
	if tool1.Enabled != 1 {
		t.Errorf("new tool enabled = %d, want 1", tool1.Enabled)
	}

	// Insert a second tool so we can compare filtered vs unfiltered.
	_, err = s.UpsertMCPTool(ctx, UpsertMCPToolParams{
		ID:          "etool2",
		ServerID:    "srv2",
		Name:        "beta",
		Description: "second",
		InputSchema: "{}",
		CreatedAt:   now,
	})
	if err != nil {
		t.Fatalf("UpsertMCPTool beta: %v", err)
	}

	// SetMCPToolEnabled toggles enabled 1 → 0.
	if err := s.SetMCPToolEnabled(ctx, SetMCPToolEnabledParams{ID: "etool1", Enabled: 0}); err != nil {
		t.Fatalf("SetMCPToolEnabled(0): %v", err)
	}
	got, err := s.GetMCPTool(ctx, "etool1")
	if err != nil {
		t.Fatalf("GetMCPTool after disable: %v", err)
	}
	if got.Enabled != 0 {
		t.Errorf("after disable: enabled = %d, want 0", got.Enabled)
	}

	// Rediscovery upsert (same server+name) must NOT reset enabled back to 1.
	if _, err := s.UpsertMCPTool(ctx, UpsertMCPToolParams{
		ID:          "etool1-new",
		ServerID:    "srv2",
		Name:        "alpha",
		Description: "refreshed desc",
		InputSchema: `{"type":"object"}`,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("UpsertMCPTool rediscovery: %v", err)
	}
	got, err = s.GetMCPTool(ctx, "etool1")
	if err != nil {
		t.Fatalf("GetMCPTool after rediscovery upsert: %v", err)
	}
	if got.Enabled != 0 {
		t.Errorf("after rediscovery upsert: enabled reset to %d, want 0 (operator flag must survive)", got.Enabled)
	}
	if got.Description != "refreshed desc" {
		t.Errorf("after rediscovery: description = %q, want refreshed desc", got.Description)
	}

	// ListEnabledMCPToolsByServer returns only enabled rows.
	enabled, err := s.ListEnabledMCPToolsByServer(ctx, "srv2")
	if err != nil {
		t.Fatalf("ListEnabledMCPToolsByServer: %v", err)
	}
	if len(enabled) != 1 {
		t.Fatalf("ListEnabledMCPToolsByServer: got %d tools, want 1", len(enabled))
	}
	if enabled[0].Name != "beta" {
		t.Errorf("ListEnabledMCPToolsByServer: got %q, want beta", enabled[0].Name)
	}

	// ListMCPToolsByServer returns all rows including disabled.
	all, err := s.ListMCPToolsByServer(ctx, "srv2")
	if err != nil {
		t.Fatalf("ListMCPToolsByServer: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListMCPToolsByServer: got %d tools, want 2", len(all))
	}

	// SetMCPToolEnabled toggles 0 → 1 (re-enable).
	if err := s.SetMCPToolEnabled(ctx, SetMCPToolEnabledParams{ID: "etool1", Enabled: 1}); err != nil {
		t.Fatalf("SetMCPToolEnabled(1): %v", err)
	}
	got, err = s.GetMCPTool(ctx, "etool1")
	if err != nil {
		t.Fatalf("GetMCPTool after re-enable: %v", err)
	}
	if got.Enabled != 1 {
		t.Errorf("after re-enable: enabled = %d, want 1", got.Enabled)
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
		Model:          "claude-sonnet-4-6",
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
		Model:          "claude-sonnet-4-6",
		TriggerType:    "webhook",
		TriggerPayload: `{}`,
		StartedAt:      now,
		CreatedAt:      now,
	}); err != nil {
		t.Fatalf("CreateRun run2: %v", err)
	}

	allRuns, err := s.ListRuns(ctx, ListRunsParams{
		PolicyID: "pol1",
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("ListRuns by policy: %v", err)
	}
	if len(allRuns) != 2 {
		t.Errorf("ListRuns by policy: got %d, want 2", len(allRuns))
	}

	if _, err := s.UpdateRunStatus(ctx, UpdateRunStatusParams{
		Status:          "running",
		CompletedAt:     nil,
		ID:              "run1",
		ExpectedVersion: 0,
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
	if _, err := s.UpdateRunStatus(ctx, UpdateRunStatusParams{
		Status:          "complete",
		CompletedAt:     &completedAt,
		ID:              "run1",
		ExpectedVersion: 1, // version was incremented to 1 by the UpdateRunStatus(running) call above
	}); err != nil {
		t.Fatalf("UpdateRunStatus complete: %v", err)
	}

	// UpdateRunError sets status, error message, and completed_at atomically.
	// Use a dedicated run so run2 stays 'pending' for the ListRunsByStatus check below.
	insertRun(t, s, "run-fail", "pol1", "running")
	errMsg := "tool returned non-zero exit code"
	if _, err := s.UpdateRunError(ctx, UpdateRunErrorParams{
		Status:          "failed",
		Error:           &errMsg,
		CompletedAt:     &completedAt,
		ID:              "run-fail",
		ExpectedVersion: 0,
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

	completeRuns, err := s.ListRuns(ctx, ListRunsParams{
		Status: "complete",
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("ListRuns by status complete: %v", err)
	}
	found := false
	for _, r := range completeRuns {
		if r.ID == "run1" {
			found = true
		}
	}
	if !found {
		t.Error("run1 not found in ListRuns status=complete")
	}

	pendingRuns, err := s.ListRuns(ctx, ListRunsParams{
		Status: "pending",
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("ListRuns by status pending: %v", err)
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
		t.Error("run1 (complete) should not appear in ListRuns status=pending")
	}
	if !foundSecond {
		t.Error("run2 not found in ListRuns status=pending")
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

	steps, err := s.ListRunSteps(ctx, ListRunStepsParams{RunID: "run1", After: -1, Limit: 100})
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

	// Cursor pagination: after=1 with limit=2 should return steps 2 only (step_number 2).
	cursorSteps, err := s.ListRunSteps(ctx, ListRunStepsParams{RunID: "run1", After: 1, Limit: 2})
	if err != nil {
		t.Fatalf("ListRunSteps cursor: %v", err)
	}
	if len(cursorSteps) != 1 {
		t.Errorf("ListRunSteps cursor: got %d, want 1 (only step_number 2)", len(cursorSteps))
	} else if cursorSteps[0].StepNumber != 2 {
		t.Errorf("ListRunSteps cursor: step_number = %d, want 2", cursorSteps[0].StepNumber)
	}

	// Cursor past the end returns empty.
	emptySteps, err := s.ListRunSteps(ctx, ListRunStepsParams{RunID: "run1", After: 100, Limit: 100})
	if err != nil {
		t.Fatalf("ListRunSteps past end: %v", err)
	}
	if len(emptySteps) != 0 {
		t.Errorf("ListRunSteps past end: got %d, want 0", len(emptySteps))
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
	if _, err := s.UpdateApprovalRequestStatus(ctx, UpdateApprovalRequestStatusParams{
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
	if _, err := s.UpdateApprovalRequestStatus(ctx, UpdateApprovalRequestStatusParams{
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
	if _, err := s.UpdateApprovalRequestStatus(ctx, UpdateApprovalRequestStatusParams{
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

// insertPlugin inserts a minimal plugins row and returns its id.
func insertPlugin(t *testing.T, s *Store, id string) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
		 VALUES (?, ?, '1.0.0', '{}', 'pubkey', 'active', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		id, "plugin-"+id,
	)
	if err != nil {
		t.Fatalf("insertPlugin %s: %v", id, err)
	}
}

// insertPluginInstance inserts a minimal plugin_instances row and returns its id.
func insertPluginInstance(t *testing.T, s *Store, id, pluginID string) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO plugin_instances(id, plugin_id, instance_name, config_json, handshake_versions, health_state, version, created_at, updated_at)
		 VALUES (?, ?, ?, '{}', '{}', 'healthy', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		id, pluginID, "instance-"+id,
	)
	if err != nil {
		t.Fatalf("insertPluginInstance %s: %v", id, err)
	}
}

func TestPluginAudienceRoundTrip(t *testing.T) {
	ctx := context.Background()
	now := "2024-01-01T00:00:00Z"

	t.Run("create get list", func(t *testing.T) {
		s := newTestStore(t)
		q := s.Queries()

		aud, err := q.CreatePluginAudience(ctx, CreatePluginAudienceParams{
			ID:        "aud1",
			Name:      "test-audience",
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("CreatePluginAudience: %v", err)
		}
		if aud.ID != "aud1" || aud.Name != "test-audience" || aud.Version != 0 {
			t.Errorf("created audience = %+v, unexpected fields", aud)
		}

		byID, err := q.GetPluginAudienceByID(ctx, "aud1")
		if err != nil {
			t.Fatalf("GetPluginAudienceByID: %v", err)
		}
		if byID.Name != "test-audience" {
			t.Errorf("GetPluginAudienceByID: name = %q, want %q", byID.Name, "test-audience")
		}

		byName, err := q.GetPluginAudienceByName(ctx, "test-audience")
		if err != nil {
			t.Fatalf("GetPluginAudienceByName: %v", err)
		}
		if byName.ID != "aud1" {
			t.Errorf("GetPluginAudienceByName: id = %q, want %q", byName.ID, "aud1")
		}

		list, err := q.ListPluginAudiences(ctx)
		if err != nil {
			t.Fatalf("ListPluginAudiences: %v", err)
		}
		if len(list) != 1 || list[0].ID != "aud1" {
			t.Errorf("ListPluginAudiences: got %v, want [aud1]", list)
		}
	})

	t.Run("update CAS happy path", func(t *testing.T) {
		s := newTestStore(t)
		q := s.Queries()

		if _, err := q.CreatePluginAudience(ctx, CreatePluginAudienceParams{
			ID:        "aud2",
			Name:      "old-name",
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreatePluginAudience: %v", err)
		}

		rows, err := q.UpdatePluginAudience(ctx, UpdatePluginAudienceParams{
			ID:              "aud2",
			Name:            "new-name",
			UpdatedAt:       now,
			ExpectedVersion: 0,
		})
		if err != nil {
			t.Fatalf("UpdatePluginAudience: %v", err)
		}
		if rows != 1 {
			t.Errorf("UpdatePluginAudience rows = %d, want 1", rows)
		}

		updated, err := q.GetPluginAudienceByID(ctx, "aud2")
		if err != nil {
			t.Fatalf("GetPluginAudienceByID after update: %v", err)
		}
		if updated.Name != "new-name" {
			t.Errorf("name after update = %q, want %q", updated.Name, "new-name")
		}
		if updated.Version != 1 {
			t.Errorf("version after update = %d, want 1", updated.Version)
		}
	})

	t.Run("update CAS stale version returns 0 rows", func(t *testing.T) {
		s := newTestStore(t)
		q := s.Queries()

		if _, err := q.CreatePluginAudience(ctx, CreatePluginAudienceParams{
			ID:        "aud3",
			Name:      "name",
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreatePluginAudience: %v", err)
		}

		rows, err := q.UpdatePluginAudience(ctx, UpdatePluginAudienceParams{
			ID:              "aud3",
			Name:            "changed",
			UpdatedAt:       now,
			ExpectedVersion: 99, // stale
		})
		if err != nil {
			t.Fatalf("UpdatePluginAudience stale: %v", err)
		}
		if rows != 0 {
			t.Errorf("stale CAS: rows = %d, want 0", rows)
		}
	})
}

func TestAudienceCascadeAndRestrict(t *testing.T) {
	ctx := context.Background()
	now := "2024-01-01T00:00:00Z"

	t.Run("delete audience cascades to entries", func(t *testing.T) {
		s := newTestStore(t)
		q := s.Queries()

		insertPlugin(t, s, "pl1")
		insertPluginInstance(t, s, "pi1", "pl1")

		if _, err := q.CreatePluginAudience(ctx, CreatePluginAudienceParams{
			ID:        "aud1",
			Name:      "cascade-aud",
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreatePluginAudience: %v", err)
		}
		if _, err := q.CreateAudienceEntry(ctx, CreateAudienceEntryParams{
			ID:               "ae1",
			AudienceID:       "aud1",
			PluginInstanceID: "pi1",
			Position:         0,
			Notify:           1,
			Request:          0,
			ConfigJson:       "{}",
		}); err != nil {
			t.Fatalf("CreateAudienceEntry: %v", err)
		}

		if _, err := q.DeletePluginAudience(ctx, "aud1"); err != nil {
			t.Fatalf("DeletePluginAudience: %v", err)
		}

		var count int
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audience_entries WHERE id = 'ae1'`).Scan(&count); err != nil {
			t.Fatalf("count audience_entries: %v", err)
		}
		if count != 0 {
			t.Errorf("entry count after audience delete = %d, want 0 (cascade)", count)
		}
	})

	t.Run("delete plugin_instance with referencing entry is rejected", func(t *testing.T) {
		s := newTestStore(t)
		q := s.Queries()

		insertPlugin(t, s, "pl1")
		insertPluginInstance(t, s, "pi1", "pl1")

		if _, err := q.CreatePluginAudience(ctx, CreatePluginAudienceParams{
			ID:        "aud1",
			Name:      "restrict-aud",
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreatePluginAudience: %v", err)
		}
		if _, err := q.CreateAudienceEntry(ctx, CreateAudienceEntryParams{
			ID:               "ae1",
			AudienceID:       "aud1",
			PluginInstanceID: "pi1",
			Position:         0,
			Notify:           1,
			Request:          0,
			ConfigJson:       "{}",
		}); err != nil {
			t.Fatalf("CreateAudienceEntry: %v", err)
		}

		// Deleting the referenced plugin_instance must fail (ON DELETE RESTRICT).
		_, err := s.DB().ExecContext(ctx, `DELETE FROM plugin_instances WHERE id = 'pi1'`)
		if err == nil {
			t.Error("expected FK restrict error when deleting referenced plugin_instance, got nil")
		}
	})

	t.Run("pending_request audience_entry_id set to NULL when entry deleted", func(t *testing.T) {
		s := newTestStore(t)
		q := s.Queries()

		insertPlugin(t, s, "pl1")
		insertPluginInstance(t, s, "pi1", "pl1")
		insertPolicy(t, s, "pol1")
		insertRun(t, s, "run1", "pol1", "running")

		if _, err := q.CreatePluginAudience(ctx, CreatePluginAudienceParams{
			ID:        "aud1",
			Name:      "setnull-aud",
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreatePluginAudience: %v", err)
		}
		entryID := "ae1"
		if _, err := q.CreateAudienceEntry(ctx, CreateAudienceEntryParams{
			ID:               entryID,
			AudienceID:       "aud1",
			PluginInstanceID: "pi1",
			Position:         0,
			Notify:           1,
			Request:          0,
			ConfigJson:       "{}",
		}); err != nil {
			t.Fatalf("CreateAudienceEntry: %v", err)
		}

		if _, err := q.CreatePluginPendingRequest(ctx, CreatePluginPendingRequestParams{
			ID:               "req1",
			PluginInstanceID: "pi1",
			RunID:            "run1",
			AudienceEntryID:  &entryID,
			ToolName:         "my_tool",
			CreatedAt:        now,
		}); err != nil {
			t.Fatalf("CreatePluginPendingRequest: %v", err)
		}

		// Deleting the audience entry should SET NULL on the pending request.
		if _, err := q.DeleteAudienceEntry(ctx, entryID); err != nil {
			t.Fatalf("DeleteAudienceEntry: %v", err)
		}

		req, err := q.GetPluginPendingRequest(ctx, "req1")
		if err != nil {
			t.Fatalf("GetPluginPendingRequest: %v", err)
		}
		if req.AudienceEntryID != nil {
			t.Errorf("audience_entry_id = %v, want nil after entry delete (SET NULL)", req.AudienceEntryID)
		}
	})
}

func TestGetPluginAudienceWithEntries(t *testing.T) {
	ctx := context.Background()
	now := "2024-01-01T00:00:00Z"

	t.Run("zero-entry audience returns one NULL-entry row", func(t *testing.T) {
		s := newTestStore(t)
		q := s.Queries()

		if _, err := q.CreatePluginAudience(ctx, CreatePluginAudienceParams{
			ID:        "aud1",
			Name:      "empty-aud",
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreatePluginAudience: %v", err)
		}

		rows, err := q.GetPluginAudienceWithEntries(ctx, "aud1")
		if err != nil {
			t.Fatalf("GetPluginAudienceWithEntries: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1 (NULL entry row for empty audience)", len(rows))
		}
		if rows[0].EntryID != nil {
			t.Errorf("EntryID = %v, want nil for empty audience", rows[0].EntryID)
		}
	})

	t.Run("audience with N entries returns N rows ordered by position", func(t *testing.T) {
		s := newTestStore(t)
		q := s.Queries()

		insertPlugin(t, s, "pl1")
		insertPluginInstance(t, s, "pi1", "pl1")
		insertPluginInstance(t, s, "pi2", "pl1")
		insertPluginInstance(t, s, "pi3", "pl1")

		if _, err := q.CreatePluginAudience(ctx, CreatePluginAudienceParams{
			ID:        "aud1",
			Name:      "multi-aud",
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreatePluginAudience: %v", err)
		}

		// Insert entries out of order to test ORDER BY position.
		for _, e := range []struct {
			id  string
			pi  string
			pos int64
		}{
			{"ae3", "pi3", 2},
			{"ae1", "pi1", 0},
			{"ae2", "pi2", 1},
		} {
			if _, err := q.CreateAudienceEntry(ctx, CreateAudienceEntryParams{
				ID:               e.id,
				AudienceID:       "aud1",
				PluginInstanceID: e.pi,
				Position:         e.pos,
				ConfigJson:       "{}",
			}); err != nil {
				t.Fatalf("CreateAudienceEntry %s: %v", e.id, err)
			}
		}

		rows, err := q.GetPluginAudienceWithEntries(ctx, "aud1")
		if err != nil {
			t.Fatalf("GetPluginAudienceWithEntries: %v", err)
		}
		if len(rows) != 3 {
			t.Fatalf("rows = %d, want 3", len(rows))
		}
		for i, row := range rows {
			if row.Position == nil || *row.Position != int64(i) {
				t.Errorf("row[%d].Position = %v, want %d", i, row.Position, i)
			}
		}
	})
}

func TestPluginPendingRequestCAS(t *testing.T) {
	ctx := context.Background()
	now := "2024-01-01T00:00:00Z"
	resolvedAt := "2024-01-01T01:00:00Z"
	response := "ok"

	t.Run("first update returns 1, second returns 0", func(t *testing.T) {
		s := newTestStore(t)
		q := s.Queries()

		insertPlugin(t, s, "pl1")
		insertPluginInstance(t, s, "pi1", "pl1")
		insertPolicy(t, s, "pol1")
		insertRun(t, s, "run1", "pol1", "running")

		if _, err := q.CreatePluginPendingRequest(ctx, CreatePluginPendingRequestParams{
			ID:               "req1",
			PluginInstanceID: "pi1",
			RunID:            "run1",
			ToolName:         "my_tool",
			CreatedAt:        now,
		}); err != nil {
			t.Fatalf("CreatePluginPendingRequest: %v", err)
		}

		rows, err := q.UpdatePluginPendingRequestStatus(ctx, UpdatePluginPendingRequestStatusParams{
			ID:         "req1",
			Status:     "resolved",
			Response:   &response,
			ResolvedAt: &resolvedAt,
		})
		if err != nil {
			t.Fatalf("first UpdatePluginPendingRequestStatus: %v", err)
		}
		if rows != 1 {
			t.Errorf("first update rows = %d, want 1", rows)
		}

		// Second call must be a no-op — status is no longer 'pending'.
		rows, err = q.UpdatePluginPendingRequestStatus(ctx, UpdatePluginPendingRequestStatusParams{
			ID:         "req1",
			Status:     "timed_out",
			ResolvedAt: &resolvedAt,
		})
		if err != nil {
			t.Fatalf("second UpdatePluginPendingRequestStatus: %v", err)
		}
		if rows != 0 {
			t.Errorf("second update rows = %d, want 0 (CAS guard)", rows)
		}
	})
}

func TestPluginPendingRequestRunCascade(t *testing.T) {
	ctx := context.Background()
	now := "2024-01-01T00:00:00Z"

	s := newTestStore(t)
	q := s.Queries()

	insertPlugin(t, s, "pl1")
	insertPluginInstance(t, s, "pi1", "pl1")
	insertPolicy(t, s, "pol1")
	insertRun(t, s, "run1", "pol1", "running")

	if _, err := q.CreatePluginPendingRequest(ctx, CreatePluginPendingRequestParams{
		ID:               "req1",
		PluginInstanceID: "pi1",
		RunID:            "run1",
		ToolName:         "my_tool",
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePluginPendingRequest: %v", err)
	}

	// Deleting the run must cascade to the pending request.
	if _, err := s.DB().ExecContext(ctx, `DELETE FROM runs WHERE id = 'run1'`); err != nil {
		t.Fatalf("delete run: %v", err)
	}

	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_pending_requests WHERE id = 'req1'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("pending request count after run delete = %d, want 0 (cascade)", count)
	}
}

func TestListExpiredPluginPendingRequests(t *testing.T) {
	ctx := context.Background()
	now := "2024-01-01T00:00:00Z"

	s := newTestStore(t)
	q := s.Queries()

	insertPlugin(t, s, "pl1")
	insertPluginInstance(t, s, "pi1", "pl1")
	insertPolicy(t, s, "pol1")
	insertRun(t, s, "run1", "pol1", "running")

	// Row with past expires_at — should be returned.
	if _, err := q.CreatePluginPendingRequest(ctx, CreatePluginPendingRequestParams{
		ID:               "req-expired",
		PluginInstanceID: "pi1",
		RunID:            "run1",
		ToolName:         "expired_tool",
		ExpiresAt:        strPtr("2023-12-31T00:00:00Z"),
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePluginPendingRequest expired: %v", err)
	}

	// Row with future expires_at — must not be returned.
	if _, err := q.CreatePluginPendingRequest(ctx, CreatePluginPendingRequestParams{
		ID:               "req-future",
		PluginInstanceID: "pi1",
		RunID:            "run1",
		ToolName:         "future_tool",
		ExpiresAt:        strPtr("2099-01-01T00:00:00Z"),
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePluginPendingRequest future: %v", err)
	}

	// Row with NULL expires_at — must not be returned.
	if _, err := q.CreatePluginPendingRequest(ctx, CreatePluginPendingRequestParams{
		ID:               "req-no-expiry",
		PluginInstanceID: "pi1",
		RunID:            "run1",
		ToolName:         "no_expiry_tool",
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePluginPendingRequest no-expiry: %v", err)
	}

	cutoff := strPtr("2024-01-01T00:00:00Z")
	expired, err := q.ListExpiredPluginPendingRequests(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListExpiredPluginPendingRequests: %v", err)
	}

	if len(expired) != 1 {
		t.Fatalf("expired count = %d, want 1", len(expired))
	}
	if expired[0].ID != "req-expired" {
		t.Errorf("expired[0].ID = %q, want %q", expired[0].ID, "req-expired")
	}
	if expired[0].ToolName != "expired_tool" {
		t.Errorf("expired[0].ToolName = %q, want %q", expired[0].ToolName, "expired_tool")
	}
	if expired[0].RunID != "run1" {
		t.Errorf("expired[0].RunID = %q, want %q", expired[0].RunID, "run1")
	}
}

// TestDeferredUniqueNotSupported documents that modernc.org/sqlite does not
// support DEFERRABLE on table constraints. Multi-row position swaps inside a
// single transaction require a sentinel intermediate value.
func TestDeferredUniqueNotSupported(t *testing.T) {
	ctx := context.Background()
	now := "2024-01-01T00:00:00Z"

	s := newTestStore(t)
	q := s.Queries()

	insertPlugin(t, s, "pl1")
	insertPluginInstance(t, s, "pi1", "pl1")

	if _, err := q.CreatePluginAudience(ctx, CreatePluginAudienceParams{
		ID:        "aud1",
		Name:      "reorder-aud",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginAudience: %v", err)
	}
	if _, err := q.CreateAudienceEntry(ctx, CreateAudienceEntryParams{
		ID:               "ae1",
		AudienceID:       "aud1",
		PluginInstanceID: "pi1",
		Position:         0,
		ConfigJson:       "{}",
	}); err != nil {
		t.Fatalf("CreateAudienceEntry ae1: %v", err)
	}
	if _, err := q.CreateAudienceEntry(ctx, CreateAudienceEntryParams{
		ID:               "ae2",
		AudienceID:       "aud1",
		PluginInstanceID: "pi1",
		Position:         1,
		ConfigJson:       "{}",
	}); err != nil {
		t.Fatalf("CreateAudienceEntry ae2: %v", err)
	}

	// Direct position swap inside a transaction fails because the constraint is
	// NOT deferrable — moving ae1 to position 1 collides with ae2 before ae2
	// is moved away.
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err1 := tx.ExecContext(ctx, `UPDATE audience_entries SET position = 1 WHERE id = 'ae1'`)
	_, err2 := tx.ExecContext(ctx, `UPDATE audience_entries SET position = 0 WHERE id = 'ae2'`)

	if err1 == nil && err2 == nil {
		// If both succeed we may be on a future version that supports deferred
		// constraints — commit and verify correctness rather than failing.
		if cerr := tx.Commit(); cerr != nil {
			// Commit itself rejected — DEFERRABLE still not supported.
			t.Logf("deferred-UNIQUE swap: commit rejected (%v); sentinel approach required", cerr)
		} else {
			t.Logf("deferred-UNIQUE swap: both UPDATEs and COMMIT succeeded — check SQLite version")
		}
	} else {
		// At least one UPDATE failed — expected behaviour with non-deferrable UNIQUE.
		t.Logf("deferred-UNIQUE swap: UPDATE failed as expected (%v / %v); sentinel approach required for multi-row reorders", err1, err2)
	}

	// Verify the sentinel approach works: use a large out-of-range position
	// as a temporary placeholder to avoid the transient violation.
	s2 := newTestStore(t)
	q2 := s2.Queries()

	insertPlugin(t, s2, "pl1")
	insertPluginInstance(t, s2, "pi1", "pl1")

	if _, err := q2.CreatePluginAudience(ctx, CreatePluginAudienceParams{
		ID:        "aud1",
		Name:      "sentinel-aud",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginAudience (s2): %v", err)
	}
	if _, err := q2.CreateAudienceEntry(ctx, CreateAudienceEntryParams{
		ID:               "ae1",
		AudienceID:       "aud1",
		PluginInstanceID: "pi1",
		Position:         0,
		ConfigJson:       "{}",
	}); err != nil {
		t.Fatalf("CreateAudienceEntry ae1 (s2): %v", err)
	}
	if _, err := q2.CreateAudienceEntry(ctx, CreateAudienceEntryParams{
		ID:               "ae2",
		AudienceID:       "aud1",
		PluginInstanceID: "pi1",
		Position:         1,
		ConfigJson:       "{}",
	}); err != nil {
		t.Fatalf("CreateAudienceEntry ae2 (s2): %v", err)
	}

	tx2, err := s2.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}

	const sentinel int64 = 9999
	_, err = tx2.ExecContext(ctx, `UPDATE audience_entries SET position = ? WHERE id = 'ae1'`, sentinel)
	if err != nil {
		tx2.Rollback() //nolint:errcheck
		t.Fatalf("sentinel move ae1: %v", err)
	}
	_, err = tx2.ExecContext(ctx, `UPDATE audience_entries SET position = 0 WHERE id = 'ae2'`)
	if err != nil {
		tx2.Rollback() //nolint:errcheck
		t.Fatalf("move ae2: %v", err)
	}
	_, err = tx2.ExecContext(ctx, `UPDATE audience_entries SET position = 1 WHERE id = 'ae1'`)
	if err != nil {
		tx2.Rollback() //nolint:errcheck
		t.Fatalf("finalize ae1: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit sentinel swap: %v", err)
	}

	entries, err := q2.ListAudienceEntries(ctx, "aud1")
	if err != nil {
		t.Fatalf("ListAudienceEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
	if entries[0].ID != "ae2" || entries[0].Position != 0 {
		t.Errorf("entries[0] = {id:%s pos:%d}, want {id:ae2 pos:0}", entries[0].ID, entries[0].Position)
	}
	if entries[1].ID != "ae1" || entries[1].Position != 1 {
		t.Errorf("entries[1] = {id:%s pos:%d}, want {id:ae1 pos:1}", entries[1].ID, entries[1].Position)
	}
}

// strPtr is a test helper that returns a pointer to a string literal.
func strPtr(s string) *string {
	return &s
}
