package conform

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// fakeServer is a canned MCP server whose every answer a test can replace.
// Its zero-change state passes every check, so each failing fixture is one
// field away from a passing one.
type fakeServer struct {
	// discover is the server/discover result.
	discover string
	// toolsPages are tools/list results, one per page; a page whose index is
	// not the last should carry a nextCursor.
	toolsPages []string
	// toolsErr, when set, is returned for tools/list instead.
	toolsErr string
	// callResult answers tools/call; callErr and callStatus replace it.
	callResult string
	callErr    string
	callStatus int
	// eventKinds is the events/discover result.
	eventKinds string

	// requests counts calls by method.
	requests map[string]int
}

func newFakeServer() *fakeServer {
	return &fakeServer{
		discover:   `{"supportedVersions":["2026-07-28"],"capabilities":{"tools":{}}}`,
		toolsPages: []string{`{"tools":[{"name":"echo","inputSchema":{"type":"object","properties":{"message":{"type":"string"}}}},{"name":"conformance.ping"}]}`},
		callErr:    `{"code":-32602,"message":"unknown tool"}`,
		callStatus: http.StatusBadRequest,
		eventKinds: `{"kinds":[{"kind":"thing.happened"}]}`,
		requests:   map[string]int{},
	}
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Method string `json:"method"`
		Params struct {
			Cursor string `json:"cursor"`
		} `json:"params"`
	}
	_ = json.Unmarshal(raw, &req)
	f.requests[req.Method]++

	writeResult := func(result string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":`+result+`}`)
	}
	writeError := func(status int, rpcErr string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":`+rpcErr+`}`)
	}

	switch req.Method {
	case "server/discover":
		writeResult(f.discover)
	case "tools/list":
		if f.toolsErr != "" {
			writeError(http.StatusOK, f.toolsErr)
			return
		}
		page := 0
		if req.Params.Cursor != "" {
			_ = json.Unmarshal([]byte(req.Params.Cursor), &page)
		}
		if page >= len(f.toolsPages) {
			writeError(http.StatusOK, `{"code":-32602,"message":"bad cursor"}`)
			return
		}
		writeResult(f.toolsPages[page])
	case "tools/call":
		switch {
		case f.callResult != "":
			writeResult(f.callResult)
		case f.callErr != "":
			writeError(f.callStatus, f.callErr)
		default:
			w.WriteHeader(f.callStatus)
		}
	case "events/discover":
		writeResult(f.eventKinds)
	default:
		writeError(http.StatusNotFound, `{"code":-32601,"message":"method not found"}`)
	}
}

// fullManifest declares tool_provider and event_source, with metadata for the
// fake server's "echo" tool and its one event kind.
func fullManifest() *manifestv2.Manifest {
	m := &manifestv2.Manifest{}
	m.Gleipnir.Profiles.ToolProvider = &manifestv2.ToolProviderProfile{}
	m.Gleipnir.Profiles.EventSource = &manifestv2.EventSourceProfile{}
	m.Gleipnir.Tools = []manifestv2.ToolDecl{{Name: "echo"}}
	m.Gleipnir.EventKinds = []manifestv2.EventKindDecl{{Kind: "thing.happened"}}
	return m
}

// serve starts h and returns a Target for it with manifest m.
func serve(t *testing.T, h http.Handler, m *manifestv2.Manifest) Target {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return Target{BaseURL: srv.URL + "/", Manifest: m}
}

// resultsByID indexes a report.
func resultsByID(r Report) map[string]Result {
	out := make(map[string]Result, len(r.Results))
	for _, res := range r.Results {
		out[res.ID] = res
	}
	return out
}
