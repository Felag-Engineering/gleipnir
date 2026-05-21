package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	pluginmanifest "github.com/felag-engineering/gleipnir/internal/plugin/manifest"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

const (
	// auditPubkeyRotated is the event type emitted when an admin accepts a new
	// plugin signing key via POST /api/v1/admin/plugins/{id}/accept-new-key.
	auditPubkeyRotated = "plugin_pubkey_rotated"

	// auditManifestAccepted is the event type emitted when an admin accepts a
	// material manifest change via POST /api/v1/admin/plugins/{id}/accept-manifest.
	auditManifestAccepted = "plugin_manifest_accepted"

	// auditManifestMaterialChange is the event type written by the install pipeline
	// when a hot-reload introduces a material manifest change. AcceptManifest
	// scans these to find the candidate awaiting admin approval.
	auditManifestMaterialChange = "plugin_manifest_material_change"
)

// PluginQuerier is the narrow DB interface required by PluginHandler.
// Accepting an interface (not *db.Queries) keeps the handler testable with
// a fake querier and mirrors the AdminQuerier pattern in handler.go.
// It is a superset of pluginstate.Querier so h.q can be passed directly into
// pluginstate.SetHealthState.
type PluginQuerier interface {
	// Instance read (used by GetInstance and state machine).
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	// Plugin read (used by AcceptNewKey).
	GetPluginByID(ctx context.Context, id string) (db.Plugin, error)
	// Plugin write (used by AcceptNewKey to rotate the pinned pubkey).
	UpdatePluginTrustedPubkey(ctx context.Context, arg db.UpdatePluginTrustedPubkeyParams) (int64, error)
	// Instance list (used by AcceptNewKey to unblock pending instances).
	ListPluginInstancesByPlugin(ctx context.Context, pluginID string) ([]db.PluginInstance, error)
	// Instance health write (required by pluginstate.Querier interface).
	UpdatePluginInstanceHealth(ctx context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error)
	// Audit write (used by AcceptNewKey to record the key rotation).
	InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)
	// Audit read (used by AcceptManifest to find the pending candidate).
	ListPluginAuditEventsByType(ctx context.Context, arg db.ListPluginAuditEventsByTypeParams) ([]db.PluginAuditEvent, error)
	// Plugin manifest write (used by AcceptManifest to commit the candidate snapshot).
	UpdatePluginManifest(ctx context.Context, arg db.UpdatePluginManifestParams) (int64, error)
	// Instance subscription scope write (used by PutSubscriptionScope).
	UpdatePluginInstanceSubscriptionScope(ctx context.Context, arg db.UpdatePluginInstanceSubscriptionScopeParams) (int64, error)
	// Instance create (used by CreateInstance).
	CreatePluginInstance(ctx context.Context, arg db.CreatePluginInstanceParams) (db.PluginInstance, error)
	// Instance lookup by (plugin_id, instance_name) uniqueness check (used by CreateInstance).
	GetPluginInstanceByName(ctx context.Context, arg db.GetPluginInstanceByNameParams) (db.PluginInstance, error)
}

// PluginInstaller is the narrow interface used by the Install handler.
// Implemented by *loader.Installer; kept narrow here so the admin package
// does not import internal/plugin/loader directly and tests can inject a stub.
type PluginInstaller interface {
	Install(ctx context.Context, tarPath string) (string, error)
}

// TriggerRestarter is the narrow interface used to restart a plugin's trigger
// stream after its subscription scope changes. Implemented by
// *plugintrigger.Supervisor; kept narrow here so the admin package does not
// import the trigger package directly.
type TriggerRestarter interface {
	Restart(ctx context.Context, instanceID string)
}

// PluginHandler handles plugin-related admin endpoints.
type PluginHandler struct {
	q                PluginQuerier
	publisher        event.Publisher
	clock            func() time.Time
	triggerRestarter TriggerRestarter // may be nil if plugins are disabled
	installer        PluginInstaller  // may be nil if GLEIPNIR_PLUGINS_ENABLED=false
}

// NewPluginHandler returns a PluginHandler backed by the given querier, event
// publisher, and clock. publisher may be nil — events are skipped when nil.
// clock may be nil — time.Now is used as the default.
func NewPluginHandler(q PluginQuerier, publisher event.Publisher, clock func() time.Time) *PluginHandler {
	if clock == nil {
		clock = time.Now
	}
	return &PluginHandler{q: q, publisher: publisher, clock: clock}
}

// SetTriggerRestarter wires the TriggerRestarter (typically *plugintrigger.Supervisor)
// into the handler after it is constructed. Called from main.go after the supervisor
// is created so construction order does not create an import cycle.
func (h *PluginHandler) SetTriggerRestarter(r TriggerRestarter) {
	h.triggerRestarter = r
}

// SetInstaller wires the PluginInstaller into the handler. A nil installer
// disables the Install endpoint (returns 503). Called from main.go after
// loader.StartWatcher runs so plugins disabled mode is handled cleanly.
func (h *PluginHandler) SetInstaller(inst PluginInstaller) {
	h.installer = inst
}

// instanceResponse is the JSON shape returned by GetInstance and PutSubscriptionScope.
// Credentials and other write-only fields are intentionally absent — mirrors
// the ADR-039 read-restraint pattern for encrypted auth headers.
type instanceResponse struct {
	ID                    string  `json:"id"`
	PluginID              string  `json:"plugin_id"`
	InstanceName          string  `json:"instance_name"`
	State                 string  `json:"state"`
	Detail                *string `json:"detail"`
	Version               int64   `json:"version"`
	UpdatedAt             string  `json:"updated_at"`
	SubscriptionScopeJson string  `json:"subscription_scope_json"`
}

// GetInstance handles GET /api/v1/admin/plugins/{id}/instances/{iid}.
// Returns the health state and detail for a single plugin instance. 404 is
// returned when the instance does not exist or belongs to a different plugin.
func (h *PluginHandler) GetInstance(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	row, err := h.q.GetPluginInstanceByID(r.Context(), instanceID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get instance", "")
		return
	}

	// Return 404 (not 403) on a plugin/instance mismatch to avoid leaking
	// instance existence across plugins.
	if row.PluginID != pluginID {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, instanceResponse{
		ID:                    row.ID,
		PluginID:              row.PluginID,
		InstanceName:          row.InstanceName,
		State:                 row.HealthState,
		Detail:                row.HealthDetail,
		Version:               row.Version,
		UpdatedAt:             row.UpdatedAt,
		SubscriptionScopeJson: row.SubscriptionScopeJson,
	})
}

// acceptNewKeyRequest is the JSON body for POST /api/v1/admin/plugins/{id}/accept-new-key.
type acceptNewKeyRequest struct {
	CandidatePubkey string `json:"candidate_pubkey"`
}

// acceptNewKeyResponse is the JSON body returned on success.
type acceptNewKeyResponse struct {
	AcceptedPubkeyFingerprint string `json:"accepted_pubkey_fingerprint"`
	InstancesUnblocked        int    `json:"instances_unblocked"`
}

// AcceptNewKey handles POST /api/v1/admin/plugins/{id}/accept-new-key.
// It rotates the trusted pubkey on the plugin row (CAS-guarded) and transitions
// all instances in pending_key_approval to healthy.
//
// Request body: { "candidate_pubkey": "<base64-encoded minisign signing.pub bytes>" }
// The candidate_pubkey is sourced from the plugin_pubkey_mismatch audit event's
// new_pubkey_b64 field, which contains the full Minisign-formatted signing.pub bytes.
func (h *PluginHandler) AcceptNewKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")

	var body acceptNewKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return
	}
	if body.CandidatePubkey == "" {
		httputil.WriteError(w, http.StatusBadRequest, "candidate_pubkey is required", "")
		return
	}

	rawPubkey, err := base64.StdEncoding.DecodeString(body.CandidatePubkey)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "candidate_pubkey: invalid base64", "")
		return
	}
	newKey, _, err := signing.ParsePublicKey(rawPubkey)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "candidate_pubkey: not a valid Minisign public key", err.Error())
		return
	}

	plugin, err := h.q.GetPluginByID(ctx, pluginID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
		return
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		return
	}

	nowStr := h.clock().UTC().Format(time.RFC3339Nano)
	oldFingerprint := fmt.Sprintf("%x", deriveFingerprint([]byte(plugin.TrustedPubkey)))
	newFingerprint := fmt.Sprintf("%x", newKey.KeyID)

	// CAS-guarded pubkey rotation (ADR-038).
	rows, err := h.q.UpdatePluginTrustedPubkey(ctx, db.UpdatePluginTrustedPubkeyParams{
		TrustedPubkey:   string(rawPubkey),
		UpdatedAt:       nowStr,
		ID:              pluginID,
		ExpectedVersion: plugin.Version,
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update trusted pubkey", "")
		return
	}
	if rows == 0 {
		httputil.WriteError(w, http.StatusConflict, "concurrent modification detected; retry", "")
		return
	}

	unblocked := h.unblockInstances(
		ctx, pluginID,
		model.PluginHealthStatePendingKeyApproval,
		model.PluginHealthStateHealthy,
		"operator accepted new signing key",
		"accept-new-key",
	)

	h.writeAuditEvent(ctx, auditPubkeyRotated, "high", nowStr, map[string]any{
		"plugin_id":              pluginID,
		"name":                   plugin.Name,
		"old_pubkey_fingerprint": oldFingerprint,
		"new_pubkey_fingerprint": newFingerprint,
	})

	httputil.WriteJSON(w, http.StatusOK, acceptNewKeyResponse{
		AcceptedPubkeyFingerprint: newFingerprint,
		InstancesUnblocked:        unblocked,
	})
}

// acceptManifestResponse is the JSON body returned on success.
type acceptManifestResponse struct {
	AcceptedManifestVersion string `json:"accepted_manifest_version"`
	InstancesUnblocked      int    `json:"instances_unblocked"`
	InstancesPendingConfig  int    `json:"instances_pending_config"`
}

// AcceptManifest handles POST /api/v1/admin/plugins/{id}/accept-manifest.
// It commits the candidate manifest (stored in the most recent
// plugin_manifest_material_change audit event) as the new snapshot, then
// transitions instances out of pending_manifest_approval.
//
// Instances with no newly-required config fields move to healthy.
// Instances for which new required config fields appear move to pending_config_migration.
func (h *PluginHandler) AcceptManifest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")

	plugin, err := h.q.GetPluginByID(ctx, pluginID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
		return
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		return
	}

	candidatePayload, err := h.findCandidateManifestEvent(ctx, pluginID)
	if err != nil {
		writeCandidateError(w, err)
		return
	}

	candidateBytes, newManifest, err := decodeCandidateManifest(candidatePayload)
	if err != nil {
		writeCandidateError(w, err)
		return
	}

	var oldManifest sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &oldManifest); parseErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", parseErr.Error())
		return
	}
	newlyRequired := pluginmanifest.ConfigSchemaNewlyRequiredFields(&oldManifest, &newManifest)

	nowStr := h.clock().UTC().Format(time.RFC3339Nano)

	// CAS-guarded manifest commit (ADR-038). Verify rows-affected before touching instances.
	rows, err := h.q.UpdatePluginManifest(ctx, db.UpdatePluginManifestParams{
		ManifestSnapshot: string(candidateBytes),
		PluginVersion:    newManifest.Version,
		Status:           "pending_review",
		UpdatedAt:        nowStr,
		ID:               pluginID,
		ExpectedVersion:  plugin.Version,
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update manifest", "")
		return
	}
	if rows == 0 {
		httputil.WriteError(w, http.StatusConflict, "concurrent modification detected; retry", "")
		return
	}

	targetState := model.PluginHealthStateHealthy
	if len(newlyRequired) > 0 {
		targetState = model.PluginHealthStatePendingConfigMigration
	}
	transitioned := h.unblockInstances(
		ctx, pluginID,
		model.PluginHealthStatePendingManifestApproval,
		targetState,
		"admin accepted manifest change",
		"accept-manifest",
	)
	unblocked, pendingConfig := 0, 0
	if targetState == model.PluginHealthStateHealthy {
		unblocked = transitioned
	} else {
		pendingConfig = transitioned
	}

	oldVersion, _ := candidatePayload["old_version"].(string)
	h.writeAuditEvent(ctx, auditManifestAccepted, "info", nowStr, map[string]any{
		"plugin_id":                pluginID,
		"name":                     plugin.Name,
		"old_version":              oldVersion,
		"new_version":              newManifest.Version,
		"instances_unblocked":      unblocked,
		"instances_pending_config": pendingConfig,
	})

	httputil.WriteJSON(w, http.StatusOK, acceptManifestResponse{
		AcceptedManifestVersion: newManifest.Version,
		InstancesUnblocked:      unblocked,
		InstancesPendingConfig:  pendingConfig,
	})
}

// candidateLookupError categorises failures locating the pending candidate
// manifest so the HTTP boundary can map them to the right status code.
type candidateLookupError struct {
	status int
	msg    string
	detail string
}

func (e *candidateLookupError) Error() string { return e.msg }

// writeCandidateError maps a candidateLookupError to the configured HTTP
// status. All call sites in this file produce *candidateLookupError values, so
// the type assertion is a tight contract; an unexpected error type still
// surfaces as 500 rather than panicking.
func writeCandidateError(w http.ResponseWriter, err error) {
	var cle *candidateLookupError
	if errors.As(err, &cle) {
		httputil.WriteError(w, cle.status, cle.msg, cle.detail)
		return
	}
	httputil.WriteError(w, http.StatusInternalServerError, err.Error(), "")
}

// findCandidateManifestEvent scans the most recent material-change audit events
// for one matching pluginID. plugin_audit_events has no plugin_id column, so
// filtering happens in Go.
// TODO(v2): add a plugin-scoped query to avoid this in-Go filter on busy installs.
func (h *PluginHandler) findCandidateManifestEvent(ctx context.Context, pluginID string) (map[string]any, error) {
	events, err := h.q.ListPluginAuditEventsByType(ctx, db.ListPluginAuditEventsByTypeParams{
		EventType: auditManifestMaterialChange,
		Offset:    0,
		Limit:     200,
	})
	if err != nil {
		return nil, &candidateLookupError{
			status: http.StatusInternalServerError,
			msg:    "failed to list audit events",
		}
	}

	// Events are ordered DESC by created_at; first match for this plugin is newest.
	for i := range events {
		var payload map[string]any
		if jsonErr := json.Unmarshal([]byte(events[i].PayloadJson), &payload); jsonErr != nil {
			continue
		}
		if payload["plugin_id"] == pluginID {
			return payload, nil
		}
	}
	return nil, &candidateLookupError{
		status: http.StatusConflict,
		msg:    "no pending manifest change found for this plugin",
	}
}

// decodeCandidateManifest extracts and parses the candidate manifest from the
// audit event payload.
func decodeCandidateManifest(payload map[string]any) ([]byte, sdkmanifest.Manifest, error) {
	candidateB64, _ := payload["candidate_manifest_b64"].(string)
	if candidateB64 == "" {
		return nil, sdkmanifest.Manifest{}, &candidateLookupError{
			status: http.StatusUnprocessableEntity,
			msg:    "candidate manifest not found in audit event",
		}
	}
	candidateBytes, err := base64.StdEncoding.DecodeString(candidateB64)
	if err != nil {
		return nil, sdkmanifest.Manifest{}, &candidateLookupError{
			status: http.StatusUnprocessableEntity,
			msg:    "candidate manifest: invalid base64",
		}
	}
	var m sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal(candidateBytes, &m); parseErr != nil {
		return nil, sdkmanifest.Manifest{}, &candidateLookupError{
			status: http.StatusUnprocessableEntity,
			msg:    "candidate manifest: parse failed",
			detail: parseErr.Error(),
		}
	}
	return candidateBytes, m, nil
}

// putSubscriptionScopeRequest is the JSON body for
// PUT /api/v1/admin/plugins/{id}/instances/{iid}/subscription-scope.
type putSubscriptionScopeRequest struct {
	Scope           map[string]any `json:"scope"`
	ExpectedVersion *int64         `json:"expected_version,omitempty"`
}

// PutSubscriptionScope handles PUT /api/v1/admin/plugins/{id}/instances/{iid}/subscription-scope.
// Validates the scope against the manifest's subscription_schema (if declared),
// persists the new scope (CAS-guarded via ADR-038), and restarts the trigger
// stream so the plugin re-establishes substrate connections with the new scope.
func (h *PluginHandler) PutSubscriptionScope(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	var req putSubscriptionScopeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.ExpectedVersion == nil {
		httputil.WriteError(w, http.StatusBadRequest, "expected_version is required", "")
		return
	}

	inst, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get instance", "")
		return
	}
	// Return 404 (not 403) on a plugin/instance mismatch to avoid leaking
	// instance existence across plugins.
	if inst.PluginID != pluginID {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}

	plugin, err := h.q.GetPluginByID(ctx, inst.PluginID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
		return
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		return
	}

	var m sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &m); parseErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", parseErr.Error())
		return
	}
	if m.SubscriptionSchema == nil {
		httputil.WriteError(w, http.StatusBadRequest, "plugin declares no subscription_schema", "")
		return
	}

	validator, err := configvalidate.ForSubscriptionScope(&m)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to build scope validator", err.Error())
		return
	}
	scope := req.Scope
	if scope == nil {
		scope = map[string]any{}
	}
	fieldErrs, err := validator.Validate(scope)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "validation error", err.Error())
		return
	}
	if len(fieldErrs) > 0 {
		issues := make([]httputil.ErrorIssue, 0, len(fieldErrs))
		for _, fe := range fieldErrs {
			issues = append(issues, httputil.ErrorIssue{Field: fe.Field, Message: fe.Message})
		}
		httputil.WriteValidationError(w, http.StatusUnprocessableEntity, "validation failed", "", issues)
		return
	}

	scopeBytes, err := json.Marshal(scope)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to marshal scope", err.Error())
		return
	}

	nowStr := h.clock().UTC().Format(time.RFC3339)
	rows, err := h.q.UpdatePluginInstanceSubscriptionScope(ctx, db.UpdatePluginInstanceSubscriptionScopeParams{
		SubscriptionScopeJson: string(scopeBytes),
		UpdatedAt:             nowStr,
		ID:                    instanceID,
		ExpectedVersion:       *req.ExpectedVersion,
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update subscription scope", "")
		return
	}
	if rows == 0 {
		httputil.WriteError(w, http.StatusConflict, "version conflict", "")
		return
	}

	// Restart the trigger stream so the plugin picks up the new scope. This is
	// fire-and-continue — the supervisor's Start call is fast; stream opening
	// is asynchronous inside the goroutine.
	if h.triggerRestarter != nil {
		h.triggerRestarter.Restart(ctx, instanceID)
	}

	// Re-fetch to return the updated row.
	updated, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		// The write succeeded; fall back to a synthesised response.
		httputil.WriteJSON(w, http.StatusOK, instanceResponse{
			ID:                    instanceID,
			PluginID:              pluginID,
			InstanceName:          inst.InstanceName,
			State:                 inst.HealthState,
			Detail:                inst.HealthDetail,
			Version:               inst.Version + 1,
			UpdatedAt:             nowStr,
			SubscriptionScopeJson: string(scopeBytes),
		})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, instanceResponse{
		ID:                    updated.ID,
		PluginID:              updated.PluginID,
		InstanceName:          updated.InstanceName,
		State:                 updated.HealthState,
		Detail:                updated.HealthDetail,
		Version:               updated.Version,
		UpdatedAt:             updated.UpdatedAt,
		SubscriptionScopeJson: updated.SubscriptionScopeJson,
	})
}

// unblockInstances transitions every instance of pluginID currently in fromState
// to toState, returning the count of successful transitions. Listing failures
// and per-instance state-machine failures are logged and skipped — the caller
// has already committed the upstream change (pubkey rotation or manifest commit)
// and the audit event is the operator's signal.
func (h *PluginHandler) unblockInstances(
	ctx context.Context,
	pluginID string,
	fromState, toState model.PluginHealthState,
	detail, logTag string,
) int {
	instances, err := h.q.ListPluginInstancesByPlugin(ctx, pluginID)
	if err != nil {
		slog.ErrorContext(ctx, logTag+": list instances failed", "plugin_id", pluginID, "err", err)
		return 0
	}

	count := 0
	for _, inst := range instances {
		if model.PluginHealthState(inst.HealthState) != fromState {
			continue
		}
		stateErr := pluginstate.SetHealthState(ctx, h.q, h.publisher, inst.ID, pluginstate.OriginHost, toState, detail)
		if stateErr != nil {
			if !errors.Is(stateErr, pluginstate.ErrTransitionConflict) {
				slog.WarnContext(ctx, logTag+": set health state failed", "instance_id", inst.ID, "err", stateErr)
			}
			continue
		}
		count++
	}
	return count
}

// writeAuditEvent inserts a plugin-level audit row (PluginInstanceID = nil) with
// the calling user as actor when one is on the request context. Failures are
// logged but not surfaced — the upstream DB change has already committed.
func (h *PluginHandler) writeAuditEvent(ctx context.Context, eventType, severity, nowStr string, payload map[string]any) {
	caller, _ := auth.UserFromContext(ctx)
	var actorUserID *string
	if caller != nil {
		actorUserID = &caller.ID
	}
	payloadJSON, _ := json.Marshal(payload)
	_, err := h.q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: nil,
		EventType:        eventType,
		Severity:         severity,
		ActorUserID:      actorUserID,
		PayloadJson:      string(payloadJSON),
		CreatedAt:        nowStr,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit event failed", "event_type", eventType, "err", err)
	}
}

// installResponse is the JSON shape returned by Install on success.
type installResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// Install handles POST /api/v1/admin/plugins.
// Accepts an application/octet-stream tarball body, passes it through the
// existing Installer pipeline, and returns the plugin row ID + metadata.
//
// The route is registered outside the /api/v1/admin group so it can carry a
// 100 MiB body-size limit independent of the group's 1 MiB cap. See router.go.
//
// Status map:
//   - 201  — install accepted (may still be pending_review or pending_key_approval)
//   - 400  — empty body, or tarball is malformed / manifest invalid
//   - 409  — CAS conflict (concurrent update to same plugin)
//   - 413  — body exceeds 100 MiB cap
//   - 422  — bundle signature rejected; see audit log
//   - 503  — plugin subsystem disabled (installer == nil)
//   - 500  — DB error or unexpected installer failure
func (h *PluginHandler) Install(w http.ResponseWriter, r *http.Request) {
	if h.installer == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "plugin install endpoint disabled", "")
		return
	}

	// Cap body at 100 MiB, matching loader.maxTarballBytes.
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)

	tmpFile, err := os.CreateTemp("", "gleipnir-plugin-upload-*.tar.gz")
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to create temp file", "")
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	defer tmpFile.Close()

	n, err := io.Copy(tmpFile, r.Body)
	if err != nil {
		// http.MaxBytesReader wraps the underlying error with "request body too large"
		// when the limit is hit. Map that to 413; anything else is an I/O error.
		if strings.Contains(err.Error(), "request body too large") {
			httputil.WriteError(w, http.StatusRequestEntityTooLarge, "tarball too large", "")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to read request body", "")
		}
		return
	}
	if n == 0 {
		httputil.WriteError(w, http.StatusBadRequest, "empty tarball body", "")
		return
	}
	if err := tmpFile.Close(); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to flush temp file", "")
		return
	}

	pluginID, err := h.installer.Install(r.Context(), tmpPath)
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "extract tarball"),
			strings.Contains(msg, "read manifest"),
			strings.Contains(msg, "parse manifest"),
			strings.Contains(msg, "manifest.name"):
			httputil.WriteError(w, http.StatusBadRequest, "malformed tarball", msg)
		case strings.Contains(msg, "CAS conflict"):
			httputil.WriteError(w, http.StatusConflict, "concurrent plugin update; retry", "")
		default:
			httputil.WriteError(w, http.StatusInternalServerError, "install failed", msg)
		}
		return
	}
	if pluginID == "" {
		// Signature verification failed; audit event was recorded.
		httputil.WriteError(w, http.StatusUnprocessableEntity, "plugin signature invalid; see audit log", "")
		return
	}

	plugin, err := h.q.GetPluginByID(r.Context(), pluginID)
	if err != nil {
		// Orphaned write: installer succeeded but the row isn't readable. Internal error.
		httputil.WriteError(w, http.StatusInternalServerError, "install succeeded but plugin not found", "")
		return
	}

	httputil.WriteCreated(w, "/api/v1/admin/plugins/"+pluginID, installResponse{
		ID:      plugin.ID,
		Name:    plugin.Name,
		Version: plugin.PluginVersion,
		Status:  plugin.Status,
	})
}

// createInstanceRequest is the JSON body for POST /api/v1/admin/plugins/{id}/instances.
type createInstanceRequest struct {
	InstanceName string `json:"instance_name"`
}

// createInstanceResponse is the JSON shape returned by CreateInstance on success.
// config_json is intentionally absent — it is always '{}' on create.
type createInstanceResponse struct {
	ID           string  `json:"id"`
	PluginID     string  `json:"plugin_id"`
	InstanceName string  `json:"instance_name"`
	HealthState  string  `json:"health_state"`
	HealthDetail *string `json:"health_detail"`
	Version      int64   `json:"version"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// CreateInstance handles POST /api/v1/admin/plugins/{id}/instances.
// Creates a plugin_instances row with safe defaults and returns its ID.
//
// Status map:
//   - 201  — instance created
//   - 400  — malformed body, or instance_name empty / whitespace-only
//   - 404  — plugin ID not found
//   - 409  — instance_name already exists for this plugin
//   - 500  — DB error
func (h *PluginHandler) CreateInstance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")

	var req createInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	instanceName := strings.TrimSpace(req.InstanceName)
	if instanceName == "" {
		httputil.WriteError(w, http.StatusBadRequest, "instance_name is required", "")
		return
	}

	if _, err := h.q.GetPluginByID(ctx, pluginID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		}
		return
	}

	// Pre-check uniqueness for a clean 409 before hitting the UNIQUE constraint.
	if _, err := h.q.GetPluginInstanceByName(ctx, db.GetPluginInstanceByNameParams{
		PluginID:     pluginID,
		InstanceName: instanceName,
	}); err == nil {
		httputil.WriteError(w, http.StatusConflict, "instance_name already exists for this plugin", "")
		return
	}

	instanceID := model.NewULID()
	nowStr := h.clock().UTC().Format(time.RFC3339Nano)
	healthDetail := "config_missing"

	inst, err := h.q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID:                    instanceID,
		PluginID:              pluginID,
		InstanceName:          instanceName,
		ConfigJson:            "{}",
		SubscriptionScopeJson: "{}",
		CredentialsEncrypted:  nil,
		CredentialsExpiresAt:  nil,
		HandshakeVersions:     "{}",
		HealthState:           "unhealthy",
		HealthDetail:          &healthDetail,
		LastOauthCallbackUrl:  nil,
		CreatedAt:             nowStr,
		UpdatedAt:             nowStr,
	})
	if err != nil {
		// Race after pre-check: UNIQUE constraint triggered by concurrent insert.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			httputil.WriteError(w, http.StatusConflict, "instance_name already exists for this plugin", "")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to create instance", "")
		}
		return
	}

	httputil.WriteCreated(w,
		"/api/v1/admin/plugins/"+pluginID+"/instances/"+instanceID,
		createInstanceResponse{
			ID:           inst.ID,
			PluginID:     inst.PluginID,
			InstanceName: inst.InstanceName,
			HealthState:  inst.HealthState,
			HealthDetail: inst.HealthDetail,
			Version:      inst.Version,
			CreatedAt:    inst.CreatedAt,
			UpdatedAt:    inst.UpdatedAt,
		})
}

// deriveFingerprint extracts the 8-byte key ID from a Minisign public key blob.
// Returns a zero array if the blob is empty or unparseable (e.g. unsigned plugins
// have an empty trusted_pubkey).
func deriveFingerprint(pubkeyBytes []byte) [8]byte {
	if len(pubkeyBytes) == 0 {
		return [8]byte{}
	}
	pk, _, err := signing.ParsePublicKey(pubkeyBytes)
	if err != nil {
		return [8]byte{}
	}
	return pk.KeyID
}
