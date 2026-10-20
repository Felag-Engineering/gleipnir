package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient/hostclienttest"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

type publishedRequest struct {
	Method string
	Path   string
	Auth   string
	Type   string
	Body   string
}

// fakeNtfy records every publish and answers with a fixed status.
type fakeNtfy struct {
	*httptest.Server
	mu       sync.Mutex
	requests []publishedRequest
}

func newFakeNtfy(t *testing.T, status int, body string) *fakeNtfy {
	t.Helper()
	f := &fakeNtfy{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, publishedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			Type:   r.Header.Get("Content-Type"),
			Body:   string(b),
		})
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeNtfy) published() []publishedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]publishedRequest(nil), f.requests...)
}

func newTestNotifier(t *testing.T, configJSON string) (*notifier, *hostclienttest.Server) {
	t.Helper()
	host := hostclienttest.NewServer(t, hostclienttest.WithInstanceConfigJSON(configJSON))
	client, err := hostclient.New()
	if err != nil {
		t.Fatalf("hostclient.New: %v", err)
	}
	return newNotifier(client, nil), host
}

func configJSON(t *testing.T, cfg instanceConfig) string {
	t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func notification(address, message string) channelext.Notification {
	return channelext.Notification{
		Target:  channelext.Target{Delivery: channelext.DeliveryShared, Address: address},
		Message: message,
	}
}

func TestNotify_TopicResolution(t *testing.T) {
	tests := []struct {
		name         string
		address      string
		defaultTopic string
		wantPath     string
		wantErr      string
	}{
		{name: "address wins over default", address: "alerts", defaultTopic: "fallback", wantPath: "/alerts"},
		{name: "default topic when address empty", defaultTopic: "fallback", wantPath: "/fallback"},
		{name: "no topic anywhere", wantErr: "no topic"},
		{name: "address cannot add a path segment", address: "a/b", wantErr: "not a valid ntfy topic"},
		{name: "address cannot add a query", address: "a?x=1", wantErr: "not a valid ntfy topic"},
		{name: "invalid default topic is refused too", defaultTopic: "bad topic", wantErr: "not a valid ntfy topic"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ntfy := newFakeNtfy(t, http.StatusOK, "{}")
			n, _ := newTestNotifier(t, configJSON(t, instanceConfig{ServerURL: ntfy.URL, DefaultTopic: tc.defaultTopic}))

			err := n.Notify(t.Context(), notification(tc.address, "disk full"))

			got := ntfy.published()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				if len(got) != 0 {
					t.Errorf("made %d HTTP requests, want none", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("Notify: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("requests = %d, want 1", len(got))
			}
			if got[0].Method != http.MethodPost || got[0].Path != tc.wantPath {
				t.Errorf("request = %s %s, want POST %s", got[0].Method, got[0].Path, tc.wantPath)
			}
			if got[0].Body != "disk full" {
				t.Errorf("body = %q, want %q", got[0].Body, "disk full")
			}
		})
	}
}

func TestNotify_AuthHeaderOnlyWithToken(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		wantAuth string
	}{
		{name: "token configured", token: "tk_secret", wantAuth: "Bearer tk_secret"},
		{name: "no token", wantAuth: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ntfy := newFakeNtfy(t, http.StatusOK, "{}")
			n, _ := newTestNotifier(t, configJSON(t, instanceConfig{ServerURL: ntfy.URL, DefaultTopic: "t", APIToken: tc.token}))

			if err := n.Notify(t.Context(), notification("", "hi")); err != nil {
				t.Fatalf("Notify: %v", err)
			}
			got := ntfy.published()
			if len(got) != 1 || got[0].Auth != tc.wantAuth {
				t.Errorf("requests = %+v, want Authorization %q", got, tc.wantAuth)
			}
		})
	}
}

func TestNotify_ServerURLDefaultsToNtfySh(t *testing.T) {
	n, _ := newTestNotifier(t, `{"default_topic":"t"}`)
	cfg, err := n.loadConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerURL != "https://ntfy.sh" {
		t.Errorf("ServerURL = %q, want https://ntfy.sh", cfg.ServerURL)
	}
}

func TestNotify_ConfigErrors(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		wantErr string
	}{
		{name: "unparseable config", cfg: `{not json`, wantErr: "parse instance config"},
		{name: "non-http scheme", cfg: `{"server_url":"ftp://x","default_topic":"t"}`, wantErr: "http or https"},
		{name: "no host", cfg: `{"server_url":"https://","default_topic":"t"}`, wantErr: "http or https"},
		{name: "not a url", cfg: `{"server_url":"://bad","default_topic":"t"}`, wantErr: "not a valid URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := newTestNotifier(t, tc.cfg)
			err := n.Notify(t.Context(), notification("", "hi"))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestNotify_NonSuccessStatusIsAnError(t *testing.T) {
	ntfy := newFakeNtfy(t, http.StatusForbidden, "forbidden topic")
	n, host := newTestNotifier(t, configJSON(t, instanceConfig{ServerURL: ntfy.URL, DefaultTopic: "t", APIToken: "tk_secret"}))

	err := n.Notify(t.Context(), notification("", "hi"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "forbidden topic") {
		t.Fatalf("err = %v, want HTTP 403 with server detail", err)
	}
	if strings.Contains(err.Error(), "tk_secret") {
		t.Errorf("error leaks the API token: %v", err)
	}

	logs := host.Logs()
	if len(logs) != 1 || logs[0].Level != hostclient.LogLevelError {
		t.Errorf("logs = %+v, want one error-level entry", logs)
	}
}

func TestNotify_SuccessIsLoggedToHost(t *testing.T) {
	ntfy := newFakeNtfy(t, http.StatusOK, "{}")
	n, host := newTestNotifier(t, configJSON(t, instanceConfig{ServerURL: ntfy.URL, DefaultTopic: "t"}))

	if err := n.Notify(t.Context(), notification("", "hi")); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	logs := host.Logs()
	if len(logs) != 1 || logs[0].Level != hostclient.LogLevelInfo {
		t.Errorf("logs = %+v, want one info-level entry", logs)
	}
}

func TestNotify_UnreachableServer(t *testing.T) {
	ntfy := newFakeNtfy(t, http.StatusOK, "{}")
	url := ntfy.URL
	ntfy.Close()
	n, _ := newTestNotifier(t, configJSON(t, instanceConfig{ServerURL: url, DefaultTopic: "t"}))

	err := n.Notify(t.Context(), notification("", "hi"))
	if err == nil || !strings.Contains(err.Error(), "publish to ntfy") {
		t.Fatalf("err = %v, want publish failure", err)
	}
}

func TestNotify_HonoursContextCancellation(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	n, _ := newTestNotifier(t, configJSON(t, instanceConfig{ServerURL: slow.URL, DefaultTopic: "t"}))

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() { errCh <- n.Notify(ctx, notification("", "hi")) }()

	select {
	case <-arrived:
	case <-time.After(30 * time.Second):
		t.Fatal("publish never reached the server")
	}
	cancel()

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("err = %v, want context canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Notify did not return after cancellation")
	}
}

// --- served surface and manifest ---

func serveRPC(t *testing.T, url, method string, params map[string]any) (json.RawMessage, *struct{ Code int }) {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{
		mcpserver.MetaKeyProtocolVersion:    mcpserver.ProtocolVersion,
		mcpserver.MetaKeyClientCapabilities: map[string]any{},
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
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("%s: decode: %v", method, err)
	}
	if out.Error != nil {
		return nil, &struct{ Code int }{out.Error.Code}
	}
	return out.Result, nil
}

func newTestServer(t *testing.T, ntfyURL string) *httptest.Server {
	t.Helper()
	n, _ := newTestNotifier(t, configJSON(t, instanceConfig{ServerURL: ntfyURL, DefaultTopic: "t"}))
	srv, err := newServer(n)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func loadManifest(t *testing.T) (*manifestv2.Manifest, []byte) {
	t.Helper()
	raw, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifestv2.Parse(raw)
	if err != nil {
		t.Fatalf("manifestv2.Parse: %v", err)
	}
	return m, raw
}

func TestManifestIsCanonical(t *testing.T) {
	m, raw := loadManifest(t)
	again, err := manifestv2.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(raw, again) {
		t.Errorf("manifest.yaml is not canonical; canonical form:\n%s", again)
	}
}

func TestManifestMatchesServedDeclaration(t *testing.T) {
	m, _ := loadManifest(t)
	ntfy := newFakeNtfy(t, http.StatusOK, "{}")
	ts := newTestServer(t, ntfy.URL)

	if got := m.Gleipnir.Profiles.Declared(); !reflect.DeepEqual(got, []string{"human_channel"}) {
		t.Errorf("manifest profiles = %v, want [human_channel]", got)
	}
	if len(m.Gleipnir.Tools) != 0 || len(m.Gleipnir.EventKinds) != 0 {
		t.Errorf("manifest declares tools %v / event kinds %v, but the server mounts neither", m.Gleipnir.Tools, m.Gleipnir.EventKinds)
	}

	discover, rpcErr := serveRPC(t, ts.URL, "server/discover", nil)
	if rpcErr != nil {
		t.Fatalf("server/discover: error %d", rpcErr.Code)
	}
	var caps struct {
		Capabilities struct {
			Extensions map[string]struct {
				Assurance  string   `json:"assurance"`
				Deliveries []string `json:"deliveries"`
			} `json:"extensions"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(discover, &caps); err != nil {
		t.Fatal(err)
	}
	served, ok := caps.Capabilities.Extensions["io.gleipnir/channel"]
	if !ok {
		t.Fatalf("io.gleipnir/channel not served: %s", discover)
	}
	if got := m.Gleipnir.Profiles.HumanChannel.Assurance; got != served.Assurance || got != manifestv2.AssuranceWeak {
		t.Errorf("manifest assurance %q, served %q, want weak", got, served.Assurance)
	}
	if !reflect.DeepEqual(served.Deliveries, []string{channelext.DeliveryShared}) {
		t.Errorf("served deliveries = %v, want [shared]", served.Deliveries)
	}
}

func TestManifestDeclaresEgressAndSecretConfig(t *testing.T) {
	m, _ := loadManifest(t)
	if len(m.Gleipnir.Egress) != 1 || m.Gleipnir.Egress[0].Domain != "ntfy.sh" {
		t.Errorf("egress = %+v, want exactly ntfy.sh", m.Gleipnir.Egress)
	}
	raw, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "x-gleipnir-secret: true") {
		t.Error("api_token is not marked x-gleipnir-secret")
	}
}

func TestChannelNotifyOverWire(t *testing.T) {
	ntfy := newFakeNtfy(t, http.StatusOK, "{}")
	ts := newTestServer(t, ntfy.URL)

	_, rpcErr := serveRPC(t, ts.URL, "channel/notify", map[string]any{
		"target":  map[string]string{"delivery": "shared", "address": "alerts"},
		"message": "disk full",
	})
	if rpcErr != nil {
		t.Fatalf("channel/notify: error %d", rpcErr.Code)
	}
	got := ntfy.published()
	if len(got) != 1 || got[0].Path != "/alerts" || got[0].Body != "disk full" {
		t.Errorf("published = %+v, want one POST /alerts with the message", got)
	}
}

func TestChannelRequestIsRefused(t *testing.T) {
	ntfy := newFakeNtfy(t, http.StatusOK, "{}")
	ts := newTestServer(t, ntfy.URL)

	_, rpcErr := serveRPC(t, ts.URL, "channel/request", map[string]any{
		"target":  map[string]string{"delivery": "shared", "address": "alerts"},
		"message": "approve?",
		"options": []map[string]string{{"id": "yes", "label": "Yes"}},
	})
	if rpcErr == nil {
		t.Fatal("channel/request succeeded; a notify-only channel must refuse it")
	}
	if rpcErr.Code != -32601 {
		t.Errorf("error code = %d, want -32601 (method not found)", rpcErr.Code)
	}
	if got := ntfy.published(); len(got) != 0 {
		t.Errorf("a refused request still published: %+v", got)
	}
}
