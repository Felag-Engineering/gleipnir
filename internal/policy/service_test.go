package policy

import (
	"context"
	"testing"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// stubLookup implements ToolLookup for testing.
type stubLookup struct {
	existing map[string]bool // key: "server.tool"
}

func (s *stubLookup) ToolExists(_ context.Context, serverName, toolName string) (bool, error) {
	return s.existing[serverName+"."+toolName], nil
}

func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

const validYAML = `
name: test-policy
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: github.list_repos
agent:
  task: Check all repos
`

func TestService_Create(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(store, nil)

	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Policy.Name != "test-policy" {
		t.Errorf("name = %q, want %q", result.Policy.Name, "test-policy")
	}
	if result.Policy.TriggerType != model.TriggerTypeWebhook {
		t.Errorf("trigger_type = %q, want %q", result.Policy.TriggerType, model.TriggerTypeWebhook)
	}
	if result.Policy.ID == "" {
		t.Error("expected non-empty policy ID")
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings (nil lookup), got: %v", result.Warnings)
	}
}

func TestService_Create_ValidationError(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(store, nil)

	_, err := svc.Create(context.Background(), `name: ""`)
	if err == nil {
		t.Fatal("expected error for invalid policy")
	}
}

func TestService_Create_ParseError(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(store, nil)

	_, err := svc.Create(context.Background(), "{{bad yaml")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestService_Create_ToolWarnings(t *testing.T) {
	store := newTestStore(t)
	lookup := &stubLookup{existing: map[string]bool{}}
	svc := NewService(store, lookup)

	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(result.Warnings), result.Warnings)
	}
	if result.Warnings[0] != `tool "github.list_repos" not found in MCP registry` {
		t.Errorf("unexpected warning: %s", result.Warnings[0])
	}
}

func TestService_Create_NoWarningWhenToolExists(t *testing.T) {
	store := newTestStore(t)
	lookup := &stubLookup{existing: map[string]bool{"github.list_repos": true}}
	svc := NewService(store, lookup)

	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", result.Warnings)
	}
}

func TestService_Update(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(store, nil)

	createResult, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updatedYAML := `
name: test-policy
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: github.list_repos
    - tool: github.list_issues
agent:
  task: Check all repos and issues
`
	result, err := svc.Update(context.Background(), createResult.Policy.ID, updatedYAML)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.Policy.YAML != updatedYAML {
		t.Error("expected updated YAML to be stored")
	}
}

func TestService_Update_ChangedTriggerType(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(store, nil)

	createResult, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if createResult.Policy.TriggerType != model.TriggerTypeWebhook {
		t.Fatalf("initial trigger_type = %q, want webhook", createResult.Policy.TriggerType)
	}

	cronYAML := `
name: test-policy-renamed
trigger:
  type: cron
  schedule: "0 * * * *"
capabilities:
  sensors:
    - tool: github.list_repos
agent:
  task: Check repos on schedule
`
	result, err := svc.Update(context.Background(), createResult.Policy.ID, cronYAML)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.Policy.TriggerType != model.TriggerTypeCron {
		t.Errorf("trigger_type = %q after update, want cron", result.Policy.TriggerType)
	}
	if result.Policy.Name != "test-policy-renamed" {
		t.Errorf("name = %q after update, want test-policy-renamed", result.Policy.Name)
	}
}

func TestService_Create_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	store := newTestStore(t)
	lookup := &stubLookup{existing: map[string]bool{}}
	svc := NewService(store, lookup)

	// Parse + validate don't use context, so we test checkToolRefs directly.
	yamlWithManyTools := `
name: ctx-test
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: a.one
    - tool: a.two
    - tool: a.three
agent:
  task: test
`
	parsed, err := Parse(yamlWithManyTools)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	warnings := svc.checkToolRefs(ctx, parsed)
	if len(warnings) != 1 {
		t.Fatalf("expected 1 abort warning, got %d: %v", len(warnings), warnings)
	}
	if warnings[0] == "" {
		t.Error("expected non-empty warning")
	}
}
