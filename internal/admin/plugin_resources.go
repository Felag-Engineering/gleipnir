package admin

import (
	"database/sql"
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
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle/desiredstate"
	pluginmanifest "github.com/felag-engineering/gleipnir/internal/plugin/manifest"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
)

// instanceResources is the resource envelope of one v2 instance: the admin
// override (null when none) and the limits that result from applying it over
// the manifest and the host default.
type instanceResources struct {
	Override  *pluginV2Resources         `json:"override"`
	Effective pluginV2EffectiveResources `json:"effective"`
}

func instanceOverrideAmounts(inst db.PluginInstance) *pluginV2Resources {
	if inst.ResourceMemoryMbOverride == nil && inst.ResourceCpuMillicoresOverride == nil {
		return nil
	}
	out := &pluginV2Resources{}
	if inst.ResourceMemoryMbOverride != nil {
		out.MemoryMB = int(*inst.ResourceMemoryMbOverride)
	}
	if inst.ResourceCpuMillicoresOverride != nil {
		out.CPUMillicores = int(*inst.ResourceCpuMillicoresOverride)
	}
	return out
}

func buildInstanceResources(snap pluginmanifest.Snapshot, inst db.PluginInstance) (*instanceResources, error) {
	override := instanceOverrideAmounts(inst)
	var overrideLimits resources.Limits
	if override != nil {
		var err error
		overrideLimits, err = resources.FromManifestMiB(override.MemoryMB, override.CPUMillicores)
		if err != nil {
			return nil, fmt.Errorf("stored resource override: %w", err)
		}
	}
	effective, err := resolveV2Resources(snap.Resources, overrideLimits)
	if err != nil {
		return nil, err
	}
	return &instanceResources{Override: override, Effective: effective}, nil
}

// putResourcesRequest is the body of
// PUT /api/v1/admin/plugins/{id}/instances/{iid}/resources. An absent or null
// limit clears that field's override; both absent clears the override.
type putResourcesRequest struct {
	MemoryMB        *int64 `json:"memory_mb"`
	CPUMillicores   *int64 `json:"cpu_millicores"`
	ExpectedVersion *int64 `json:"expected_version"`
}

// PutResources handles PUT /api/v1/admin/plugins/{id}/instances/{iid}/resources.
//
// Sets or clears the admin override of a container plugin's memory and CPU
// limits. Bounds are those of a manifest declaration. The override and the
// instance's desired-state limits are written in one transaction, then the
// reconciler is kicked: a changed envelope rotates the container, so it takes
// effect at the next rotation rather than in place.
//
// 400 invalid body, out-of-bounds value, or a v1 plugin; 404 unknown instance;
// 409 stale expected_version or a build with no container substrate.
func (h *PluginHandler) PutResources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	var req putResourcesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.ExpectedVersion == nil {
		httputil.WriteError(w, http.StatusBadRequest, "expected_version is required", "")
		return
	}

	inst, ok := h.resolveInstance(ctx, w, pluginID, instanceID)
	if !ok {
		return
	}
	plugin, err := h.q.GetPluginByID(ctx, pluginID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		return
	}
	snap, err := pluginmanifest.Read([]byte(plugin.ManifestSnapshot))
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", err.Error())
		return
	}
	if snap.Version != 2 {
		httputil.WriteError(w, http.StatusBadRequest, "resource limits can only be set on container plugins", "")
		return
	}
	if h.store == nil || h.provisioner == nil {
		httputil.WriteError(w, http.StatusConflict, "resource limits are not enabled in this build", "")
		return
	}

	updated, err := h.commitResourceOverride(r, inst, req)
	switch {
	case errors.Is(err, desiredstate.ErrInvalidLimits):
		httputil.WriteError(w, http.StatusBadRequest, "invalid resource limits", err.Error())
		return
	case errors.Is(err, desiredstate.ErrCASConflict):
		httputil.WriteError(w, http.StatusConflict, casConflictMsg, "")
		return
	case err != nil:
		slog.ErrorContext(ctx, "put resources: commit failed", "instance_id", instanceID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update resource limits", "")
		return
	}

	if kicker, ok := h.processManager.(reconcilerKicker); ok {
		kicker.Kick()
	}

	payload := map[string]any{
		"plugin_id": pluginID,
		"old":       instanceOverrideAmounts(inst),
		"new":       instanceOverrideAmounts(updated),
	}
	if res, resErr := buildInstanceResources(snap, updated); resErr == nil {
		payload["effective"] = res.Effective
	}
	h.writeInstanceAuditEvent(r, auditInstanceResourcesUpdated, inst.ID, payload)

	h.writeInstanceResponseWithRedaction(ctx, w, pluginID, updated)
}

// commitResourceOverride runs the override write and the desired-state rewrite
// in one transaction and returns the re-read instance row.
func (h *PluginHandler) commitResourceOverride(r *http.Request, inst db.PluginInstance, req putResourcesRequest) (db.PluginInstance, error) {
	ctx := r.Context()
	tx, err := h.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return db.PluginInstance{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.ErrorContext(ctx, "put resources: rollback failed", "err", rbErr)
		}
	}()

	q := db.New(tx)
	if err := h.provisioner.SetResourceOverride(ctx, q, inst.ID, req.MemoryMB, req.CPUMillicores, *req.ExpectedVersion); err != nil {
		return db.PluginInstance{}, err
	}
	updated, err := q.GetPluginInstanceByID(ctx, inst.ID)
	if err != nil {
		return db.PluginInstance{}, fmt.Errorf("re-read instance: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return db.PluginInstance{}, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// writeInstanceAuditEvent records an audit event against one instance, with
// the authenticated caller as actor.
func (h *PluginHandler) writeInstanceAuditEvent(r *http.Request, eventType, instanceID string, payload map[string]any) {
	ctx := r.Context()
	var actorUserID *string
	if caller, _ := auth.UserFromContext(ctx); caller != nil {
		actorUserID = &caller.ID
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		slog.ErrorContext(ctx, "audit payload marshal failed", "event_type", eventType, "err", err)
		return
	}
	if _, err := h.q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: &instanceID,
		EventType:        eventType,
		Severity:         "info",
		ActorUserID:      actorUserID,
		PayloadJson:      string(payloadJSON),
		CreatedAt:        h.clock().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		slog.ErrorContext(ctx, "audit event failed", "event_type", eventType, "err", err)
	}
}
