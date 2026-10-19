package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

var update = flag.Bool("update", false, "rewrite testdata/tools_list.golden.json")

const goldenPath = "testdata/tools_list.golden.json"

// TestToolsListMatchesGolden pins the tools/list result. The root module has a
// test that reads this same file and runs every inputSchema through
// internal/schemanorm, so the schemas the host will canonicalize at discovery
// are checked without the root module importing this package. A schema change
// therefore has to show up in a reviewed golden diff: run
// `go test -run TestToolsListMatchesGolden -update` to regenerate it.
func TestToolsListMatchesGolden(t *testing.T) {
	srv := newTestServer(t, &fakeHost{}, "http://unused.invalid/api/")

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("MCP-Protocol-Version", mcpserver.ProtocolVersion)
	req.Header.Set("Mcp-Method", "tools/list")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || len(env.Result) == 0 {
		t.Fatalf("decode tools/list %q: %v", w.Body.String(), err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, env.Result, "", "  "); err != nil {
		t.Fatalf("indent: %v", err)
	}
	pretty.WriteByte('\n')

	if *update {
		if err := os.WriteFile(goldenPath, pretty.Bytes(), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(pretty.Bytes(), want) {
		t.Errorf("tools/list differs from %s; review the change and re-run with -update\n got: %s\nwant: %s", goldenPath, pretty.Bytes(), want)
	}
}
