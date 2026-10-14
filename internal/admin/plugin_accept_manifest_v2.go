package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle/desiredstate"
	pluginmanifest "github.com/felag-engineering/gleipnir/internal/plugin/manifest"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// reconcilerKicker is implemented by a process manager that can ask the
// reconciler for an immediate pass. Optional: without it the reconciler picks
// the change up on its own tick.
type reconcilerKicker interface {
	Kick()
}

// acceptManifestV2Request is the accept-manifest body. ExpectedCandidateSHA256
// is required for a v2 manifest and ignored for v1, whose behaviour is unchanged.
type acceptManifestV2Request struct {
	ExpectedCandidateSHA256 string `json:"expected_candidate_sha256"`
}

func candidateSHA256(candidate []byte) string {
	sum := sha256.Sum256(candidate)
	return hex.EncodeToString(sum[:])
}

// pendingV2Consent returns the consent surface and SHA-256 of the pending v2
// candidate for pluginID, or nil and "" when there is none. A candidate that
// cannot be rendered is an error: a consent screen that cannot show a field
// must not be shown at all.
func (h *PluginHandler) pendingV2Consent(ctx context.Context, pluginID string) (*pluginV2Consent, string, error) {
	row, err := h.q.GetPluginPendingManifest(ctx, pluginID)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("load pending manifest: %w", err)
	}
	candidate := []byte(row.CandidateManifest)
	if !manifestv2.IsV2(candidate) {
		return nil, "", nil
	}
	snap, err := pluginmanifest.Read(candidate)
	if err != nil {
		return nil, "", fmt.Errorf("read pending manifest: %w", err)
	}
	consent, err := buildPluginV2Consent(snap)
	if err != nil {
		return nil, "", fmt.Errorf("build pending consent: %w", err)
	}
	return consent, candidateSHA256(candidate), nil
}

// acceptManifestV2 handles accept-manifest when either the stored snapshot or
// the pending candidate is a schema_version 2 manifest. It reports whether it
// wrote the response; false means both sides are v1 and the v1 path in
// AcceptManifest continues. Only called when approveV2 is set.
//
// Accepting commits the candidate as the consented snapshot AND rewrites the
// instances' plugin_containers rows from it in one transaction. The second
// half is what makes a changed image digest take effect: the desired image is
// derived from the snapshot when an instance is created, so a snapshot update
// alone would change the consent without changing what the reconciler runs.
// Once the rows disagree with the running generation the reconciler rotates.
func (h *PluginHandler) acceptManifestV2(w http.ResponseWriter, r *http.Request, plugin db.Plugin) bool {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")

	pendingRow, err := h.q.GetPluginPendingManifest(ctx, pluginID)
	if err != nil {
		// No pending change, or a failed read: the v1 path reports both.
		return false
	}
	candidateBytes := []byte(pendingRow.CandidateManifest)

	snapshotIsV2 := manifestv2.IsV2([]byte(plugin.ManifestSnapshot))
	candidateIsV2 := manifestv2.IsV2(candidateBytes)
	if !snapshotIsV2 && !candidateIsV2 {
		return false
	}
	if snapshotIsV2 != candidateIsV2 {
		httputil.WriteError(w, http.StatusConflict, "pending manifest is a different schema generation than the installed plugin; it cannot be accepted", "")
		return true
	}

	// The hash binds acceptance to the candidate the admin reviewed: a later
	// bundle can overwrite the pending row between the detail read and this call.
	var body acceptManifestV2Request
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", "")
		return true
	}
	if body.ExpectedCandidateSHA256 == "" {
		httputil.WriteError(w, http.StatusBadRequest, "expected_candidate_sha256 is required to accept a v2 manifest", "")
		return true
	}
	candidateSHA := candidateSHA256(candidateBytes)
	if !strings.EqualFold(body.ExpectedCandidateSHA256, candidateSHA) {
		httputil.WriteError(w, http.StatusConflict, "the pending manifest changed since it was shown; reload and review it again", "")
		return true
	}

	if plugin.Status == string(model.PluginStatusRemoved) {
		httputil.WriteError(w, http.StatusConflict, "plugin has been removed", "")
		return true
	}
	if h.store == nil || h.provisioner == nil {
		httputil.WriteError(w, http.StatusInternalServerError, "v2 manifest acceptance is not configured", "")
		return true
	}

	// Parse through the version-dispatching reader first so both sides fail
	// closed on the same validation every other consumer applies.
	if _, err := pluginmanifest.Read([]byte(plugin.ManifestSnapshot)); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", err.Error())
		return true
	}
	if _, err := pluginmanifest.Read(candidateBytes); err != nil {
		httputil.WriteError(w, http.StatusUnprocessableEntity, "candidate manifest: parse failed", err.Error())
		return true
	}
	oldManifest, err := manifestv2.Parse([]byte(plugin.ManifestSnapshot))
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", err.Error())
		return true
	}
	newManifest, err := manifestv2.Parse(candidateBytes)
	if err != nil {
		httputil.WriteError(w, http.StatusUnprocessableEntity, "candidate manifest: parse failed", err.Error())
		return true
	}
	if newManifest.Name != plugin.Name {
		httputil.WriteError(w, http.StatusUnprocessableEntity, "candidate manifest names a different plugin", "")
		return true
	}

	newlyRequired, schemaErr := pluginmanifest.ConfigSchemaNewlyRequiredFieldsV2(oldManifest, newManifest)
	// A malformed schema cannot be judged, so fail closed into migration.
	forceMigration := schemaErr != nil
	if forceMigration {
		slog.WarnContext(ctx, "accept-manifest: config schema unparseable; forcing pending_config_migration",
			"plugin_id", pluginID, "err", schemaErr)
	}

	nowStr := h.clock().UTC().Format(time.RFC3339Nano)
	resynced, err := h.commitAcceptedManifestV2(ctx, plugin, pendingRow, newManifest, candidateSHA, nowStr)
	switch {
	case errors.Is(err, errAcceptCASConflict), errors.Is(err, desiredstate.ErrCASConflict):
		httputil.WriteError(w, http.StatusConflict, casConflictMsg, "")
		return true
	case errors.Is(err, errAcceptCandidateChanged):
		httputil.WriteError(w, http.StatusConflict, "the pending manifest changed since it was shown; reload and review it again", "")
		return true
	case err != nil:
		slog.ErrorContext(ctx, "accept-manifest: commit failed", "plugin_id", pluginID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update manifest", "")
		return true
	}

	if kicker, ok := h.processManager.(reconcilerKicker); ok {
		kicker.Kick()
	}

	targetState := model.PluginHealthStateHealthy
	if forceMigration || len(newlyRequired) > 0 {
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

	h.writeAuditEvent(ctx, auditManifestAccepted, "info", nowStr, map[string]any{
		"plugin_id":                 pluginID,
		"name":                      plugin.Name,
		"old_version":               pendingRow.OldVersion,
		"new_version":               newManifest.Version,
		"old_image_digest":          oldManifest.Package.Digest(),
		"new_image_digest":          newManifest.Package.Digest(),
		"candidate_sha256":          candidateSHA,
		"instances_resynced":        resynced,
		"instances_unblocked":       unblocked,
		"instances_pending_config":  pendingConfig,
		"config_schema_unparseable": forceMigration,
	})

	httputil.WriteJSON(w, http.StatusOK, acceptManifestResponse{
		AcceptedManifestVersion: newManifest.Version,
		InstancesUnblocked:      unblocked,
		InstancesPendingConfig:  pendingConfig,
	})
	return true
}

var (
	errAcceptCASConflict      = errors.New("accept-manifest: CAS conflict")
	errAcceptCandidateChanged = errors.New("accept-manifest: pending candidate changed")
)

// commitAcceptedManifestV2 writes the new snapshot, clears the pending row and
// resyncs the instances' desired state in one transaction, so a failure in any
// part leaves the consent and the desired image agreeing with each other.
// Status is preserved: unlike v1 this does not promote a plugin to active, so
// accepting a candidate for a plugin still awaiting its first review does not
// approve it.
func (h *PluginHandler) commitAcceptedManifestV2(ctx context.Context, plugin db.Plugin, pending db.PluginPendingManifest, accepted *manifestv2.Manifest, candidateSHA, nowStr string) (int, error) {
	tx, err := h.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.ErrorContext(ctx, "accept-manifest: rollback failed", "err", rbErr)
		}
	}()

	q := db.New(tx)

	// Re-read inside the transaction: a newer bundle may have replaced the
	// pending row since the check above, and deleting it would discard a
	// candidate nobody reviewed.
	current, err := q.GetPluginPendingManifest(ctx, plugin.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errAcceptCandidateChanged
	}
	if err != nil {
		return 0, fmt.Errorf("re-read pending manifest: %w", err)
	}
	if candidateSHA256([]byte(current.CandidateManifest)) != candidateSHA {
		return 0, errAcceptCandidateChanged
	}

	rows, err := q.UpdatePluginManifest(ctx, db.UpdatePluginManifestParams{
		ManifestSnapshot: pending.CandidateManifest,
		PluginVersion:    accepted.Version,
		Status:           plugin.Status,
		UpdatedAt:        nowStr,
		ID:               plugin.ID,
		ExpectedVersion:  plugin.Version,
	})
	if err != nil {
		return 0, fmt.Errorf("update manifest: %w", err)
	}
	if rows == 0 {
		return 0, errAcceptCASConflict
	}
	if err := q.DeletePluginPendingManifest(ctx, plugin.ID); err != nil {
		return 0, fmt.Errorf("delete pending manifest: %w", err)
	}
	resynced, err := h.provisioner.ResyncForPlugin(ctx, q, plugin.ID)
	if err != nil {
		return 0, fmt.Errorf("resync desired state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return resynced, nil
}
