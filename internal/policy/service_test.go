package policy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/settings"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// stubLookup implements ToolLookup for testing. canonical maps "server.tool"
// to the canonical schema LookupTool should return for that entry; a key
// present in existing but absent from canonical returns a nil schema (the
// documented "no canonical form stored" case). err, when set, is returned
// from every LookupTool call (drives the lookup-error path).
type stubLookup struct {
	existing  map[string]bool // key: "server.tool"
	canonical map[string]json.RawMessage
	err       error
}

func (s *stubLookup) LookupTool(_ context.Context, serverName, toolName string) (bool, json.RawMessage, error) {
	if s.err != nil {
		return false, nil, s.err
	}
	key := serverName + "." + toolName
	return s.existing[key], s.canonical[key], nil
}

// stubModelValidator implements ModelValidator for testing.
type stubModelValidator struct {
	err error // if non-nil, ValidateModelName returns this error
}

func (s *stubModelValidator) ValidateModelName(_ context.Context, provider, modelName string) error {
	return s.err
}

const validYAML = `
name: test-policy
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: github.list_repos
agent:
  task: Check all repos
`

func TestService_Create(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil, nil, nil)

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
	svc := NewService(store, nil, nil, nil, nil)

	_, err := svc.Create(context.Background(), `name: ""`)
	if err == nil {
		t.Fatal("expected error for invalid policy")
	}
}

func TestService_Create_ParseError(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil, nil, nil)

	_, err := svc.Create(context.Background(), "{{bad yaml")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestService_Create_ToolWarnings(t *testing.T) {
	store := testutil.NewTestStore(t)
	lookup := &stubLookup{existing: map[string]bool{}}
	svc := NewService(store, lookup, nil, nil, nil)

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
	svc := NewService(store, lookup, nil, nil, nil)

	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", result.Warnings)
	}
}

// validYAMLWithParams grants github.list_repos with a params block scoping
// the "a" key — used by the ADR-017 save-time params-scope tests below.
const validYAMLWithParams = `
name: test-policy
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: github.list_repos
      params:
        a: 1
agent:
  task: Check all repos
`

func TestService_Create_RejectsUnknownParamKey(t *testing.T) {
	store := testutil.NewTestStore(t)
	lookup := &stubLookup{
		existing:  map[string]bool{"github.list_repos": true},
		canonical: map[string]json.RawMessage{"github.list_repos": json.RawMessage(`{"type":"object","properties":{"b":{}}}`)},
	}
	svc := NewService(store, lookup, nil, nil, nil)

	_, err := svc.Create(context.Background(), validYAMLWithParams)
	if err == nil {
		t.Fatal("expected error for unknown param key, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if len(ve.Errors) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(ve.Errors), ve.Errors)
	}
	if ve.Errors[0].Field != "capabilities.tools[0].params.a" {
		t.Errorf("Field = %q, want capabilities.tools[0].params.a", ve.Errors[0].Field)
	}
	wantMsg := `"a" is not a top-level property of tool "github.list_repos"`
	if ve.Errors[0].Message != wantMsg {
		t.Errorf("Message = %q, want %q", ve.Errors[0].Message, wantMsg)
	}

	policies, listErr := store.ListPolicies(context.Background())
	if listErr != nil {
		t.Fatalf("list: %v", listErr)
	}
	if len(policies) != 0 {
		t.Errorf("expected 0 saved policies, got %d", len(policies))
	}
}

func TestService_Create_RejectsOneOfGovernedParamKey(t *testing.T) {
	store := testutil.NewTestStore(t)
	lookup := &stubLookup{
		existing:  map[string]bool{"github.list_repos": true},
		canonical: map[string]json.RawMessage{"github.list_repos": json.RawMessage(`{"oneOf":[{"properties":{"a":{}}},{"properties":{"b":{}}}]}`)},
	}
	svc := NewService(store, lookup, nil, nil, nil)

	_, err := svc.Create(context.Background(), validYAMLWithParams)
	if err == nil {
		t.Fatal("expected error for oneOf-governed param key, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if len(ve.Errors) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(ve.Errors), ve.Errors)
	}
	if ve.Errors[0].Field != "capabilities.tools[0].params.a" {
		t.Errorf("Field = %q, want capabilities.tools[0].params.a", ve.Errors[0].Field)
	}
	wantMsg := `cannot scope "a" — tool "github.list_repos" declares a top-level "oneOf"; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas`
	if ve.Errors[0].Message != wantMsg {
		t.Errorf("Message = %q, want %q", ve.Errors[0].Message, wantMsg)
	}
}

func TestService_Create_RejectsParamsWhenCanonicalSchemaMissing(t *testing.T) {
	store := testutil.NewTestStore(t)
	lookup := &stubLookup{existing: map[string]bool{"github.list_repos": true}}
	svc := NewService(store, lookup, nil, nil, nil)

	_, err := svc.Create(context.Background(), validYAMLWithParams)
	if err == nil {
		t.Fatal("expected error for missing canonical schema, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if len(ve.Errors) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(ve.Errors), ve.Errors)
	}
	if ve.Errors[0].Field != "capabilities.tools[0].params" {
		t.Errorf("Field = %q, want capabilities.tools[0].params", ve.Errors[0].Field)
	}
	wantMsg := `tool "github.list_repos" has no stored canonical schema — schema could not be canonicalized; parameter scoping unavailable for this tool (refresh the MCP server's tools, then save again)`
	if ve.Errors[0].Message != wantMsg {
		t.Errorf("Message = %q, want %q", ve.Errors[0].Message, wantMsg)
	}
}

func TestService_Create_AllowsToolWithoutParamsWhenCanonicalSchemaMissing(t *testing.T) {
	store := testutil.NewTestStore(t)
	lookup := &stubLookup{existing: map[string]bool{"github.list_repos": true}}
	svc := NewService(store, lookup, nil, nil, nil)

	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", result.Warnings)
	}
}

func TestService_Create_AcceptsPlainTopLevelParamKeys(t *testing.T) {
	store := testutil.NewTestStore(t)
	lookup := &stubLookup{
		existing:  map[string]bool{"github.list_repos": true},
		canonical: map[string]json.RawMessage{"github.list_repos": json.RawMessage(`{"type":"object","properties":{"a":{},"b":{}}}`)},
	}
	svc := NewService(store, lookup, nil, nil, nil)

	result, err := svc.Create(context.Background(), validYAMLWithParams)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", result.Warnings)
	}
}

func TestService_Update_RejectsUnknownParamKey(t *testing.T) {
	store := testutil.NewTestStore(t)
	lookup := &stubLookup{
		existing:  map[string]bool{"github.list_repos": true},
		canonical: map[string]json.RawMessage{"github.list_repos": json.RawMessage(`{"type":"object","properties":{"a":{},"b":{}}}`)},
	}
	svc := NewService(store, lookup, nil, nil, nil)

	createResult, err := svc.Create(context.Background(), validYAMLWithParams)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Swap the stub's canonical schema so it no longer declares "a" — this
	// mirrors a server refresh (or a schema change) narrowing the property
	// set out from under an already-saved policy.
	lookup.canonical["github.list_repos"] = json.RawMessage(`{"type":"object","properties":{"b":{}}}`)

	_, err = svc.Update(context.Background(), createResult.Policy.ID, validYAMLWithParams)
	if err == nil {
		t.Fatal("expected error for unknown param key on update, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if len(ve.Errors) != 1 || ve.Errors[0].Field != "capabilities.tools[0].params.a" {
		t.Fatalf("unexpected issues: %v", ve.Errors)
	}

	// The stored YAML must be unchanged by the rejected update.
	stored, getErr := store.GetPolicy(context.Background(), createResult.Policy.ID)
	if getErr != nil {
		t.Fatalf("get policy: %v", getErr)
	}
	if stored.Yaml != validYAMLWithParams {
		t.Error("expected stored YAML to be unchanged after rejected update")
	}
}

func TestService_Create_ParamsSkippedWhenToolNotInRegistry(t *testing.T) {
	store := testutil.NewTestStore(t)
	lookup := &stubLookup{existing: map[string]bool{}}
	svc := NewService(store, lookup, nil, nil, nil)

	result, err := svc.Create(context.Background(), validYAMLWithParams)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(result.Warnings), result.Warnings)
	}
	// With a params block present, the carve-out warning must say the block
	// was NOT verified — not just that the tool wasn't found (security review
	// finding 1, #745 cycle 2).
	wantWarning := `tool "github.list_repos" not found in MCP registry; its params block was NOT verified against a canonical schema`
	if result.Warnings[0] != wantWarning {
		t.Errorf("warning = %q, want %q", result.Warnings[0], wantWarning)
	}
}

func TestService_Create_LookupErrorWithParamsIsBlocking(t *testing.T) {
	store := testutil.NewTestStore(t)
	lookupErr := errors.New("db unavailable")
	lookup := &stubLookup{err: lookupErr}
	svc := NewService(store, lookup, nil, nil, nil)

	_, err := svc.Create(context.Background(), validYAMLWithParams)
	if err == nil {
		t.Fatal("expected error when lookup fails for a tool with params, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if len(ve.Errors) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(ve.Errors), ve.Errors)
	}
	// The underlying DB error text must NOT reach the client (security review
	// finding 3, #745 cycle 2) — the message is fixed, not %v-wrapped.
	wantMsg := `could not verify parameter scoping for tool "github.list_repos"; try again`
	if ve.Errors[0].Message != wantMsg {
		t.Errorf("Message = %q, want %q", ve.Errors[0].Message, wantMsg)
	}
	if strings.Contains(ve.Errors[0].Message, lookupErr.Error()) {
		t.Errorf("Message leaks the underlying lookup error: %q", ve.Errors[0].Message)
	}
}

func TestService_Create_LookupErrorWithoutParamsIsNonBlocking(t *testing.T) {
	store := testutil.NewTestStore(t)
	lookupErr := errors.New("db unavailable")
	lookup := &stubLookup{err: lookupErr}
	svc := NewService(store, lookup, nil, nil, nil)

	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(result.Warnings), result.Warnings)
	}
	wantWarning := `could not check tool "github.list_repos": db unavailable`
	if result.Warnings[0] != wantWarning {
		t.Errorf("warning = %q, want %q", result.Warnings[0], wantWarning)
	}
}

func TestService_Update(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil, nil, nil)

	createResult, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updatedYAML := `
name: test-policy
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
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
	svc := NewService(store, nil, nil, nil, nil)

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
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
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
	svc := NewService(store, lookup, nil, nil, nil)

	// Parse + validate don't use context, so we test checkToolRefs directly.
	// a.three carries a params block: since ctx is already cancelled, none of
	// these three tools are ever looked up, and a.three's scoping is
	// therefore unverifiable — the fail-closed fix (security review finding
	// 4, #745 cycle 2) must report it as a blocking issue rather than
	// silently letting the caller persist an unverified narrowing.
	yamlWithManyTools := `
name: ctx-test
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: a.one
    - tool: a.two
    - tool: a.three
      params:
        x: 1
agent:
  task: test
`
	parsed, err := Parse(yamlWithManyTools, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	warnings, issues := svc.checkToolRefs(ctx, parsed)
	if len(warnings) != 1 {
		t.Fatalf("expected 1 abort warning, got %d: %v", len(warnings), warnings)
	}
	if warnings[0] == "" {
		t.Error("expected non-empty warning")
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue (a.three's unverifiable params block), got %d: %v", len(issues), issues)
	}
	if issues[0].Field != "capabilities.tools[2].params" {
		t.Errorf("Field = %q, want capabilities.tools[2].params", issues[0].Field)
	}
	wantPrefix := `could not verify parameter scoping for tool "a.three": `
	if !strings.HasPrefix(issues[0].Message, wantPrefix) {
		t.Errorf("Message = %q, want prefix %q", issues[0].Message, wantPrefix)
	}
}

// TestService_Create_ContextCancelled_NoParamsNoIssues locks the companion
// case: a cancelled context with no params anywhere still yields zero issues
// (the fail-closed fix only fires for tools that actually carry params).
func TestService_Create_ContextCancelled_NoParamsNoIssues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	store := testutil.NewTestStore(t)
	lookup := &stubLookup{existing: map[string]bool{}}
	svc := NewService(store, lookup, nil, nil, nil)

	yamlWithManyTools := `
name: ctx-test
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: a.one
    - tool: a.two
    - tool: a.three
agent:
  task: test
`
	parsed, err := Parse(yamlWithManyTools, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	warnings, issues := svc.checkToolRefs(ctx, parsed)
	if len(warnings) != 1 {
		t.Fatalf("expected 1 abort warning, got %d: %v", len(warnings), warnings)
	}
	if len(issues) != 0 {
		t.Errorf("expected 0 issues, got %d: %v", len(issues), issues)
	}
}

func TestService_Create_ModelValidatorCalled(t *testing.T) {
	store := testutil.NewTestStore(t)
	mv := &stubModelValidator{err: errors.New("model not found")}
	svc := NewService(store, nil, mv, nil, nil)

	// Model validation failures are non-blocking — the policy is saved and
	// the error is reported as a warning so a missing API key doesn't hard-block saves.
	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(result.Warnings), result.Warnings)
	}
	if result.Warnings[0] != "model not found" {
		t.Errorf("unexpected warning: %s", result.Warnings[0])
	}
}

func TestService_Create_NilModelValidatorSkipsCheck(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil, nil, nil)

	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Policy.ID == "" {
		t.Error("expected non-empty policy ID")
	}
}

func TestService_Create_ModelValidationWarningIncludesContext(t *testing.T) {
	store := testutil.NewTestStore(t)
	mv := &stubModelValidator{err: fmt.Errorf("unknown Anthropic model %q", "claude-sonnet-4-6")}
	svc := NewService(store, nil, mv, nil, nil)

	result, err := svc.Create(context.Background(), validYAMLWithOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(result.Warnings), result.Warnings)
	}
	if !strings.Contains(result.Warnings[0], "claude-sonnet-4-6") {
		t.Errorf("warning %q does not mention model name", result.Warnings[0])
	}
}

func TestService_Update_ModelValidatorCalled(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil, nil, nil)

	createResult, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	mv := &stubModelValidator{err: errors.New("model not found")}
	svcWithMV := NewService(store, nil, mv, nil, nil)

	// Model validation failures are non-blocking — the update succeeds and the
	// error surfaces as a warning.
	result, err := svcWithMV.Update(context.Background(), createResult.Policy.ID, validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(result.Warnings), result.Warnings)
	}
	if result.Warnings[0] != "model not found" {
		t.Errorf("unexpected warning: %s", result.Warnings[0])
	}
}

// stubOptionsValidator implements OptionsValidator for testing.
type stubOptionsValidator struct {
	err error // returned from ValidateProviderOptions
}

func (s *stubOptionsValidator) ValidateProviderOptions(provider string, options map[string]any) error {
	return s.err
}

const validYAMLWithOptions = `
name: test-policy
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
  options:
    temperature: 0.7
capabilities:
  tools:
    - tool: github.list_repos
agent:
  task: Check all repos
`

func TestService_Create_ValidOptionsPass(t *testing.T) {
	store := testutil.NewTestStore(t)
	ov := &stubOptionsValidator{err: nil}
	svc := NewService(store, nil, nil, ov, nil)

	result, err := svc.Create(context.Background(), validYAMLWithOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Policy.Name != "test-policy" {
		t.Errorf("name = %q, want %q", result.Policy.Name, "test-policy")
	}
}

func TestService_Create_InvalidOptionsError(t *testing.T) {
	store := testutil.NewTestStore(t)
	ov := &stubOptionsValidator{err: fmt.Errorf("provider %q: temperature must be between 0 and 1", "anthropic")}
	svc := NewService(store, nil, nil, ov, nil)

	_, err := svc.Create(context.Background(), validYAMLWithOptions)
	if err == nil {
		t.Fatal("expected error for invalid options, got nil")
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Errorf("error %q does not contain provider name %q", err.Error(), "anthropic")
	}

	// Verify the policy was NOT saved.
	policies, listErr := store.ListPolicies(context.Background())
	if listErr != nil {
		t.Fatalf("list: %v", listErr)
	}
	if len(policies) != 0 {
		t.Errorf("expected 0 saved policies, got %d", len(policies))
	}
}

func TestService_Create_UnknownProviderError(t *testing.T) {
	store := testutil.NewTestStore(t)
	ov := &stubOptionsValidator{err: fmt.Errorf("unknown provider %q: cannot validate model options", "fake")}
	svc := NewService(store, nil, nil, ov, nil)

	_, err := svc.Create(context.Background(), validYAMLWithOptions)
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "fake") {
		t.Errorf("error %q does not contain provider name %q", err.Error(), "fake")
	}
}

func TestService_Create_NilOptionsValidatorSkipsCheck(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil, nil, nil)

	result, err := svc.Create(context.Background(), validYAMLWithOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Policy.ID == "" {
		t.Error("expected non-empty policy ID")
	}
}

func TestService_Create_WithModelSectionPasses(t *testing.T) {
	store := testutil.NewTestStore(t)
	ov := &stubOptionsValidator{err: nil}
	svc := NewService(store, nil, nil, ov, nil)

	// validYAML includes a model section; verify the policy is created successfully.
	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Policy.ID == "" {
		t.Error("expected non-empty policy ID")
	}
}

// stubSettingsQuerier is a minimal settings.Querier for policy service tests.
type stubSettingsQuerier struct {
	settings map[string]db.SystemSetting
}

func (q *stubSettingsQuerier) GetSystemSetting(_ context.Context, key string) (db.SystemSetting, error) {
	row, ok := q.settings[key]
	if !ok {
		return db.SystemSetting{}, sql.ErrNoRows
	}
	return row, nil
}

// newTestSettings builds a *settings.Service that returns the given provider and
// model name from GetSystemDefault. Pass ("", "") to simulate "no default configured".
func newTestSettings(provider, modelName string) *settings.Service {
	q := &stubSettingsQuerier{settings: make(map[string]db.SystemSetting)}
	if provider != "" || modelName != "" {
		q.settings["default_model"] = db.SystemSetting{
			Key:   "default_model",
			Value: provider + ":" + modelName,
		}
	}
	return settings.NewService(q)
}

// noModelYAML is a policy YAML with no model block — used to test that the
// system default is applied (or that validation fires when there is no default).
const noModelYAML = `
name: test-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: github.list_repos
agent:
  task: Check all repos
`

func TestCreate_NoModelInYAMLAndNoSystemDefault(t *testing.T) {
	store := testutil.NewTestStore(t)
	// nil settings means resolveDefaults returns ("", ""), and the validator
	// must surface a clear error rather than silently passing empty strings.
	svc := NewService(store, nil, nil, nil, nil)

	_, err := svc.Create(context.Background(), noModelYAML)
	if err == nil {
		t.Fatal("expected validation error when no model in YAML and no system default, got nil")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	found := false
	for _, e := range ve.Errors {
		if e.Field == "model.provider" && strings.Contains(e.Message, "model.provider is required") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error with field 'model.provider' containing 'model.provider is required', got %v", ve.Error())
	}
}

func TestCreate_UsesSystemDefault_WhenYAMLModelOmitted(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil, nil, newTestSettings("anthropic", "claude-sonnet-4-6"))

	result, err := svc.Create(context.Background(), noModelYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Policy.ID == "" {
		t.Error("expected non-empty policy ID")
	}

	// The stored YAML should still be the raw input (no mutation), but parsing it
	// with the same default should round-trip to the expected ModelConfig.
	parsed, parseErr := Parse(result.Policy.YAML, "anthropic", "claude-sonnet-4-6")
	if parseErr != nil {
		t.Fatalf("parse stored YAML: %v", parseErr)
	}
	if parsed.Agent.ModelConfig.Provider != "anthropic" {
		t.Errorf("ModelConfig.Provider = %q, want %q", parsed.Agent.ModelConfig.Provider, "anthropic")
	}
	if parsed.Agent.ModelConfig.Name != "claude-sonnet-4-6" {
		t.Errorf("ModelConfig.Name = %q, want %q", parsed.Agent.ModelConfig.Name, "claude-sonnet-4-6")
	}
}

func TestService_Update_InvalidOptionsError(t *testing.T) {
	store := testutil.NewTestStore(t)

	// Create with nil validator first so the initial save succeeds.
	svc := NewService(store, nil, nil, nil, nil)
	createResult, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Now update with a validator that rejects the options.
	ov := &stubOptionsValidator{err: fmt.Errorf("provider %q: temperature must be between 0 and 1", "anthropic")}
	svcWithOV := NewService(store, nil, nil, ov, nil)

	_, err = svcWithOV.Update(context.Background(), createResult.Policy.ID, validYAMLWithOptions)
	if err == nil {
		t.Fatal("expected error for invalid options on update, got nil")
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Errorf("error %q does not contain provider name %q", err.Error(), "anthropic")
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

// --- Webhook secret tests ---

// fakeEncrypter implements secretEncrypter + decrypter interface for tests.
// It XORs bytes with a fixed key byte so Encrypt/Decrypt are inverses without
// importing a real crypto package.
type fakeEncrypter struct {
	key byte
}

func (f *fakeEncrypter) EncryptWebhookSecret(plaintext string) (string, error) {
	b := []byte(plaintext)
	for i := range b {
		b[i] ^= f.key
	}
	return fmt.Sprintf("enc:%x", b), nil
}

func (f *fakeEncrypter) DecryptWebhookSecret(ciphertext string) (string, error) {
	var hex string
	if _, err := fmt.Sscanf(ciphertext, "enc:%s", &hex); err != nil {
		return "", fmt.Errorf("bad fake ciphertext %q", ciphertext)
	}
	// hex-decode the bytes
	decoded := make([]byte, len(hex)/2)
	for i := range decoded {
		var b byte
		if _, err := fmt.Sscanf(hex[i*2:i*2+2], "%02x", &b); err != nil {
			return "", fmt.Errorf("decode hex: %w", err)
		}
		decoded[i] = b ^ f.key
	}
	return string(decoded), nil
}

const webhookYAML = `
name: webhook-policy
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.tool
agent:
  task: run webhook
`

const manualYAML = `
name: manual-policy
trigger:
  type: manual
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.tool
agent:
  task: run manual
`

func newWebhookService(t *testing.T) (*Service, *db.Store) {
	t.Helper()
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil, nil, nil)
	svc.WithWebhookSecretEncrypter(&fakeEncrypter{key: 0xAB})
	return svc, store
}

func TestService_RotateWebhookSecret_RoundTrip(t *testing.T) {
	svc, _ := newWebhookService(t)
	ctx := context.Background()

	createResult, err := svc.Create(ctx, webhookYAML)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := createResult.Policy.ID

	plaintext, err := svc.RotateWebhookSecret(ctx, id)
	if err != nil {
		t.Fatalf("RotateWebhookSecret: %v", err)
	}
	if len(plaintext) != 64 {
		t.Errorf("plaintext length = %d, want 64", len(plaintext))
	}

	// GetWebhookSecret must return the same value.
	got, err := svc.GetWebhookSecret(ctx, id)
	if err != nil {
		t.Fatalf("GetWebhookSecret: %v", err)
	}
	if got != plaintext {
		t.Errorf("GetWebhookSecret = %q, want %q", got, plaintext)
	}
}

func TestService_RotateWebhookSecret_NotWebhook(t *testing.T) {
	svc, _ := newWebhookService(t)
	ctx := context.Background()

	createResult, err := svc.Create(ctx, manualYAML)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = svc.RotateWebhookSecret(ctx, createResult.Policy.ID)
	if !errors.Is(err, ErrNotWebhookTrigger) {
		t.Errorf("got %v, want ErrNotWebhookTrigger", err)
	}
}

func TestService_RotateWebhookSecret_NoEncrypter(t *testing.T) {
	store := testutil.NewTestStore(t)
	// No encrypter set.
	svc := NewService(store, nil, nil, nil, nil)
	ctx := context.Background()

	createResult, err := svc.Create(ctx, webhookYAML)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = svc.RotateWebhookSecret(ctx, createResult.Policy.ID)
	if !errors.Is(err, ErrEncryptionUnavailable) {
		t.Errorf("got %v, want ErrEncryptionUnavailable", err)
	}
}

func TestService_RotateWebhookSecret_PolicyNotFound(t *testing.T) {
	svc, _ := newWebhookService(t)
	ctx := context.Background()

	_, err := svc.RotateWebhookSecret(ctx, "no-such-policy-id")
	if !errors.Is(err, ErrNoSuchPolicy) {
		t.Errorf("got %v, want ErrNoSuchPolicy", err)
	}
}

func TestService_GetWebhookSecret_NullColumn(t *testing.T) {
	svc, _ := newWebhookService(t)
	ctx := context.Background()

	createResult, err := svc.Create(ctx, webhookYAML)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// No rotate call — column should still be NULL.
	_, err = svc.GetWebhookSecret(ctx, createResult.Policy.ID)
	if !errors.Is(err, ErrNoSecret) {
		t.Errorf("got %v, want ErrNoSecret", err)
	}
}

func TestService_GetWebhookSecret_NoEncrypter(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil, nil, nil)
	ctx := context.Background()

	createResult, err := svc.Create(ctx, webhookYAML)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = svc.GetWebhookSecret(ctx, createResult.Policy.ID)
	if !errors.Is(err, ErrEncryptionUnavailable) {
		t.Errorf("got %v, want ErrEncryptionUnavailable", err)
	}
}

func TestService_UpdatePreservesWebhookSecretEncrypted(t *testing.T) {
	svc, store := newWebhookService(t)
	ctx := context.Background()

	createResult, err := svc.Create(ctx, webhookYAML)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := createResult.Policy.ID

	plaintext, err := svc.RotateWebhookSecret(ctx, id)
	if err != nil {
		t.Fatalf("RotateWebhookSecret: %v", err)
	}

	// Update the policy YAML (different task text), which should not clear the secret.
	// webhookYAML already contains the model block so ReplaceAll preserves it.
	updatedYAML := strings.ReplaceAll(webhookYAML, "run webhook", "run webhook v2")
	if _, err := svc.Update(ctx, id, updatedYAML); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Verify the ciphertext column is still populated.
	ciphertext, err := store.GetPolicyWebhookSecret(ctx, id)
	if err != nil {
		t.Fatalf("GetPolicyWebhookSecret: %v", err)
	}
	if ciphertext == nil {
		t.Fatal("webhook_secret_encrypted was cleared by Update")
	}

	// Full round-trip: decrypt must still match the original plaintext.
	got, err := svc.GetWebhookSecret(ctx, id)
	if err != nil {
		t.Fatalf("GetWebhookSecret: %v", err)
	}
	if got != plaintext {
		t.Errorf("GetWebhookSecret after Update = %q, want %q", got, plaintext)
	}
}

func TestService_Create_RejectsLegacyWebhookSecret(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil, nil, nil)

	legacyYAML := `
name: legacy
trigger:
  type: webhook
  webhook_secret: mysecret
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.tool
agent:
  task: do something
`
	_, err := svc.Create(context.Background(), legacyYAML)
	if err == nil {
		t.Fatal("expected error for legacy webhook_secret, got nil")
	}
	if !strings.Contains(err.Error(), "rotate") {
		t.Errorf("error %q does not mention rotate endpoint", err.Error())
	}
}

func TestService_Update_RejectsLegacyWebhookSecret(t *testing.T) {
	store := testutil.NewTestStore(t)
	svc := NewService(store, nil, nil, nil, nil)

	// Create clean policy first.
	createResult, err := svc.Create(context.Background(), webhookYAML)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	legacyYAML := `
name: webhook-policy
trigger:
  type: webhook
  webhook_secret: newsecret
model:
  provider: anthropic
  name: claude-sonnet-4-6
capabilities:
  tools:
    - tool: srv.tool
agent:
  task: run webhook
`
	_, err = svc.Update(context.Background(), createResult.Policy.ID, legacyYAML)
	if err == nil {
		t.Fatal("expected error for legacy webhook_secret on update, got nil")
	}
	if !strings.Contains(err.Error(), "rotate") {
		t.Errorf("error %q does not mention rotate endpoint", err.Error())
	}
}
