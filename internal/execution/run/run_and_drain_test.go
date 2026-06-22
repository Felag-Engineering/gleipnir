package run

// Tests for RunLauncher.runAndDrain. This file uses package run (not run_test)
// so it can call the unexported method directly.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// newStubMCPServerInternal mirrors the helper in launcher_test.go but lives in
// package run so this internal test file can use it without a cross-file import.
func newStubMCPServerInternal(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		method, _ := req["method"].(string)
		switch method {
		case "tools/list":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "read_data",
						"description": "reads data",
						"inputSchema": map[string]any{
							"type": "object", "properties": map[string]any{},
						},
					}},
				},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "stub result"}},
					"isError": false,
				},
			})
		}
	}))
}

// buildBoundAgent constructs a BoundAgent backed by a mock LLM client, wiring
// the minimal dependencies (state machine, audit writer) against the given store
// and run row. The run row must already exist in the DB.
func buildBoundAgent(t *testing.T, store *db.Store, runRow db.Run, resolvedTools []mcp.ResolvedTool, parsedPolicy *model.ParsedPolicy) *agent.BoundAgent {
	t.Helper()
	sm := agent.NewRunStateMachine(
		runRow.ID,
		model.RunStatus(runRow.Status),
		store.DB(),
		store.Queries(),
		agent.WithInitialVersion(runRow.Version),
	)
	audit := agent.NewAuditWriter(store.Queries())
	ba, err := agent.New(agent.Config{
		Tools:        resolvedTools,
		Policy:       parsedPolicy,
		Audit:        audit,
		StateMachine: sm,
		ApprovalCh:   make(chan bool, 1),
		LLMClient: testutil.NewFakeClientOnly(
			testutil.MakeLLMTextResponse("done", llm.StopReasonEndTurn, 10, 5),
		),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return ba
}

// insertRunRow creates a run row via SQL directly so we can get the db.Run back.
func insertRunRow(t *testing.T, store *db.Store, policyID string) db.Run {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	runRow, err := store.CreateRun(ctx, db.CreateRunParams{
		ID:             model.NewULID(),
		PolicyID:       policyID,
		Model:          "claude-sonnet-4-6",
		TriggerType:    string(model.TriggerTypeWebhook),
		TriggerPayload: `{}`,
		StartedAt:      now,
		CreatedAt:      now,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return runRow
}

const parallelPolicyYAML = `
name: run-and-drain-parallel
trigger:
  type: webhook
capabilities:
  tools:
    - tool: stub-server.read_data
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: parallel
`

const queuePolicyYAML = `
name: run-and-drain-queue
trigger:
  type: webhook
capabilities:
  tools:
    - tool: stub-server.read_data
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: queue
  queue_depth: 2
`

// TestRunAndDrain_NonQueuePolicy verifies that runAndDrain completes the agent
// run without attempting a queue drain when the policy uses parallel concurrency.
func TestRunAndDrain_NonQueuePolicy(t *testing.T) {
	store := testutil.NewTestStore(t)
	mcpSrv := newStubMCPServerInternal(t)
	t.Cleanup(mcpSrv.Close)
	registry := mcp.NewRegistry(store.Queries())
	if _, err := mcp.RegisterServerForTest(context.Background(), store.Queries(), registry, "stub-server", mcpSrv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	testutil.InsertPolicy(t, store, "p-parallel", "policy-p-parallel", "webhook", parallelPolicyYAML)
	parsed, err := policy.Parse(parallelPolicyYAML, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	resolvedTools, err := registry.ResolveForPolicy(context.Background(), parsed)
	if err != nil {
		t.Fatalf("ResolveForPolicy: %v", err)
	}

	runRow := insertRunRow(t, store, "p-parallel")
	ba := buildBoundAgent(t, store, runRow, resolvedTools, parsed)

	launcher := NewRunLauncher(RunLauncherConfig{
		Store:                  store,
		Resolver:               NewDefaultToolResolver(registry, nil, nil),
		Manager:                NewRunManager(),
		AgentFactory:           nil,
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          nil,
	})

	launcher.runAndDrain(context.Background(), runRow.ID, model.TriggerTypeWebhook, "p-parallel", parsed, `{}`, ba)

	// The run should be in a terminal state (complete or failed).
	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-parallel", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected run row, got none")
	}
	status := model.RunStatus(runs[0].Status)
	if status != model.RunStatusComplete && status != model.RunStatusFailed {
		t.Errorf("run.Status = %q, want complete or failed", status)
	}

	// No additional runs should be created (no drain for parallel policy).
	if len(runs) != 1 {
		t.Errorf("expected exactly 1 run (no drain), got %d", len(runs))
	}
}

// TestRunAndDrain_QueuePolicy verifies that runAndDrain drains the next queued
// trigger after the agent run completes when the policy uses queue concurrency.
func TestRunAndDrain_QueuePolicy(t *testing.T) {
	store := testutil.NewTestStore(t)
	mcpSrv := newStubMCPServerInternal(t)
	t.Cleanup(mcpSrv.Close)
	registry := mcp.NewRegistry(store.Queries())
	if _, err := mcp.RegisterServerForTest(context.Background(), store.Queries(), registry, "stub-server", mcpSrv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	testutil.InsertPolicy(t, store, "p-queue", "policy-p-queue", "webhook", queuePolicyYAML)
	parsed, err := policy.Parse(queuePolicyYAML, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	// Pre-load a queue entry so DrainQueue has something to launch.
	testutil.InsertQueueEntry(t, store, "p-queue", "webhook")

	resolvedTools, err := registry.ResolveForPolicy(context.Background(), parsed)
	if err != nil {
		t.Fatalf("ResolveForPolicy: %v", err)
	}

	runRow := insertRunRow(t, store, "p-queue")
	ba := buildBoundAgent(t, store, runRow, resolvedTools, parsed)

	manager := NewRunManager()
	launcher := NewRunLauncher(RunLauncherConfig{
		Store:    store,
		Resolver: NewDefaultToolResolver(registry, nil, nil),
		Manager:  manager,
		AgentFactory: func(cfg agent.Config) (*agent.BoundAgent, error) {
			cfg.LLMClient = testutil.NewFakeClientOnly(
				testutil.MakeLLMTextResponse("done", llm.StopReasonEndTurn, 10, 5),
			)
			return agent.New(cfg)
		},
		Publisher:              nil,
		DefaultFeedbackTimeout: 0,
		ModelResolver:          nil,
	})

	launcher.runAndDrain(context.Background(), runRow.ID, model.TriggerTypeWebhook, "p-queue", parsed, `{}`, ba)

	// Wait for the drained run goroutine to finish.
	manager.Wait()

	// The original run plus the drained run should both exist.
	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-queue", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) < 2 {
		t.Errorf("expected at least 2 runs (original + drained), got %d", len(runs))
	}

	// The queue should be empty after draining.
	count, err := store.CountQueuedTriggers(context.Background(), "p-queue")
	if err != nil {
		t.Fatalf("CountQueuedTriggers: %v", err)
	}
	if count != 0 {
		t.Errorf("expected queue to be empty after drain, got %d entries", count)
	}
}
