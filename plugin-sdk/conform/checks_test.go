package conform

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/events"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

// TestChecks gives every check a passing and a failing fixture. The passing
// fixture is the fake server unchanged; each failing one changes the one
// thing the check is about, and asserts the failure names it.
func TestChecks(t *testing.T) {
	tests := []struct {
		id         string
		breakIt    func(f *fakeServer, m *manifestv2.Manifest)
		wantDetail string
	}{
		{
			id:         "TP-PROTO-01",
			breakIt:    func(f *fakeServer, _ *manifestv2.Manifest) { f.discover = `{"supportedVersions":["2025-06-18"]}` },
			wantDetail: "does not include 2026-07-28",
		},
		{
			id: "TP-DISC-01",
			breakIt: func(f *fakeServer, _ *manifestv2.Manifest) {
				f.toolsErr = `{"code":-32603,"message":"session required"}`
			},
			wantDetail: "session required",
		},
		{
			id: "TP-DISC-02",
			breakIt: func(f *fakeServer, _ *manifestv2.Manifest) {
				f.toolsPages = []string{`{"tools":[{"name":"echo","inputSchema":{"type":"object","type":"string"}}]}`}
			},
			wantDetail: `duplicate object key "type"`,
		},
		{
			id: "TP-DISC-03",
			breakIt: func(f *fakeServer, _ *manifestv2.Manifest) {
				f.toolsPages = []string{`{"tools":[{"name":"echo"},{"name":"echo"}]}`}
			},
			wantDetail: `"echo"`,
		},
		{
			id: "TP-NAME-01",
			breakIt: func(f *fakeServer, _ *manifestv2.Manifest) {
				f.toolsPages = []string{`{"tools":[{"name":"echo"},{"name":"send email"}]}`}
			},
			wantDetail: `"send email"`,
		},
		{
			id: "TP-NAME-02",
			breakIt: func(f *fakeServer, _ *manifestv2.Manifest) {
				f.toolsPages = []string{`{"tools":[{"name":"echo"},{"name":"host/log"}]}`}
			},
			wantDetail: `"host/log"`,
		},
		{
			id: "TP-NAME-03",
			breakIt: func(f *fakeServer, _ *manifestv2.Manifest) {
				f.toolsPages = []string{`{"tools":[{"name":"echo"},{"name":"channel/request"}]}`}
			},
			wantDetail: `"channel/request"`,
		},
		{
			id: "TP-MAN-01",
			breakIt: func(_ *fakeServer, m *manifestv2.Manifest) {
				m.Gleipnir.Tools = append(m.Gleipnir.Tools, manifestv2.ToolDecl{Name: "delete_everything"})
			},
			wantDetail: `"delete_everything"`,
		},
		{
			id: "TP-CALL-01",
			breakIt: func(f *fakeServer, _ *manifestv2.Manifest) {
				f.callResult = `{"content":[{"type":"text","text":"ok"}]}`
			},
			wantDetail: "returned a success result",
		},
		{
			id:         "EV-GL-01",
			breakIt:    func(f *fakeServer, _ *manifestv2.Manifest) { f.eventKinds = `{"kinds":[{"kind":"thing.other"}]}` },
			wantDetail: `"thing.happened" is in the manifest's event_kinds but not served`,
		},
	}

	// Every registered check has a row: a new check without fixtures fails here.
	covered := map[string]bool{}
	for _, tc := range tests {
		covered[tc.id] = true
	}
	for _, c := range Checks() {
		if !covered[c.ID] {
			t.Errorf("check %s has no passing/failing fixture in TestChecks", c.ID)
		}
	}

	for _, tc := range tests {
		t.Run(tc.id+"/passes", func(t *testing.T) {
			res := runOne(t, tc.id, newFakeServer(), fullManifest())
			if res.Status != StatusPass {
				t.Errorf("%s = %s (%s), want pass on the conforming fixture", tc.id, res.Status, res.Detail)
			}
		})
		t.Run(tc.id+"/fails", func(t *testing.T) {
			f, m := newFakeServer(), fullManifest()
			tc.breakIt(f, m)
			res := runOne(t, tc.id, f, m)
			if res.Status != StatusFail || !strings.Contains(res.Detail, tc.wantDetail) {
				t.Errorf("%s = %s (%s), want fail naming %s", tc.id, res.Status, res.Detail, tc.wantDetail)
			}
		})
	}
}

// runOne runs the whole suite and returns one check's result, so every check
// is exercised through Run's selection and shared session.
func runOne(t *testing.T, id string, f *fakeServer, m *manifestv2.Manifest) Result {
	t.Helper()
	report, err := Run(context.Background(), serve(t, f, m))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res, ok := resultsByID(report)[id]
	if !ok {
		t.Fatalf("report has no %s result: %+v", id, report.Results)
	}
	return res
}

func TestSchemaFailures(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   string
	}{
		{"nested duplicate key", `{"type":"object","properties":{"a":{"type":"string","type":"number"}}}`, `duplicate object key "type"`},
		{"invalid UTF-8", "{\"type\":\"string\",\"description\":\"\xff\"}", "not valid UTF-8"},
		{"does not compile", `{"type":"objekt"}`, "does not compile"},
		{"external reference refused", `{"$ref":"file:///etc/passwd"}`, "does not compile"},
		{"trailing content", `{"type":"object"} {}`, "content after the schema"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSchema([]byte(tc.schema))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("validateSchema = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	t.Run("same key in sibling objects is fine", func(t *testing.T) {
		schema := `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}`
		if err := validateSchema([]byte(schema)); err != nil {
			t.Errorf("validateSchema = %v, want nil", err)
		}
	})
}

func TestToolsListPagination(t *testing.T) {
	f := newFakeServer()
	f.toolsPages = []string{
		`{"tools":[{"name":"echo"}],"nextCursor":"1"}`,
		`{"tools":[{"name":"echo"}]}`,
	}
	res := runOne(t, "TP-DISC-03", f, fullManifest())
	if res.Status != StatusFail || !strings.Contains(res.Detail, `"echo"`) {
		t.Errorf("TP-DISC-03 = %s (%s), want a duplicate found across pages", res.Status, res.Detail)
	}
}

// A failed discovery is reported once, by TP-DISC-01; the checks that need the
// tool list skip rather than each repeating it.
func TestFailedDiscoverySkipsDependents(t *testing.T) {
	f := newFakeServer()
	f.toolsErr = `{"code":-32603,"message":"boom"}`
	report, err := Run(context.Background(), serve(t, f, fullManifest()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	byID := resultsByID(report)
	if byID["TP-DISC-01"].Status != StatusFail {
		t.Errorf("TP-DISC-01 = %+v, want fail", byID["TP-DISC-01"])
	}
	for _, id := range []string{"TP-DISC-02", "TP-DISC-03", "TP-NAME-01", "TP-NAME-02", "TP-NAME-03", "TP-MAN-01"} {
		if byID[id].Status != StatusSkip {
			t.Errorf("%s = %+v, want skip", id, byID[id])
		}
	}
	if f.requests["tools/list"] != 1 {
		t.Errorf("tools/list requested %d times, want once for the whole run", f.requests["tools/list"])
	}
	if report.Passed() {
		t.Error("Report.Passed() = true with a failed check")
	}
}

func TestUnknownCallAcceptsEveryKindOfRefusal(t *testing.T) {
	tests := []struct {
		name   string
		modify func(f *fakeServer)
	}{
		{"JSON-RPC error with HTTP 400", func(f *fakeServer) {}},
		{"JSON-RPC error with HTTP 200", func(f *fakeServer) { f.callStatus = http.StatusOK }},
		{"isError result", func(f *fakeServer) {
			f.callResult = `{"content":[{"type":"text","text":"no such tool"}],"isError":true}`
		}},
		{"bare HTTP 404", func(f *fakeServer) { f.callErr = ""; f.callStatus = http.StatusNotFound }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer()
			tc.modify(f)
			if res := runOne(t, "TP-CALL-01", f, fullManifest()); res.Status != StatusPass {
				t.Errorf("TP-CALL-01 = %s (%s), want pass", res.Status, res.Detail)
			}
		})
	}
}

// DoD: an event_source-only plugin runs zero TP-* checks.
func TestEventOnlyPluginRunsNoToolChecks(t *testing.T) {
	m := &manifestv2.Manifest{}
	m.Gleipnir.Profiles.EventSource = &manifestv2.EventSourceProfile{}
	m.Gleipnir.EventKinds = []manifestv2.EventKindDecl{{Kind: "thing.happened"}}

	f := newFakeServer()
	report, err := Run(context.Background(), serve(t, f, m))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, res := range report.Results {
		if strings.HasPrefix(res.ID, "TP-") {
			t.Errorf("event-only plugin ran %s", res.ID)
		}
	}
	if _, ok := resultsByID(report)["EV-GL-01"]; !ok {
		t.Error("event-only plugin did not run EV-GL-01")
	}
	if f.requests["tools/list"] != 0 || f.requests["tools/call"] != 0 {
		t.Errorf("event-only plugin was sent tool requests: %v", f.requests)
	}
}

func TestRunRequiresManifestAndURL(t *testing.T) {
	if _, err := Run(context.Background(), Target{BaseURL: "http://127.0.0.1:1/"}); err == nil {
		t.Error("Run without a manifest succeeded")
	}
	if _, err := Run(context.Background(), Target{Manifest: fullManifest()}); err == nil {
		t.Error("Run without a base URL succeeded")
	}
}

// The SDK's own server, with tools and the events extension mounted, passes
// every check: what plugin-sdk helps an author build is conformant as built.
func TestSDKServerConforms(t *testing.T) {
	srv := mcpserver.NewServer("conform-test", "1.0.0")
	if err := srv.RegisterTool(mcpserver.Tool{
		Name:        "echo",
		Description: "echoes",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
		Handler:     func(context.Context, json.RawMessage) (any, error) { return map[string]string{}, nil },
	}); err != nil {
		t.Fatal(err)
	}
	h := events.NewHandler("conform-test", []events.Kind{{Kind: "thing.happened"}})
	t.Cleanup(func() { h.Close("test over") })
	if err := srv.Mount(h); err != nil {
		t.Fatal(err)
	}

	report, err := Run(context.Background(), serve(t, srv, fullManifest()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Passed() {
		t.Errorf("the SDK's own server does not conform:\n%s", report)
	}
	if len(report.Results) != len(Checks()) {
		t.Errorf("ran %d checks, want all %d for a plugin declaring both profiles", len(report.Results), len(Checks()))
	}
}
