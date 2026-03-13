package trigger_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// integrationFakeMessages is a concurrency-safe test double for the Anthropic
// Messages API that returns pre-canned responses in sequence.
type integrationFakeMessages struct {
	mu        sync.Mutex
	responses []*anthropic.Message
	calls     int
}

func (f *integrationFakeMessages) New(ctx context.Context, body anthropic.MessageNewParams, opts ...option.RequestOption) (*anthropic.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls >= len(f.responses) {
		return nil, fmt.Errorf("fakeMessages: no more responses (called %d times)", f.calls)
	}
	resp := f.responses[f.calls]
	f.calls++
	return resp, nil
}

// makeTextMsg constructs an anthropic.Message with a text content block via
// JSON unmarshal so that AsAny() (which inspects the raw JSON) works correctly.
func makeTextMsg(text string) *anthropic.Message {
	raw, _ := json.Marshal(map[string]any{
		"id":            "msg_test",
		"type":          "message",
		"role":          "assistant",
		"stop_reason":   string(anthropic.StopReasonEndTurn),
		"stop_sequence": "",
		"model":         "claude-sonnet-4-6",
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"usage": map[string]any{
			"input_tokens":                int64(10),
			"output_tokens":               int64(5),
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
			"service_tier":                "standard",
		},
	})
	var msg anthropic.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		panic("makeTextMsg: " + err.Error())
	}
	return &msg
}

// makeToolUseMsg constructs a message with a tool_use content block via JSON
// unmarshal so that AsAny() works correctly. The tool name must match the full
// dot-notation name that the agent registers (server_name.tool_name).
func makeToolUseMsg(toolUseID, toolName string, input map[string]any) *anthropic.Message {
	inputJSON, _ := json.Marshal(input)
	raw, _ := json.Marshal(map[string]any{
		"id":            "msg_test",
		"type":          "message",
		"role":          "assistant",
		"stop_reason":   "tool_use",
		"stop_sequence": "",
		"model":         "claude-sonnet-4-6",
		"content": []map[string]any{
			{
				"type":  "tool_use",
				"id":    toolUseID,
				"name":  toolName,
				"input": json.RawMessage(inputJSON),
			},
		},
		"usage": map[string]any{
			"input_tokens":                int64(10),
			"output_tokens":               int64(5),
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
			"service_tier":                "standard",
		},
	})
	var msg anthropic.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		panic("makeToolUseMsg: " + err.Error())
	}
	return &msg
}

// newStubMCPServer starts an httptest.Server that handles MCP JSON-RPC over
// HTTP. It responds to tools/list with a single "read_data" tool and to all
// other methods with a stub result.
func newStubMCPServer(t *testing.T) *httptest.Server {
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

// setupIntegrationFixture opens a temp SQLite store, starts a stub MCP server,
// and registers it with a fresh Registry. Cleanup for both is registered via
// t.Cleanup — callers do not need to close anything manually.
func setupIntegrationFixture(t *testing.T) (*db.Store, *mcp.Registry) {
	t.Helper()
	store := testutil.NewTestStore(t)
	mcpSrv := newStubMCPServer(t)
	t.Cleanup(mcpSrv.Close)
	registry := mcp.NewRegistry(store.DB())
	if err := registry.RegisterServer(context.Background(), "stub-server", mcpSrv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}
	return store, registry
}

// integrationPolicy is a policy YAML that grants the stub-server.read_data
// sensor to the agent, with parallel concurrency so sub-tests can fire
// multiple concurrent runs if needed.
const integrationPolicy = `
name: integration-test-policy
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: stub-server.read_data
agent:
  model: claude-sonnet-4-6
  task: "run the integration test"
  concurrency: parallel
`

// buildIntegrationRouter wires a WebhookHandler and RunsHandler together into
// a chi router suitable for httptest requests. It returns the router and the
// RunManager so callers can call manager.Wait() for deterministic cleanup.
func buildIntegrationRouter(store *db.Store, registry *mcp.Registry, msgs agent.MessagesAPI) (http.Handler, *trigger.RunManager) {
	manager := trigger.NewRunManager()
	factory := trigger.AgentFactory(func(cfg agent.Config) (*agent.BoundAgent, error) {
		cfg.MessagesOverride = msgs
		return agent.New(cfg)
	})
	launcher := trigger.NewRunLauncher(store, registry, manager, factory, nil)
	wh := trigger.NewWebhookHandler(store, launcher)
	rh := trigger.NewRunsHandler(store, manager)

	// Reuse newRunsRouter for the runs routes so both stay in sync automatically.
	r := newRunsRouter(rh)
	r.Post("/api/v1/webhooks/{policyID}", wh.Handle)
	return r, manager
}

// waitForRun blocks until the run goroutine exits via manager.Wait(), then
// fetches and returns the run summary in a single GET. Because the goroutine
// writes the terminal DB status before calling Deregister, the GET after Wait()
// is guaranteed to observe the final status — no polling loop needed.
func waitForRun(t *testing.T, manager *trigger.RunManager, router http.Handler, runID string) trigger.RunSummary {
	t.Helper()
	manager.Wait()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /runs/%s: status %d", runID, rec.Code)
	}
	var env struct {
		Data trigger.RunSummary `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode run summary: %v", err)
	}
	return env.Data
}

// fireWebhook sends a POST to the webhook endpoint and returns the run_id from
// the 202 response body. Fails the test if the response code is not 202.
func fireWebhook(t *testing.T, router http.Handler, policyID string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/"+policyID,
		strings.NewReader(`{"event":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST webhook: status %d, body: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode webhook response: %v", err)
	}
	runID, ok := env.Data["run_id"]
	if !ok || runID == "" {
		t.Fatal("webhook response missing run_id")
	}
	return runID
}

func TestIntegration(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		store, registry := setupIntegrationFixture(t)

		insertTestPolicy(t, store, "pol-happy", integrationPolicy)

		// Two responses: tool-use on the first turn, then end-turn text.
		msgs := &integrationFakeMessages{
			responses: []*anthropic.Message{
				makeToolUseMsg("tu-1", "stub-server.read_data", map[string]any{}),
				makeTextMsg("All done."),
			},
		}

		router, manager := buildIntegrationRouter(store, registry, msgs)
		runID := fireWebhook(t, router, "pol-happy")

		summary := waitForRun(t, manager, router, runID)
		if summary.Status != string(model.RunStatusComplete) {
			t.Errorf("run status = %q, want %q", summary.Status, model.RunStatusComplete)
		}
		if summary.TokenCost <= 0 {
			t.Errorf("run token_cost = %d, want > 0", summary.TokenCost)
		}

		// Fetch and verify the step trace.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID+"/steps", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET steps: status %d", rec.Code)
		}
		var stepsEnv struct {
			Data []trigger.StepSummary `json:"data"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&stepsEnv); err != nil {
			t.Fatalf("decode steps: %v", err)
		}
		steps := stepsEnv.Data

		// Verify expected step types appear in order.
		wantTypes := []string{
			string(model.StepTypeCapabilitySnapshot),
			string(model.StepTypeToolCall),
			string(model.StepTypeToolResult),
			string(model.StepTypeThought),
			string(model.StepTypeComplete),
		}
		if len(steps) != len(wantTypes) {
			types := make([]string, len(steps))
			for i, s := range steps {
				types[i] = s.Type
			}
			t.Fatalf("step count = %d, want %d; types = %v", len(steps), len(wantTypes), types)
		}
		for i, wt := range wantTypes {
			if steps[i].Type != wt {
				t.Errorf("step[%d].Type = %q, want %q", i, steps[i].Type, wt)
			}
		}

		// Step numbers must be 1-indexed and contiguous.
		for i, s := range steps {
			want := int64(i + 1)
			if s.StepNumber != want {
				t.Errorf("step[%d].StepNumber = %d, want %d", i, s.StepNumber, want)
			}
		}
	})

	t.Run("concurrent_fires", func(t *testing.T) {
		// Both runs share one store, registry, and RunManager to exercise the
		// real concurrent-write path (two goroutines writing steps to the same
		// SQLite DB simultaneously).
		//
		// The fake has 4 responses: [tool_use, tool_use, text, text]. Because
		// both runs call the same tool on the same stub server, whichever run
		// grabs the first tool_use response proceeds correctly — the tool name
		// and input are identical for both. The first two API calls (one per run)
		// each consume a tool_use; the next two (after tool dispatch) each
		// consume a text/end_turn. Order of consumption is non-deterministic but
		// every interleaving produces two complete runs.
		store, registry := setupIntegrationFixture(t)
		insertTestPolicy(t, store, "pol-concurrent", integrationPolicy)

		msgs := &integrationFakeMessages{
			responses: []*anthropic.Message{
				makeToolUseMsg("tu-1", "stub-server.read_data", map[string]any{}),
				makeToolUseMsg("tu-2", "stub-server.read_data", map[string]any{}),
				makeTextMsg("Done A."),
				makeTextMsg("Done B."),
			},
		}
		router, manager := buildIntegrationRouter(store, registry, msgs)

		// Fire both webhooks before waiting so the goroutines run in parallel.
		idA := fireWebhook(t, router, "pol-concurrent")
		idB := fireWebhook(t, router, "pol-concurrent")

		// waitForRun calls manager.Wait() which blocks until ALL registered
		// goroutines finish — so the first call waits for both runs.
		summaryA := waitForRun(t, manager, router, idA)
		summaryB := waitForRun(t, manager, router, idB)
		for _, summary := range []trigger.RunSummary{summaryA, summaryB} {
			if summary.Status != string(model.RunStatusComplete) {
				t.Errorf("run %s status = %q, want %q", summary.ID, summary.Status, model.RunStatusComplete)
			}
		}

		// Verify each run has its own non-empty, contiguous step trace.
		for _, id := range []string{idA, idB} {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+id+"/steps", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET steps for %s: status %d", id, rec.Code)
			}
			var stepsEnv struct {
				Data []trigger.StepSummary `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&stepsEnv); err != nil {
				t.Fatalf("decode steps for %s: %v", id, err)
			}
			steps := stepsEnv.Data
			if len(steps) == 0 {
				t.Errorf("run %s: expected non-zero steps, got 0", id)
			}
			for i, s := range steps {
				if s.StepNumber != int64(i+1) {
					t.Errorf("run %s step[%d].StepNumber = %d, want %d", id, i, s.StepNumber, i+1)
				}
			}
		}
	})

	t.Run("unknown_policy", func(t *testing.T) {
		store, registry := setupIntegrationFixture(t)

		msgs := &integrationFakeMessages{}
		router, _ := buildIntegrationRouter(store, registry, msgs)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/nonexistent-policy",
			strings.NewReader(`{"event":"test"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
		}
	})
}
