package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// fakePluginQuerier is an in-memory PluginQuerier for tests.
type fakePluginQuerier struct {
	instances map[string]db.PluginInstance
}

func (f *fakePluginQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	row, ok := f.instances[id]
	if !ok {
		return db.PluginInstance{}, ErrNotFound
	}
	return row, nil
}

func newFakePluginQuerier() *fakePluginQuerier {
	return &fakePluginQuerier{instances: make(map[string]db.PluginInstance)}
}

func (f *fakePluginQuerier) seed(inst db.PluginInstance) {
	f.instances[inst.ID] = inst
}

func TestPluginHandler_GetInstance(t *testing.T) {
	detail := "verified by host"

	tests := []struct {
		name         string
		pluginID     string
		instanceID   string
		seed         *db.PluginInstance // nil means don't seed
		wantStatus   int
		wantState    string
		wantPluginID string
	}{
		{
			name:       "200 happy path",
			pluginID:   "plugin-1",
			instanceID: "inst-1",
			seed: &db.PluginInstance{
				ID:           "inst-1",
				PluginID:     "plugin-1",
				InstanceName: "prod",
				HealthState:  "healthy",
				HealthDetail: &detail,
				Version:      3,
				UpdatedAt:    "2024-01-01T00:00:00Z",
			},
			wantStatus:   http.StatusOK,
			wantState:    "healthy",
			wantPluginID: "plugin-1",
		},
		{
			name:       "404 instance not found",
			pluginID:   "plugin-1",
			instanceID: "inst-missing",
			seed:       nil,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "404 mismatched plugin_id",
			pluginID:   "plugin-X",
			instanceID: "inst-1",
			seed: &db.PluginInstance{
				ID:           "inst-1",
				PluginID:     "plugin-1", // belongs to a different plugin
				InstanceName: "prod",
				HealthState:  "healthy",
				Version:      0,
				UpdatedAt:    "2024-01-01T00:00:00Z",
			},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newFakePluginQuerier()
			if tt.seed != nil {
				q.seed(*tt.seed)
			}
			h := NewPluginHandler(q)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/"+tt.pluginID+"/instances/"+tt.instanceID, nil)
			req = withChiParams(req, map[string]string{"id": tt.pluginID, "iid": tt.instanceID})
			rec := httptest.NewRecorder()
			h.GetInstance(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}

			if tt.wantStatus != http.StatusOK {
				return
			}

			data := parseDataResponse(t, rec)
			var resp instanceResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if resp.State != tt.wantState {
				t.Errorf("state = %q, want %q", resp.State, tt.wantState)
			}
			if resp.PluginID != tt.wantPluginID {
				t.Errorf("plugin_id = %q, want %q", resp.PluginID, tt.wantPluginID)
			}
			if resp.ID != tt.instanceID {
				t.Errorf("id = %q, want %q", resp.ID, tt.instanceID)
			}
		})
	}
}

// withChiParams sets multiple chi URL params on a request in a single route
// context so none are lost when chaining (withChiParam creates a new context
// each time, which would overwrite any previously set params).
func withChiParams(r *http.Request, params map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}
