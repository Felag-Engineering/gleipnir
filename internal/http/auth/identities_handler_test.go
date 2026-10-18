package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// identitiesFixture is a real store with two users and two plugin instances.
type identitiesFixture struct {
	h     *Handler
	store *db.Store
}

func newIdentitiesFixture(t *testing.T) identitiesFixture {
	t.Helper()
	store, sqlDB := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	q := store.Queries()
	for _, id := range []string{"ann", "bob"} {
		if _, err := q.CreateUser(ctx, db.CreateUserParams{ID: id, Username: id, PasswordHash: "x", CreatedAt: now}); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
	}
	if _, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID: "pl1", Name: "chat", PluginVersion: "1.0.0", ManifestSnapshot: "{}",
		TrustedPubkey: "pk", Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	detail := ""
	for _, id := range []string{"inst-a", "inst-b"} {
		if _, err := q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
			ID: id, PluginID: "pl1", InstanceName: id + "-name", ConfigJson: "{}",
			SubscriptionScopeJson: "{}", HandshakeVersions: "{}", HealthState: "healthy",
			HealthDetail: &detail, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreatePluginInstance: %v", err)
		}
	}
	return identitiesFixture{h: NewHandler(q, sqlDB), store: store}
}

func identityRequest(method, userID, instanceID, body string) *http.Request {
	path := "/api/v1/users/" + userID + "/identities"
	if instanceID != "" {
		path += "/" + instanceID
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", userID)
	if instanceID != "" {
		rctx.URLParams.Add("instanceID", instanceID)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func (f identitiesFixture) put(t *testing.T, userID, instanceID, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.SetIdentityHandler(rec, identityRequest(http.MethodPut, userID, instanceID, body))
	return rec
}

func (f identitiesFixture) list(t *testing.T, userID string) []identityResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.ListIdentitiesHandler(rec, identityRequest(http.MethodGet, userID, "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []identityResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Data
}

func TestSetIdentityHandler(t *testing.T) {
	tests := []struct {
		name       string
		userID     string
		instanceID string
		body       string
		wantStatus int
	}{
		{"maps a user on an instance", "ann", "inst-a", `{"external_user_id":" U1 "}`, http.StatusOK},
		{"missing external id", "ann", "inst-a", `{}`, http.StatusBadRequest},
		{"blank external id", "ann", "inst-a", `{"external_user_id":"   "}`, http.StatusBadRequest},
		{"oversize external id", "ann", "inst-a", `{"external_user_id":"` + strings.Repeat("x", 257) + `"}`, http.StatusBadRequest},
		{"malformed body", "ann", "inst-a", `{`, http.StatusBadRequest},
		{"unknown user", "nobody", "inst-a", `{"external_user_id":"U1"}`, http.StatusNotFound},
		{"unknown instance", "ann", "inst-zzz", `{"external_user_id":"U1"}`, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentitiesFixture(t)
			rec := f.put(t, tc.userID, tc.instanceID, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var resp struct {
				Data identityResponse `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			got := resp.Data
			if got.InstanceID != "inst-a" || got.InstanceName != "inst-a-name" || got.ExternalUserID != "U1" || got.LinkMethod != "admin_set" {
				t.Errorf("response = %+v, want inst-a mapped to trimmed U1 via admin_set", got)
			}
		})
	}
}

func TestSetIdentityHandler_DuplicateAndReplace(t *testing.T) {
	f := newIdentitiesFixture(t)
	if rec := f.put(t, "ann", "inst-a", `{"external_user_id":"U1"}`); rec.Code != http.StatusOK {
		t.Fatalf("map ann = %d: %s", rec.Code, rec.Body.String())
	}

	// The same actor id for another user on the same instance is a conflict...
	if rec := f.put(t, "bob", "inst-a", `{"external_user_id":"U1"}`); rec.Code != http.StatusConflict {
		t.Errorf("duplicate actor id on one instance = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	// ...but on another instance it is a different person and is allowed.
	if rec := f.put(t, "bob", "inst-b", `{"external_user_id":"U1"}`); rec.Code != http.StatusOK {
		t.Errorf("same actor id on another instance = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Re-mapping a user on an instance replaces their identity there.
	if rec := f.put(t, "ann", "inst-a", `{"external_user_id":"U2"}`); rec.Code != http.StatusOK {
		t.Fatalf("re-map ann = %d: %s", rec.Code, rec.Body.String())
	}
	got := f.list(t, "ann")
	if len(got) != 1 || got[0].ExternalUserID != "U2" {
		t.Errorf("ann's identities = %+v, want exactly inst-a -> U2", got)
	}
}

func TestDeleteIdentityHandler(t *testing.T) {
	f := newIdentitiesFixture(t)
	if rec := f.put(t, "ann", "inst-a", `{"external_user_id":"U1"}`); rec.Code != http.StatusOK {
		t.Fatalf("map = %d", rec.Code)
	}

	rec := httptest.NewRecorder()
	f.h.DeleteIdentityHandler(rec, identityRequest(http.MethodDelete, "ann", "inst-a", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := f.list(t, "ann"); len(got) != 0 {
		t.Errorf("identities after delete = %+v, want none", got)
	}

	rec = httptest.NewRecorder()
	f.h.DeleteIdentityHandler(rec, identityRequest(http.MethodDelete, "ann", "inst-a", ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", rec.Code)
	}
}

func TestListIdentitiesHandler_UnknownUser(t *testing.T) {
	f := newIdentitiesFixture(t)
	rec := httptest.NewRecorder()
	f.h.ListIdentitiesHandler(rec, identityRequest(http.MethodGet, "nobody", "", ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("list for unknown user = %d, want 404", rec.Code)
	}
}
