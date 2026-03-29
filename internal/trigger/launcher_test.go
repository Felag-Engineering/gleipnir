package trigger_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/testutil"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// failingAgentFactory returns an AgentFactory whose New call always fails.
func failingAgentFactory(err error) trigger.AgentFactory {
	return func(cfg agent.Config) (*agent.BoundAgent, error) {
		return nil, err
	}
}

func TestCheckConcurrency(t *testing.T) {
	cases := []struct {
		name        string
		hasActive   bool
		concurrency model.ConcurrencyPolicy
		wantErr     error
		wantNil     bool
	}{
		{
			name:        "skip with active run returns ErrConcurrencySkipActive",
			hasActive:   true,
			concurrency: model.ConcurrencySkip,
			wantErr:     trigger.ErrConcurrencySkipActive,
		},
		{
			name:        "skip with no active runs returns nil",
			hasActive:   false,
			concurrency: model.ConcurrencySkip,
			wantNil:     true,
		},
		{
			name:        "parallel returns nil",
			concurrency: model.ConcurrencyParallel,
			wantNil:     true,
		},
		{
			name:        "queue returns ErrConcurrencyNotImplemented",
			concurrency: model.ConcurrencyQueue,
			wantErr:     trigger.ErrConcurrencyNotImplemented,
		},
		{
			name:        "replace returns ErrConcurrencyNotImplemented",
			concurrency: model.ConcurrencyReplace,
			wantErr:     trigger.ErrConcurrencyNotImplemented,
		},
		{
			name:        "unknown concurrency returns ErrConcurrencyUnrecognised",
			concurrency: model.ConcurrencyPolicy("unknown"),
			wantErr:     trigger.ErrConcurrencyUnrecognised,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			policyID := "cp-test"
			insertTestPolicy(t, store, policyID, minimalWebhookPolicy)

			if tc.hasActive {
				insertTestRun(t, store, "r-active", policyID, model.RunStatusRunning)
			}

			registry := mcp.NewRegistry(store.Queries())
			manager := trigger.NewRunManager()
			// factory is nil — CheckConcurrency never calls it.
			launcher := trigger.NewRunLauncher(store, registry, manager, nil, nil)

			err := launcher.CheckConcurrency(context.Background(), policyID, tc.concurrency)
			if tc.wantNil {
				if err != nil {
					t.Errorf("CheckConcurrency() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("CheckConcurrency() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestLaunch_ToolResolutionFailure(t *testing.T) {
	// Policy grants a tool that does not exist in the registry — ResolveForPolicy
	// returns an error. Launch should mark the run failed and return an error.
	store := testutil.NewTestStore(t)
	// No MCP server registered, so any tool reference will fail resolution.
	registry := mcp.NewRegistry(store.Queries())
	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, nil, nil)

	const policyWithMissingTool = `
name: tool-failure-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: nonexistent-server.some_tool
agent:
  model: claude-opus-4-5
  task: "test task"
  concurrency: parallel
`
	insertTestPolicy(t, store, "p-tool-fail", policyWithMissingTool)
	parsed, err := policy.Parse(policyWithMissingTool, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	_, launchErr := launcher.Launch(context.Background(), trigger.LaunchParams{
		PolicyID:       "p-tool-fail",
		TriggerType:    model.TriggerTypeWebhook,
		TriggerPayload: `{}`,
		ParsedPolicy:   parsed,
	})
	if launchErr == nil {
		t.Fatal("Launch() expected error on tool resolution failure, got nil")
	}

	// The run should exist in DB and be marked failed.
	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-tool-fail", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected run to be created before failure, got 0 runs")
	}
	if runs[0].Status != string(model.RunStatusFailed) {
		t.Errorf("run.Status = %q, want %q", runs[0].Status, model.RunStatusFailed)
	}
}

func TestLaunch_AgentConstructionFailure(t *testing.T) {
	// Factory always returns an error — agent construction fails after tools are resolved.
	store, registry := setupIntegrationFixture(t)
	manager := trigger.NewRunManager()

	agentErr := errors.New("deliberate construction failure")
	launcher := trigger.NewRunLauncher(store, registry, manager, failingAgentFactory(agentErr), nil)

	const launchPolicy = `
name: agent-fail-policy
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
	insertTestPolicy(t, store, "p-agent-fail", launchPolicy)
	parsed, err := policy.Parse(launchPolicy, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	_, launchErr := launcher.Launch(context.Background(), trigger.LaunchParams{
		PolicyID:       "p-agent-fail",
		TriggerType:    model.TriggerTypeWebhook,
		TriggerPayload: `{}`,
		ParsedPolicy:   parsed,
	})
	if launchErr == nil {
		t.Fatal("Launch() expected error on agent construction failure, got nil")
	}

	// The run should exist in DB and be marked failed.
	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-agent-fail", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected run to be created before failure, got 0 runs")
	}
	if runs[0].Status != string(model.RunStatusFailed) {
		t.Errorf("run.Status = %q, want %q", runs[0].Status, model.RunStatusFailed)
	}
}

func TestLaunch_Successful(t *testing.T) {
	// Full happy-path launch: run should appear in DB with correct trigger_type
	// and payload, and LaunchResult.RunID should be non-empty.
	store, registry := setupIntegrationFixture(t)
	manager := trigger.NewRunManager()
	launcher := trigger.NewRunLauncher(store, registry, manager, schedulerFactory(), nil)

	const launchPolicy = `
name: launch-success-policy
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
	insertTestPolicy(t, store, "p-launch-ok", launchPolicy)
	parsed, err := policy.Parse(launchPolicy, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}

	result, err := launcher.Launch(context.Background(), trigger.LaunchParams{
		PolicyID:       "p-launch-ok",
		TriggerType:    model.TriggerTypeWebhook,
		TriggerPayload: `{"event":"test"}`,
		ParsedPolicy:   parsed,
	})
	if err != nil {
		t.Fatalf("Launch() unexpected error: %v", err)
	}
	if result.RunID == "" {
		t.Fatal("Launch() returned empty RunID")
	}

	// Run must exist in the DB immediately (created synchronously before goroutine launches).
	runs, err := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-launch-ok", Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected run in DB after successful Launch, got 0")
	}
	run := runs[0]
	if run.ID != result.RunID {
		t.Errorf("run.ID = %q, want %q", run.ID, result.RunID)
	}
	if run.TriggerType != string(model.TriggerTypeWebhook) {
		t.Errorf("run.TriggerType = %q, want %q", run.TriggerType, model.TriggerTypeWebhook)
	}
	if run.TriggerPayload != `{"event":"test"}` {
		t.Errorf("run.TriggerPayload = %q, want %q", run.TriggerPayload, `{"event":"test"}`)
	}

	// Wait for the background goroutine to finish so the test does not leak goroutines.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		updated, _ := store.ListRuns(context.Background(), db.ListRunsParams{PolicyID: "p-launch-ok", Limit: 100})
		if len(updated) > 0 && updated[0].Status != string(model.RunStatusPending) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	manager.Wait()
}

// TestNewAgentFactory_ProviderLookup verifies that NewAgentFactory resolves the
// correct LLMClient from the registry using the policy's Agent.Provider field,
// and returns a descriptive error for unknown providers.
//
// Note: TestLaunch_Successful does not exercise the registry path — it uses
// schedulerFactory(), an inline factory that bypasses NewAgentFactory.
func TestNewAgentFactory_ProviderLookup(t *testing.T) {
	anthropicClient := testutil.NewNoopLLMClient()
	googleClient := testutil.NewNoopLLMClient()

	cases := []struct {
		name            string
		registerClients map[string]llm.LLMClient
		policyProvider  string
		wantErrContains string // empty means no error expected from provider lookup
	}{
		{
			name:            "known provider anthropic",
			registerClients: map[string]llm.LLMClient{"anthropic": anthropicClient},
			policyProvider:  "anthropic",
			// error may come from agent.New (missing state machine etc.) but
			// must NOT be a provider lookup error
			wantErrContains: "",
		},
		{
			name:            "known provider google",
			registerClients: map[string]llm.LLMClient{"google": googleClient},
			policyProvider:  "google",
			wantErrContains: "",
		},
		{
			name:            "unknown provider openai",
			registerClients: map[string]llm.LLMClient{"anthropic": anthropicClient},
			policyProvider:  "openai",
			wantErrContains: "openai",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := llm.NewProviderRegistry()
			for name, client := range tc.registerClients {
				reg.Register(name, client)
			}

			factory := trigger.NewAgentFactory(reg)

			cfg := agent.Config{
				Policy: &model.ParsedPolicy{
					Agent: model.AgentConfig{
						ModelConfig: model.ModelConfig{Provider: tc.policyProvider},
					},
				},
			}

			_, err := factory(cfg)

			if tc.wantErrContains != "" {
				// Unknown provider — must get an error containing the provider name.
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrContains)
				}
				if !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrContains)
				}
			} else {
				// Known provider — error must not be a provider lookup failure.
				// agent.New will fail due to the minimal config, which is acceptable.
				if err != nil && strings.Contains(err.Error(), "unknown LLM provider") {
					t.Errorf("unexpected provider lookup error: %v", err)
				}
			}
		})
	}
}
