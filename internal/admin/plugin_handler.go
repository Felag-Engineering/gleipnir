package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
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
}

// PluginHandler handles plugin-related admin endpoints.
type PluginHandler struct {
	q         PluginQuerier
	publisher event.Publisher
	clock     func() time.Time
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

// instanceResponse is the JSON shape returned by GetInstance.
// Credentials and other write-only fields are intentionally absent — mirrors
// the ADR-039 read-restraint pattern for encrypted auth headers.
type instanceResponse struct {
	ID           string  `json:"id"`
	PluginID     string  `json:"plugin_id"`
	InstanceName string  `json:"instance_name"`
	State        string  `json:"state"`
	Detail       *string `json:"detail"`
	Version      int64   `json:"version"`
	UpdatedAt    string  `json:"updated_at"`
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

	// Validate that the instance belongs to the requested plugin. Return 404
	// rather than 403 to avoid leaking instance existence across plugins.
	if row.PluginID != pluginID {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, instanceResponse{
		ID:           row.ID,
		PluginID:     row.PluginID,
		InstanceName: row.InstanceName,
		State:        row.HealthState,
		Detail:       row.HealthDetail,
		Version:      row.Version,
		UpdatedAt:    row.UpdatedAt,
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

	// Decode the base64 candidate and parse as a Minisign public key to validate format.
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

	// Look up the plugin row.
	plugin, err := h.q.GetPluginByID(ctx, pluginID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
		return
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		return
	}

	now := h.clock().UTC()
	nowStr := now.Format(time.RFC3339Nano)

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
		// CAS miss — a concurrent writer advanced the version.
		httputil.WriteError(w, http.StatusConflict, "concurrent modification detected; retry", "")
		return
	}

	// Unblock all instances currently in pending_key_approval.
	instances, err := h.q.ListPluginInstancesByPlugin(ctx, pluginID)
	if err != nil {
		// Non-fatal — pubkey is already rotated; log and report 0 unblocked.
		slog.ErrorContext(ctx, "accept-new-key: list instances failed", "plugin_id", pluginID, "err", err)
		instances = nil
	}

	unblocked := 0
	for _, inst := range instances {
		if model.PluginHealthState(inst.HealthState) != model.PluginHealthStatePendingKeyApproval {
			continue
		}
		if stateErr := pluginstate.SetHealthState(ctx, h.q, h.publisher, inst.ID, pluginstate.OriginHost, model.PluginHealthStateHealthy, "operator accepted new signing key"); stateErr != nil {
			if !errors.Is(stateErr, pluginstate.ErrTransitionConflict) {
				slog.WarnContext(ctx, "accept-new-key: set health state failed", "instance_id", inst.ID, "err", stateErr)
			}
			continue
		}
		unblocked++
	}

	// Emit a plugin_pubkey_rotated audit event.
	caller, _ := auth.UserFromContext(ctx)
	var actorUserID *string
	if caller != nil {
		actorUserID = &caller.ID
	}
	auditPayload, _ := json.Marshal(map[string]string{
		"plugin_id":              pluginID,
		"name":                   plugin.Name,
		"old_pubkey_fingerprint": oldFingerprint,
		"new_pubkey_fingerprint": newFingerprint,
	})
	if _, auditErr := h.q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: nil,
		EventType:        auditPubkeyRotated,
		Severity:         "high",
		ActorUserID:      actorUserID,
		PayloadJson:      string(auditPayload),
		CreatedAt:        nowStr,
	}); auditErr != nil {
		// Non-fatal — the rotation already succeeded; log the audit failure.
		slog.ErrorContext(ctx, "accept-new-key: audit event failed", "plugin_id", pluginID, "err", auditErr)
	}

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

	// Find the most recent unaccepted plugin_manifest_material_change event for
	// this plugin. plugin_audit_events has no dedicated plugin_id column, so we
	// fetch by event type with a generous limit and filter in Go.
	// TODO(v2): add a plugin-scoped query to avoid this in-Go filter on busy installs.
	allEvents, err := h.q.ListPluginAuditEventsByType(ctx, db.ListPluginAuditEventsByTypeParams{
		EventType: "plugin_manifest_material_change",
		Offset:    0,
		Limit:     200,
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to list audit events", "")
		return
	}

	var candidateEvent *db.PluginAuditEvent
	for i := range allEvents {
		ev := &allEvents[i]
		var payload map[string]any
		if jsonErr := json.Unmarshal([]byte(ev.PayloadJson), &payload); jsonErr != nil {
			continue
		}
		if payload["plugin_id"] == pluginID {
			candidateEvent = ev
			break // events are ordered DESC by created_at; first match is newest
		}
	}
	if candidateEvent == nil {
		httputil.WriteError(w, http.StatusConflict, "no pending manifest change found for this plugin", "")
		return
	}

	var candidatePayload map[string]any
	if err := json.Unmarshal([]byte(candidateEvent.PayloadJson), &candidatePayload); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to parse candidate audit event", "")
		return
	}
	candidateB64, _ := candidatePayload["candidate_manifest_b64"].(string)
	if candidateB64 == "" {
		httputil.WriteError(w, http.StatusUnprocessableEntity, "candidate manifest not found in audit event", "")
		return
	}

	candidateBytes, err := base64.StdEncoding.DecodeString(candidateB64)
	if err != nil {
		httputil.WriteError(w, http.StatusUnprocessableEntity, "candidate manifest: invalid base64", "")
		return
	}

	var newManifest sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal(candidateBytes, &newManifest); parseErr != nil {
		httputil.WriteError(w, http.StatusUnprocessableEntity, "candidate manifest: parse failed", parseErr.Error())
		return
	}

	// Determine which config fields are newly required so we can branch each instance.
	var oldManifest sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &oldManifest); parseErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", parseErr.Error())
		return
	}
	newlyRequired := pluginmanifest.ConfigSchemaNewlyRequiredFields(&oldManifest, &newManifest)

	now := h.clock().UTC()
	nowStr := now.Format(time.RFC3339Nano)

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

	// Unblock instances currently in pending_manifest_approval.
	instances, err := h.q.ListPluginInstancesByPlugin(ctx, pluginID)
	if err != nil {
		slog.ErrorContext(ctx, "accept-manifest: list instances failed", "plugin_id", pluginID, "err", err)
		instances = nil
	}

	unblocked := 0
	pendingConfig := 0
	for _, inst := range instances {
		if model.PluginHealthState(inst.HealthState) != model.PluginHealthStatePendingManifestApproval {
			continue
		}
		var targetState model.PluginHealthState
		if len(newlyRequired) == 0 {
			targetState = model.PluginHealthStateHealthy
		} else {
			targetState = model.PluginHealthStatePendingConfigMigration
		}
		detail := "admin accepted manifest change"
		if stateErr := pluginstate.SetHealthState(ctx, h.q, h.publisher, inst.ID, pluginstate.OriginHost, targetState, detail); stateErr != nil {
			if !errors.Is(stateErr, pluginstate.ErrTransitionConflict) {
				slog.WarnContext(ctx, "accept-manifest: set health state failed", "instance_id", inst.ID, "err", stateErr)
			}
			continue
		}
		if targetState == model.PluginHealthStateHealthy {
			unblocked++
		} else {
			pendingConfig++
		}
	}

	caller, _ := auth.UserFromContext(ctx)
	var actorUserID *string
	if caller != nil {
		actorUserID = &caller.ID
	}

	oldVersion, _ := candidatePayload["old_version"].(string)
	auditPayload, _ := json.Marshal(map[string]any{
		"plugin_id":                pluginID,
		"name":                     plugin.Name,
		"old_version":              oldVersion,
		"new_version":              newManifest.Version,
		"instances_unblocked":      unblocked,
		"instances_pending_config": pendingConfig,
	})
	if _, auditErr := h.q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: nil,
		EventType:        auditManifestAccepted,
		Severity:         "info",
		ActorUserID:      actorUserID,
		PayloadJson:      string(auditPayload),
		CreatedAt:        nowStr,
	}); auditErr != nil {
		slog.ErrorContext(ctx, "accept-manifest: audit event failed", "plugin_id", pluginID, "err", auditErr)
	}

	httputil.WriteJSON(w, http.StatusOK, acceptManifestResponse{
		AcceptedManifestVersion: newManifest.Version,
		InstancesUnblocked:      unblocked,
		InstancesPendingConfig:  pendingConfig,
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
