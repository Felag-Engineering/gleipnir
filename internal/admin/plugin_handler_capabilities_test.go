package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
)

func seedCapabilityInstance(q *fakePluginQuerier) {
	q.seed(db.PluginInstance{
		ID:           "inst-1",
		PluginID:     "plugin-1",
		InstanceName: "prod",
		HealthState:  "healthy",
		ConfigJson:   "{}",
		Version:      1,
		UpdatedAt:    "2026-01-01T00:00:00Z",
	})
}

func getCapabilities(t *testing.T, h *PluginHandler, pluginID, instanceID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withChiParams(req, map[string]string{"id": pluginID, "iid": instanceID})
	rec := httptest.NewRecorder()
	h.ListInstanceCapabilities(rec, req)
	return rec
}

func TestListInstanceCapabilities_PopulatedRegistry(t *testing.T) {
	q := newFakePluginQuerier()
	seedCapabilityInstance(q)

	reg := caphealth.NewRegistry()
	reg.SetCapability("inst-1", caphealth.Entry{
		Capability: caphealth.Capability{Profile: caphealth.ProfileToolProvider},
		State:      model.PluginHealthStateHealthy,
	})
	reg.SelfReportCapability("inst-1", caphealth.Entry{
		Capability: caphealth.Capability{Profile: caphealth.ProfileEventSource, Name: "channel_message"},
		State:      model.PluginHealthStateUnhealthy,
		Detail:     "missing scope",
	})
	// Another instance's entries must not leak in.
	reg.SetCapability("inst-other", caphealth.Entry{
		Capability: caphealth.Capability{Profile: caphealth.ProfileHumanChannel},
		State:      model.PluginHealthStateHealthy,
	})

	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{capHealth: reg})
	rec := getCapabilities(t, h, "plugin-1", "inst-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var got []capabilityResponse
	if err := json.Unmarshal(parseDataResponse(t, rec), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []capabilityResponse{
		{Profile: "event_source", Name: "channel_message", State: "unhealthy", Detail: "missing scope", Source: "self_report"},
		{Profile: "tool_provider", Name: "", State: "healthy", Detail: "", Source: "probe"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListInstanceCapabilities_NilRegistryReturnsEmptyArray(t *testing.T) {
	q := newFakePluginQuerier()
	seedCapabilityInstance(q)

	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})
	rec := getCapabilities(t, h, "plugin-1", "inst-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if data := string(parseDataResponse(t, rec)); data != "[]" {
		t.Errorf("data = %s, want []", data)
	}
}

func TestListInstanceCapabilities_NotFound(t *testing.T) {
	tests := []struct {
		name       string
		pluginID   string
		instanceID string
	}{
		{"unknown instance", "plugin-1", "inst-missing"},
		{"instance belongs to another plugin", "plugin-X", "inst-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newFakePluginQuerier()
			seedCapabilityInstance(q)
			h := newTestPluginHandler(q, nil, testPluginHandlerConfig{capHealth: caphealth.NewRegistry()})
			rec := getCapabilities(t, h, tt.pluginID, tt.instanceID)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
		})
	}
}
