package policy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// stubLookup implements ToolLookup for testing.
type stubLookup struct {
	existing map[string]bool // key: "server.tool"
}

func (s *stubLookup) ToolExists(_ context.Context, serverName, toolName string) (bool, error) {
	return s.existing[serverName+"."+toolName], nil
}

// stubModelValidator implements ModelValidator for testing.
type stubModelValidator struct {
	err error // if non-nil, ValidateModel returns this error
}

func (s *stubModelValidator) ValidateModel(_ context.Context, _ string) error {
	return s.err
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
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil)

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
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil)

	_, err := svc.Create(context.Background(), `name: ""`)
	if err == nil {
		t.Fatal("expected error for invalid policy")
	}
}

func TestService_Create_ParseError(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil)

	_, err := svc.Create(context.Background(), "{{bad yaml")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestService_Create_ToolWarnings(t *testing.T) {
	store := testutil.NewTestStore(t)
	lookup := &stubLookup{existing: map[string]bool{}}
	svc := NewService(store, lookup, nil)

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
	store := testutil.NewTestStore(t)
	lookup := &stubLookup{existing: map[string]bool{"github.list_repos": true}}
	svc := NewService(store, lookup, nil)

	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", result.Warnings)
	}
}

func TestService_Update(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil)

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

func TestService_Update_ChangedTriggerType_WebhookToManual(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil)

	createResult, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if createResult.Policy.TriggerType != model.TriggerTypeWebhook {
		t.Fatalf("initial trigger_type = %q, want webhook", createResult.Policy.TriggerType)
	}

	manualYAML := `
name: test-policy-renamed
trigger:
  type: manual
capabilities:
  sensors:
    - tool: github.list_repos
agent:
  task: Check repos on demand
`
	result, err := svc.Update(context.Background(), createResult.Policy.ID, manualYAML)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.Policy.TriggerType != model.TriggerTypeManual {
		t.Errorf("trigger_type = %q after update, want manual", result.Policy.TriggerType)
	}
	if result.Policy.Name != "test-policy-renamed" {
		t.Errorf("name = %q after update, want test-policy-renamed", result.Policy.Name)
	}
}

func TestService_Create_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	store := testutil.NewTestStore(t)
	lookup := &stubLookup{existing: map[string]bool{}}
	svc := NewService(store, lookup, nil)

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

func TestService_Create_ModelValidatorCalled(t *testing.T) {
	store := testutil.NewTestStore(t)
	mv := &stubModelValidator{err: errors.New("model not found")}
	svc := NewService(store, nil, mv)

	_, err := svc.Create(context.Background(), validYAML)
	if err == nil {
		t.Fatal("expected error from model validator, got nil")
	}
}

func TestService_Create_NilModelValidatorSkipsCheck(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil)

	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Policy.ID == "" {
		t.Error("expected non-empty policy ID")
	}
}

func TestService_Update_ModelValidatorCalled(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil)

	createResult, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	mv := &stubModelValidator{err: errors.New("model not found")}
	svcWithMV := NewService(store, nil, mv)

	_, err = svcWithMV.Update(context.Background(), createResult.Policy.ID, validYAML)
	if err == nil {
		t.Fatal("expected error from model validator on update, got nil")
	}
}

func TestToModelPolicy_ValidTimestamps(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Nanosecond)
	nowStr := now.Format(time.RFC3339Nano)
	pausedStr := now.Add(time.Minute).Format(time.RFC3339Nano)
	row := db.Policy{
		ID:          "id1",
		Name:        "p",
		TriggerType: string(model.TriggerTypeWebhook),
		Yaml:        "yaml",
		CreatedAt:   nowStr,
		UpdatedAt:   nowStr,
		PausedAt:    &pausedStr,
	}

	p, err := toModelPolicy(row)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", p.CreatedAt, now)
	}
	if !p.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", p.UpdatedAt, now)
	}
	want := now.Add(time.Minute)
	if p.PausedAt == nil || !p.PausedAt.Equal(want) {
		t.Errorf("PausedAt = %v, want %v", p.PausedAt, want)
	}
}

func TestToModelPolicy_InvalidCreatedAt(t *testing.T) {
	row := db.Policy{
		CreatedAt: "not-a-time",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	_, err := toModelPolicy(row)
	if err == nil {
		t.Fatal("expected error for invalid created_at, got nil")
	}
	if !strings.Contains(err.Error(), "parse created_at") {
		t.Errorf("error %q does not contain %q", err.Error(), "parse created_at")
	}
}

func TestToModelPolicy_InvalidUpdatedAt(t *testing.T) {
	row := db.Policy{
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt: "not-a-time",
	}
	_, err := toModelPolicy(row)
	if err == nil {
		t.Fatal("expected error for invalid updated_at, got nil")
	}
	if !strings.Contains(err.Error(), "parse updated_at") {
		t.Errorf("error %q does not contain %q", err.Error(), "parse updated_at")
	}
}

func TestToModelPolicy_InvalidPausedAt(t *testing.T) {
	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	bad := "not-a-time"
	row := db.Policy{
		CreatedAt: nowStr,
		UpdatedAt: nowStr,
		PausedAt:  &bad,
	}
	_, err := toModelPolicy(row)
	if err == nil {
		t.Fatal("expected error for invalid paused_at, got nil")
	}
	if !strings.Contains(err.Error(), "parse paused_at") {
		t.Errorf("error %q does not contain %q", err.Error(), "parse paused_at")
	}
}

func TestToModelPolicy_NilPausedAt(t *testing.T) {
	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	row := db.Policy{
		CreatedAt: nowStr,
		UpdatedAt: nowStr,
		PausedAt:  nil,
	}
	p, err := toModelPolicy(row)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.PausedAt != nil {
		t.Errorf("PausedAt = %v, want nil", p.PausedAt)
	}
}
