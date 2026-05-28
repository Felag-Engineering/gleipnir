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
	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	pluginmanifest "github.com/felag-engineering/gleipnir/internal/plugin/manifest"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	"github.com/felag-engineering/gleipnir/internal/policy"
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

// PluginHandler handles plugin-related admin endpoints.
type PluginHandler struct {
	q                PluginQuerier
	publisher        event.Publisher
	clock            func() time.Time
	triggerRestarter TriggerRestarter     // may be nil if plugins are disabled
	installer        PluginInstaller      // may be nil if GLEIPNIR_PLUGINS_ENABLED=false
	store            *db.Store            // nil disables DeleteInstance and Uninstall (503)
	pluginsDir       string               // empty disables FS cleanup in Uninstall
	processManager   PluginProcessManager // nil means skip subprocess Stop
	toolUnregistrar  ToolUnregistrar      // nil until #194 wires tools.Registrar
	inflightCounter  InflightCounter      // may be nil; gates deactivate/delete on zero in-flight calls
	rssAggregator    RSSAggregator        // nil when plugins are disabled; GetPluginRSS returns 503
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

// SetStore wires the *db.Store into the handler so DeleteInstance and Uninstall
// can open transactions via store.DB().BeginTx. A nil store disables both
// endpoints (returns 503). Called from main.go unconditionally because *db.Store
// is always available — the transactional delete path needs it regardless of
// whether the plugin subsystem is enabled.
func (h *PluginHandler) SetStore(s *db.Store) {
	h.store = s
}

// SetPluginsDir sets the plugins directory used for FS cleanup during Uninstall.
// An empty string disables binary removal (DB rows are still deleted). Called
// from main.go inside the plugins-enabled block.
func (h *PluginHandler) SetPluginsDir(dir string) {
	h.pluginsDir = dir
}

// SetProcessManager wires the PluginProcessManager so DeleteInstance and
// Uninstall can stop subprocesses before clearing DB rows. A nil manager skips
// subprocess stop (DB-only cleanup, matching the kitchen-sink recovery path).
func (h *PluginHandler) SetProcessManager(pm PluginProcessManager) {
	h.processManager = pm
}

// SetToolUnregistrar wires the ToolUnregistrar for post-delete tool-namespace
// cleanup. Currently not called from main.go — tools.Registrar is not yet in
// the live process path (see TODO at main.go). A nil unregistrar is a no-op.
// When #194 wires tools.New(arbiter, ...), add the one-line wire-up there.
func (h *PluginHandler) SetToolUnregistrar(u ToolUnregistrar) {
	h.toolUnregistrar = u
}

// SetInflightCounter wires the InflightCounter (typically *dispatch.Pool) for
// the in-flight gate in DeactivateInstance and DeleteInstance. A nil counter
// skips the gate (safe default for tests and the GLEIPNIR_PLUGINS_ENABLED=false
// path). Called from main.go inside the plugins-enabled block.
func (h *PluginHandler) SetInflightCounter(ic InflightCounter) {
	h.inflightCounter = ic
}

// SetRSSAggregator wires the RSSAggregator (a main.go adapter wrapping
// *process.RSSSampler) into the handler. A nil aggregator disables
// GetPluginRSS (returns 503). Called from main.go inside the plugins-enabled
// block after the RSSSampler is constructed.
func (h *PluginHandler) SetRSSAggregator(a RSSAggregator) {
	h.rssAggregator = a
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
// Returns 503 when the plugin subsystem is disabled (GLEIPNIR_PLUGINS_ENABLED=false).
func (h *PluginHandler) GetPluginRSS(w http.ResponseWriter, r *http.Request) {
	if h.rssAggregator == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "plugin system is disabled", "")
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

	// Load the manifest to derive the set of secret properties for redaction.
	// A second DB round-trip is consistent with PutInstanceConfig and
	// PutSubscriptionScope, which both load the manifest on every request.
	plugin, err := h.q.GetPluginByID(r.Context(), pluginID)
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
		// Fail-closed: we must not return unredacted config when we cannot
		// determine which fields are secret (ADR-049 §6, ADR-001 posture).
		httputil.WriteError(w, http.StatusInternalServerError, "corrupt manifest snapshot", parseErr.Error())
		return
	}

	secretNames, err := configvalidate.SecretPropertyNames(m.ConfigSchema)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to parse config schema", err.Error())
		return
	}

	redactedConfig, err := configvalidate.RedactSecrets(row.ConfigJson, secretNames)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to redact config", err.Error())
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
		ConfigJson:            redactedConfig,
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
	newlyRequired := pluginmanifest.ConfigSchemaNewlyRequiredFields(&oldManifest, &newManifest)

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
		httputil.WriteError(w, http.StatusConflict, "concurrent modification detected; retry", "")
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

	h.writeAuditEvent(ctx, auditManifestAccepted, "info", nowStr, map[string]any{
		"plugin_id":                pluginID,
		"name":                     plugin.Name,
		"old_version":              pendingRow.OldVersion,
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

	// Ensure the trigger stream is running with the latest scope. Start is
	// a no-op if already supervised; Restart cancels and re-opens so the
	// plugin picks up the new scope. Both are needed because instances
	// created after boot were never supervised by StartAll.
	if h.triggerRestarter != nil {
		h.triggerRestarter.Start(ctx, instanceID)
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
			ConfigJson:            inst.ConfigJson,
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
		ConfigJson:            updated.ConfigJson,
	})
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

	// Derive the set of secret property names for sentinel rejection and
	// response redaction (ADR-049).
	secretNames, err := configvalidate.SecretPropertyNames(m.ConfigSchema)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to parse config schema", err.Error())
		return
	}

	// ForInstanceConfig returns a validator that accepts anything when ConfigSchema is nil
	// (per Q7 in the plan). Do NOT early-return on nil schema — it is valid.
	validator, err := configvalidate.ForInstanceConfig(&m)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to build config validator", err.Error())
		return
	}
	cfg := req.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	fieldErrs, err := validator.Validate(cfg)
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

	// Reject any secret field whose submitted value is the redaction sentinel.
	// This prevents the round-trip clobber: UI reads "***", user hits Save,
	// real secret would be overwritten with the sentinel (ADR-049 §5).
	if offenders := configvalidate.ContainsRedactionSentinel(cfg, secretNames); len(offenders) > 0 {
		issues := make([]httputil.ErrorIssue, 0, len(offenders))
		for _, field := range offenders {
			issues = append(issues, httputil.ErrorIssue{
				Field:   field,
				Message: "value '***' is the redaction sentinel; submit the real secret or omit the field to leave it unchanged",
			})
		}
		httputil.WriteValidationError(w, http.StatusBadRequest, "sentinel value rejected", "", issues)
		return
	}

	configBytes, err := json.Marshal(cfg)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to marshal config", err.Error())
		return
	}

	nowStr := h.clock().UTC().Format(time.RFC3339)
	rows, err := h.q.UpdatePluginInstanceConfig(ctx, db.UpdatePluginInstanceConfigParams{
		ConfigJson:      string(configBytes),
		UpdatedAt:       nowStr,
		ID:              instanceID,
		ExpectedVersion: *req.ExpectedVersion,
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update instance config", "")
		return
	}
	if rows == 0 {
		httputil.WriteError(w, http.StatusConflict, "version conflict", "")
		return
	}

	// Compute the redacted form of the written config once. Both the re-fetch
	// success branch and the fallback synthesized branch use this value so
	// neither path can emit raw secret JSON (ADR-049 §7, §6).
	redactedWrittenConfig, err := configvalidate.RedactSecrets(string(configBytes), secretNames)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to redact config", err.Error())
		return
	}

	// Re-fetch to return the updated row.
	updated, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		// The write succeeded; fall back to a synthesised response.
		// Use the pre-computed redacted config — never the raw written bytes.
		httputil.WriteJSON(w, http.StatusOK, instanceResponse{
			ID:                    instanceID,
			PluginID:              pluginID,
			InstanceName:          inst.InstanceName,
			State:                 inst.HealthState,
			Detail:                inst.HealthDetail,
			Version:               inst.Version + 1,
			UpdatedAt:             nowStr,
			SubscriptionScopeJson: inst.SubscriptionScopeJson,
			ConfigJson:            redactedWrittenConfig,
		})
		return
	}

	// Advance the readiness detail (config_missing → credentials_missing → "")
	// so the admin UI tells the operator what's still missing. Best-effort —
	// the config write has already committed; advanceInstanceReadiness logs
	// failures internally.
	h.advanceInstanceReadiness(ctx, updated, &m)
	if refreshed, refetchErr := h.q.GetPluginInstanceByID(ctx, instanceID); refetchErr == nil {
		updated = refreshed
	}

	// Redact the re-fetched config before returning.
	redactedFetchedConfig, err := configvalidate.RedactSecrets(updated.ConfigJson, secretNames)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to redact config", err.Error())
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
		ConfigJson:            redactedFetchedConfig,
	})
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

	// Validate that the property name exists in the manifest's ConfigSchema.
	// A property not in the schema cannot be set via this endpoint; use the
	// bulk PUT for schema-less plugins.
	if !propertyExistsInSchema(m.ConfigSchema, property) {
		httputil.WriteError(w, http.StatusNotFound, "property not found in config_schema", "")
		return
	}

	// Derive secret names for sentinel rejection and redaction (ADR-049).
	secretNames, err := configvalidate.SecretPropertyNames(m.ConfigSchema)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to parse config schema", err.Error())
		return
	}

	// Reject the redaction sentinel — the caller must supply the real value.
	if strVal, isStr := req.Value.(string); isStr && strVal == configvalidate.RedactionSentinel {
		httputil.WriteError(w, http.StatusBadRequest,
			"value '***' is the redaction sentinel; submit the real secret",
			"")
		return
	}

	// Merge the new value into the existing config.
	var cfg map[string]any
	if inst.ConfigJson != "" && inst.ConfigJson != "{}" {
		if err := json.Unmarshal([]byte(inst.ConfigJson), &cfg); err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to parse existing config", err.Error())
			return
		}
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg[property] = req.Value

	// Validate the full merged config against the schema.
	validator, err := configvalidate.ForInstanceConfig(&m)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to build config validator", err.Error())
		return
	}
	fieldErrs, err := validator.Validate(cfg)
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

	configBytes, err := json.Marshal(cfg)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to marshal config", err.Error())
		return
	}

	nowStr := h.clock().UTC().Format(time.RFC3339)
	rows, err := h.q.UpdatePluginInstanceConfig(ctx, db.UpdatePluginInstanceConfigParams{
		ConfigJson:      string(configBytes),
		UpdatedAt:       nowStr,
		ID:              instanceID,
		ExpectedVersion: *req.ExpectedVersion,
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update instance config", "")
		return
	}
	if rows == 0 {
		httputil.WriteError(w, http.StatusConflict, "version conflict", "")
		return
	}

	// Pre-compute the redacted form of the written config. Both the re-fetch
	// success branch and the fallback synthesized branch use this value so
	// neither path can emit raw secret JSON (ADR-049 §7).
	redactedWrittenConfig, err := configvalidate.RedactSecrets(string(configBytes), secretNames)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to redact config", err.Error())
		return
	}

	// Re-fetch to return the updated row.
	updated, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		// The write succeeded; fall back to a synthesised response.
		// Use the pre-computed redacted config — never the raw written bytes.
		httputil.WriteJSON(w, http.StatusOK, instanceResponse{
			ID:                    instanceID,
			PluginID:              pluginID,
			InstanceName:          inst.InstanceName,
			State:                 inst.HealthState,
			Detail:                inst.HealthDetail,
			Version:               inst.Version + 1,
			UpdatedAt:             nowStr,
			SubscriptionScopeJson: inst.SubscriptionScopeJson,
			ConfigJson:            redactedWrittenConfig,
		})
		return
	}

	// Redact the re-fetched config before returning.
	redactedFetchedConfig, err := configvalidate.RedactSecrets(updated.ConfigJson, secretNames)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to redact config", err.Error())
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
		ConfigJson:            redactedFetchedConfig,
	})
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

// advanceInstanceReadiness re-evaluates the instance's readiness detail and
// writes it back through SetHealthState if it has changed. Best-effort — any
// failure is logged but not returned to the caller because the underlying
// config/credentials write has already succeeded. Bypasses the update entirely
// when the instance is not currently in unhealthy (e.g. already healthy, or
// pending some other admin action).
func (h *PluginHandler) advanceInstanceReadiness(ctx context.Context, inst db.PluginInstance, m *sdkmanifest.Manifest) {
	if model.PluginHealthState(inst.HealthState) != model.PluginHealthStateUnhealthy {
		return
	}
	credentialsPresent := inst.CredentialsEncrypted != nil && *inst.CredentialsEncrypted != ""
	wanted := computeInstanceReadinessDetail(m, inst.ConfigJson, credentialsPresent)
	var currentDetail string
	if inst.HealthDetail != nil {
		currentDetail = *inst.HealthDetail
	}
	if wanted == currentDetail {
		return
	}
	err := pluginstate.SetHealthState(ctx, h.q, h.publisher, inst.ID, pluginstate.OriginHost,
		model.PluginHealthStateUnhealthy, wanted)
	if err != nil && !errors.Is(err, pluginstate.ErrTransitionConflict) {
		slog.WarnContext(ctx, "advanceInstanceReadiness: set health detail failed",
			"instance_id", inst.ID, "from", currentDetail, "to", wanted, "err", err)
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

// writeInstanceResponseWithRedaction loads the manifest for pluginID, derives
// secret property names, redacts configJSON, and writes a 200 instanceResponse
// to w. On manifest parse or redaction failure it writes a 500 and returns false.
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

	if _, err := h.q.GetPluginByID(ctx, pluginID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		}
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
	if inst.PluginID != pluginID {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}

	current := model.PluginHealthState(inst.HealthState)

	if current == model.PluginHealthStateInactive {
		httputil.WriteError(w, http.StatusConflict, "instance is already deactivated", "")
		return
	}

	// Terminal states cannot transition to inactive (state machine enforces this,
	// but we surface a clear error here before hitting ErrIllegalTransition).
	if current == model.PluginHealthStateSignatureInvalid || current == model.PluginHealthStateVerificationError {
		httputil.WriteError(w, http.StatusConflict,
			"cannot deactivate instance in terminal state",
			fmt.Sprintf("current state: %s", current))
		return
	}

	// Gate on zero in-flight tool calls. TOCTOU is acceptable here: a new call
	// arriving after this check will fail at the dispatch layer once the subprocess
	// is stopped. The gate prevents disrupting calls that are actively running.
	if h.inflightCounter != nil {
		count := h.inflightCounter.InflightCountByInstance(inst.InstanceName)
		if count > 0 {
			httputil.WriteError(w, http.StatusConflict,
				"cannot deactivate while tool calls are in progress",
				fmt.Sprintf("%d in-flight calls", count))
			return
		}
	}

	if err := pluginstate.SetHealthState(ctx, h.q, h.publisher, instanceID, pluginstate.OriginHost,
		model.PluginHealthStateInactive, "deactivated by admin"); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to set health state", err.Error())
		return
	}

	// Best-effort subprocess stop (5s deadline — same pattern as DeleteInstance).
	if h.processManager != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := h.processManager.Stop(stopCtx, instanceID); err != nil {
			slog.WarnContext(ctx, "deactivate instance: subprocess stop failed",
				"instance_id", instanceID, "err", err)
		}
		cancel()
	}

	// Best-effort trigger stream stop.
	if h.triggerRestarter != nil {
		h.triggerRestarter.Stop(instanceID)
	}

	nowStr := h.clock().UTC().Format(time.RFC3339Nano)
	h.writeAuditEvent(ctx, auditInstanceDeactivated, "info", nowStr, map[string]any{
		"plugin_id":     pluginID,
		"instance_id":   instanceID,
		"instance_name": inst.InstanceName,
	})

	// Re-fetch to return the current row after the state transition.
	updated, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		// State write succeeded but the re-fetch failed. Return 500 — the client
		// can re-fetch via GET. We must not synthesize a response here because
		// config_json may contain unredacted secrets (ADR-049).
		slog.WarnContext(ctx, "deactivate instance: re-fetch after transition failed",
			"instance_id", instanceID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "state transition succeeded but re-fetch failed", "")
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

	if _, err := h.q.GetPluginByID(ctx, pluginID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		}
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
	if inst.PluginID != pluginID {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}

	if model.PluginHealthState(inst.HealthState) != model.PluginHealthStateInactive {
		httputil.WriteError(w, http.StatusConflict,
			"instance is not deactivated",
			fmt.Sprintf("current state: %s", inst.HealthState))
		return
	}

	// Transition to unhealthy first. The subprocess handshake will drive the
	// state to healthy once the process comes up.
	if err := pluginstate.SetHealthState(ctx, h.q, h.publisher, instanceID, pluginstate.OriginHost,
		model.PluginHealthStateUnhealthy, "reactivated by admin"); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to set health state", err.Error())
		return
	}

	// Best-effort subprocess spawn. StartByPluginID spawns all instances for the
	// plugin; siblings already running return "already running" (swallowed internally).
	if h.processManager != nil {
		if err := h.processManager.StartByPluginID(context.Background(), pluginID); err != nil {
			slog.WarnContext(ctx, "activate instance: spawn failed", "plugin_id", pluginID, "err", err)
		}
	}

	// Best-effort trigger stream start.
	if h.triggerRestarter != nil {
		h.triggerRestarter.Start(ctx, instanceID)
	}

	nowStr := h.clock().UTC().Format(time.RFC3339Nano)
	h.writeAuditEvent(ctx, auditInstanceActivated, "info", nowStr, map[string]any{
		"plugin_id":     pluginID,
		"instance_id":   instanceID,
		"instance_name": inst.InstanceName,
	})

	// Re-fetch to return the current row after the state transition.
	updated, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		// State write succeeded but the re-fetch failed. Return 500 — the client
		// can re-fetch via GET. We must not synthesize a response here because
		// config_json may contain unredacted secrets (ADR-049).
		slog.WarnContext(ctx, "activate instance: re-fetch after transition failed",
			"instance_id", instanceID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "state transition succeeded but re-fetch failed", "")
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
//   - 503 — plugin store not configured (SetStore not called)
//   - 500 — DB or unexpected error
func (h *PluginHandler) DeleteInstance(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "plugin management not configured", "")
		return
	}
	ctx := r.Context()
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")

	// Verify the instance exists and belongs to the given plugin.
	if _, err := h.q.GetPluginByID(ctx, pluginID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "plugin not found", "")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, "failed to get plugin", "")
		}
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

	// Policy reference guard: refuse deletion if any policy still references
	// this instance's tools or subscribed trigger. TOCTOU with concurrent policy
	// create is acceptable — mirrors the audience-delete precedent.
	policyRefs, err := policy.ScanPolicyToolRefsForInstance(ctx, h.q, inst.InstanceName)
	if err != nil {
		slog.ErrorContext(ctx, "delete instance: scan policy refs", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if len(policyRefs) > 0 {
		names := make([]string, len(policyRefs))
		for i, ref := range policyRefs {
			names[i] = ref.Name
		}
		httputil.WriteError(w, http.StatusConflict, "instance is referenced by policies",
			strings.Join(names, ", "))
		return
	}

	// Audience reference guard: refuse deletion if any audience entry references
	// this instance (the operator must update audiences first).
	audienceEntries, err := h.q.ListAudienceEntriesByInstance(ctx, instanceID)
	if err != nil {
		slog.ErrorContext(ctx, "delete instance: list audience entries", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if len(audienceEntries) > 0 {
		// Deduplicate audience names — one instance can appear in multiple entries
		// of the same audience.
		seen := make(map[string]bool)
		var audienceNames []string
		for _, ae := range audienceEntries {
			if !seen[ae.AudienceName] {
				seen[ae.AudienceName] = true
				audienceNames = append(audienceNames, ae.AudienceName)
			}
		}
		httputil.WriteError(w, http.StatusConflict,
			"instance is referenced by audiences", strings.Join(audienceNames, ", "))
		return
	}

	// In-flight gate: refuse deletion if tool calls are actively dispatched to
	// this instance. TOCTOU is acceptable — mirrors the policy/audience guards.
	if h.inflightCounter != nil {
		count := h.inflightCounter.InflightCountByInstance(inst.InstanceName)
		if count > 0 {
			httputil.WriteError(w, http.StatusConflict,
				"instance has in-flight tool calls",
				fmt.Sprintf("%d in-flight calls", count))
			return
		}
	}

	// Best-effort subprocess stop before DB cleanup so in-flight RPCs don't hit
	// a half-deleted instance. A wedged subprocess must not block DB cleanup.
	if h.processManager != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := h.processManager.Stop(stopCtx, instanceID); err != nil {
			slog.WarnContext(ctx, "delete instance: subprocess stop failed",
				"instance_id", instanceID, "err", err)
		}
		cancel()
	}

	// Transactional delete in FK-safe order:
	//   1. plugin_pending_requests (RESTRICT FK — must clear first)
	//   2. plugin_oauth_nonces (instance_id FK)
	//   3. plugin_instances row (audit events use SET NULL so history survives)
	tx, err := h.store.DB().BeginTx(ctx, nil)
	if err != nil {
		slog.ErrorContext(ctx, "delete instance: begin tx", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.ErrorContext(ctx, "delete instance: rollback failed", "err", rbErr)
		}
	}()

	q := db.New(tx)
	if err := q.DeletePluginPendingRequestsByInstance(ctx, instanceID); err != nil {
		slog.ErrorContext(ctx, "delete instance: clear pending requests", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if err := q.DeletePluginOAuthNoncesByInstance(ctx, instanceID); err != nil {
		slog.ErrorContext(ctx, "delete instance: clear oauth nonces", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if _, err := q.DeletePluginInstance(ctx, instanceID); err != nil {
		slog.ErrorContext(ctx, "delete instance: delete row", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if err := tx.Commit(); err != nil {
		slog.ErrorContext(ctx, "delete instance: commit", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	// Best-effort tool-namespace release. Nil-safe: not yet wired in main.go
	// (see SetToolUnregistrar godoc and TODO at main.go). #194 follow-up will
	// add the one-line wire-up.
	if h.toolUnregistrar != nil {
		h.toolUnregistrar.UnregisterInstance(ctx, inst.InstanceName)
	}

	nowStr := h.clock().UTC().Format(time.RFC3339Nano)
	h.writeAuditEvent(ctx, auditInstanceDeleted, "info", nowStr, map[string]any{
		"plugin_id":     pluginID,
		"instance_id":   instanceID,
		"instance_name": inst.InstanceName,
	})

	w.WriteHeader(http.StatusNoContent)
}

// Uninstall handles DELETE /api/v1/admin/plugins/{id}.
//
// Validates that no policies or audience entries reference any of the plugin's
// instances, stops all subprocesses (best-effort), removes pending requests and
// OAuth nonces for every instance in a transaction, then removes the plugin row
// (which cascades to plugin_instances). After the commit, removes the plugin
// binary directory from disk (best-effort, containment-checked). Emits an audit
// event on success.
//
// Status codes:
//   - 204 — uninstalled
//   - 404 — plugin not found
//   - 409 — audience or policy references still exist
//   - 503 — plugin store not configured (SetStore not called)
//   - 500 — DB or unexpected error
func (h *PluginHandler) Uninstall(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "plugin management not configured", "")
		return
	}
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

	// Collect all instances. Per the issue (#243): removing the plugin (binary +
	// manifest) requires all instances to be removed first — per-instance
	// DeleteInstance enforces the policy/audience/in-flight gates individually.
	// This simpler gate avoids duplicating those checks here.
	instances, err := h.q.ListPluginInstancesByPlugin(ctx, pluginID)
	if err != nil {
		slog.ErrorContext(ctx, "uninstall: list instances", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	if len(instances) > 0 {
		names := make([]string, len(instances))
		for i, inst := range instances {
			names[i] = inst.InstanceName
		}
		httputil.WriteError(w, http.StatusConflict,
			"all instances must be removed before uninstalling the plugin",
			strings.Join(names, ", "))
		return
	}

	// Best-effort: stop all running subprocesses before clearing rows.
	if h.processManager != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := h.processManager.StopByPluginID(stopCtx, pluginID); err != nil {
			slog.WarnContext(ctx, "uninstall: stop subprocesses failed",
				"plugin_id", pluginID, "err", err)
		}
		cancel()
	}

	// Transactional delete:
	//   1. For each instance: clear pending requests + OAuth nonces (RESTRICT FKs).
	//   2. DeletePlugin — cascades to plugin_instances via ON DELETE CASCADE.
	tx, err := h.store.DB().BeginTx(ctx, nil)
	if err != nil {
		slog.ErrorContext(ctx, "uninstall: begin tx", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.ErrorContext(ctx, "uninstall: rollback failed", "err", rbErr)
		}
	}()

	q := db.New(tx)
	for _, inst := range instances {
		if err := q.DeletePluginPendingRequestsByInstance(ctx, inst.ID); err != nil {
			slog.ErrorContext(ctx, "uninstall: clear pending requests",
				"instance_id", inst.ID, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
			return
		}
		if err := q.DeletePluginOAuthNoncesByInstance(ctx, inst.ID); err != nil {
			slog.ErrorContext(ctx, "uninstall: clear oauth nonces",
				"instance_id", inst.ID, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
			return
		}
	}
	if _, err := q.DeletePlugin(ctx, pluginID); err != nil {
		slog.ErrorContext(ctx, "uninstall: delete plugin", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if err := tx.Commit(); err != nil {
		slog.ErrorContext(ctx, "uninstall: commit", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	// Best-effort tool-namespace release for each instance. Nil-safe.
	if h.toolUnregistrar != nil {
		for _, inst := range instances {
			h.toolUnregistrar.UnregisterInstance(ctx, inst.InstanceName)
		}
	}

	// Post-commit: remove the binary directory from disk.
	// Containment check (fail-closed per ADR-001): only remove paths that are
	// strictly within pluginsDir. A path that starts with ".." after filepath.Rel
	// is a traversal attempt — we refuse and log a warning.
	//
	// Note: if the fsnotify watcher is mid-publishBundle at the moment of
	// RemoveAll, the newly extracted bundle may land with a nil binary_path.
	// In that case the operator may need to re-install via the upload endpoint.
	binaryPathRemoved := false
	if plugin.BinaryPath != nil && h.pluginsDir != "" {
		dir := filepath.Dir(*plugin.BinaryPath)
		rel, relErr := filepath.Rel(h.pluginsDir, dir)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			slog.WarnContext(ctx, "uninstall: binary path escapes plugins dir — skipping removal",
				"binary_path", *plugin.BinaryPath,
				"plugins_dir", h.pluginsDir)
		} else {
			if err := os.RemoveAll(dir); err != nil {
				slog.WarnContext(ctx, "uninstall: remove binary dir failed",
					"dir", dir, "err", err)
			} else {
				binaryPathRemoved = true
			}
		}
	}

	nowStr := h.clock().UTC().Format(time.RFC3339Nano)
	h.writeAuditEvent(ctx, auditPluginUninstalled, "info", nowStr, map[string]any{
		"plugin_id":           pluginID,
		"plugin_name":         plugin.Name,
		"plugin_version":      plugin.PluginVersion,
		"instance_count":      len(instances),
		"binary_path_removed": binaryPathRemoved,
	})

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
