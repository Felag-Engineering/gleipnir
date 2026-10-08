package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

type serverInfoEnvelope struct {
	Data struct {
		ID         string `json:"id"`
		ServerInfo *struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"server_info"`
	} `json:"data"`
}

// TestCreate_PersistsAndReturnsServerInfo covers #772 on the create path: the
// identity from the pre-flight probe is stored and echoed as server_info, and
// a server that reports none yields a null server_info.
func TestCreate_PersistsAndReturnsServerInfo(t *testing.T) {
	tests := []struct {
		name        string
		fake        []mcp.FakeServerOption
		wantNil     bool
		wantName    string
		wantVersion string
	}{
		{
			name:        "modern server reporting identity",
			fake:        []mcp.FakeServerOption{mcp.WithFakeMode(mcp.FakeModern), mcp.WithFakeServerInfo("acme-mcp", "2.3.1")},
			wantName:    "acme-mcp",
			wantVersion: "2.3.1",
		},
		{
			name:    "modern server reporting nothing",
			fake:    []mcp.FakeServerOption{mcp.WithFakeMode(mcp.FakeModern), mcp.WithFakeServerInfo("", "")},
			wantNil: true,
		},
		{
			name:    "legacy server has no serverInfo",
			fake:    []mcp.FakeServerOption{mcp.WithFakeMode(mcp.FakeLegacy)},
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			registry := mcp.NewRegistry(store.Queries())
			fake := httptest.NewServer(mcp.NewFakeMCPServer(tt.fake...))
			t.Cleanup(fake.Close)
			srv := httptest.NewServer(newMCPRouter(store, registry))
			t.Cleanup(srv.Close)

			body, _ := json.Marshal(map[string]string{"name": "info-server", "url": fake.URL})
			resp, err := http.Post(srv.URL+"/servers", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("POST /servers: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("status = %d, want 201", resp.StatusCode)
			}
			var env serverInfoEnvelope
			if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
				t.Fatalf("decode: %v", err)
			}

			row, err := store.GetMCPServer(context.Background(), env.Data.ID)
			if err != nil {
				t.Fatalf("GetMCPServer: %v", err)
			}
			if tt.wantNil {
				if env.Data.ServerInfo != nil {
					t.Errorf("response server_info = %+v, want null", env.Data.ServerInfo)
				}
				if row.ServerName != nil || row.ServerVersion != nil {
					t.Errorf("db server info = %v/%v, want NULL", row.ServerName, row.ServerVersion)
				}
				return
			}
			if env.Data.ServerInfo == nil ||
				env.Data.ServerInfo.Name != tt.wantName || env.Data.ServerInfo.Version != tt.wantVersion {
				t.Errorf("response server_info = %+v, want %s/%s", env.Data.ServerInfo, tt.wantName, tt.wantVersion)
			}
			if row.ServerName == nil || *row.ServerName != tt.wantName ||
				row.ServerVersion == nil || *row.ServerVersion != tt.wantVersion {
				t.Errorf("db server info = %v/%v, want %s/%s", row.ServerName, row.ServerVersion, tt.wantName, tt.wantVersion)
			}
		})
	}
}

// TestUpdate_ServerInfoClearedOnURLChange: the identity describes the endpoint,
// so it must not survive a repoint, but must survive an edit that keeps the url.
func TestUpdate_ServerInfoClearedOnURLChange(t *testing.T) {
	const origURL = "http://localhost:9999"
	tests := []struct {
		name     string
		newName  string
		newURL   string
		wantKept bool
	}{
		{name: "url changed clears server info", newName: "info", newURL: "http://localhost:8888", wantKept: false},
		{name: "name-only change keeps server info", newName: "info-renamed", newURL: origURL, wantKept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			encKey := testEncKey(t)
			registry := mcp.NewRegistry(store.Queries())
			id := insertTestMCPServerWithHeaders(t, store, "info", origURL, encKey, nil)
			if err := store.UpdateMCPServerInfo(context.Background(),
				mcp.ServerInfoParams(id, mcp.ServerInfo{Name: "acme-mcp", Version: "2.3.1"})); err != nil {
				t.Fatalf("seed server info: %v", err)
			}

			srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
			t.Cleanup(srv.Close)

			resp := updateMCPServer(t, srv.URL, id, tt.newName, tt.newURL)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			var env serverInfoEnvelope
			if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if tt.wantKept && (env.Data.ServerInfo == nil || env.Data.ServerInfo.Name != "acme-mcp") {
				t.Errorf("server_info = %+v, want kept", env.Data.ServerInfo)
			}
			if !tt.wantKept && env.Data.ServerInfo != nil {
				t.Errorf("server_info = %+v after url change, want null", env.Data.ServerInfo)
			}
		})
	}
}
