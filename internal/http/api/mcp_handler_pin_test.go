package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// TestUpdate_ProtocolPinClearedOnURLChange covers #764: the protocol pin
// selects the whole transport, so repointing a server must drop it (atomically
// with the url write) while edits that leave the url alone must keep it.
func TestUpdate_ProtocolPinClearedOnURLChange(t *testing.T) {
	const origURL = "http://localhost:9999"
	tests := []struct {
		name    string
		newName string
		newURL  string
		wantPin bool
	}{
		{name: "url changed clears pin", newName: "pinned", newURL: "http://localhost:8888", wantPin: false},
		{name: "name-only change keeps pin", newName: "pinned-renamed", newURL: origURL, wantPin: true},
		{name: "same url resubmitted keeps pin", newName: "pinned", newURL: origURL, wantPin: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			encKey := testEncKey(t)
			registry := mcp.NewRegistry(store.Queries())
			id := insertTestMCPServerWithHeaders(t, store, "pinned", origURL, encKey, nil)

			pin := mcp.ProtocolVersion20260728
			if err := store.UpdateMCPServerProtocolVersion(context.Background(), db.UpdateMCPServerProtocolVersionParams{
				ProtocolVersion: &pin, ID: id,
			}); err != nil {
				t.Fatalf("pin protocol version: %v", err)
			}

			srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
			t.Cleanup(srv.Close)

			resp := updateMCPServer(t, srv.URL, id, tt.newName, tt.newURL)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			var envelope struct {
				Data struct {
					ProtocolVersion *string `json:"protocol_version"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			row, err := store.GetMCPServer(context.Background(), id)
			if err != nil {
				t.Fatalf("get server: %v", err)
			}
			for label, got := range map[string]*string{"response": envelope.Data.ProtocolVersion, "db": row.ProtocolVersion} {
				if tt.wantPin && (got == nil || *got != pin) {
					t.Errorf("%s protocol_version = %v, want %q", label, got, pin)
				}
				if !tt.wantPin && got != nil {
					t.Errorf("%s protocol_version = %q, want nil", label, *got)
				}
			}
		})
	}
}

// TestUpdate_AuthHeadersClearedOnURLChange covers the second half of #764:
// header values are write-only, so a repoint must wipe them (same statement as
// the url write) or an operator could exfiltrate them to a collector URL.
func TestUpdate_AuthHeadersClearedOnURLChange(t *testing.T) {
	const origURL = "http://localhost:9999"
	tests := []struct {
		name        string
		newName     string
		newURL      string
		wantCleared bool
	}{
		{name: "url changed clears headers and pin", newName: "hdrs", newURL: "http://localhost:8888", wantCleared: true},
		{name: "name-only change keeps headers and pin", newName: "hdrs-renamed", newURL: origURL, wantCleared: false},
		{name: "same url resubmitted keeps headers and pin", newName: "hdrs", newURL: origURL, wantCleared: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := testutil.NewTestStore(t)
			encKey := testEncKey(t)
			registry := mcp.NewRegistry(store.Queries())
			id := insertTestMCPServerWithHeaders(t, store, "hdrs", origURL, encKey,
				[]map[string]string{{"key": "Authorization", "value": "Bearer secret"}})

			pin := mcp.ProtocolVersion20260728
			if err := store.UpdateMCPServerProtocolVersion(context.Background(), db.UpdateMCPServerProtocolVersionParams{
				ProtocolVersion: &pin, ID: id,
			}); err != nil {
				t.Fatalf("pin protocol version: %v", err)
			}

			srv := httptest.NewServer(newMCPRouter(store, registry, encKey))
			t.Cleanup(srv.Close)

			resp := updateMCPServer(t, srv.URL, id, tt.newName, tt.newURL)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			var envelope struct {
				Data struct {
					AuthHeaderKeys []string `json:"auth_header_keys"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			row, err := store.GetMCPServer(context.Background(), id)
			if err != nil {
				t.Fatalf("get server: %v", err)
			}
			if tt.wantCleared {
				if row.AuthHeadersEncrypted != nil {
					t.Errorf("auth_headers_encrypted kept after url change, want NULL")
				}
				if row.ProtocolVersion != nil {
					t.Errorf("protocol_version = %q after url change, want nil", *row.ProtocolVersion)
				}
				if len(envelope.Data.AuthHeaderKeys) != 0 {
					t.Errorf("response header keys = %v, want none", envelope.Data.AuthHeaderKeys)
				}
				return
			}
			if row.AuthHeadersEncrypted == nil {
				t.Errorf("auth_headers_encrypted cleared without a url change")
			}
			if row.ProtocolVersion == nil {
				t.Errorf("protocol_version cleared without a url change")
			}
			if len(envelope.Data.AuthHeaderKeys) != 1 {
				t.Errorf("response header keys = %v, want 1", envelope.Data.AuthHeaderKeys)
			}
		})
	}
}
