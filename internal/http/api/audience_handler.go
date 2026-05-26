package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/model"
	audiencepkg "github.com/felag-engineering/gleipnir/internal/plugin/audience"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
	"github.com/felag-engineering/gleipnir/internal/policy"
)

// AudienceHandler serves audience CRUD endpoints under /api/v1/admin/audiences.
type AudienceHandler struct {
	store *db.Store
	snap  *configvalidate.Snapshotter
	clock func() time.Time
}

// NewAudienceHandler creates an AudienceHandler. clock may be nil, in which
// case time.Now is used.
func NewAudienceHandler(store *db.Store, snap *configvalidate.Snapshotter, clock func() time.Time) *AudienceHandler {
	if clock == nil {
		clock = time.Now
	}
	return &AudienceHandler{store: store, snap: snap, clock: clock}
}

// --- DTOs ---

type audienceListItemDTO struct {
	ID                      string `json:"id"`
	Name                    string `json:"name"`
	EntryCount              int    `json:"entry_count"`
	ReferencedByPolicyCount int    `json:"referenced_by_policy_count"`
	HasInFlightRuns         bool   `json:"has_in_flight_runs"`
	DisableInAppFallback    bool   `json:"disable_in_app_fallback"`
	Version                 int64  `json:"version"`
	CreatedAt               string `json:"created_at"`
	UpdatedAt               string `json:"updated_at"`
}

type audienceEntryDTO struct {
	ID               string          `json:"id"`
	PluginInstanceID string          `json:"plugin_instance_id"`
	Position         int64           `json:"position"`
	Notify           bool            `json:"notify"`
	Request          bool            `json:"request"`
	Config           json.RawMessage `json:"config"`
	Auto             bool            `json:"auto,omitempty"`
}

type audienceDTO struct {
	ID                   string             `json:"id"`
	Name                 string             `json:"name"`
	DisableInAppFallback bool               `json:"disable_in_app_fallback"`
	Version              int64              `json:"version"`
	CreatedAt            string             `json:"created_at"`
	UpdatedAt            string             `json:"updated_at"`
	Entries              []audienceEntryDTO `json:"entries"`
}

type audienceEntrySaveRequest struct {
	PluginInstanceID string          `json:"plugin_instance_id"`
	Notify           bool            `json:"notify"`
	Request          bool            `json:"request"`
	Config           json.RawMessage `json:"config"`
}

type audienceSaveRequest struct {
	Name                 string                     `json:"name"`
	DisableInAppFallback bool                       `json:"disable_in_app_fallback"`
	Entries              []audienceEntrySaveRequest `json:"entries"`
	ExpectedVersion      *int64                     `json:"expected_version,omitempty"` // PUT only
}

type policyRefDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type inFlightRunDTO struct {
	ID       string `json:"id"`
	PolicyID string `json:"policy_id"`
	Status   string `json:"status"`
}

type audienceReferencesDTO struct {
	Policies     []policyRefDTO   `json:"policies"`
	InFlightRuns []inFlightRunDTO `json:"in_flight_runs"`
}

// pluginEventExampleDTO is a single named example payload for a plugin event kind.
// Used by the "Test against sample" feature in the policy editor (spec §7.5).
type pluginEventExampleDTO struct {
	Name    string      `json:"name"`
	Payload interface{} `json:"payload"`
}

// pluginEventKindDTO is a single event kind from an installed plugin instance's
// manifest — used by the trigger picker to show plugin-sourced trigger options.
type pluginEventKindDTO struct {
	Kind          string                  `json:"kind"`
	Description   string                  `json:"description"`
	BindingSchema interface{}             `json:"binding_schema,omitempty"`
	Examples      []pluginEventExampleDTO `json:"examples,omitempty"`
}

// pluginInstanceForAudienceDTO describes one installed plugin instance for use
// in the audience editor and trigger picker. Second consumer: the trigger
// picker (#219) reads event_kinds to populate plugin-sourced entries.
type pluginInstanceForAudienceDTO struct {
	ID                 string               `json:"id"`
	PluginID           string               `json:"plugin_id"`
	PluginName         string               `json:"plugin_name"`
	InstanceName       string               `json:"instance_name"`
	State              string               `json:"state"`
	ImplementsNotify   bool                 `json:"implements_notify"`
	ImplementsRequest  bool                 `json:"implements_request"`
	ConfigSchema       interface{}          `json:"config_schema"`
	EventKinds         []pluginEventKindDTO `json:"event_kinds"`
	SubscriptionSchema interface{}          `json:"subscription_schema"`
	SubscriptionScope  map[string]any       `json:"subscription_scope"`
	Version            int64                `json:"version"`
	// AuthStrategy is the auth strategy declared in the manifest (e.g.
	// "oauth2_authcode", "oauth2_clientcred", "static_api_key"). Used by the
	// admin UI to decide whether to show the Re-authorize button (#228).
	AuthStrategy string `json:"auth_strategy"`
	// HealthDetail is the detail string from the most recent health transition.
	// Omitted when empty. Used together with AuthStrategy to gate the
	// Re-authorize banner: detail prefix "oauth refresh failed" + oauth strategy
	// → show the button.
	HealthDetail string `json:"health_detail,omitempty"`
	// LastOauthCallbackUrl is the OAuth callback URL recorded the last time this
	// instance completed an OAuth flow. Omitted when nil (never authorized). Used
	// by the admin/plugins page to help operators understand which URL needs to be
	// updated in the provider after a public_url change (#230).
	LastOauthCallbackUrl string `json:"last_oauth_callback_url,omitempty"`
	// PluginVersion is the SemVer version string from the manifest (e.g. "1.0.0").
	// Used by the admin/plugins page to display the version on each plugin card.
	PluginVersion string `json:"plugin_version"`
	// Services lists the gRPC services declared by the plugin manifest.
	// Possible values: "tool", "trigger", "channel". Omitted when empty.
	// Used by the admin/plugins page to render service badges on plugin cards.
	Services []string `json:"services,omitempty"`
}

// ListPluginInstances handles GET /api/v1/admin/plugin-instances.
// Returns all installed plugin instances with their manifest-derived
// capabilities for use in the audience editor and trigger picker.
func (h *AudienceHandler) ListPluginInstances(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	instances, err := h.store.Queries().ListPluginInstances(ctx)
	if err != nil {
		slog.Error("list plugin instances: DB error", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	dtos := make([]pluginInstanceForAudienceDTO, 0, len(instances))
	for _, inst := range instances {
		manifest, err := h.snap.ForInstanceID(ctx, inst.ID)
		if err != nil {
			// Skip instances whose manifest cannot be parsed rather than failing
			// the entire list — the operator can still use other instances.
			slog.Warn("list plugin instances: parse manifest", "instance_id", inst.ID, "err", err)
			continue
		}

		var implementsNotify, implementsRequest bool
		var configSchema interface{}
		if len(manifest.Channels) > 0 {
			ch := manifest.Channels[0]
			implementsNotify = ch.ImplementsNotify
			implementsRequest = ch.ImplementsRequest
			if ch.ConfigSchema != nil {
				// Convert the YAML node to a plain map so it serializes as JSON.
				var csMap interface{}
				if err := ch.ConfigSchema.Decode(&csMap); err == nil {
					configSchema = csMap
				}
			}
		}

		eventKinds := make([]pluginEventKindDTO, 0, len(manifest.EventKinds))
		for _, ek := range manifest.EventKinds {
			if ek.Kind == "" {
				continue
			}

			dto := pluginEventKindDTO{
				Kind:        ek.Kind,
				Description: ek.Description,
			}

			// Decode binding_schema into a plain map so it serializes as JSON.
			if ek.BindingSchema != nil {
				var bsMap interface{}
				if err := ek.BindingSchema.Decode(&bsMap); err == nil {
					dto.BindingSchema = bsMap
				} else {
					slog.Warn("list plugin instances: decode binding_schema", "instance_id", inst.ID, "kind", ek.Kind, "err", err)
				}
			}

			// Decode examples; skip malformed entries rather than failing the request.
			for i, exNode := range ek.Examples {
				var m map[string]any
				if err := exNode.Decode(&m); err != nil {
					slog.Warn("list plugin instances: decode example", "instance_id", inst.ID, "kind", ek.Kind, "index", i, "err", err)
					continue
				}
				name, _ := m["name"].(string)
				payload, payloadOK := m["payload"]
				if name == "" || !payloadOK {
					slog.Warn("list plugin instances: example missing name or payload", "instance_id", inst.ID, "kind", ek.Kind, "index", i)
					continue
				}
				dto.Examples = append(dto.Examples, pluginEventExampleDTO{Name: name, Payload: payload})
			}

			eventKinds = append(eventKinds, dto)
		}

		// Decode manifest-level subscription_schema (OUTSIDE the Channels guard —
		// this is a manifest top-level field, not a per-channel field).
		var subscriptionSchema interface{}
		if manifest.SubscriptionSchema != nil {
			var ssMap interface{}
			if err := manifest.SubscriptionSchema.Decode(&ssMap); err == nil {
				subscriptionSchema = ssMap
			} else {
				slog.Warn("list plugin instances: decode subscription_schema", "instance_id", inst.ID, "err", err)
			}
		}

		// Decode current subscription scope from the raw DB row (no extra query).
		var subscriptionScope map[string]any
		if inst.SubscriptionScopeJson != "" && inst.SubscriptionScopeJson != "{}" {
			if err := json.Unmarshal([]byte(inst.SubscriptionScopeJson), &subscriptionScope); err != nil {
				slog.Warn("list plugin instances: decode subscription_scope_json", "instance_id", inst.ID, "err", err)
				subscriptionScope = map[string]any{}
			}
		} else {
			subscriptionScope = map[string]any{}
		}

		healthDetail := ""
		if inst.HealthDetail != nil {
			healthDetail = *inst.HealthDetail
		}

		lastCallbackURL := ""
		if inst.LastOauthCallbackUrl != nil {
			lastCallbackURL = *inst.LastOauthCallbackUrl
		}

		// Derive the declared service list from the manifest Services field.
		// All instances of the same plugin share the same manifest, so this
		// value is identical across instances.
		var services []string
		if manifest.Services.Tool != "" {
			services = append(services, "tool")
		}
		if manifest.Services.Trigger != "" {
			services = append(services, "trigger")
		}
		if manifest.Services.Channel != "" {
			services = append(services, "channel")
		}

		dtos = append(dtos, pluginInstanceForAudienceDTO{
			ID:                   inst.ID,
			PluginID:             inst.PluginID,
			PluginName:           manifest.Name,
			InstanceName:         inst.InstanceName,
			State:                inst.HealthState,
			ImplementsNotify:     implementsNotify,
			ImplementsRequest:    implementsRequest,
			ConfigSchema:         configSchema,
			EventKinds:           eventKinds,
			SubscriptionSchema:   subscriptionSchema,
			SubscriptionScope:    subscriptionScope,
			Version:              inst.Version,
			AuthStrategy:         string(manifest.Auth.Strategy),
			HealthDetail:         healthDetail,
			LastOauthCallbackUrl: lastCallbackURL,
			PluginVersion:        manifest.Version,
			Services:             services,
		})
	}

	httputil.WriteJSON(w, http.StatusOK, dtos)
}

// List handles GET /api/v1/admin/audiences.
func (h *AudienceHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := h.store.Queries()

	audiences, err := q.ListPluginAudiences(ctx)
	if err != nil {
		slog.Error("audience list: DB error", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	// Bulk entry counts (no N+1).
	entryCounts, err := q.CountAudienceEntriesGrouped(ctx)
	if err != nil {
		slog.Error("audience list: count entries", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	entryCountByID := make(map[string]int, len(entryCounts))
	for _, row := range entryCounts {
		entryCountByID[row.AudienceID] = int(row.EntryCount)
	}

	// Bulk policy reference counts.
	refCounts, err := policy.BulkScanPolicyReferenceCounts(ctx, q)
	if err != nil {
		slog.Error("audience list: scan policy refs", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	// Bulk in-flight run detection.
	inFlightIDs, err := q.ListAudienceIDsWithPendingRequests(ctx)
	if err != nil {
		slog.Error("audience list: list in-flight", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	inFlight := make(map[string]bool, len(inFlightIDs))
	for _, id := range inFlightIDs {
		inFlight[id] = true
	}

	items := make([]audienceListItemDTO, 0, len(audiences))
	for _, a := range audiences {
		items = append(items, audienceListItemDTO{
			ID:                      a.ID,
			Name:                    a.Name,
			EntryCount:              entryCountByID[a.ID],
			ReferencedByPolicyCount: refCounts[a.Name],
			HasInFlightRuns:         inFlight[a.ID],
			DisableInAppFallback:    a.DisableInAppFallback != 0,
			Version:                 a.Version,
			CreatedAt:               a.CreatedAt,
			UpdatedAt:               a.UpdatedAt,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, items)
}

// Get handles GET /api/v1/admin/audiences/{id}.
func (h *AudienceHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	a, err := h.store.Queries().GetPluginAudienceByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "audience not found", "")
			return
		}
		slog.Error("audience get: GetPluginAudienceByID", "id", id, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	entries, err := audiencepkg.ResolveByID(ctx, h.store.Queries(), id)
	if err != nil && !errors.Is(err, audiencepkg.ErrAudienceNotFound) {
		slog.Error("audience get: resolve entries", "id", id, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, h.buildAudienceDTO(a, entries))
}

// Create handles POST /api/v1/admin/audiences.
func (h *AudienceHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req audienceSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.Name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "name is required", "")
		return
	}

	fieldErrs, err := h.validateSavePayload(ctx, req.Entries, req.DisableInAppFallback)
	if err != nil {
		slog.Error("audience create: validate payload", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if len(fieldErrs) > 0 {
		issues := fieldErrMapToIssues(fieldErrs)
		httputil.WriteValidationError(w, http.StatusUnprocessableEntity, "validation failed", "", issues)
		return
	}

	now := h.clock().UTC().Format(time.RFC3339)
	id := model.NewULID()

	// Determine the creating user (best-effort; may be absent in tests or when
	// the user context has no real ID).
	var createdByUserID *string
	if u, ok := auth.UserFromContext(ctx); ok && u.ID != "" {
		createdByUserID = &u.ID
	}

	tx, err := h.store.DB().BeginTx(ctx, nil)
	if err != nil {
		slog.Error("audience create: begin tx", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.Error("audience create: rollback", "err", rbErr)
		}
	}()

	q := db.New(tx)

	disableFlag := int64(0)
	if req.DisableInAppFallback {
		disableFlag = 1
	}

	audience, err := q.CreatePluginAudience(ctx, db.CreatePluginAudienceParams{
		ID:                   id,
		Name:                 req.Name,
		CreatedByUserID:      createdByUserID,
		CreatedAt:            now,
		UpdatedAt:            now,
		DisableInAppFallback: disableFlag,
	})
	if err != nil {
		if isAudienceUniqueConstraintError(err) {
			httputil.WriteError(w, http.StatusConflict, "audience name already exists", "")
			return
		}
		slog.Error("audience create: insert audience", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	if err := insertAudienceEntries(ctx, q, audience.ID, req.Entries); err != nil {
		slog.Error("audience create: insert entries", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	if err := tx.Commit(); err != nil {
		slog.Error("audience create: commit", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	// Re-read with entries for the response.
	entries, err := audiencepkg.ResolveByID(ctx, h.store.Queries(), audience.ID)
	if err != nil && !errors.Is(err, audiencepkg.ErrAudienceNotFound) {
		slog.Warn("audience create: resolve after insert", "err", err)
	}

	dto := h.buildAudienceDTO(audience, entries)
	httputil.WriteCreated(w, "/api/v1/admin/audiences/"+audience.ID, dto)
}

// Update handles PUT /api/v1/admin/audiences/{id}.
func (h *AudienceHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	var req audienceSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.Name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "name is required", "")
		return
	}
	if req.ExpectedVersion == nil {
		httputil.WriteError(w, http.StatusBadRequest, "expected_version is required for update", "")
		return
	}

	fieldErrs, err := h.validateSavePayload(ctx, req.Entries, req.DisableInAppFallback)
	if err != nil {
		slog.Error("audience update: validate payload", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if len(fieldErrs) > 0 {
		issues := fieldErrMapToIssues(fieldErrs)
		httputil.WriteValidationError(w, http.StatusUnprocessableEntity, "validation failed", "", issues)
		return
	}

	now := h.clock().UTC().Format(time.RFC3339)

	tx, err := h.store.DB().BeginTx(ctx, nil)
	if err != nil {
		slog.Error("audience update: begin tx", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.Error("audience update: rollback", "err", rbErr)
		}
	}()

	q := db.New(tx)

	disableFlag := int64(0)
	if req.DisableInAppFallback {
		disableFlag = 1
	}

	rows, err := q.UpdatePluginAudience(ctx, db.UpdatePluginAudienceParams{
		ID:                   id,
		Name:                 req.Name,
		DisableInAppFallback: disableFlag,
		UpdatedAt:            now,
		ExpectedVersion:      *req.ExpectedVersion,
	})
	if err != nil {
		if isAudienceUniqueConstraintError(err) {
			httputil.WriteError(w, http.StatusConflict, "audience name already exists", "")
			return
		}
		slog.Error("audience update: UpdatePluginAudience", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if rows == 0 {
		// CAS miss — either the audience does not exist or version was stale.
		_, lookupErr := q.GetPluginAudienceByID(ctx, id)
		if errors.Is(lookupErr, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "audience not found", "")
			return
		}
		// Stale version — mirror ADR-038 conflict shape.
		httputil.WriteError(w, http.StatusConflict, "run status transition lost to concurrent writer", "")
		return
	}

	// Clear-then-reinsert entries inside the same transaction. The FK on
	// plugin_pending_requests.audience_entry_id is ON DELETE SET NULL, so
	// in-flight pointers are nullified during the brief empty state.
	if err := q.DeleteAudienceEntriesByAudience(ctx, id); err != nil {
		slog.Error("audience update: delete entries", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	if err := insertAudienceEntries(ctx, q, id, req.Entries); err != nil {
		slog.Error("audience update: insert entries", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	if err := tx.Commit(); err != nil {
		slog.Error("audience update: commit", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	// Re-read for response.
	a, err := h.store.Queries().GetPluginAudienceByID(ctx, id)
	if err != nil {
		slog.Error("audience update: re-read audience", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	entries, err := audiencepkg.ResolveByID(ctx, h.store.Queries(), id)
	if err != nil && !errors.Is(err, audiencepkg.ErrAudienceNotFound) {
		slog.Warn("audience update: resolve after update", "err", err)
	}

	httputil.WriteJSON(w, http.StatusOK, h.buildAudienceDTO(a, entries))
}

// Delete handles DELETE /api/v1/admin/audiences/{id}.
func (h *AudienceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	a, err := h.store.Queries().GetPluginAudienceByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "audience not found", "")
			return
		}
		slog.Error("audience delete: GetPluginAudienceByID", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	refs, err := policy.ScanPolicyReferences(ctx, h.store.Queries(), a.Name)
	if err != nil {
		slog.Error("audience delete: scan refs", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}
	if len(refs) > 0 {
		names := make([]string, len(refs))
		for i, ref := range refs {
			names[i] = ref.Name
		}
		// Mirror mcp_handler.go:454 — comma-joined names in detail.
		httputil.WriteError(w, http.StatusConflict, "audience is referenced by policies",
			strings.Join(names, ", "))
		return
	}

	// DELETE CASCADE handles entries.
	if _, err := h.store.Queries().DeletePluginAudience(ctx, id); err != nil {
		slog.Error("audience delete: DB error", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// References handles GET /api/v1/admin/audiences/{id}/references.
func (h *AudienceHandler) References(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	a, err := h.store.Queries().GetPluginAudienceByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "audience not found", "")
			return
		}
		slog.Error("audience references: GetPluginAudienceByID", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	policyRefs, err := policy.ScanPolicyReferences(ctx, h.store.Queries(), a.Name)
	if err != nil {
		slog.Error("audience references: scan policy refs", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	pendingReqs, err := h.store.Queries().ListPendingPluginRequestsByAudience(ctx, id)
	if err != nil {
		slog.Error("audience references: list pending requests", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	// De-duplicate runs — multiple pending requests may belong to the same run.
	seenRuns := make(map[string]bool)
	var inFlightRuns []inFlightRunDTO
	for _, req := range pendingReqs {
		if seenRuns[req.RunID] {
			continue
		}
		seenRuns[req.RunID] = true
		inFlightRuns = append(inFlightRuns, inFlightRunDTO{
			ID:     req.RunID,
			Status: req.RunStatus,
		})
	}

	// Load policy_id for each unique run via sqlc. O(n) round-trips is
	// acceptable for /references — small cardinality of in-flight runs per
	// audience. Deferred bulk-by-IDs query if metric warrants it.
	for i, run := range inFlightRuns {
		r, err := h.store.GetRun(ctx, run.ID)
		if err != nil {
			slog.Warn("audience references: load run", "run_id", run.ID, "err", err)
			continue
		}
		inFlightRuns[i].PolicyID = r.PolicyID
	}

	policies := make([]policyRefDTO, len(policyRefs))
	for i, ref := range policyRefs {
		policies[i] = policyRefDTO{ID: ref.ID, Name: ref.Name}
	}

	if inFlightRuns == nil {
		inFlightRuns = []inFlightRunDTO{}
	}

	httputil.WriteJSON(w, http.StatusOK, audienceReferencesDTO{
		Policies:     policies,
		InFlightRuns: inFlightRuns,
	})
}

// --- helpers ---

// validateSavePayload validates all entries against plugin manifests.
// Returns a field-error map (key = "entries[i].field") on validation failures,
// or a non-nil Go error on system/DB failures. Returns nil, nil when valid.
func (h *AudienceHandler) validateSavePayload(
	ctx context.Context,
	entries []audienceEntrySaveRequest,
	disableInAppFallback bool,
) (map[string]string, error) {
	fieldErrs := make(map[string]string)
	proposed := make([]audiencepkg.ProposedEntry, 0, len(entries))

	for i, e := range entries {
		manifest, err := h.snap.ForInstanceID(ctx, e.PluginInstanceID)
		if err != nil {
			fieldErrs[audienceFieldKey(i, "plugin_instance_id")] = "plugin instance not found: " + e.PluginInstanceID
			continue
		}

		// Validate notify/request capability toggles against manifest.
		capErrs := configvalidate.ValidateChannelCapabilities(manifest, e.Notify, e.Request)
		for _, fe := range capErrs {
			fieldErrs[audienceFieldKey(i, fe.Field)] = fe.Message
		}

		// Validate config against the channel ConfigSchema.
		validator, err := configvalidate.ForChannelAudience(manifest)
		if err != nil {
			if errors.Is(err, configvalidate.ErrNotChannelPlugin) {
				fieldErrs[audienceFieldKey(i, "plugin_instance_id")] = "plugin does not provide a ChannelService"
				continue
			}
			return nil, fmt.Errorf("build channel validator: %w", err)
		}

		configBytes := []byte(e.Config)
		if len(configBytes) == 0 {
			configBytes = []byte("{}")
		}
		var parsedConfig any
		if err := json.Unmarshal(configBytes, &parsedConfig); err != nil {
			fieldErrs[audienceFieldKey(i, "config")] = "invalid JSON: " + err.Error()
			continue
		}

		schemaErrs, err := validator.Validate(parsedConfig)
		if err != nil {
			return nil, fmt.Errorf("validate config: %w", err)
		}
		for _, fe := range schemaErrs {
			key := audienceFieldKey(i, "config")
			if fe.Field != "" {
				key = audienceFieldKey(i, "config."+fe.Field)
			}
			fieldErrs[key] = fe.Message
		}

		proposed = append(proposed, audiencepkg.ProposedEntry{
			PluginInstanceID: e.PluginInstanceID,
			Notify:           e.Notify,
			Request:          e.Request,
		})
	}

	// Only run coverage check when no prior field errors (entries might have
	// unknown plugins so proposed may be incomplete).
	if len(fieldErrs) == 0 {
		instanceCanRequest := func(instanceID string) (bool, error) {
			m, err := h.snap.ForInstanceID(ctx, instanceID)
			if err != nil {
				return false, err
			}
			if len(m.Channels) == 0 {
				return false, nil
			}
			return m.Channels[0].ImplementsRequest, nil
		}

		coverageErrs, err := audiencepkg.ValidateAudienceCoverage(proposed, disableInAppFallback, instanceCanRequest)
		if err != nil {
			return nil, fmt.Errorf("validate audience coverage: %w", err)
		}
		for _, fe := range coverageErrs {
			fieldErrs[fe.Field] = fe.Message
		}
	}

	if len(fieldErrs) == 0 {
		return nil, nil
	}
	return fieldErrs, nil
}

// audienceFieldKey returns "entries[i].field".
func audienceFieldKey(index int, field string) string {
	return fmt.Sprintf("entries[%d].%s", index, field)
}

// fieldErrMapToIssues converts a field-error map to a slice of ErrorIssues for
// use with WriteValidationError.
func fieldErrMapToIssues(m map[string]string) []httputil.ErrorIssue {
	issues := make([]httputil.ErrorIssue, 0, len(m))
	for field, msg := range m {
		issues = append(issues, httputil.ErrorIssue{Field: field, Message: msg})
	}
	return issues
}

// insertAudienceEntries inserts all entries for audienceID using the provided
// transactional Queries. Position is the 0-indexed slice order.
func insertAudienceEntries(ctx context.Context, q *db.Queries, audienceID string, entries []audienceEntrySaveRequest) error {
	for i, e := range entries {
		configStr := "{}"
		if len(e.Config) > 0 {
			configStr = string(e.Config)
		}
		notifyVal := int64(0)
		if e.Notify {
			notifyVal = 1
		}
		requestVal := int64(0)
		if e.Request {
			requestVal = 1
		}
		if _, err := q.CreateAudienceEntry(ctx, db.CreateAudienceEntryParams{
			ID:               model.NewULID(),
			AudienceID:       audienceID,
			PluginInstanceID: e.PluginInstanceID,
			Position:         int64(i),
			Notify:           notifyVal,
			Request:          requestVal,
			ConfigJson:       configStr,
		}); err != nil {
			return err
		}
	}
	return nil
}

// buildAudienceDTO converts a DB audience row and resolved effective entries
// into the API DTO.
func (h *AudienceHandler) buildAudienceDTO(a db.PluginAudience, entries []audiencepkg.EffectiveEntry) audienceDTO {
	entryDTOs := make([]audienceEntryDTO, 0, len(entries))
	for _, e := range entries {
		cfg := json.RawMessage(e.ConfigJSON)
		if len(cfg) == 0 {
			cfg = json.RawMessage("{}")
		}
		dto := audienceEntryDTO{
			ID:               e.EntryID,
			PluginInstanceID: e.PluginInstanceID,
			Position:         e.Position,
			Notify:           e.Notify,
			Request:          e.Request,
			Config:           cfg,
		}
		if e.Auto {
			dto.Auto = true
		}
		entryDTOs = append(entryDTOs, dto)
	}
	return audienceDTO{
		ID:                   a.ID,
		Name:                 a.Name,
		DisableInAppFallback: a.DisableInAppFallback != 0,
		Version:              a.Version,
		CreatedAt:            a.CreatedAt,
		UpdatedAt:            a.UpdatedAt,
		Entries:              entryDTOs,
	}
}

// isAudienceUniqueConstraintError reports whether err is a SQLite UNIQUE
// constraint violation.
func isAudienceUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
