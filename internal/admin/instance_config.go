package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// ─── Typed errors for InstanceConfig ─────────────────────────────────────────

// ErrNoSubscriptionSchema is returned by PutSubscriptionScope when the plugin's
// manifest declares no subscription_schema.
var ErrNoSubscriptionSchema = errors.New("plugin declares no subscription_schema")

// ErrPropertyNotFound is returned by PutConfigProperty when the named property
// does not exist in the manifest's config_schema.
var ErrPropertyNotFound = errors.New("property not found in config_schema")

// ErrCASConflict is returned when the CAS update writes zero rows (concurrent
// modification). Maps to 409 casConflictMsg.
var ErrCASConflict = errors.New(casConflictMsg)

// CorruptManifestError is returned when the manifest snapshot cannot be parsed.
// Maps to 500 "corrupt manifest snapshot", detail = parse error.
type CorruptManifestError struct {
	Detail string
}

func (e CorruptManifestError) Error() string {
	return "corrupt manifest snapshot: " + e.Detail
}

// configValidationError carries field-level validation issues from
// configvalidate.Validator.Validate() when len(issues) > 0. Maps to 422.
type configValidationError struct {
	Issues []configvalidate.FieldError
}

func (e configValidationError) Error() string {
	return fmt.Sprintf("validation failed (%d issues)", len(e.Issues))
}

// SentinelRejectedError is returned when the caller submits the redaction
// sentinel "***" for a secret field. Single indicates the per-property endpoint
// (plain WriteError); otherwise bulk (WriteValidationError with issues slice).
// Maps to 400 in both cases — shape differs per the plan's ERROR SURFACE.
type SentinelRejectedError struct {
	Issues []configvalidate.FieldError // populated for bulk PutConfig
	Single bool                        // true for PutConfigProperty (single-field rejection)
}

func (e SentinelRejectedError) Error() string {
	if e.Single {
		return "value '***' is the redaction sentinel; submit the real secret"
	}
	return "sentinel value rejected"
}

// configInternalError wraps an internal failure with a public message for the
// handler to write verbatim, plus an optional detail string and the underlying
// error for logging. Mirrors lifecycleInternalError in instance_lifecycle.go.
type configInternalError struct {
	PublicMsg string
	Detail    string // empty string → no detail in response (matches "failed to update …" cases)
	Err       error
}

func (e *configInternalError) Error() string { return e.PublicMsg + ": " + e.Err.Error() }
func (e *configInternalError) Unwrap() error { return e.Err }

// ─── InstanceConfigResult ────────────────────────────────────────────────────

// InstanceConfigResult is returned by the three config-write methods. The
// Response field's ConfigJson is ALREADY REDACTED (ADR-049 fail-closed:
// redaction happens inside the module so a handler bug cannot leak secrets).
// The handler simply calls httputil.WriteJSON(200, result.Response).
type InstanceConfigResult struct {
	Response instanceResponse
}

// ─── InstanceConfigDeps ──────────────────────────────────────────────────────

// InstanceConfigDeps holds all constructor-injected dependencies for
// InstanceConfig. Trigger may be nil (PutSubscriptionScope is a no-op).
type InstanceConfigDeps struct {
	Q         PluginQuerier
	Publisher event.Publisher
	Clock     func() time.Time
	Trigger   TriggerRestarter
}

// InstanceConfig owns the three config-write choreographies:
// PutSubscriptionScope, PutConfig, and PutConfigProperty. It has no knowledge
// of HTTP — all methods return InstanceConfigResult or typed errors.
type InstanceConfig struct {
	q         PluginQuerier
	publisher event.Publisher
	clock     func() time.Time
	trigger   TriggerRestarter
}

// NewInstanceConfig constructs an InstanceConfig with the given deps.
// clock defaults to time.Now when nil.
func NewInstanceConfig(deps InstanceConfigDeps) *InstanceConfig {
	clk := deps.Clock
	if clk == nil {
		clk = time.Now
	}
	return &InstanceConfig{
		q:         deps.Q,
		publisher: deps.Publisher,
		clock:     clk,
		trigger:   deps.Trigger,
	}
}

// resolveConfigInstance fetches the instance row and verifies it belongs to
// pluginID. Returns typed not-found errors so the handler can map them
// without touching http.ResponseWriter.
func (m *InstanceConfig) resolveConfigInstance(ctx context.Context, pluginID, instanceID string) (db.PluginInstance, error) {
	row, err := m.q.GetPluginInstanceByID(ctx, instanceID)
	if errors.Is(err, ErrNotFound) {
		return db.PluginInstance{}, ErrInstanceNotFound
	}
	if err != nil {
		return db.PluginInstance{}, &configInternalError{
			PublicMsg: "failed to get instance",
			Err:       err,
		}
	}
	// Return 404 on mismatch to avoid leaking instance existence across plugins.
	if row.PluginID != pluginID {
		return db.PluginInstance{}, ErrInstanceNotFound
	}
	return row, nil
}

// PutSubscriptionScope validates scope against the manifest's subscription_schema,
// persists the new scope (CAS-guarded via ADR-038), restarts the trigger stream,
// and re-fetches (or synthesizes) the updated row with ConfigJson redacted.
//
// expected_version validation (nil → 400 "expected_version is required") stays
// in the HANDLER — this method takes a plain int64.
//
// Typed errors: ErrInstanceNotFound, ErrPluginNotFound, CorruptManifestError,
// ErrNoSubscriptionSchema, configInternalError ("failed to build scope validator",
// "validation error", "failed to marshal scope", "failed to update subscription scope"),
// configValidationError (422), ErrCASConflict (409).
func (m *InstanceConfig) PutSubscriptionScope(ctx context.Context, pluginID, instanceID string, scope map[string]any, expectedVersion int64) (InstanceConfigResult, error) {
	inst, err := m.resolveConfigInstance(ctx, pluginID, instanceID)
	if err != nil {
		return InstanceConfigResult{}, err
	}

	plugin, err := m.q.GetPluginByID(ctx, inst.PluginID)
	if errors.Is(err, ErrNotFound) {
		return InstanceConfigResult{}, ErrPluginNotFound
	}
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{PublicMsg: "failed to get plugin", Err: err}
	}

	var manifest sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &manifest); parseErr != nil {
		return InstanceConfigResult{}, CorruptManifestError{Detail: parseErr.Error()}
	}
	if manifest.SubscriptionSchema == nil {
		return InstanceConfigResult{}, ErrNoSubscriptionSchema
	}

	validator, err := configvalidate.ForSubscriptionScope(&manifest)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to build scope validator",
			Detail:    err.Error(),
			Err:       err,
		}
	}
	if scope == nil {
		scope = map[string]any{}
	}
	fieldErrs, err := validator.Validate(scope)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "validation error",
			Detail:    err.Error(),
			Err:       err,
		}
	}
	if len(fieldErrs) > 0 {
		return InstanceConfigResult{}, configValidationError{Issues: fieldErrs}
	}

	scopeBytes, err := json.Marshal(scope)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to marshal scope",
			Detail:    err.Error(),
			Err:       err,
		}
	}

	nowStr := m.clock().UTC().Format(time.RFC3339)
	rows, err := m.q.UpdatePluginInstanceSubscriptionScope(ctx, db.UpdatePluginInstanceSubscriptionScopeParams{
		SubscriptionScopeJson: string(scopeBytes),
		UpdatedAt:             nowStr,
		ID:                    instanceID,
		ExpectedVersion:       expectedVersion,
	})
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to update subscription scope",
			Err:       err,
		}
	}
	if rows == 0 {
		return InstanceConfigResult{}, ErrCASConflict
	}

	// Ensure the trigger stream is running with the latest scope.
	if m.trigger != nil {
		m.trigger.Start(ctx, instanceID)
		m.trigger.Restart(ctx, instanceID)
	}

	// Derive secret names for ConfigJson redaction (ADR-049). We use the
	// manifest already loaded above — no second DB round-trip. This call has
	// no textual counterpart in the old PutSubscriptionScope body (which
	// delegated to writeInstanceResponseWithRedaction), but the outcome is
	// byte-identical: ConfigJson is "***"-redacted in every branch.
	secretNames, secretErr := configvalidate.SecretPropertyNames(manifest.ConfigSchema)
	if secretErr != nil {
		// Fail-closed (ADR-049): we cannot safely redact; surface as 500.
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to parse config schema",
			Detail:    secretErr.Error(),
			Err:       secretErr,
		}
	}

	// Decode the manifest's instance-level config_schema for the response. The
	// schema is metadata and returned verbatim (ADR-049); decode error → nil, not
	// a failure. Both response branches (re-fetch success and synthesised fallback)
	// must carry the same field so the frontend always gets the schema from PUT too.
	var configSchema interface{}
	if manifest.ConfigSchema != nil {
		if decodeErr := manifest.ConfigSchema.Decode(&configSchema); decodeErr != nil {
			configSchema = nil
		}
	}

	// Re-fetch to return the updated row. On failure synthesise the response
	// from the pre-write snapshot + known deltas; secret fields must still be
	// redacted in both paths (ADR-049).
	updated, fetchErr := m.q.GetPluginInstanceByID(ctx, instanceID)
	if fetchErr != nil {
		synthetic := inst
		synthetic.Version++
		synthetic.UpdatedAt = nowStr
		synthetic.SubscriptionScopeJson = string(scopeBytes)
		redactedConfig, redactErr := configvalidate.RedactSecrets(synthetic.ConfigJson, secretNames)
		if redactErr != nil {
			return InstanceConfigResult{}, &configInternalError{
				PublicMsg: "failed to redact config",
				Detail:    redactErr.Error(),
				Err:       redactErr,
			}
		}
		return InstanceConfigResult{Response: instanceResponse{
			ID:                    instanceID,
			PluginID:              pluginID,
			InstanceName:          synthetic.InstanceName,
			State:                 synthetic.HealthState,
			Detail:                synthetic.HealthDetail,
			Version:               synthetic.Version,
			UpdatedAt:             synthetic.UpdatedAt,
			SubscriptionScopeJson: synthetic.SubscriptionScopeJson,
			ConfigJson:            redactedConfig,
			ConfigSchema:          configSchema,
		}}, nil
	}

	redactedConfig, redactErr := configvalidate.RedactSecrets(updated.ConfigJson, secretNames)
	if redactErr != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to redact config",
			Detail:    redactErr.Error(),
			Err:       redactErr,
		}
	}
	return InstanceConfigResult{Response: instanceResponse{
		ID:                    updated.ID,
		PluginID:              updated.PluginID,
		InstanceName:          updated.InstanceName,
		State:                 updated.HealthState,
		Detail:                updated.HealthDetail,
		Version:               updated.Version,
		UpdatedAt:             updated.UpdatedAt,
		SubscriptionScopeJson: updated.SubscriptionScopeJson,
		ConfigJson:            redactedConfig,
		ConfigSchema:          configSchema,
	}}, nil
}

// PutConfig validates cfg against the manifest's config_schema (nil schema
// accepts anything), persists it (CAS-guarded via ADR-038), and returns the
// updated instance row with ConfigJson redacted.
//
// expected_version nil-check stays in HANDLER. This method takes a plain int64.
//
// Typed errors: ErrInstanceNotFound, ErrPluginNotFound, CorruptManifestError,
// configInternalError ("failed to parse config schema", "failed to build config
// validator", "validation error", "failed to marshal config", "failed to update
// instance config", "failed to redact config"), configValidationError (422),
// SentinelRejectedError (bulk, Single=false) (400), ErrCASConflict (409).
func (m *InstanceConfig) PutConfig(ctx context.Context, pluginID, instanceID string, cfg map[string]any, expectedVersion int64) (InstanceConfigResult, error) {
	inst, err := m.resolveConfigInstance(ctx, pluginID, instanceID)
	if err != nil {
		return InstanceConfigResult{}, err
	}

	plugin, err := m.q.GetPluginByID(ctx, inst.PluginID)
	if errors.Is(err, ErrNotFound) {
		return InstanceConfigResult{}, ErrPluginNotFound
	}
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{PublicMsg: "failed to get plugin", Err: err}
	}

	var manifest sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &manifest); parseErr != nil {
		return InstanceConfigResult{}, CorruptManifestError{Detail: parseErr.Error()}
	}

	// Derive secret names for sentinel rejection and response redaction (ADR-049).
	secretNames, err := configvalidate.SecretPropertyNames(manifest.ConfigSchema)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to parse config schema",
			Detail:    err.Error(),
			Err:       err,
		}
	}

	// ForInstanceConfig returns a validator that accepts anything when ConfigSchema
	// is nil (per Q7 in the plan). Do NOT early-return on nil schema — it is valid.
	validator, err := configvalidate.ForInstanceConfig(&manifest)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to build config validator",
			Detail:    err.Error(),
			Err:       err,
		}
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	fieldErrs, err := validator.Validate(cfg)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "validation error",
			Detail:    err.Error(),
			Err:       err,
		}
	}
	if len(fieldErrs) > 0 {
		return InstanceConfigResult{}, configValidationError{Issues: fieldErrs}
	}

	// Reject any secret field whose submitted value is the redaction sentinel.
	// This prevents the round-trip clobber: UI reads "***", user hits Save,
	// real secret would be overwritten with the sentinel (ADR-049 §5).
	if offenders := configvalidate.ContainsRedactionSentinel(cfg, secretNames); len(offenders) > 0 {
		issues := make([]configvalidate.FieldError, 0, len(offenders))
		for _, field := range offenders {
			issues = append(issues, configvalidate.FieldError{
				Field:   field,
				Message: "value '***' is the redaction sentinel; submit the real secret or omit the field to leave it unchanged",
			})
		}
		return InstanceConfigResult{}, SentinelRejectedError{Issues: issues, Single: false}
	}

	configBytes, err := json.Marshal(cfg)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to marshal config",
			Detail:    err.Error(),
			Err:       err,
		}
	}

	nowStr := m.clock().UTC().Format(time.RFC3339)
	rows, err := m.q.UpdatePluginInstanceConfig(ctx, db.UpdatePluginInstanceConfigParams{
		ConfigJson:      string(configBytes),
		UpdatedAt:       nowStr,
		ID:              instanceID,
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to update instance config",
			Err:       err,
		}
	}
	if rows == 0 {
		return InstanceConfigResult{}, ErrCASConflict
	}

	// Compute the redacted form of the written config once. Both the re-fetch
	// success branch and the fallback synthesized branch use this value so
	// neither path can emit raw secret JSON (ADR-049 §7, §6).
	redactedWrittenConfig, err := configvalidate.RedactSecrets(string(configBytes), secretNames)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to redact config",
			Detail:    err.Error(),
			Err:       err,
		}
	}

	// Decode the manifest's instance-level config_schema for the response. The
	// schema is metadata and returned verbatim (ADR-049); decode error → nil, not
	// a failure. Both response branches must carry the same field for shape consistency.
	var configSchema interface{}
	if manifest.ConfigSchema != nil {
		if decodeErr := manifest.ConfigSchema.Decode(&configSchema); decodeErr != nil {
			configSchema = nil
		}
	}

	// Re-fetch to return the updated row.
	updated, fetchErr := m.q.GetPluginInstanceByID(ctx, instanceID)
	if fetchErr != nil {
		// The write succeeded; fall back to a synthesised response.
		// Use the pre-computed redacted config — never the raw written bytes.
		return InstanceConfigResult{Response: instanceResponse{
			ID:                    instanceID,
			PluginID:              pluginID,
			InstanceName:          inst.InstanceName,
			State:                 inst.HealthState,
			Detail:                inst.HealthDetail,
			Version:               inst.Version + 1,
			UpdatedAt:             nowStr,
			SubscriptionScopeJson: inst.SubscriptionScopeJson,
			ConfigJson:            redactedWrittenConfig,
			ConfigSchema:          configSchema,
		}}, nil
	}

	// Advance the readiness detail (config_missing → credentials_missing → "")
	// so the admin UI tells the operator what's still missing. Best-effort —
	// the config write has already committed; advanceInstanceReadiness logs
	// failures internally.
	m.advanceInstanceReadiness(ctx, updated, &manifest)
	if refreshed, refetchErr := m.q.GetPluginInstanceByID(ctx, instanceID); refetchErr == nil {
		updated = refreshed
	}

	// Redact the re-fetched config before returning.
	redactedFetchedConfig, err := configvalidate.RedactSecrets(updated.ConfigJson, secretNames)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to redact config",
			Detail:    err.Error(),
			Err:       err,
		}
	}
	return InstanceConfigResult{Response: instanceResponse{
		ID:                    updated.ID,
		PluginID:              updated.PluginID,
		InstanceName:          updated.InstanceName,
		State:                 updated.HealthState,
		Detail:                updated.HealthDetail,
		Version:               updated.Version,
		UpdatedAt:             updated.UpdatedAt,
		SubscriptionScopeJson: updated.SubscriptionScopeJson,
		ConfigJson:            redactedFetchedConfig,
		ConfigSchema:          configSchema,
	}}, nil
}

// PutConfigProperty updates a single property in the instance's config_json,
// CAS-guarded via ADR-038. Mirrors the ADR-039 per-header pattern (ADR-049).
//
// expected_version nil-check stays in HANDLER. This method takes a plain int64.
//
// Typed errors: ErrInstanceNotFound, ErrPluginNotFound, CorruptManifestError,
// ErrPropertyNotFound, configInternalError ("failed to parse config schema",
// "failed to parse existing config", "failed to build config validator",
// "validation error", "failed to marshal config", "failed to update instance
// config", "failed to redact config"), configValidationError (422),
// SentinelRejectedError (single, Single=true) (400), ErrCASConflict (409).
func (m *InstanceConfig) PutConfigProperty(ctx context.Context, pluginID, instanceID, property string, value any, expectedVersion int64) (InstanceConfigResult, error) {
	inst, err := m.resolveConfigInstance(ctx, pluginID, instanceID)
	if err != nil {
		return InstanceConfigResult{}, err
	}

	plugin, err := m.q.GetPluginByID(ctx, inst.PluginID)
	if errors.Is(err, ErrNotFound) {
		return InstanceConfigResult{}, ErrPluginNotFound
	}
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{PublicMsg: "failed to get plugin", Err: err}
	}

	var manifest sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &manifest); parseErr != nil {
		return InstanceConfigResult{}, CorruptManifestError{Detail: parseErr.Error()}
	}

	// Validate that the property name exists in the manifest's ConfigSchema.
	if !propertyExistsInSchema(manifest.ConfigSchema, property) {
		return InstanceConfigResult{}, ErrPropertyNotFound
	}

	// Derive secret names for sentinel rejection and redaction (ADR-049).
	secretNames, err := configvalidate.SecretPropertyNames(manifest.ConfigSchema)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to parse config schema",
			Detail:    err.Error(),
			Err:       err,
		}
	}

	// Reject the redaction sentinel — the caller must supply the real value.
	if strVal, isStr := value.(string); isStr && strVal == configvalidate.RedactionSentinel {
		return InstanceConfigResult{}, SentinelRejectedError{Single: true}
	}

	// Merge the new value into the existing config.
	var cfg map[string]any
	if inst.ConfigJson != "" && inst.ConfigJson != "{}" {
		if err := json.Unmarshal([]byte(inst.ConfigJson), &cfg); err != nil {
			return InstanceConfigResult{}, &configInternalError{
				PublicMsg: "failed to parse existing config",
				Detail:    err.Error(),
				Err:       err,
			}
		}
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg[property] = value

	// Validate the full merged config against the schema.
	validator, err := configvalidate.ForInstanceConfig(&manifest)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to build config validator",
			Detail:    err.Error(),
			Err:       err,
		}
	}
	fieldErrs, err := validator.Validate(cfg)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "validation error",
			Detail:    err.Error(),
			Err:       err,
		}
	}
	if len(fieldErrs) > 0 {
		return InstanceConfigResult{}, configValidationError{Issues: fieldErrs}
	}

	configBytes, err := json.Marshal(cfg)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to marshal config",
			Detail:    err.Error(),
			Err:       err,
		}
	}

	nowStr := m.clock().UTC().Format(time.RFC3339)
	rows, err := m.q.UpdatePluginInstanceConfig(ctx, db.UpdatePluginInstanceConfigParams{
		ConfigJson:      string(configBytes),
		UpdatedAt:       nowStr,
		ID:              instanceID,
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to update instance config",
			Err:       err,
		}
	}
	if rows == 0 {
		return InstanceConfigResult{}, ErrCASConflict
	}

	// Pre-compute the redacted form of the written config. Both the re-fetch
	// success branch and the fallback synthesized branch use this value so
	// neither path can emit raw secret JSON (ADR-049 §7).
	redactedWrittenConfig, err := configvalidate.RedactSecrets(string(configBytes), secretNames)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to redact config",
			Detail:    err.Error(),
			Err:       err,
		}
	}

	// Decode the manifest's instance-level config_schema for the response. The
	// schema is metadata and returned verbatim (ADR-049); decode error → nil, not
	// a failure. Both response branches must carry the same field for shape consistency.
	var configSchema interface{}
	if manifest.ConfigSchema != nil {
		if decodeErr := manifest.ConfigSchema.Decode(&configSchema); decodeErr != nil {
			configSchema = nil
		}
	}

	// Re-fetch to return the updated row.
	updated, fetchErr := m.q.GetPluginInstanceByID(ctx, instanceID)
	if fetchErr != nil {
		// The write succeeded; fall back to a synthesised response.
		// Use the pre-computed redacted config — never the raw written bytes.
		return InstanceConfigResult{Response: instanceResponse{
			ID:                    instanceID,
			PluginID:              pluginID,
			InstanceName:          inst.InstanceName,
			State:                 inst.HealthState,
			Detail:                inst.HealthDetail,
			Version:               inst.Version + 1,
			UpdatedAt:             nowStr,
			SubscriptionScopeJson: inst.SubscriptionScopeJson,
			ConfigJson:            redactedWrittenConfig,
			ConfigSchema:          configSchema,
		}}, nil
	}

	// Redact the re-fetched config before returning.
	redactedFetchedConfig, err := configvalidate.RedactSecrets(updated.ConfigJson, secretNames)
	if err != nil {
		return InstanceConfigResult{}, &configInternalError{
			PublicMsg: "failed to redact config",
			Detail:    err.Error(),
			Err:       err,
		}
	}
	return InstanceConfigResult{Response: instanceResponse{
		ID:                    updated.ID,
		PluginID:              updated.PluginID,
		InstanceName:          updated.InstanceName,
		State:                 updated.HealthState,
		Detail:                updated.HealthDetail,
		Version:               updated.Version,
		UpdatedAt:             updated.UpdatedAt,
		SubscriptionScopeJson: updated.SubscriptionScopeJson,
		ConfigJson:            redactedFetchedConfig,
		ConfigSchema:          configSchema,
	}}, nil
}

// AdvanceInstanceReadiness re-evaluates an instance's readiness detail and
// writes it back through SetHealthState if it has changed. This is called after
// the operator saves instance config or credentials so the admin UI progresses
// through config_missing → credentials_missing → "" (fully ready) without
// requiring a manual page refresh.
//
// Best-effort: any failure is logged but not returned to the caller because the
// underlying config/credentials write has already committed. The update is
// skipped when the instance is not currently in unhealthy (e.g. already healthy
// or pending an admin action).
//
// The querier param satisfies pluginstate.Querier (GetPluginInstanceByID +
// UpdatePluginInstanceHealth). Both InstanceConfig.q and PluginCredentialsHandler's
// querier satisfy it, which is why this is a package-level helper rather than a
// method — it lets both handlers share the same logic without a circular dep.
func AdvanceInstanceReadiness(ctx context.Context, q pluginstate.Querier, pub event.Publisher, inst db.PluginInstance, manifest *sdkmanifest.Manifest) {
	if model.PluginHealthState(inst.HealthState) != model.PluginHealthStateUnhealthy {
		return
	}
	credentialsPresent := inst.CredentialsEncrypted != nil && *inst.CredentialsEncrypted != ""
	wanted := computeInstanceReadinessDetail(manifest, inst.ConfigJson, credentialsPresent)
	var currentDetail string
	if inst.HealthDetail != nil {
		currentDetail = *inst.HealthDetail
	}
	if wanted == currentDetail {
		return
	}
	err := pluginstate.SetHealthState(ctx, q, pub, inst.ID, pluginstate.OriginHost,
		model.PluginHealthStateUnhealthy, wanted)
	if err != nil && !errors.Is(err, pluginstate.ErrTransitionConflict) {
		slog.WarnContext(ctx, "AdvanceInstanceReadiness: set health detail failed",
			"instance_id", inst.ID, "from", currentDetail, "to", wanted, "err", err)
	}
}

// advanceInstanceReadiness is the method-scoped thin forward to AdvanceInstanceReadiness
// used by PutConfig (and PutConfigProperty which calls PutConfig indirectly).
// Keeping the method preserves the existing PutConfig call site unchanged.
func (m *InstanceConfig) advanceInstanceReadiness(ctx context.Context, inst db.PluginInstance, manifest *sdkmanifest.Manifest) {
	AdvanceInstanceReadiness(ctx, m.q, m.publisher, inst, manifest)
}

// computeInstanceReadinessDetail returns the appropriate health_detail string
// for an instance that is sitting in PluginHealthStateUnhealthy, based on what
// the operator still needs to configure. This is used after the operator saves
// instance config or credentials so the admin UI tells them what's still
// missing — without it, the detail stayed at "config_missing" forever even
// after config was set, giving operators no signal what to do next.
//
// The returned string is empty when the instance has everything it needs and
// is just waiting for the subprocess to come up healthy.
func computeInstanceReadinessDetail(m *sdkmanifest.Manifest, configJSON string, credentialsPresent bool) string {
	if configJSON == "" || configJSON == "{}" {
		return "config_missing"
	}
	// Credentials are only required for auth strategies that consume them.
	// "none" plugins (and unset strategy, which defaults to "none" in the parser)
	// are config-only.
	switch m.Auth.Strategy {
	case "", sdkmanifest.AuthStrategyNone:
		return "" // ready — subprocess will mark healthy on handshake
	default:
		if !credentialsPresent {
			return "credentials_missing"
		}
		return "" // ready
	}
}

// propertyExistsInSchema returns true when the named property appears in the
// root-level properties map of schemaNode. Returns false when schemaNode is nil
// or has no properties key (including schema-less plugins).
func propertyExistsInSchema(schemaNode *yaml.Node, property string) bool {
	if schemaNode == nil {
		return false
	}
	var schema map[string]any
	if err := schemaNode.Decode(&schema); err != nil {
		return false
	}
	propertiesRaw, ok := schema["properties"]
	if !ok {
		return false
	}
	propertiesMap, ok := propertiesRaw.(map[string]any)
	if !ok {
		return false
	}
	_, exists := propertiesMap[property]
	return exists
}
