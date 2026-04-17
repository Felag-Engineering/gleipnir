package policy

import (
	"context"
	"errors"
	"fmt"
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
	err error // if non-nil, ValidateModelName returns this error
}

func (s *stubModelValidator) ValidateModelName(_ context.Context, provider, modelName string) error {
	return s.err
}

const validYAML = `
name: test-policy
trigger:
  type: webhook
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
	yamlWithManyTools := `
name: ctx-test
trigger:
  type: webhook
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
capabilities:
  tools:
    - tool: github.list_repos
agent:
  task: Check all repos
  model:
    provider: anthropic
    name: claude-sonnet-4-6
    options:
      temperature: 0.7
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

func TestService_Create_NoModelSectionDefaultsPass(t *testing.T) {
	store := testutil.NewTestStore(t)
	ov := &stubOptionsValidator{err: nil}
	svc := NewService(store, nil, nil, ov, nil)

	// validYAML has no model section; the parser fills in provider defaults.
	result, err := svc.Create(context.Background(), validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Policy.ID == "" {
		t.Error("expected non-empty policy ID")
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
