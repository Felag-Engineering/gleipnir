package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/model"
	optionsv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/options/v1"
)

// OptionsProvider is the narrow interface over dispatch.OptionsClient consumed
// by PluginOptionsHandler. Using an interface keeps the admin package free of
// dispatch internals and makes the handler testable with a stub.
type OptionsProvider interface {
	ListOptions(ctx context.Context, instanceName, instanceID, source, query, cursor string) (*optionsv1.ListOptionsResponse, error)
}

// OptionsPluginQuerier is the narrow DB interface required by PluginOptionsHandler
// to resolve plugin-instance details for the options proxy endpoint.
type OptionsPluginQuerier interface {
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
}

// optionsCacheKey uniquely identifies a cached options response.
type optionsCacheKey struct {
	instanceID string
	source     string
	query      string
	cursor     string
}

// optionsCacheEntry holds a cached response and the wall-clock time it was stored.
type optionsCacheEntry struct {
	resp     *pluginOptionsResponse
	storedAt time.Time
}

// pluginOptionsResponse is the JSON body returned by GetInstanceOptions.
type pluginOptionsResponse struct {
	Options    []*apiOption `json:"options"`
	NextCursor string       `json:"next_cursor"`
	Degraded   bool         `json:"degraded,omitempty"`
}

// apiOption is a single option item in the response.
type apiOption struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Group    string `json:"group,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// PluginOptionsHandler proxies ConfigOptionsService.ListOptions calls from the
// admin UI to plugin instances. It applies a 30s TTL cache to avoid hammering
// plugin subprocesses on every keystroke in an async combobox.
//
// Graceful degradation: when the instance is unhealthy/inactive, or when the
// plugin returns codes.Unimplemented/Unavailable (no provider registered), the
// handler returns HTTP 200 with {options:[], degraded:true} so the UI can fall
// back to a plain text field.
//
// Route (admin-only group): GET /plugins/{id}/instances/{iid}/options/{source}
// Query params: query (search string), cursor (pagination cursor)
type PluginOptionsHandler struct {
	q        OptionsPluginQuerier
	provider OptionsProvider // nil when plugins are disabled
	cacheTTL time.Duration
	timeNow  func() time.Time

	mu    sync.RWMutex
	cache map[optionsCacheKey]optionsCacheEntry
}

// NewPluginOptionsHandler constructs a PluginOptionsHandler. provider may be
// nil when the plugin subsystem is not running; GetInstanceOptions will always
// return {options:[], degraded:true} in that case. cacheTTL controls how long
// responses are cached; zero defaults to 30s.
func NewPluginOptionsHandler(q OptionsPluginQuerier, provider OptionsProvider, cacheTTL time.Duration) *PluginOptionsHandler {
	if cacheTTL == 0 {
		cacheTTL = 30 * time.Second
	}
	return &PluginOptionsHandler{
		q:        q,
		provider: provider,
		cacheTTL: cacheTTL,
		timeNow:  time.Now,
		cache:    make(map[optionsCacheKey]optionsCacheEntry),
	}
}

// GetInstanceOptions handles GET /plugins/{id}/instances/{iid}/options/{source}.
//
// On success: 200 {data: {options, next_cursor}}
// On degraded: 200 {data: {options:[], next_cursor:"", degraded:true}}
// On bad instance: 404
// On auth failure: 403 (enforced by the enclosing admin-only router group)
func (h *PluginOptionsHandler) GetInstanceOptions(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "iid")
	source := chi.URLParam(r, "source")
	query := r.URL.Query().Get("query")
	cursor := r.URL.Query().Get("cursor")

	ctx := r.Context()

	// Resolve and validate the instance.
	inst, err := h.q.GetPluginInstanceByID(ctx, instanceID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to get instance", "")
		return
	}
	// Verify the instance belongs to the given plugin (avoids cross-plugin leakage).
	if inst.PluginID != pluginID {
		httputil.WriteError(w, http.StatusNotFound, "instance not found", "")
		return
	}

	// Degrade immediately for inactive or unhealthy instances — no point in
	// attempting a gRPC call to a subprocess that is down.
	state := model.PluginHealthState(inst.HealthState)
	if state == model.PluginHealthStateInactive ||
		state == model.PluginHealthStateUnhealthy {
		httputil.WriteJSON(w, http.StatusOK, &pluginOptionsResponse{
			Options:  []*apiOption{},
			Degraded: true,
		})
		return
	}

	// Provider is nil when plugins are not running (test / no-plugin-key paths).
	if h.provider == nil {
		httputil.WriteJSON(w, http.StatusOK, &pluginOptionsResponse{
			Options:  []*apiOption{},
			Degraded: true,
		})
		return
	}

	// Check cache first.
	key := optionsCacheKey{instanceID: instanceID, source: source, query: query, cursor: cursor}
	if resp, ok := h.cacheGet(key); ok {
		httputil.WriteJSON(w, http.StatusOK, resp)
		return
	}

	resp, err := h.provider.ListOptions(ctx, inst.InstanceName, instanceID, source, query, cursor)
	if err != nil {
		if isDegradedError(err) {
			slog.DebugContext(ctx, "plugin options degraded (no provider)",
				"instance_id", instanceID,
				"source", source,
				"err", err,
			)
			httputil.WriteJSON(w, http.StatusOK, &pluginOptionsResponse{
				Options:  []*apiOption{},
				Degraded: true,
			})
			return
		}
		slog.ErrorContext(ctx, "plugin options ListOptions error",
			"instance_id", instanceID,
			"source", source,
			"err", err,
		)
		httputil.WriteError(w, http.StatusBadGateway, "options provider error", err.Error())
		return
	}

	out := &pluginOptionsResponse{
		NextCursor: resp.GetNextCursor(),
		Options:    make([]*apiOption, 0, len(resp.GetOptions())),
	}
	for _, opt := range resp.GetOptions() {
		out.Options = append(out.Options, &apiOption{
			Value:    opt.GetValue(),
			Label:    opt.GetLabel(),
			Group:    opt.GetGroup(),
			Disabled: opt.GetDisabled(),
		})
	}

	// Cache on success.
	h.cachePut(key, out)

	httputil.WriteJSON(w, http.StatusOK, out)
}

// isDegradedError reports whether the error from OptionsProvider.ListOptions
// should be treated as a graceful degradation rather than a hard failure.
// codes.Unimplemented means the plugin registered no ConfigOptionsService.
// codes.Unavailable means the subprocess is not yet ready (transient).
//
// OptionsClient wraps the gRPC error with fmt.Errorf("...: %w", ...), so we
// walk the chain looking for a value that carries a real gRPC status code
// (not codes.Unknown, which is what status.FromError returns for plain errors).
func isDegradedError(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		st, ok := status.FromError(e)
		if ok && st.Code() != codes.OK && st.Code() != codes.Unknown {
			return st.Code() == codes.Unimplemented || st.Code() == codes.Unavailable
		}
	}
	return false
}

// cacheGet returns a cached response if it exists and has not exceeded the TTL.
// Returns (nil, false) on miss or expiry.
func (h *PluginOptionsHandler) cacheGet(key optionsCacheKey) (*pluginOptionsResponse, bool) {
	h.mu.RLock()
	entry, ok := h.cache[key]
	h.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if h.timeNow().Sub(entry.storedAt) > h.cacheTTL {
		return nil, false
	}
	return entry.resp, true
}

// cachePut stores a response in the cache (lazy-evicts expired entries on write).
func (h *PluginOptionsHandler) cachePut(key optionsCacheKey, resp *pluginOptionsResponse) {
	now := h.timeNow()
	h.mu.Lock()
	defer h.mu.Unlock()
	// Lazy eviction: remove stale entries when the map is already large enough
	// that a sweep is worthwhile. Keeps the cache from growing without bound when
	// many distinct (source, query, cursor) combos are queried.
	if len(h.cache) > 500 {
		for k, e := range h.cache {
			if now.Sub(e.storedAt) > h.cacheTTL {
				delete(h.cache, k)
			}
		}
	}
	h.cache[key] = optionsCacheEntry{resp: resp, storedAt: now}
}
