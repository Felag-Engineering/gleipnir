package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
)

func (f *acceptV2Fixture) putResources(body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Put("/admin/plugins/{id}/instances/{iid}/resources", f.h.PutResources)
	req := httptest.NewRequest(http.MethodPut,
		"/admin/plugins/plugin-v2/instances/"+f.instanceID+"/resources", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func (f *acceptV2Fixture) instanceVersion(t *testing.T) int64 {
	t.Helper()
	inst, err := f.store.Queries().GetPluginInstanceByID(context.Background(), f.instanceID)
	if err != nil {
		t.Fatalf("get instance: %v", err)
	}
	return inst.Version
}

func TestPutResources(t *testing.T) {
	ctx := context.Background()

	t.Run("override is stored, resolved per field and written to the desired row", func(t *testing.T) {
		f := newAcceptV2Fixture(t, "active", true)
		rec := f.putResources(`{"memory_mb": 512, "expected_version": ` + itoa(f.instanceVersion(t)) + `}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
		}

		var resp instanceResponse
		if err := json.Unmarshal(parseDataResponse(t, rec), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Resources == nil || resp.Resources.Override == nil || resp.Resources.Override.MemoryMB != 512 {
			t.Fatalf("resources = %+v, want a 512 MiB override", resp.Resources)
		}
		eff := resp.Resources.Effective
		if eff.MemoryMB != 512 || eff.MemorySource != "admin_override" {
			t.Errorf("effective memory = %d (%s), want 512 admin_override", eff.MemoryMB, eff.MemorySource)
		}
		if eff.CPUMillicores != 100 || eff.CPUSource != "manifest" {
			t.Errorf("effective cpu = %d (%s), want the manifest's 100", eff.CPUMillicores, eff.CPUSource)
		}

		row, err := f.store.Queries().GetPluginContainerByInstance(ctx, f.instanceID)
		if err != nil {
			t.Fatalf("get container row: %v", err)
		}
		if row.MemoryLimitBytes == nil || *row.MemoryLimitBytes != int64(512)<<20 {
			t.Errorf("desired memory = %v, want 512 MiB", row.MemoryLimitBytes)
		}
		if row.CpuLimitMillicores == nil || *row.CpuLimitMillicores != 100 {
			t.Errorf("desired cpu = %v, want 100", row.CpuLimitMillicores)
		}

		events, err := f.store.Queries().ListPluginAuditEventsByInstance(ctx, db.ListPluginAuditEventsByInstanceParams{
			PluginInstanceID: &f.instanceID, Limit: 50,
		})
		if err != nil {
			t.Fatalf("list audit events: %v", err)
		}
		found := false
		for _, ev := range events {
			if ev.EventType == auditInstanceResourcesUpdated {
				found = true
			}
		}
		if !found {
			t.Error("no plugin_instance_resources_updated audit event for the instance")
		}
	})

	t.Run("an empty body clears the override", func(t *testing.T) {
		f := newAcceptV2Fixture(t, "active", true)
		if rec := f.putResources(`{"memory_mb": 512, "expected_version": ` + itoa(f.instanceVersion(t)) + `}`); rec.Code != http.StatusOK {
			t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
		}
		rec := f.putResources(`{"expected_version": ` + itoa(f.instanceVersion(t)) + `}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
		}
		var resp instanceResponse
		if err := json.Unmarshal(parseDataResponse(t, rec), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Resources.Override != nil || resp.Resources.Effective.MemorySource != "manifest" {
			t.Errorf("resources = %+v, want no override and the manifest value back", resp.Resources)
		}
		row, _ := f.store.Queries().GetPluginContainerByInstance(ctx, f.instanceID)
		if row.MemoryLimitBytes == nil || *row.MemoryLimitBytes != int64(64)<<20 {
			t.Errorf("desired memory = %v, want the manifest's 64 MiB", row.MemoryLimitBytes)
		}
	})

	t.Run("rejections change nothing", func(t *testing.T) {
		tests := []struct {
			name     string
			body     string
			wantCode int
		}{
			{"missing expected_version", `{"memory_mb": 512}`, http.StatusBadRequest},
			{"malformed body", `{"memory_mb":`, http.StatusBadRequest},
			{"memory below what the runtime can enforce", `{"memory_mb": 1, "expected_version": %d}`, http.StatusBadRequest},
			{"negative cpu", `{"cpu_millicores": -5, "expected_version": %d}`, http.StatusBadRequest},
			{"cpu above the 64-core ceiling", `{"cpu_millicores": 64001, "expected_version": %d}`, http.StatusBadRequest},
			{"zero is not a limit", `{"memory_mb": 0, "expected_version": %d}`, http.StatusBadRequest},
			{"stale version", `{"memory_mb": 512, "expected_version": 99}`, http.StatusConflict},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				f := newAcceptV2Fixture(t, "active", true)
				before, _ := f.store.Queries().GetPluginContainerByInstance(ctx, f.instanceID)
				version := f.instanceVersion(t)

				rec := f.putResources(strings.ReplaceAll(tc.body, "%d", itoa(version)))
				if rec.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantCode, rec.Body.String())
				}
				if got := f.instanceVersion(t); got != version {
					t.Errorf("instance version moved %d -> %d on a rejected write", version, got)
				}
				after, _ := f.store.Queries().GetPluginContainerByInstance(ctx, f.instanceID)
				if after.Version != before.Version {
					t.Error("desired row changed on a rejected write")
				}
			})
		}
	})

	t.Run("a stale write loses the CAS race after a successful one", func(t *testing.T) {
		f := newAcceptV2Fixture(t, "active", true)
		version := f.instanceVersion(t)
		if rec := f.putResources(`{"cpu_millicores": 250, "expected_version": ` + itoa(version) + `}`); rec.Code != http.StatusOK {
			t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
		}
		if rec := f.putResources(`{"cpu_millicores": 300, "expected_version": ` + itoa(version) + `}`); rec.Code != http.StatusConflict {
			t.Errorf("replayed version: status = %d, want 409", rec.Code)
		}
	})

	t.Run("a v1 plugin is refused", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", PluginVersion: "1.0.0",
			ManifestSnapshot: instanceConfigManifestNoSchema, Status: "active"})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "i", ConfigJson: "{}"})
		h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

		r := chi.NewRouter()
		r.Put("/admin/plugins/{id}/instances/{iid}/resources", h.PutResources)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/admin/plugins/plugin-1/instances/inst-1/resources",
			strings.NewReader(`{"memory_mb": 512, "expected_version": 0}`)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestPutResources_NoSubstrateIs409(t *testing.T) {
	f := newAcceptV2Fixture(t, "active", true)
	f.h.provisioner = nil
	rec := f.putResources(`{"memory_mb": 512, "expected_version": ` + itoa(f.instanceVersion(t)) + `}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
}

func TestInstanceResponse_ResourcesOnlyForV2(t *testing.T) {
	f := newAcceptV2Fixture(t, "active", true)
	r := chi.NewRouter()
	r.Get("/admin/plugins/{id}/instances/{iid}", f.h.GetInstance)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/plugins/plugin-v2/instances/"+f.instanceID, nil))
	var resp instanceResponse
	if err := json.Unmarshal(parseDataResponse(t, rec), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Resources == nil || resp.Resources.Override != nil ||
		resp.Resources.Effective.MemoryMB != 64 || resp.Resources.Effective.MemorySource != "manifest" {
		t.Errorf("v2 resources = %+v, want the manifest's 64 MiB and no override", resp.Resources)
	}

	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", PluginVersion: "1.0.0",
		ManifestSnapshot: instanceConfigManifestNoSchema, Status: "active"})
	q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "i", ConfigJson: "{}"})
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})
	r = chi.NewRouter()
	r.Get("/admin/plugins/{id}/instances/{iid}", h.GetInstance)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/plugins/plugin-1/instances/inst-1", nil))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(parseDataResponse(t, rec), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["resources"]; present {
		t.Error("v1 instance response must not carry a resources key")
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
