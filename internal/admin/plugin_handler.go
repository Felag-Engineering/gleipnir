package admin

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	"github.com/felag-engineering/gleipnir/internal/plugin/oauth"
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

	// auditInstanceDeleted is emitted by DeleteInstance on success.
	auditInstanceDeleted = "plugin_instance_deleted"

	// auditPluginUninstalled is emitted by Uninstall on success.
	auditPluginUninstalled = "plugin_uninstalled"

	// auditReviewApproved is emitted when an admin approves a pending-review plugin.
	auditReviewApproved = "plugin_review_approved"

	// auditReviewRejected is emitted when an admin rejects a pending-review plugin.
	auditReviewRejected = "plugin_review_rejected"

	// auditInstanceDeactivated is emitted when an admin deactivates an instance (#243).
	auditInstanceDeactivated = "plugin_instance_deactivated"

	// auditInstanceActivated is emitted when an admin re-activates an instance (#243).
	auditInstanceActivated = "plugin_instance_activated"

	// casConflictMsg is the standard 409 response body for CAS version conflicts.
	// Consistent with the majority phrasing used elsewhere in the file.
	casConflictMsg = "concurrent modification detected; retry"
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
	// Pending manifest read (used by AcceptManifest to retrieve the candidate).
	GetPluginPendingManifest(ctx context.Context, pluginID string) (db.PluginPendingManifest, error)
	// Pending manifest delete (best-effort cleanup after AcceptManifest commits).
	DeletePluginPendingManifest(ctx context.Context, pluginID string) error
	// Plugin manifest write (used by AcceptManifest to commit the candidate snapshot).
	UpdatePluginManifest(ctx context.Context, arg db.UpdatePluginManifestParams) (int64, error)
	// Instance subscription scope write (used by PutSubscriptionScope).
	UpdatePluginInstanceSubscriptionScope(ctx context.Context, arg db.UpdatePluginInstanceSubscriptionScopeParams) (int64, error)
	// Instance create (used by CreateInstance).
	CreatePluginInstance(ctx context.Context, arg db.CreatePluginInstanceParams) (db.PluginInstance, error)
	// Instance lookup by (plugin_id, instance_name) uniqueness check (used by CreateInstance).
	GetPluginInstanceByName(ctx context.Context, arg db.GetPluginInstanceByNameParams) (db.PluginInstance, error)
	// Instance config write (used by PutInstanceConfig).
	UpdatePluginInstanceConfig(ctx context.Context, arg db.UpdatePluginInstanceConfigParams) (int64, error)
	// Audience entries referencing an instance (used by DeleteInstance and Uninstall reference guards).
	ListAudienceEntriesByInstance(ctx context.Context, pluginInstanceID string) ([]db.ListAudienceEntriesByInstanceRow, error)
	// Pending request cleanup (RESTRICT FK; must clear before deleting the instance).
	DeletePluginPendingRequestsByInstance(ctx context.Context, pluginInstanceID string) error
	// OAuth nonce cleanup before deleting an instance.
	DeletePluginOAuthNoncesByInstance(ctx context.Context, instanceID string) error
	// Plugin delete (cascades to plugin_instances via ON DELETE CASCADE).
	DeletePlugin(ctx context.Context, id string) (int64, error)
	// Instance delete by primary key.
	DeletePluginInstance(ctx context.Context, id string) (int64, error)
	// Policy list (used by reference guard in DeleteInstance and Uninstall).
	ListPolicies(ctx context.Context) ([]db.Policy, error)
	// Plugin list (used by ListPlugins handler).
	ListPlugins(ctx context.Context) ([]db.Plugin, error)
	// Plugin status write (used by ApprovePlugin CAS transition).
	UpdatePluginStatus(ctx context.Context, arg db.UpdatePluginStatusParams) (int64, error)
}

// PluginProcessManager is the narrow interface for starting and stopping plugin
// subprocesses. Implemented by *process.Manager; kept as an interface in the
// admin package so internal/admin does not import internal/plugin/process
// (package boundary). All methods are best-effort — a subprocess failure must
// not block DB operations.
type PluginProcessManager interface {
	// Stop terminates the subprocess for a single instance.
	Stop(ctx context.Context, instanceID string) error
	// StopByPluginID terminates all running instances belonging to pluginID.
	StopByPluginID(ctx context.Context, pluginID string) error
	// StartByPluginID spawns subprocesses for all instances of pluginID that
	// are not already running. Mirrors the OnInstalled hook in main.go.
	StartByPluginID(ctx context.Context, pluginID string) error
}

// ToolUnregistrar is the narrow interface for releasing tool-namespace
// reservations when an instance is deleted. Implemented by *tools.Registrar;
// kept as an interface here so the admin package does not import
// internal/plugin/tools. The field is wired but SetToolUnregistrar is NOT
// called in main.go for this PR — tools.Registrar is not yet in the live
// process path (TODO at main.go). A nil unregistrar is a safe no-op.
type ToolUnregistrar interface {
	UnregisterInstance(ctx context.Context, instanceName string)
}

// PluginInstaller is the narrow interface used by the Install handler.
// Implemented by *loader.Installer; kept narrow here so the admin package
// does not import internal/plugin/loader directly and tests can inject a stub.
type PluginInstaller interface {
	Install(ctx context.Context, tarPath string) (string, error)
}

// TriggerRestarter is the narrow interface used to start, restart, or stop a
// plugin's trigger stream. Implemented by *plugintrigger.Supervisor; kept
// narrow here so the admin package does not import the trigger package directly.
type TriggerRestarter interface {
	Start(ctx context.Context, instanceID string)
	Restart(ctx context.Context, instanceID string)
	// Stop cancels the supervised trigger stream for instanceID. Called by
	// DeactivateInstance to ensure the plugin stops receiving trigger events
	// after its subprocess is stopped (#243).
	Stop(instanceID string)
}

// InflightCounter reports the number of tool calls currently dispatched to a
// named plugin instance. Implemented by *dispatch.Pool; kept as an interface
// here so the admin package does not import internal/plugin/dispatch (package
// boundary mirrors PluginProcessManager). Used by DeactivateInstance and
// DeleteInstance to gate actions that must not disrupt active work (#243).
type InflightCounter interface {
	InflightCountByInstance(instanceName string) int
}

// RSSSample holds one RSS reading for a single plugin instance. Defined with
// primitive types only so the admin package does not import
// internal/plugin/process — the adapter in main.go converts between the two.
type RSSSample struct {
	InstanceID   string
	InstanceName string
	PluginID     string
	Bytes        uint64
	SampledAt    time.Time
}

// RSSAggregator returns the aggregate and per-instance RSS across all running
// plugin subprocesses. Implemented by an adapter in main.go that wraps
// *process.RSSSampler — the interface lives here so the admin package does not
// import internal/plugin/process (package boundary).
//
// When plugins are disabled, the field on PluginHandler is nil and GetPluginRSS
// returns 503.
type RSSAggregator interface {
	Aggregate() (totalBytes uint64, count int, perInstance []RSSSample)
}

// CredentialSeeder writes an initial encrypted credential blob for a freshly
// created plugin instance. *oauth.DBStore satisfies it. The interface keeps the
// admin package from depending on the concrete store wiring and lets tests
// inject a recording fake.
//
// CreateInstance calls SaveCredentials with expectedVersion 0 because a row
// created by CreatePluginInstance always starts at version 0 (ADR-038 CAS).
type CredentialSeeder interface {
	SaveCredentials(ctx context.Context, instanceID string, creds oauth.StoredCredentials, expectedVersion int64) error
}

// PluginHandlerDeps holds all constructor-injected dependencies for PluginHandler.
// This replaces the 8 SetXxx late-bind setters (compile-checked per issue #504).
//
// processManager and pluginsDir are held by BOTH PluginHandler (for
// CreateInstance/ApprovePlugin and RejectPlugin respectively) AND by the
// InstanceLifecycle module. Wire the SAME instance/value to both from main.go.
type PluginHandlerDeps struct {
	Q              PluginQuerier
	Publisher      event.Publisher
	Clock          func() time.Time
	Installer      PluginInstaller      // nil disables Install (503)
	RSSAggregator  RSSAggregator        // nil disables GetPluginRSS (503)
	ProcessManager PluginProcessManager // nil skips subprocess spawn in CreateInstance/ApprovePlugin
	PluginsDir     string               // empty disables FS cleanup in RejectPlugin
	Lifecycle      *InstanceLifecycle   // extracted lifecycle module
	Config         *InstanceConfig      // extracted config module
	// CredentialSeeder seeds the initial credential blob on instance create.
	// nil (e.g. no encryption key configured) skips seeding — the row is still
	// created, the operator can set credentials later via the credentials API.
	CredentialSeeder CredentialSeeder
}

// PluginHandler handles plugin-related admin endpoints.
type PluginHandler struct {
	q              PluginQuerier
	publisher      event.Publisher
	clock          func() time.Time
	installer      PluginInstaller      // may be nil in tests
	pluginsDir     string               // empty disables FS cleanup in RejectPlugin
	processManager PluginProcessManager // nil means skip subprocess spawn in CreateInstance/ApprovePlugin
	rssAggregator  RSSAggregator        // may be nil in tests; GetPluginRSS returns 503
	lifecycle      *InstanceLifecycle   // owns Deactivate/Activate/Delete/Uninstall
	config         *InstanceConfig      // owns PutSubscriptionScope/PutConfig/PutConfigProperty
	credSeeder     CredentialSeeder     // nil means skip credential seeding on create
}

// NewPluginHandler constructs a PluginHandler from the given deps struct.
// clock defaults to time.Now when nil. All other nil-able deps are nil-safe.
func NewPluginHandler(deps PluginHandlerDeps) *PluginHandler {
	clk := deps.Clock
	if clk == nil {
		clk = time.Now
	}
	return &PluginHandler{
		q:              deps.Q,
		publisher:      deps.Publisher,
		clock:          clk,
		installer:      deps.Installer,
		pluginsDir:     deps.PluginsDir,
		processManager: deps.ProcessManager,
		rssAggregator:  deps.RSSAggregator,
		lifecycle:      deps.Lifecycle,
		config:         deps.Config,
		credSeeder:     deps.CredentialSeeder,
	}
}

// pluginRSSResponse is the JSON shape returned by GetPluginRSS.
type pluginRSSResponse struct {
	TotalBytes    uint64              `json:"total_bytes"`
	InstanceCount int                 `json:"instance_count"`
	Instances     []pluginRSSInstance `json:"instances"`
}

// pluginRSSInstance is one entry in the per-instance breakdown.
type pluginRSSInstance struct {
	InstanceID   string    `json:"instance_id"`
	InstanceName string    `json:"instance_name"`
	PluginID     string    `json:"plugin_id"`
	RSSBytes     uint64    `json:"rss_bytes"`
	SampledAt    time.Time `json:"sampled_at"`
}

// GetPluginRSS handles GET /api/v1/admin/plugins/rss.
//
// Returns the aggregate resident set size across all running plugin
// subprocesses plus a per-instance breakdown sorted by RSS descending.
// Samples are produced by the RSSSampler every 30s; the response reflects
// the most recent snapshot.
//
// Returns 503 when the RSS sampler is not initialized (no process manager).
func (h *PluginHandler) GetPluginRSS(w http.ResponseWriter, r *http.Request) {
	if h.rssAggregator == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "plugin RSS sampler not initialized", "")
		return
	}

	totalBytes, count, samples := h.rssAggregator.Aggregate()

	instances := make([]pluginRSSInstance, len(samples))
	for i, s := range samples {
		instances[i] = pluginRSSInstance{
			InstanceID:   s.InstanceID,
			InstanceName: s.InstanceName,
			PluginID:     s.PluginID,
			RSSBytes:     s.Bytes,
			SampledAt:    s.SampledAt,
		}
	}

	httputil.WriteJSON(w, http.StatusOK, pluginRSSResponse{
		TotalBytes:    totalBytes,
		InstanceCount: count,
		Instances:     instances,
	})
}

// instanceResponse is the JSON shape returned by GetInstance, PutSubscriptionScope,
// and PutInstanceConfig. Credentials and other write-only fields are intentionally
// absent — mirrors the ADR-039 read-restraint pattern for encrypted auth headers.
type instanceResponse struct {
	ID                    string  `json:"id"`
	PluginID              string  `json:"plugin_id"`
	InstanceName          string  `json:"instance_name"`
	State                 string  `json:"state"`
	Detail                *string `json:"detail"`
	Version               int64   `json:"version"`
	UpdatedAt             string  `json:"updated_at"`
	SubscriptionScopeJson string  `json:"subscription_scope_json"`
	ConfigJson            string  `json:"config_json"`
	// ConfigSchema is the manifest's instance-level JSON Schema (manifest.ConfigSchema
	// decoded verbatim). The schema is metadata — x-gleipnir-secret annotations
	// live IN the schema to drive redaction of VALUES in ConfigJson; the schema
	// itself contains no secrets and is returned as-is (ADR-049). nil when the
	// manifest declares no config_schema.
	ConfigSchema interface{} `json:"config_schema"`
}

// GetInstance handles GET /api/v1/admin/plugins/{id}/instances/{iid}.
// Returns the health state and detail for a single plugin instance. 404 is
// returned when the instance does not exist or belongs to a different plugin.
//
// Config properties marked x-gleipnir-secret: true in the manifest's
// ConfigSchema are redacted to "***" before the response is written.
// If the manifest cannot be parsed, the request fails with 500 (fail-closed
// per ADR-049 and the ADR-001 posture — never silently omit a security control).
func (h *PluginHandler) GetInstance(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	row, ok := h.resolveInstance(r.Context(), w, pluginID, instanceID)
	if !ok {
		return
	}

	// Fetch the plugin once. Map ErrNotFound to 404 explicitly so the caller
	// gets the expected status — writeInstanceResponseWithRedactionForPlugin
	// maps any plugin-fetch error to 500, which is wrong for the missing-plugin
	// case. Passing the already-fetched row to the inner variant avoids a second
	// DB round-trip and the TOCTOU window that would exist if we fetched twice.
	plugin, err := h.q.GetPluginByID(r.Context(), pluginID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		}
		return
	}

	h.writeInstanceResponseWithRedactionForPlugin(w, plugin, row)
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
		httputil.WriteError(w, http.StatusConflict, casConflictMsg, "")
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
// It commits the candidate manifest (stored in the plugin_pending_manifests
// table by handleManifestMaterialChange) as the new snapshot, then transitions
// instances out of pending_manifest_approval.
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

	pendingRow, candidateBytes, newManifest, err := h.loadPendingManifest(ctx, pluginID)
	if err != nil {
		writeCandidateError(w, err)
		return
	}

	var oldManifest sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &oldManifest); parseErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", parseErr.Error())
		return
	}
	newlyRequired, schemaErr := pluginmanifest.ConfigSchemaNewlyRequiredFields(&oldManifest, &newManifest)
	// If the new manifest's config_schema is malformed we cannot safely determine
	// whether migration is needed — fail closed by forcing pending_config_migration
	// rather than letting the instance silently land on healthy (fail-open).
	forceMigration := schemaErr != nil
	if forceMigration {
		slog.WarnContext(ctx, "accept-manifest: config schema unparseable; forcing pending_config_migration",
			"plugin_id", pluginID, "err", schemaErr)
	}

	nowStr := h.clock().UTC().Format(time.RFC3339Nano)

	// CAS-guarded manifest commit (ADR-038). Verify rows-affected before touching instances.
	// Status returns to "active" because the admin has explicitly accepted the candidate
	// manifest — leaving it pending_review would re-orphan the plugin (subprocess manager
	// and trigger supervisor both filter to status='active').
	rows, err := h.q.UpdatePluginManifest(ctx, db.UpdatePluginManifestParams{
		ManifestSnapshot: string(candidateBytes),
		PluginVersion:    newManifest.Version,
		Status:           "active",
		UpdatedAt:        nowStr,
		ID:               pluginID,
		ExpectedVersion:  plugin.Version,
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update manifest", "")
		return
	}
	if rows == 0 {
		httputil.WriteError(w, http.StatusConflict, casConflictMsg, "")
		return
	}

	// Best-effort: delete the pending-manifest row now that the candidate is committed.
	// We do not fail the request on error because the snapshot is already in plugins.manifest_snapshot.
	// A leftover row is harmless: the next material change will overwrite it via the upsert.
	if delErr := h.q.DeletePluginPendingManifest(ctx, pluginID); delErr != nil {
		slog.WarnContext(ctx, "accept-manifest: delete pending manifest row failed",
			"plugin_id", pluginID, "err", delErr)
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
		"instances_unblocked":       unblocked,
		"instances_pending_config":  pendingConfig,
		"config_schema_unparseable": forceMigration,
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

// loadPendingManifest retrieves the pending candidate manifest for pluginID from
// the plugin_pending_manifests table. Returns the DB row, the raw manifest bytes,
// and the parsed manifest. Maps sql.ErrNoRows to 409 (no pending change) so the
// caller can surface the right status without re-checking the error type.
func (h *PluginHandler) loadPendingManifest(ctx context.Context, pluginID string) (db.PluginPendingManifest, []byte, sdkmanifest.Manifest, error) {
	row, err := h.q.GetPluginPendingManifest(ctx, pluginID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.PluginPendingManifest{}, nil, sdkmanifest.Manifest{}, &candidateLookupError{
			status: http.StatusConflict,
			msg:    "no pending manifest change found for this plugin",
		}
	}
	if err != nil {
		return db.PluginPendingManifest{}, nil, sdkmanifest.Manifest{}, &candidateLookupError{
			status: http.StatusInternalServerError,
			msg:    "failed to load pending manifest",
		}
	}

	candidateBytes := []byte(row.CandidateManifest)
	var m sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal(candidateBytes, &m); parseErr != nil {
		return db.PluginPendingManifest{}, nil, sdkmanifest.Manifest{}, &candidateLookupError{
			status: http.StatusUnprocessableEntity,
			msg:    "candidate manifest: parse failed",
			detail: parseErr.Error(),
		}
	}
	return row, candidateBytes, m, nil
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

	result, err := h.config.PutSubscriptionScope(ctx, pluginID, instanceID, req.Scope, *req.ExpectedVersion)
	if err != nil {
		h.mapConfigError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result.Response)
}

// putInstanceConfigRequest is the JSON body for
// PUT /api/v1/admin/plugins/{id}/instances/{iid}/config.
type putInstanceConfigRequest struct {
	Config          map[string]any `json:"config"`
	ExpectedVersion *int64         `json:"expected_version,omitempty"`
}

// PutInstanceConfig handles PUT /api/v1/admin/plugins/{id}/instances/{iid}/config.
// Validates the config against the manifest's config_schema if present, persists
// the new config (CAS-guarded via ADR-038), and returns the updated instance row.
// Unlike PutSubscriptionScope, this does NOT restart the trigger stream — config
// changes do not invalidate the trigger subscription.
func (h *PluginHandler) PutInstanceConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	var req putInstanceConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.ExpectedVersion == nil {
		httputil.WriteError(w, http.StatusBadRequest, "expected_version is required", "")
		return
	}

	result, err := h.config.PutConfig(ctx, pluginID, instanceID, req.Config, *req.ExpectedVersion)
	if err != nil {
		h.mapConfigError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result.Response)
}

// putInstanceConfigPropertyRequest is the JSON body for
// PUT /api/v1/admin/plugins/{id}/instances/{iid}/config/{property}.
type putInstanceConfigPropertyRequest struct {
	Value           any    `json:"value"`
	ExpectedVersion *int64 `json:"expected_version,omitempty"`
}

// PutInstanceConfigProperty handles PUT /api/v1/admin/plugins/{id}/instances/{iid}/config/{property}.
// Updates a single property in the instance's config_json, CAS-guarded via ADR-038.
//
// This mirrors the ADR-039 PUT /mcp/servers/:id/headers/:name pattern (ADR-049):
// one secret property at a time so the caller never needs to transmit all config
// values (including other secrets) to update a single field.
//
// The full config (with the new value merged in) is validated against the
// manifest's config_schema before writing. The response redacts all secret
// properties — including the just-written one — to "***" (ADR-049).
func (h *PluginHandler) PutInstanceConfigProperty(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")
	property := chi.URLParam(r, "property")

	var req putInstanceConfigPropertyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.ExpectedVersion == nil {
		httputil.WriteError(w, http.StatusBadRequest, "expected_version is required", "")
		return
	}

	result, err := h.config.PutConfigProperty(ctx, pluginID, instanceID, property, req.Value, *req.ExpectedVersion)
	if err != nil {
		h.mapConfigError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result.Response)
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

// resolveInstance fetches the instance row for instanceID and verifies that it
// belongs to pluginID. On any error it writes the appropriate HTTP response and
// returns false, so callers can early-return. This is the instance-first variant
// (GetInstance, PutSubscriptionScope, PutInstanceConfig, PutInstanceConfigProperty);
// the plugin-first handlers (Deactivate/Activate/DeleteInstance) are left
// unchanged because they emit a distinct "plugin not found" 404 first.
func (h *PluginHandler) resolveInstance(ctx context.Context, w http.ResponseWriter, pluginID, instanceID string) (db.PluginInstance, bool) {
	row, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return db.PluginInstance{}, false
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get instance", "")
		return db.PluginInstance{}, false
	}
	// Return 404 (not 403) on a plugin/instance mismatch to avoid leaking
	// instance existence across plugins.
	if row.PluginID != pluginID {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return db.PluginInstance{}, false
	}
	return row, true
}

// toErrorIssues converts a slice of configvalidate.FieldError values into the
// httputil.ErrorIssue slice expected by WriteValidationError. The three
// schema-validation sites (subscription scope, instance config bulk, instance
// config per-property) all produce identical loops; this helper collapses them.
func toErrorIssues(fes []configvalidate.FieldError) []httputil.ErrorIssue {
	out := make([]httputil.ErrorIssue, 0, len(fes))
	for _, fe := range fes {
		out = append(out, httputil.ErrorIssue{Field: fe.Field, Message: fe.Message})
	}
	return out
}

// joinNames returns a comma-separated string of names extracted from a slice
// via the provided accessor. Used by deletion guards that need to format a
// human-readable list of blocking dependents.
func joinNames[T any](items []T, name func(T) string) string {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = name(item)
	}
	return strings.Join(names, ", ")
}

// mapLifecycleError maps a typed error from InstanceLifecycle to the EXACT
// current HTTP status code and response body. Every distinct string here was
// verified against the old inline handlers (ERROR SURFACE in plan.md).
func (h *PluginHandler) mapLifecycleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrStoreUnavailable):
		httputil.WriteError(w, http.StatusServiceUnavailable, "plugin management not configured", "")
	case errors.Is(err, ErrPluginNotFound):
		httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
	case errors.Is(err, ErrInstanceNotFound):
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
	case errors.Is(err, ErrAlreadyInactive):
		httputil.WriteError(w, http.StatusConflict, "instance is already deactivated", "")
	case errors.Is(err, ErrRefetchFailed):
		httputil.WriteError(w, http.StatusInternalServerError, "state transition succeeded but re-fetch failed", "")
	default:
		var termErr TerminalStateError
		if errors.As(err, &termErr) {
			httputil.WriteError(w, http.StatusConflict,
				"cannot deactivate instance in terminal state",
				fmt.Sprintf("current state: %s", termErr.State))
			return
		}
		var inflightErr InflightError
		if errors.As(err, &inflightErr) {
			switch inflightErr.Op {
			case inflightOpDeactivate:
				httputil.WriteError(w, http.StatusConflict,
					"cannot deactivate while tool calls are in progress",
					fmt.Sprintf("%d in-flight calls", inflightErr.Count))
			default: // inflightOpDelete
				httputil.WriteError(w, http.StatusConflict,
					"instance has in-flight tool calls",
					fmt.Sprintf("%d in-flight calls", inflightErr.Count))
			}
			return
		}
		var notInactiveErr NotInactiveError
		if errors.As(err, &notInactiveErr) {
			httputil.WriteError(w, http.StatusConflict,
				"instance is not deactivated",
				fmt.Sprintf("current state: %s", notInactiveErr.State))
			return
		}
		var policyRefErr PolicyRefError
		if errors.As(err, &policyRefErr) {
			httputil.WriteError(w, http.StatusConflict,
				"instance is referenced by policies",
				strings.Join(policyRefErr.Names, ", "))
			return
		}
		var audienceRefErr AudienceRefError
		if errors.As(err, &audienceRefErr) {
			httputil.WriteError(w, http.StatusConflict,
				"instance is referenced by audiences",
				strings.Join(audienceRefErr.Names, ", "))
			return
		}
		var instancesRemainErr InstancesRemainError
		if errors.As(err, &instancesRemainErr) {
			httputil.WriteError(w, http.StatusConflict,
				"all instances must be removed before uninstalling the plugin",
				strings.Join(instancesRemainErr.Names, ", "))
			return
		}
		var internalErr *lifecycleInternalError
		if errors.As(err, &internalErr) {
			httputil.WriteError(w, http.StatusInternalServerError, internalErr.PublicMsg, internalErr.Detail)
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, err.Error(), "")
	}
}

// mapConfigError maps a typed error from InstanceConfig to the EXACT current
// HTTP status code and response body. Every distinct string here was verified
// against the old inline handlers (ERROR SURFACE in plan.md).
func (h *PluginHandler) mapConfigError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInstanceNotFound):
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
	case errors.Is(err, ErrPluginNotFound):
		httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
	case errors.Is(err, ErrPropertyNotFound):
		httputil.WriteError(w, http.StatusNotFound, "property not found in config_schema", "")
	case errors.Is(err, ErrNoSubscriptionSchema):
		httputil.WriteError(w, http.StatusBadRequest, "plugin declares no subscription_schema", "")
	case errors.Is(err, ErrCASConflict):
		httputil.WriteError(w, http.StatusConflict, casConflictMsg, "")
	default:
		var corruptErr CorruptManifestError
		if errors.As(err, &corruptErr) {
			httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", corruptErr.Detail)
			return
		}
		var valErr configValidationError
		if errors.As(err, &valErr) {
			httputil.WriteValidationError(w, http.StatusUnprocessableEntity, "validation failed", "", toErrorIssues(valErr.Issues))
			return
		}
		var sentinelErr SentinelRejectedError
		if errors.As(err, &sentinelErr) {
			if sentinelErr.Single {
				// Per-property endpoint: plain error, exact current message.
				httputil.WriteError(w, http.StatusBadRequest,
					"value '***' is the redaction sentinel; submit the real secret", "")
			} else {
				// Bulk PUT: validation-style response with issues slice.
				issues := toErrorIssues(sentinelErr.Issues)
				httputil.WriteValidationError(w, http.StatusBadRequest, "sentinel value rejected", "", issues)
			}
			return
		}
		var internalErr *configInternalError
		if errors.As(err, &internalErr) {
			httputil.WriteError(w, http.StatusInternalServerError, internalErr.PublicMsg, internalErr.Detail)
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, err.Error(), "")
	}
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

// writeInstanceResponseWithRedaction loads the manifest for pluginID (via a DB
// fetch), derives secret property names, redacts configJSON, and writes a 200
// instanceResponse to w. On any error it writes a 500 and returns false.
// Callers should return immediately when this returns false.
func (h *PluginHandler) writeInstanceResponseWithRedaction(
	ctx context.Context,
	w http.ResponseWriter,
	pluginID string,
	inst db.PluginInstance,
) bool {
	plugin, err := h.q.GetPluginByID(ctx, pluginID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		return false
	}
	return h.writeInstanceResponseWithRedactionForPlugin(w, plugin, inst)
}

// writeInstanceResponseWithRedactionForPlugin is the inner variant that accepts
// an already-fetched db.Plugin row. This lets callers that already hold the
// plugin (e.g. GetInstance) avoid a second DB round-trip and the TOCTOU window
// that would otherwise exist between the fetch and the response write.
func (h *PluginHandler) writeInstanceResponseWithRedactionForPlugin(
	w http.ResponseWriter,
	plugin db.Plugin,
	inst db.PluginInstance,
) bool {
	var m sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &m); parseErr != nil {
		// Fail-closed: we must not return unredacted config (ADR-049, ADR-001).
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", parseErr.Error())
		return false
	}

	secretNames, err := configvalidate.SecretPropertyNames(m.ConfigSchema)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to parse config schema", err.Error())
		return false
	}

	redactedConfig, err := configvalidate.RedactSecrets(inst.ConfigJson, secretNames)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to redact config", err.Error())
		return false
	}

	// Decode the manifest's instance-level config_schema into a plain interface{}
	// so the frontend ConfigTab can render the correct fields. The schema is
	// metadata and returned verbatim — it is never redacted (ADR-049). A nil node
	// or a decode error yields a nil field rather than a request failure, mirroring
	// the listing endpoint's decode at plugin_instance_handler.go:117-123.
	var configSchema interface{}
	if m.ConfigSchema != nil {
		if decodeErr := m.ConfigSchema.Decode(&configSchema); decodeErr != nil {
			// Non-fatal: schema is non-load-bearing for security; continue without it.
			configSchema = nil
		}
	}

	httputil.WriteJSON(w, http.StatusOK, instanceResponse{
		ID:                    inst.ID,
		PluginID:              inst.PluginID,
		InstanceName:          inst.InstanceName,
		State:                 inst.HealthState,
		Detail:                inst.HealthDetail,
		Version:               inst.Version,
		UpdatedAt:             inst.UpdatedAt,
		SubscriptionScopeJson: inst.SubscriptionScopeJson,
		ConfigJson:            redactedConfig,
		ConfigSchema:          configSchema,
	})
	return true
}

// DeactivateInstance handles POST /api/v1/admin/plugins/{id}/instances/{iid}/deactivate.
//
// Soft deactivation: stops the subprocess and trigger stream, transitions the
// health state to inactive, and refuses new tool calls. The DB row is preserved
// so the instance can be reactivated. In-flight runs at the time of the call
// continue to completion under their existing generation.
//
// Status codes:
//   - 200 — deactivated; body is the updated instanceResponse
//   - 404 — plugin or instance not found (also returned on plugin/instance mismatch)
//   - 409 — already inactive, terminal state, or in-flight calls present
//   - 500 — DB or subprocess error
func (h *PluginHandler) DeactivateInstance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	updated, err := h.lifecycle.Deactivate(ctx, pluginID, instanceID)
	if err != nil {
		h.mapLifecycleError(w, err)
		return
	}
	h.writeInstanceResponseWithRedaction(ctx, w, pluginID, updated)
}

// ActivateInstance handles POST /api/v1/admin/plugins/{id}/instances/{iid}/activate.
//
// Re-activates a previously deactivated instance: transitions the health state
// to unhealthy (subprocess not yet running) and spawns the subprocess. Once the
// subprocess completes the handshake, the process health callback transitions
// the state to healthy.
//
// Status codes:
//   - 200 — activation initiated; body is the updated instanceResponse
//   - 404 — plugin or instance not found (also returned on plugin/instance mismatch)
//   - 409 — instance is not currently inactive
//   - 500 — DB or subprocess error
func (h *PluginHandler) ActivateInstance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	updated, err := h.lifecycle.Activate(ctx, pluginID, instanceID)
	if err != nil {
		h.mapLifecycleError(w, err)
		return
	}
	h.writeInstanceResponseWithRedaction(ctx, w, pluginID, updated)
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
//   - 409  — CAS conflict, OR bundle verified but rejected (pinned-key mismatch,
//     material manifest change, or downgrade); see audit log
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
		case strings.Contains(msg, "install rejected:"):
			// Bundle verified but refused for a trust/policy reason (pinned-key
			// mismatch, material manifest change, or downgrade). The audit event is
			// already recorded; surface 409 so the caller knows it was NOT applied,
			// instead of the previous misleading 201 with the unchanged row.
			httputil.WriteError(w, http.StatusConflict, msg, "")
		case strings.Contains(msg, "extract tarball"):
			// Tarball-format failures: bad gzip header, file-count limit,
			// disallowed symlinks, unsupported entry types, uncompressed size cap.
			httputil.WriteError(w, http.StatusBadRequest, "tarball extraction failed", msg)
		case strings.Contains(msg, "resolve bundle root"):
			// Layout failure: manifest.yaml missing both at the tarball root
			// and under a single top-level directory.
			httputil.WriteError(w, http.StatusBadRequest, "invalid bundle layout", msg)
		case strings.Contains(msg, "parse manifest"):
			// Order matters: "parse manifest" is wrapped inside "read manifest
			// from %q: %w" so this case must come first.
			httputil.WriteError(w, http.StatusBadRequest, "manifest.yaml is not valid YAML", msg)
		case strings.Contains(msg, "read manifest"):
			httputil.WriteError(w, http.StatusBadRequest, "manifest.yaml missing or unreadable", msg)
		case strings.Contains(msg, "manifest.name"):
			// manifest.name missing, empty, or carries path separators (security guard).
			httputil.WriteError(w, http.StatusBadRequest, "invalid manifest.name", msg)
		case strings.Contains(msg, "manifest.version"):
			httputil.WriteError(w, http.StatusBadRequest, "invalid manifest.version", msg)
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

	plugin, err := h.q.GetPluginByID(ctx, pluginID)
	if err != nil {
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

	// Seed the credential blob with the manifest's declared strategy + endpoints
	// (#572). This records the Strategy at creation time so any flow that reads
	// the credential structure before the first authorize (e.g. an OAuth instance
	// that hasn't run /oauth/begin yet) sees a well-formed blob rather than NULL.
	// Best-effort: the row already exists; a seed failure is logged and the
	// operator can still set credentials via the credentials API. expectedVersion
	// is 0 — CreatePluginInstance inserts at version 0 (ADR-038 CAS).
	//
	// A successful seed performs a CAS write that bumps the row to version 1. We
	// reflect that in the returned version so a caller can immediately issue a
	// CAS-guarded follow-up write without a spurious conflict.
	respVersion := inst.Version
	if h.seedInstanceCredentials(ctx, instanceID, plugin.ManifestSnapshot) {
		respVersion++
	}

	httputil.WriteCreated(w,
		"/api/v1/admin/plugins/"+pluginID+"/instances/"+instanceID,
		createInstanceResponse{
			ID:           inst.ID,
			PluginID:     inst.PluginID,
			InstanceName: inst.InstanceName,
			HealthState:  inst.HealthState,
			HealthDetail: inst.HealthDetail,
			Version:      respVersion,
			CreatedAt:    inst.CreatedAt,
			UpdatedAt:    inst.UpdatedAt,
		})

	// Spawn the subprocess immediately after the response is committed.
	// Using context.Background() rather than r.Context() because the HTTP
	// WriteTimeout and client-disconnect cancellation apply to r.Context() —
	// a cancelled context would silently prevent the instance from starting.
	// This matches the OnInstalled hook in main.go, which also uses a
	// detached context. Failures are logged and surfaced via the health-state
	// path; a spawn failure does not need to change what the caller already received.
	if h.processManager != nil {
		if err := h.processManager.StartByPluginID(context.Background(), pluginID); err != nil {
			slog.WarnContext(context.Background(), "post-create spawn failed", "plugin_id", pluginID, "err", err)
		}
	}
}

// seedInstanceCredentials initializes the credential blob for a newly created
// instance from the manifest's declared auth strategy + OAuth endpoint defaults
// (#572). It is a no-op when no seeder is wired (e.g. no encryption key), when
// the manifest snapshot cannot be parsed, or when the strategy is unrecognised
// (BuildSeedCredentials returns ok=false for an empty/unknown strategy — a
// manifest without an auth block is silently skipped).
//
// All failures are best-effort: the instance row already exists, so a seed
// failure is logged rather than surfaced to the caller. Non-OAuth strategies
// get a blob carrying only the Strategy; OAuth strategies additionally carry the
// manifest endpoint defaults.
//
// Returns true only when a credential blob was actually written (which bumps the
// row's CAS version); the caller uses this to report the post-seed version.
func (h *PluginHandler) seedInstanceCredentials(ctx context.Context, instanceID, manifestSnapshot string) bool {
	if h.credSeeder == nil {
		return false
	}

	var m sdkmanifest.Manifest
	if err := sdkmanifest.Unmarshal([]byte(manifestSnapshot), &m); err != nil {
		slog.WarnContext(ctx, "credential seed: corrupt manifest snapshot; skipping",
			"instance_id", instanceID, "err", err)
		return false
	}

	seed, ok := oauth.BuildSeedCredentials(m.Auth, m.Auth.OAuthDefaults)
	if !ok {
		// Unknown/empty strategy — nothing to seed.
		return false
	}

	if err := h.credSeeder.SaveCredentials(ctx, instanceID, seed, 0); err != nil {
		slog.WarnContext(ctx, "credential seed: save failed; instance created without seeded credentials",
			"instance_id", instanceID, "strategy", seed.Strategy, "err", err)
		return false
	}
	return true
}

// DeleteInstance handles DELETE /api/v1/admin/plugins/{id}/instances/{iid}.
//
// Validates that neither policies nor audience entries reference the instance,
// stops the subprocess (best-effort), then removes pending requests, OAuth
// nonces, and the instance row in a single transaction. Emits an audit event
// on success.
//
// Status codes:
//   - 204 — deleted
//   - 404 — plugin or instance not found (also returned on plugin/instance mismatch)
//   - 409 — audience or policy references still exist
//   - 503 — plugin store not configured (store == nil)
//   - 500 — DB or unexpected error
func (h *PluginHandler) DeleteInstance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	if err := h.lifecycle.Delete(ctx, pluginID, instanceID); err != nil {
		h.mapLifecycleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Uninstall handles DELETE /api/v1/admin/plugins/{id}.
//
// Validates that no instances remain, stops all subprocesses (best-effort),
// removes pending requests and OAuth nonces for every instance in a transaction,
// then removes the plugin row (which cascades to plugin_instances). After the
// commit, removes the plugin binary directory from disk (best-effort,
// containment-checked). Emits an audit event on success.
//
// Status codes:
//   - 204 — uninstalled
//   - 404 — plugin not found
//   - 409 — instances still exist
//   - 503 — plugin store not configured (store == nil)
//   - 500 — DB or unexpected error
func (h *PluginHandler) Uninstall(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")

	if err := h.lifecycle.Uninstall(ctx, pluginID); err != nil {
		h.mapLifecycleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetPluginSBOM handles GET /api/v1/admin/plugins/{id}/sbom.
//
// Serves the raw SBOM file declared in the plugin's manifest. The SBOM path is
// resolved relative to the installed bundle directory (filepath.Dir of
// binary_path). Returns 404 when:
//   - the plugin does not exist
//   - binary_path is nil (bundle not on disk)
//   - the manifest declares no sbom field
//   - the resolved path escapes the bundle directory
//   - the file does not exist on disk
//
// Content-Type is application/vnd.cyclonedx+json for .json/.cdx.json files;
// text/plain for everything else (acceptance criteria fallback).
func (h *PluginHandler) GetPluginSBOM(w http.ResponseWriter, r *http.Request) {
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

	if plugin.BinaryPath == nil {
		httputil.WriteError(w, http.StatusNotFound, "SBOM not available: plugin bundle not on disk", "")
		return
	}

	var m sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &m); parseErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", parseErr.Error())
		return
	}

	if m.SBOM == "" {
		httputil.WriteError(w, http.StatusNotFound, "plugin has no SBOM declared", "")
		return
	}

	bundleDir := filepath.Dir(*plugin.BinaryPath)
	sbomPath := filepath.Join(bundleDir, m.SBOM)

	// Containment check: reject paths that escape the bundle directory
	// (fail-closed per ADR-001, mirrors the Uninstall handler).
	rel, relErr := filepath.Rel(bundleDir, sbomPath)
	if relErr != nil || strings.HasPrefix(rel, "..") {
		httputil.WriteError(w, http.StatusNotFound, "invalid SBOM path", "")
		return
	}

	file, err := os.Open(sbomPath)
	if os.IsNotExist(err) {
		httputil.WriteError(w, http.StatusNotFound, "SBOM file not found on disk", "")
		return
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to open SBOM file", "")
		return
	}
	defer file.Close()

	name := filepath.Base(sbomPath)
	if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".cdx.json") {
		w.Header().Set("Content-Type", "application/vnd.cyclonedx+json")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Header().Set("Content-Disposition", "inline")

	if _, copyErr := io.Copy(w, file); copyErr != nil {
		slog.WarnContext(ctx, "GetPluginSBOM: write failed mid-stream", "err", copyErr)
	}
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

// pluginDetailResponse is the JSON shape returned by GetPluginDetail.
// It includes all information an admin needs to make an approval decision.
type pluginDetailResponse struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Version           string   `json:"version"`
	Description       string   `json:"description,omitempty"`
	Author            string   `json:"author,omitempty"`
	License           string   `json:"license,omitempty"`
	Status            string   `json:"status"`
	Services          []string `json:"services"`
	Tier2Capabilities []string `json:"tier2_capabilities,omitempty"`
	AuthStrategy      string   `json:"auth_strategy"`
	HasOAuthDefaults  bool     `json:"has_oauth_defaults"`
	PubkeyFingerprint string   `json:"pubkey_fingerprint,omitempty"`
	HasSBOM           bool     `json:"has_sbom"`
	CreatedAt         string   `json:"created_at"`
}

// pluginListItemResponse is the JSON shape for a single entry in the ListPlugins response.
type pluginListItemResponse struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Version           string   `json:"version"`
	Description       string   `json:"description,omitempty"`
	Status            string   `json:"status"`
	Services          []string `json:"services"`
	PubkeyFingerprint string   `json:"pubkey_fingerprint,omitempty"`
	HasSBOM           bool     `json:"has_sbom"`
	InstanceCount     int      `json:"instance_count"`
	CreatedAt         string   `json:"created_at"`
}

// manifestServices derives the list of declared service names from a manifest.
// A service is present when its version string is non-empty (e.g. m.Services.Tool != "").
func manifestServices(m *sdkmanifest.Manifest) []string {
	var svcs []string
	if m.Services.Tool != "" {
		svcs = append(svcs, "tool")
	}
	if m.Services.Trigger != "" {
		svcs = append(svcs, "trigger")
	}
	if m.Services.Channel != "" {
		svcs = append(svcs, "channel")
	}
	return svcs
}

// GetPluginDetail handles GET /api/v1/admin/plugins/{id}.
// Returns the full plugin detail needed for the review screen (spec §11.4).
func (h *PluginHandler) GetPluginDetail(w http.ResponseWriter, r *http.Request) {
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

	var m sdkmanifest.Manifest
	if parseErr := sdkmanifest.Unmarshal([]byte(plugin.ManifestSnapshot), &m); parseErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", parseErr.Error())
		return
	}

	// A service is present when its version string is non-empty.
	services := manifestServices(&m)

	// Derive the pubkey fingerprint for visual comparison. Empty string when
	// the plugin is unsigned (empty trusted_pubkey column).
	fingerprint := ""
	if plugin.TrustedPubkey != "" {
		fingerprint = fmt.Sprintf("%x", deriveFingerprint([]byte(plugin.TrustedPubkey)))
	}

	httputil.WriteJSON(w, http.StatusOK, pluginDetailResponse{
		ID:                plugin.ID,
		Name:              plugin.Name,
		Version:           plugin.PluginVersion,
		Description:       m.Description,
		Author:            m.Author,
		License:           m.License,
		Status:            plugin.Status,
		Services:          services,
		Tier2Capabilities: m.Tier2,
		AuthStrategy:      m.Auth.Strategy,
		HasOAuthDefaults:  m.Auth.OAuthDefaults != nil,
		PubkeyFingerprint: fingerprint,
		HasSBOM:           m.SBOM != "",
		CreatedAt:         plugin.CreatedAt,
	})
}

// ListPlugins handles GET /api/v1/admin/plugins.
// Returns all installed plugins with metadata derived from each manifest.
// A service is present when its version string is non-empty.
func (h *PluginHandler) ListPlugins(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	plugins, err := h.q.ListPlugins(ctx)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to list plugins", "")
		return
	}

	items := make([]pluginListItemResponse, 0, len(plugins))
	for _, p := range plugins {
		var m sdkmanifest.Manifest
		if parseErr := sdkmanifest.Unmarshal([]byte(p.ManifestSnapshot), &m); parseErr != nil {
			// Skip unparseable manifests rather than failing the whole list.
			slog.WarnContext(ctx, "list plugins: skipping plugin with corrupt manifest",
				"plugin_id", p.ID, "err", parseErr)
			continue
		}

		services := manifestServices(&m)

		fingerprint := ""
		if p.TrustedPubkey != "" {
			fingerprint = fmt.Sprintf("%x", deriveFingerprint([]byte(p.TrustedPubkey)))
		}

		// Count instances for this plugin. Small N — admin page, homelab-scale.
		instanceList, listErr := h.q.ListPluginInstancesByPlugin(ctx, p.ID)
		instanceCount := 0
		if listErr != nil {
			slog.WarnContext(ctx, "list plugins: failed to count instances",
				"plugin_id", p.ID, "err", listErr)
		} else {
			instanceCount = len(instanceList)
		}

		items = append(items, pluginListItemResponse{
			ID:                p.ID,
			Name:              p.Name,
			Version:           p.PluginVersion,
			Description:       m.Description,
			Status:            p.Status,
			Services:          services,
			PubkeyFingerprint: fingerprint,
			HasSBOM:           m.SBOM != "",
			InstanceCount:     instanceCount,
			CreatedAt:         p.CreatedAt,
		})
	}

	httputil.WriteJSON(w, http.StatusOK, items)
}

// approvePluginResponse is the JSON shape returned by ApprovePlugin on success.
type approvePluginResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// ApprovePlugin handles POST /api/v1/admin/plugins/{id}/approve.
// Transitions the plugin from pending_review to active (CAS-guarded) and emits
// a plugin_review_approved audit event.
func (h *PluginHandler) ApprovePlugin(w http.ResponseWriter, r *http.Request) {
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

	if plugin.Status != "pending_review" {
		httputil.WriteError(w, http.StatusConflict, "plugin is not in pending_review status", plugin.Status)
		return
	}

	nowStr := h.clock().UTC().Format(time.RFC3339Nano)
	rows, err := h.q.UpdatePluginStatus(ctx, db.UpdatePluginStatusParams{
		Status:          "active",
		UpdatedAt:       nowStr,
		ID:              pluginID,
		ExpectedVersion: plugin.Version,
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update plugin status", "")
		return
	}
	if rows == 0 {
		httputil.WriteError(w, http.StatusConflict, "concurrent modification detected; retry", "")
		return
	}

	caller, _ := auth.UserFromContext(ctx)
	actorID := ""
	if caller != nil {
		actorID = caller.ID
	}
	h.writeAuditEvent(ctx, auditReviewApproved, "info", nowStr, map[string]any{
		"plugin_id": pluginID,
		"name":      plugin.Name,
		"version":   plugin.PluginVersion,
		"actor":     actorID,
	})

	// StartByPluginID is a no-op at this point — no instances exist yet. The
	// first instance's subprocess starts when the admin creates it via
	// POST /admin/plugins/{id}/instances.
	if h.processManager != nil {
		if startErr := h.processManager.StartByPluginID(ctx, pluginID); startErr != nil {
			slog.WarnContext(ctx, "approve plugin: StartByPluginID failed (no-op expected)",
				"plugin_id", pluginID, "err", startErr)
		}
	}

	httputil.WriteJSON(w, http.StatusOK, approvePluginResponse{
		ID:      plugin.ID,
		Name:    plugin.Name,
		Version: plugin.PluginVersion,
		Status:  "active",
	})
}

// rejectPluginResponse is the JSON shape returned by RejectPlugin on success.
type rejectPluginResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// RejectPlugin handles POST /api/v1/admin/plugins/{id}/reject.
// Emits a plugin_review_rejected audit event, then deletes the plugin row
// (and its binary directory if present). Only pending_review plugins can be
// rejected — already-active plugins must be uninstalled instead.
func (h *PluginHandler) RejectPlugin(w http.ResponseWriter, r *http.Request) {
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

	if plugin.Status != "pending_review" {
		httputil.WriteError(w, http.StatusConflict, "plugin is not in pending_review status", plugin.Status)
		return
	}

	// Emit the audit event BEFORE deletion so the plugin_id is still valid
	// in the plugins table when the event is recorded.
	nowStr := h.clock().UTC().Format(time.RFC3339Nano)
	caller, _ := auth.UserFromContext(ctx)
	actorID := ""
	if caller != nil {
		actorID = caller.ID
	}
	h.writeAuditEvent(ctx, auditReviewRejected, "info", nowStr, map[string]any{
		"plugin_id": pluginID,
		"name":      plugin.Name,
		"version":   plugin.PluginVersion,
		"actor":     actorID,
	})

	// No transaction needed: pending_review plugins have no instances, so there
	// are no RESTRICT FK rows to clear before deletion. The cascade covers any
	// edge-case orphan rows.
	if _, err := h.q.DeletePlugin(ctx, pluginID); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to delete plugin", "")
		return
	}

	// Best-effort: remove the installed binary directory. Containment-checked
	// (fail-closed per ADR-001): only remove paths strictly within pluginsDir.
	if plugin.BinaryPath != nil && h.pluginsDir != "" {
		dir := filepath.Dir(*plugin.BinaryPath)
		rel, relErr := filepath.Rel(h.pluginsDir, dir)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			slog.WarnContext(ctx, "reject: binary path escapes plugins dir — skipping removal",
				"binary_path", *plugin.BinaryPath,
				"plugins_dir", h.pluginsDir)
		} else {
			if err := os.RemoveAll(dir); err != nil {
				slog.WarnContext(ctx, "reject: remove binary dir failed", "dir", dir, "err", err)
			}
		}
	}

	httputil.WriteJSON(w, http.StatusOK, rejectPluginResponse{
		ID:     plugin.ID,
		Name:   plugin.Name,
		Status: "rejected",
	})
}
