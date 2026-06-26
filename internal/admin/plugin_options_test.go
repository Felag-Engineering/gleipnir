package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/db"
	optionsv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/options/v1"
)

// --- stubs ---

// fakeOptionsQuerier satisfies OptionsPluginQuerier.
type fakeOptionsQuerier struct {
	instances map[string]db.PluginInstance
}

func (q *fakeOptionsQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	inst, ok := q.instances[id]
	if !ok {
		return db.PluginInstance{}, sql.ErrNoRows
	}
	return inst, nil
}

// fakeOptionsProvider satisfies OptionsProvider.
type fakeOptionsProvider struct {
	resp *optionsv1.ListOptionsResponse
	err  error
	// capturedArgs records the most recent ListOptions call arguments.
	capturedInstanceName string
	capturedInstanceID   string
	capturedSource       string
	capturedQuery        string
	capturedCursor       string
}

func (p *fakeOptionsProvider) ListOptions(_ context.Context, instanceName, instanceID, source, query, cursor string) (*optionsv1.ListOptionsResponse, error) {
	p.capturedInstanceName = instanceName
	p.capturedInstanceID = instanceID
	p.capturedSource = source
	p.capturedQuery = query
	p.capturedCursor = cursor
	if p.err != nil {
		return nil, p.err
	}
	return p.resp, nil
}

// --- helpers ---

// buildOptionsHandler constructs a PluginOptionsHandler with a frozen clock
// for deterministic TTL tests.
func buildOptionsHandler(q OptionsPluginQuerier, p OptionsProvider, frozenNow time.Time, ttl time.Duration) *PluginOptionsHandler {
	h := NewPluginOptionsHandler(q, p, ttl)
	h.timeNow = func() time.Time { return frozenNow }
	return h
}

// healthyInstance returns a db.PluginInstance stub in the healthy state.
func healthyInstance(id, pluginID, name string) db.PluginInstance {
	return db.PluginInstance{
		ID:           id,
		PluginID:     pluginID,
		InstanceName: name,
		HealthState:  "healthy",
	}
}

// callGetInstanceOptions fires the handler and returns the recorded response.
func callGetInstanceOptions(h *PluginOptionsHandler, pluginID, instanceID, source, query, cursor string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	q := r.URL.Query()
	if query != "" {
		q.Set("query", query)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	r.URL.RawQuery = q.Encode()
	r = withChiParams(r, map[string]string{"id": pluginID, "iid": instanceID, "source": source})

	rr := httptest.NewRecorder()
	h.GetInstanceOptions(rr, r)
	return rr
}

// decodeData decodes the {"data":...} envelope from the response body.
func decodeData(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := env["data"]
	if !ok {
		t.Fatalf("response has no 'data' field: %v", env)
	}
	m, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("'data' is not an object: %T", data)
	}
	return m
}

// --- tests ---

// TestPluginOptionsHandler_HappyPath verifies that a healthy instance returns
// the provider's options and next_cursor with no degraded flag.
func TestPluginOptionsHandler_HappyPath(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := healthyInstance("iid-1", "pid-1", "slack-prod")

	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{"iid-1": inst}}
	p := &fakeOptionsProvider{
		resp: &optionsv1.ListOptionsResponse{
			Options: []*optionsv1.Option{
				{Value: "C001", Label: "#general", Group: "Joined"},
				{Value: "C002", Label: "#private (not joined)", Group: "Not joined", Disabled: true},
			},
			NextCursor: "cursor42",
		},
	}
	h := buildOptionsHandler(q, p, now, 30*time.Second)

	rr := callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "gen", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	data := decodeData(t, rr)

	opts, _ := data["options"].([]any)
	if len(opts) != 2 {
		t.Fatalf("options length = %d, want 2", len(opts))
	}
	first := opts[0].(map[string]any)
	if first["value"] != "C001" {
		t.Errorf("options[0].value = %q, want C001", first["value"])
	}
	if data["next_cursor"] != "cursor42" {
		t.Errorf("next_cursor = %q, want cursor42", data["next_cursor"])
	}
	if _, ok := data["degraded"]; ok {
		t.Errorf("degraded should be absent on happy path")
	}

	// Verify forwarded args.
	if p.capturedInstanceName != "slack-prod" {
		t.Errorf("instanceName = %q, want slack-prod", p.capturedInstanceName)
	}
	if p.capturedSource != "channels" {
		t.Errorf("source = %q, want channels", p.capturedSource)
	}
	if p.capturedQuery != "gen" {
		t.Errorf("query = %q, want gen", p.capturedQuery)
	}
}

// TestPluginOptionsHandler_NotFound verifies that an unknown instance returns 404.
func TestPluginOptionsHandler_NotFound(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{}}
	h := buildOptionsHandler(q, nil, now, 30*time.Second)

	rr := callGetInstanceOptions(h, "pid-1", "missing-iid", "channels", "", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestPluginOptionsHandler_WrongPlugin verifies that an instance that belongs
// to a different plugin returns 404 (no cross-plugin info leakage).
func TestPluginOptionsHandler_WrongPlugin(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := healthyInstance("iid-1", "pid-OTHER", "slack-prod")
	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{"iid-1": inst}}
	h := buildOptionsHandler(q, nil, now, 30*time.Second)

	rr := callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestPluginOptionsHandler_UnhealthyDegrades verifies that unhealthy instances
// return {options:[], degraded:true} without calling the provider.
func TestPluginOptionsHandler_UnhealthyDegrades(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := db.PluginInstance{
		ID:           "iid-1",
		PluginID:     "pid-1",
		InstanceName: "slack-prod",
		HealthState:  "unhealthy",
	}
	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{"iid-1": inst}}
	p := &fakeOptionsProvider{} // provider must not be called
	h := buildOptionsHandler(q, p, now, 30*time.Second)

	rr := callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	data := decodeData(t, rr)
	if data["degraded"] != true {
		t.Errorf("degraded = %v, want true", data["degraded"])
	}
	// Provider must NOT have been called.
	if p.capturedSource != "" {
		t.Errorf("provider was called on unhealthy instance")
	}
}

// TestPluginOptionsHandler_InactiveDegrades verifies that inactive instances
// also return {options:[], degraded:true}.
func TestPluginOptionsHandler_InactiveDegrades(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := db.PluginInstance{
		ID:           "iid-1",
		PluginID:     "pid-1",
		InstanceName: "slack-prod",
		HealthState:  "inactive",
	}
	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{"iid-1": inst}}
	h := buildOptionsHandler(q, nil, now, 30*time.Second)

	rr := callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	data := decodeData(t, rr)
	if data["degraded"] != true {
		t.Errorf("degraded = %v, want true", data["degraded"])
	}
}

// TestPluginOptionsHandler_NilProviderDegrades verifies that a nil provider
// returns {options:[], degraded:true} (plugin system not running).
func TestPluginOptionsHandler_NilProviderDegrades(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := healthyInstance("iid-1", "pid-1", "slack-prod")
	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{"iid-1": inst}}
	h := buildOptionsHandler(q, nil /* provider */, now, 30*time.Second)

	rr := callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	data := decodeData(t, rr)
	if data["degraded"] != true {
		t.Errorf("degraded = %v, want true", data["degraded"])
	}
}

// TestPluginOptionsHandler_UnimplementedDegrades verifies that codes.Unimplemented
// from the provider returns {options:[], degraded:true}.
func TestPluginOptionsHandler_UnimplementedDegrades(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := healthyInstance("iid-1", "pid-1", "slack-prod")
	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{"iid-1": inst}}
	// Use a provider that wraps the gRPC error like OptionsClient does.
	grpcErr := status.Error(codes.Unimplemented, "ConfigOptionsService not implemented")
	p := &wrappedErrProvider{inner: grpcErr}
	h := buildOptionsHandler(q, p, now, 30*time.Second)

	rr := callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	data := decodeData(t, rr)
	if data["degraded"] != true {
		t.Errorf("degraded = %v, want true for Unimplemented", data["degraded"])
	}
}

// TestPluginOptionsHandler_UnavailableDegrades verifies that codes.Unavailable
// from the provider also returns {options:[], degraded:true}.
func TestPluginOptionsHandler_UnavailableDegrades(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := healthyInstance("iid-1", "pid-1", "slack-prod")
	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{"iid-1": inst}}
	grpcErr := status.Error(codes.Unavailable, "subprocess not ready")
	p := &wrappedErrProvider{inner: grpcErr}
	h := buildOptionsHandler(q, p, now, 30*time.Second)

	rr := callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	data := decodeData(t, rr)
	if data["degraded"] != true {
		t.Errorf("degraded = %v, want true for Unavailable", data["degraded"])
	}
}

// TestPluginOptionsHandler_HardError verifies that a non-degraded error returns
// HTTP 502 (bad gateway) so the UI can distinguish it from graceful degradation.
func TestPluginOptionsHandler_HardError(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := healthyInstance("iid-1", "pid-1", "slack-prod")
	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{"iid-1": inst}}
	internalErr := status.Error(codes.Internal, "redis timeout")
	p := &wrappedErrProvider{inner: internalErr}
	h := buildOptionsHandler(q, p, now, 30*time.Second)

	rr := callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "", "")
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 for hard error", rr.Code)
	}
}

// TestPluginOptionsHandler_CacheHit verifies that a second call for the same
// key hits the cache and does not call the provider again.
func TestPluginOptionsHandler_CacheHit(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := healthyInstance("iid-1", "pid-1", "slack-prod")
	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{"iid-1": inst}}

	callCount := 0
	p := &countingProvider{
		resp: &optionsv1.ListOptionsResponse{
			Options: []*optionsv1.Option{{Value: "C001", Label: "#general"}},
		},
		count: &callCount,
	}
	h := buildOptionsHandler(q, p, now, 30*time.Second)

	// First call — cache miss.
	rr1 := callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "g", "")
	if rr1.Code != http.StatusOK {
		t.Fatalf("first call status = %d", rr1.Code)
	}
	// Second call — should hit cache.
	rr2 := callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "g", "")
	if rr2.Code != http.StatusOK {
		t.Fatalf("second call status = %d", rr2.Code)
	}
	if callCount != 1 {
		t.Errorf("provider call count = %d, want 1 (cache hit)", callCount)
	}
}

// TestPluginOptionsHandler_CacheMissDifferentKey verifies that different query
// values produce separate cache entries (no cross-query contamination).
func TestPluginOptionsHandler_CacheMissDifferentKey(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := healthyInstance("iid-1", "pid-1", "slack-prod")
	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{"iid-1": inst}}

	callCount := 0
	p := &countingProvider{
		resp:  &optionsv1.ListOptionsResponse{},
		count: &callCount,
	}
	h := buildOptionsHandler(q, p, now, 30*time.Second)

	callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "alpha", "")
	callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "beta", "")

	if callCount != 2 {
		t.Errorf("provider call count = %d, want 2 (different queries = different cache keys)", callCount)
	}
}

// TestPluginOptionsHandler_CacheTTLExpiry verifies that cache entries expire
// after the TTL and trigger a fresh provider call.
// NOTE: this test mutates the handler's clock; do not run with t.Parallel().
func TestPluginOptionsHandler_CacheTTLExpiry(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	current := base
	inst := healthyInstance("iid-1", "pid-1", "slack-prod")
	q := &fakeOptionsQuerier{instances: map[string]db.PluginInstance{"iid-1": inst}}

	callCount := 0
	p := &countingProvider{
		resp:  &optionsv1.ListOptionsResponse{},
		count: &callCount,
	}
	ttl := 30 * time.Second
	h := NewPluginOptionsHandler(q, p, ttl)
	h.timeNow = func() time.Time { return current }

	// First call at t=0 — cache miss.
	callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "", "")
	if callCount != 1 {
		t.Fatalf("call count after first request = %d, want 1", callCount)
	}

	// Second call at t=10s — still within TTL, cache hit.
	current = base.Add(10 * time.Second)
	callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "", "")
	if callCount != 1 {
		t.Errorf("call count at t+10s = %d, want 1 (cache hit)", callCount)
	}

	// Third call at t=31s — TTL expired, cache miss.
	current = base.Add(31 * time.Second)
	callGetInstanceOptions(h, "pid-1", "iid-1", "channels", "", "")
	if callCount != 2 {
		t.Errorf("call count at t+31s = %d, want 2 (cache miss after TTL expiry)", callCount)
	}
}

// --- additional stubs ---

// wrappedErrProvider returns a wrapped error matching what dispatch.OptionsClient
// does (errors.Join(errors.New("ListOptions(...)"), grpcErr)).
type wrappedErrProvider struct {
	inner error
}

func (p *wrappedErrProvider) ListOptions(_ context.Context, _, _, _, _, _ string) (*optionsv1.ListOptionsResponse, error) {
	return nil, errors.Join(errors.New("ListOptions(instance=\"test\", source=\"channels\")"), p.inner)
}

// countingProvider counts how many times ListOptions is called.
type countingProvider struct {
	resp  *optionsv1.ListOptionsResponse
	count *int
}

func (p *countingProvider) ListOptions(_ context.Context, _, _, _, _, _ string) (*optionsv1.ListOptionsResponse, error) {
	*p.count++
	return p.resp, nil
}
