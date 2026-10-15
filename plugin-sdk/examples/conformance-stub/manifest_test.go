package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

var updateManifests = flag.Bool("update", false, "rewrite manifests/*.yaml from manifest.go")

// TestManifestsAreCanonical keeps each manifests/*.yaml byte-identical to the
// canonical projection of manifest.go, and itself stable under Parse/Marshal.
// Regenerate with: go test -run TestManifestsAreCanonical -update
func TestManifestsAreCanonical(t *testing.T) {
	for _, mode := range manifestModes {
		t.Run(mode.File, func(t *testing.T) {
			m, err := manifestFor(mode)
			if err != nil {
				t.Fatalf("manifestFor: %v", err)
			}
			fromGo, err := manifestv2.Marshal(&m)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			path := filepath.Join("manifests", mode.File)
			if *updateManifests {
				if err := os.WriteFile(path, fromGo, 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
			}
			disk, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if !bytes.Equal(disk, fromGo) {
				t.Errorf("%s does not match manifest.go (rerun with -update):\ndisk:\n%s\nfrom Go:\n%s", path, disk, fromGo)
			}

			parsed, err := manifestv2.Parse(disk)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			again, err := manifestv2.Marshal(parsed)
			if err != nil {
				t.Fatalf("re-Marshal: %v", err)
			}
			if !bytes.Equal(disk, again) {
				t.Errorf("%s is not canonical YAML", path)
			}
		})
	}
}

func TestEveryManifestFileIsAMode(t *testing.T) {
	entries, err := os.ReadDir("manifests")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, m := range manifestModes {
		known[m.File] = true
	}
	for _, e := range entries {
		if !known[e.Name()] {
			t.Errorf("manifests/%s has no entry in manifestModes", e.Name())
		}
	}
}

// TestManifestMatchesLiveServer is the drift check: for each mode, what the
// on-disk manifest attests (profiles, tools, event kinds, assurance) must equal
// what a server started with that mode's configuration actually serves.
func TestManifestMatchesLiveServer(t *testing.T) {
	for _, mode := range manifestModes {
		t.Run(mode.File, func(t *testing.T) {
			disk, err := os.ReadFile(filepath.Join("manifests", mode.File))
			if err != nil {
				t.Fatal(err)
			}
			m, err := manifestv2.Parse(disk)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			cfg := Config{
				Profiles:    mode.Profiles,
				Assurance:   mode.Assurance,
				AutoResolve: autoResolveOff,
				DataDir:     t.TempDir(),
			}
			stub, err := newStub(t.Context(), cfg, nil)
			if err != nil {
				t.Fatalf("newStub: %v", err)
			}
			t.Cleanup(func() { _ = stub.Close() })
			ts := httptest.NewServer(stub.Handler)
			t.Cleanup(ts.Close)

			if got, want := m.Gleipnir.Profiles.Declared(), mode.Profiles.Names(); !sameSet(got, want) {
				t.Errorf("manifest profiles = %v, server enabled %v", got, want)
			}

			discover := rpc(t, ts.URL, "server/discover", nil)
			var caps struct {
				Capabilities struct {
					Extensions map[string]struct {
						Assurance string `json:"assurance"`
					} `json:"extensions"`
				} `json:"capabilities"`
			}
			mustUnmarshal(t, discover, &caps)
			channelDecl, served := caps.Capabilities.Extensions["io.gleipnir/channel"]
			if served != mode.Profiles.HumanChannel {
				t.Errorf("channel extension served = %v, want %v", served, mode.Profiles.HumanChannel)
			}
			if hc := m.Gleipnir.Profiles.HumanChannel; hc != nil && hc.Assurance != channelDecl.Assurance {
				t.Errorf("manifest assurance %q, server declares %q", hc.Assurance, channelDecl.Assurance)
			}

			var tools struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			mustUnmarshal(t, rpc(t, ts.URL, "tools/list", nil), &tools)
			var servedTools, declaredTools []string
			for _, tool := range tools.Tools {
				servedTools = append(servedTools, tool.Name)
			}
			for _, tool := range m.Gleipnir.Tools {
				declaredTools = append(declaredTools, tool.Name)
			}
			if !sameSet(servedTools, declaredTools) {
				t.Errorf("manifest tools = %v, server serves %v", declaredTools, servedTools)
			}
			if !mode.Profiles.ToolProvider && len(servedTools) != 0 {
				t.Errorf("server without tool_provider serves tools %v", servedTools)
			}

			var declaredKinds, servedKinds []string
			for _, k := range m.Gleipnir.EventKinds {
				declaredKinds = append(declaredKinds, k.Kind)
			}
			if mode.Profiles.EventSource {
				var kinds struct {
					Kinds []struct {
						Kind          string         `json:"kind"`
						BindingSchema map[string]any `json:"binding_schema"`
					} `json:"kinds"`
				}
				mustUnmarshal(t, rpc(t, ts.URL, "events/discover", nil), &kinds)
				for _, k := range kinds.Kinds {
					servedKinds = append(servedKinds, k.Kind)
				}
				if !sameSet(servedKinds, declaredKinds) {
					t.Errorf("manifest event kinds = %v, handler serves %v", declaredKinds, servedKinds)
				}
			} else if len(declaredKinds) != 0 {
				t.Errorf("manifest without event_source attests kinds %v", declaredKinds)
			}
		})
	}
}

func sameSet(a, b []string) bool {
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return reflect.DeepEqual(a, b) || (len(a) == 0 && len(b) == 0)
}

func rpc(t *testing.T, url, method string, params any) json.RawMessage {
	t.Helper()
	if params == nil {
		params = map[string]any{"_meta": map[string]any{
			mcpserver.MetaKeyProtocolVersion:    mcpserver.ProtocolVersion,
			mcpserver.MetaKeyClientCapabilities: map[string]any{},
		}}
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", mcpserver.ProtocolVersion)
	req.Header.Set("Mcp-Method", method)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer resp.Body.Close()
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("%s: decode: %v", method, err)
	}
	if out.Error != nil {
		t.Fatalf("%s: %s", method, out.Error.Message)
	}
	return out.Result
}

func mustUnmarshal(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}
