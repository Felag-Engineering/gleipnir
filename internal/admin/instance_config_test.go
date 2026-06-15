package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
)

// Manifest fixtures used by config module tests. Shared with the handler
// tests (same package) via plugin_handler_test.go declarations, so we do
// not redeclare them here.

// newTestInstanceConfig builds an InstanceConfig with a fixed clock and the
// given querier. opts is a variadic list of functional options.
func newTestInstanceConfig(q PluginQuerier, opts ...func(*InstanceConfigDeps)) *InstanceConfig {
	deps := InstanceConfigDeps{
		Q:     q,
		Clock: func() time.Time { return time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC) },
	}
	for _, o := range opts {
		o(&deps)
	}
	return NewInstanceConfig(deps)
}

func withConfigTrigger(r TriggerRestarter) func(*InstanceConfigDeps) {
	return func(d *InstanceConfigDeps) { d.Trigger = r }
}

// ─── PutSubscriptionScope ────────────────────────────────────────────────────

func TestInstanceConfig_PutSubscriptionScope(t *testing.T) {
	ctx := context.Background()

	t.Run("ErrInstanceNotFound when instance missing", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: triggerManifestWithScope})
		m := newTestInstanceConfig(q)
		_, err := m.PutSubscriptionScope(ctx, "plugin-1", "nonexistent", map[string]any{}, 0)
		if !errors.Is(err, ErrInstanceNotFound) {
			t.Errorf("err = %v, want ErrInstanceNotFound", err)
		}
	})

	t.Run("ErrInstanceNotFound when instance belongs to different plugin", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: triggerManifestWithScope})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-other", HealthState: "healthy", Version: 0})
		m := newTestInstanceConfig(q)
		_, err := m.PutSubscriptionScope(ctx, "plugin-1", "inst-1", map[string]any{}, 0)
		if !errors.Is(err, ErrInstanceNotFound) {
			t.Errorf("err = %v, want ErrInstanceNotFound", err)
		}
	})

	t.Run("ErrNoSubscriptionSchema when manifest has no subscription_schema", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-2", Name: "p", ManifestSnapshot: triggerManifestNoScope})
		q.seed(db.PluginInstance{ID: "inst-2", PluginID: "plugin-2", HealthState: "healthy", Version: 0})
		m := newTestInstanceConfig(q)
		_, err := m.PutSubscriptionScope(ctx, "plugin-2", "inst-2", map[string]any{"x": "y"}, 0)
		if !errors.Is(err, ErrNoSubscriptionSchema) {
			t.Errorf("err = %v, want ErrNoSubscriptionSchema", err)
		}
	})

	t.Run("configValidationError on invalid scope", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-3", Name: "p", ManifestSnapshot: triggerManifestWithScope})
		q.seed(db.PluginInstance{ID: "inst-3", PluginID: "plugin-3", HealthState: "healthy", Version: 0})
		m := newTestInstanceConfig(q)
		// "channels" is required; "bad_field" is rejected by additionalProperties:false.
		_, err := m.PutSubscriptionScope(ctx, "plugin-3", "inst-3", map[string]any{"bad_field": "x"}, 0)
		var valErr configValidationError
		if !errors.As(err, &valErr) {
			t.Errorf("err = %v, want configValidationError", err)
		}
		if len(valErr.Issues) == 0 {
			t.Error("configValidationError.Issues is empty, want at least one issue")
		}
	})

	t.Run("ErrCASConflict on stale version", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-4", Name: "p", ManifestSnapshot: triggerManifestWithScope})
		q.seed(db.PluginInstance{ID: "inst-4", PluginID: "plugin-4", HealthState: "healthy", Version: 5})
		q.scopeCASFailOnID = "inst-4"
		m := newTestInstanceConfig(q)
		_, err := m.PutSubscriptionScope(ctx, "plugin-4", "inst-4", map[string]any{"channels": []any{"#ops"}}, 5)
		if !errors.Is(err, ErrCASConflict) {
			t.Errorf("err = %v, want ErrCASConflict", err)
		}
	})

	t.Run("happy path: scope persisted, trigger restarted, secret config redacted", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-sec", Name: "p", ManifestSnapshot: triggerManifestWithScopeAndSecret})
		q.seed(db.PluginInstance{
			ID:                    "inst-sec",
			PluginID:              "plugin-sec",
			InstanceName:          "prod",
			ConfigJson:            `{"app_level_token":"real-secret"}`,
			SubscriptionScopeJson: "{}",
			HealthState:           "healthy",
			Version:               1,
			UpdatedAt:             "2026-05-01T00:00:00Z",
		})
		restarter := &fakeTriggerRestarter{}
		m := newTestInstanceConfig(q, withConfigTrigger(restarter))

		result, err := m.PutSubscriptionScope(ctx, "plugin-sec", "inst-sec", map[string]any{"channels": []any{"#alerts"}}, 1)
		if err != nil {
			t.Fatalf("PutSubscriptionScope: unexpected error: %v", err)
		}
		if result.Response.ConfigJson == "" {
			t.Error("ConfigJson must not be empty")
		}
		// Secret field must be redacted.
		if result.Response.ConfigJson == `{"app_level_token":"real-secret"}` {
			t.Error("ConfigJson must have redacted secret; got raw value")
		}
	})

	t.Run("200 synthesised response: secret still redacted when re-fetch fails", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-sec2", Name: "p", ManifestSnapshot: triggerManifestWithScopeAndSecret})
		q.seed(db.PluginInstance{
			ID:                    "inst-sec2",
			PluginID:              "plugin-sec2",
			InstanceName:          "prod",
			ConfigJson:            `{"app_level_token":"xoxb-secret"}`,
			SubscriptionScopeJson: "{}",
			HealthState:           "healthy",
			Version:               1,
			UpdatedAt:             "2026-05-01T00:00:00Z",
		})
		// First call to GetPluginInstanceByID succeeds (resolveConfigInstance).
		// Second call (re-fetch after scope write) fails → synthesised response path.
		q.getInstanceErrAfterN["inst-sec2"] = 1

		m := newTestInstanceConfig(q)
		result, err := m.PutSubscriptionScope(ctx, "plugin-sec2", "inst-sec2", map[string]any{"channels": []any{"#x"}}, 1)
		if err != nil {
			t.Fatalf("PutSubscriptionScope: unexpected error: %v", err)
		}
		// Even on synthesised path, secret must be redacted.
		if result.Response.ConfigJson == `{"app_level_token":"xoxb-secret"}` {
			t.Error("ConfigJson must be redacted even in synthesised response")
		}
	})
}

// ─── PutConfig ───────────────────────────────────────────────────────────────

func TestInstanceConfig_PutConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("ErrInstanceNotFound when instance missing", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
		m := newTestInstanceConfig(q)
		_, err := m.PutConfig(ctx, "plugin-1", "nonexistent", map[string]any{}, 0)
		if !errors.Is(err, ErrInstanceNotFound) {
			t.Errorf("err = %v, want ErrInstanceNotFound", err)
		}
	})

	t.Run("SentinelRejectedError (bulk) when secret field contains redaction sentinel", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy", Version: 0})
		m := newTestInstanceConfig(q)
		_, err := m.PutConfig(ctx, "plugin-1", "inst-1", map[string]any{
			"app_level_token": configvalidate.RedactionSentinel,
		}, 0)
		var sentErr SentinelRejectedError
		if !errors.As(err, &sentErr) {
			t.Errorf("err = %v, want SentinelRejectedError", err)
		}
		if sentErr.Single {
			t.Error("SentinelRejectedError.Single should be false for bulk PutConfig")
		}
		if len(sentErr.Issues) == 0 {
			t.Error("SentinelRejectedError.Issues should list offending fields")
		}
	})

	t.Run("configValidationError on schema violation", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestYAML})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy", Version: 0})
		m := newTestInstanceConfig(q)
		// app_level_token is required but missing.
		_, err := m.PutConfig(ctx, "plugin-1", "inst-1", map[string]any{"other_field": "x"}, 0)
		var valErr configValidationError
		if !errors.As(err, &valErr) {
			t.Errorf("err = %v, want configValidationError", err)
		}
	})

	t.Run("ErrCASConflict on stale version", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy", Version: 3})
		q.configCASFailOnID = "inst-1"
		m := newTestInstanceConfig(q)
		_, err := m.PutConfig(ctx, "plugin-1", "inst-1", map[string]any{"key": "val"}, 3)
		if !errors.Is(err, ErrCASConflict) {
			t.Errorf("err = %v, want ErrCASConflict", err)
		}
	})

	t.Run("happy path: secret redacted in response", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
		q.seed(db.PluginInstance{
			ID:           "inst-1",
			PluginID:     "plugin-1",
			InstanceName: "prod",
			HealthState:  "healthy",
			Version:      0,
			UpdatedAt:    "2026-05-01T00:00:00Z",
		})
		m := newTestInstanceConfig(q)
		result, err := m.PutConfig(ctx, "plugin-1", "inst-1", map[string]any{"app_level_token": "xoxb-real"}, 0)
		if err != nil {
			t.Fatalf("PutConfig: unexpected error: %v", err)
		}
		if result.Response.ConfigJson == `{"app_level_token":"xoxb-real"}` {
			t.Error("secret field must be redacted in response")
		}
		if result.Response.ConfigJson == "" {
			t.Error("ConfigJson must not be empty")
		}
	})

	t.Run("happy path: nil schema accepts anything", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{
			ID:           "inst-1",
			PluginID:     "plugin-1",
			InstanceName: "prod",
			HealthState:  "healthy",
			Version:      0,
			UpdatedAt:    "2026-05-01T00:00:00Z",
		})
		m := newTestInstanceConfig(q)
		result, err := m.PutConfig(ctx, "plugin-1", "inst-1", map[string]any{"arbitrary_key": 42}, 0)
		if err != nil {
			t.Fatalf("PutConfig: unexpected error (nil schema must accept any object): %v", err)
		}
		if result.Response.ID != "inst-1" {
			t.Errorf("Response.ID = %q, want %q", result.Response.ID, "inst-1")
		}
	})

	t.Run("synthesised response: secret still redacted when re-fetch fails", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
		q.seed(db.PluginInstance{
			ID:           "inst-1",
			PluginID:     "plugin-1",
			InstanceName: "prod",
			HealthState:  "healthy",
			Version:      0,
			UpdatedAt:    "2026-05-01T00:00:00Z",
		})
		// resolveConfigInstance (call 1) succeeds; re-fetch after write (call 2) fails.
		q.getInstanceErrAfterN["inst-1"] = 1
		m := newTestInstanceConfig(q)
		result, err := m.PutConfig(ctx, "plugin-1", "inst-1", map[string]any{"app_level_token": "xoxb-secret"}, 0)
		if err != nil {
			t.Fatalf("PutConfig: unexpected error: %v", err)
		}
		if result.Response.ConfigJson == `{"app_level_token":"xoxb-secret"}` {
			t.Error("secret must be redacted even in synthesised response path")
		}
	})
}

// ─── PutConfigProperty ───────────────────────────────────────────────────────

func TestInstanceConfig_PutConfigProperty(t *testing.T) {
	ctx := context.Background()

	t.Run("ErrInstanceNotFound when instance missing", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
		m := newTestInstanceConfig(q)
		_, err := m.PutConfigProperty(ctx, "plugin-1", "nonexistent", "app_level_token", "val", 0)
		if !errors.Is(err, ErrInstanceNotFound) {
			t.Errorf("err = %v, want ErrInstanceNotFound", err)
		}
	})

	t.Run("ErrPropertyNotFound when property absent from config_schema", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy", Version: 0})
		m := newTestInstanceConfig(q)
		_, err := m.PutConfigProperty(ctx, "plugin-1", "inst-1", "nonexistent_prop", "val", 0)
		if !errors.Is(err, ErrPropertyNotFound) {
			t.Errorf("err = %v, want ErrPropertyNotFound", err)
		}
	})

	t.Run("SentinelRejectedError (single) when value is redaction sentinel", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy", Version: 0})
		m := newTestInstanceConfig(q)
		_, err := m.PutConfigProperty(ctx, "plugin-1", "inst-1", "app_level_token", configvalidate.RedactionSentinel, 0)
		var sentErr SentinelRejectedError
		if !errors.As(err, &sentErr) {
			t.Errorf("err = %v, want SentinelRejectedError", err)
		}
		if !sentErr.Single {
			t.Error("SentinelRejectedError.Single should be true for per-property endpoint")
		}
	})

	t.Run("ErrCASConflict on stale version", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
		q.seed(db.PluginInstance{
			ID:           "inst-1",
			PluginID:     "plugin-1",
			InstanceName: "prod",
			HealthState:  "healthy",
			ConfigJson:   `{"app_level_token":"old"}`,
			Version:      3,
		})
		q.configCASFailOnID = "inst-1"
		m := newTestInstanceConfig(q)
		_, err := m.PutConfigProperty(ctx, "plugin-1", "inst-1", "app_level_token", "new-val", 3)
		if !errors.Is(err, ErrCASConflict) {
			t.Errorf("err = %v, want ErrCASConflict", err)
		}
	})

	t.Run("happy path: property merged, secret redacted in response", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
		q.seed(db.PluginInstance{
			ID:           "inst-1",
			PluginID:     "plugin-1",
			InstanceName: "prod",
			HealthState:  "healthy",
			ConfigJson:   `{}`,
			Version:      0,
			UpdatedAt:    "2026-05-01T00:00:00Z",
		})
		m := newTestInstanceConfig(q)
		result, err := m.PutConfigProperty(ctx, "plugin-1", "inst-1", "app_level_token", "xoxb-real", 0)
		if err != nil {
			t.Fatalf("PutConfigProperty: unexpected error: %v", err)
		}
		if result.Response.ConfigJson == `{"app_level_token":"xoxb-real"}` {
			t.Error("secret must be redacted in response")
		}
		if result.Response.ID != "inst-1" {
			t.Errorf("Response.ID = %q, want inst-1", result.Response.ID)
		}
	})

	t.Run("synthesised response: secret redacted when re-fetch fails", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
		q.seed(db.PluginInstance{
			ID:           "inst-1",
			PluginID:     "plugin-1",
			InstanceName: "prod",
			HealthState:  "healthy",
			ConfigJson:   `{}`,
			Version:      0,
			UpdatedAt:    "2026-05-01T00:00:00Z",
		})
		// resolveConfigInstance (call 1) succeeds; re-fetch after write (call 2) fails.
		q.getInstanceErrAfterN["inst-1"] = 1
		m := newTestInstanceConfig(q)
		result, err := m.PutConfigProperty(ctx, "plugin-1", "inst-1", "app_level_token", "secret-val", 0)
		if err != nil {
			t.Fatalf("PutConfigProperty: unexpected error: %v", err)
		}
		if result.Response.ConfigJson == `{"app_level_token":"secret-val"}` {
			t.Error("secret must be redacted even in synthesised response")
		}
	})

	t.Run("ErrPropertyNotFound returns immediately (no nil-schema path)", func(t *testing.T) {
		// When config_schema is nil, propertyExistsInSchema returns false for any
		// property name. This means PutConfigProperty always returns ErrPropertyNotFound
		// for a schema-less plugin, preventing arbitrary key injection.
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy", Version: 0})
		m := newTestInstanceConfig(q)
		_, err := m.PutConfigProperty(ctx, "plugin-1", "inst-1", "any_property", "val", 0)
		if !errors.Is(err, ErrPropertyNotFound) {
			t.Errorf("err = %v, want ErrPropertyNotFound for schema-less plugin", err)
		}
	})
}

// ─── propertyExistsInSchema ──────────────────────────────────────────────────

func TestPropertyExistsInSchema(t *testing.T) {
	// propertyExistsInSchema is tested indirectly through PutConfigProperty:
	// a known property name must NOT return ErrPropertyNotFound, while an
	// absent one must.

	ctx := context.Background()

	t.Run("nil schema → ErrPropertyNotFound for any name", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "p", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst", PluginID: "p", HealthState: "healthy", Version: 0})
		m := newTestInstanceConfig(q)
		_, err := m.PutConfigProperty(ctx, "p", "inst", "any_property", "val", 0)
		if !errors.Is(err, ErrPropertyNotFound) {
			t.Errorf("err = %v, want ErrPropertyNotFound for nil config_schema", err)
		}
	})

	const manifestWithProp = `schema_version: v1
name: p
version: 1.0.0
services:
  tool: v1
auth:
  mode: instance_credentials
  strategy: none
config_schema:
  type: object
  properties:
    app_level_token:
      type: string
`

	t.Run("declared property → found (not ErrPropertyNotFound)", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "p", Name: "p", ManifestSnapshot: manifestWithProp})
		q.seed(db.PluginInstance{ID: "inst", PluginID: "p", HealthState: "healthy", ConfigJson: "{}", Version: 0, UpdatedAt: "2026-05-01T00:00:00Z"})
		m := newTestInstanceConfig(q)
		_, err := m.PutConfigProperty(ctx, "p", "inst", "app_level_token", "val", 0)
		if errors.Is(err, ErrPropertyNotFound) {
			t.Errorf("declared property should be found, got ErrPropertyNotFound")
		}
	})

	t.Run("undeclared property → ErrPropertyNotFound", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "p", Name: "p", ManifestSnapshot: manifestWithProp})
		q.seed(db.PluginInstance{ID: "inst", PluginID: "p", HealthState: "healthy", Version: 0})
		m := newTestInstanceConfig(q)
		_, err := m.PutConfigProperty(ctx, "p", "inst", "nonexistent_prop", "val", 0)
		if !errors.Is(err, ErrPropertyNotFound) {
			t.Errorf("undeclared property: err = %v, want ErrPropertyNotFound", err)
		}
	})
}
