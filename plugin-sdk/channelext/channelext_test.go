package channelext

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

func TestDeclaration_Validate(t *testing.T) {
	valid := Declaration{
		Version:    ExtensionVersion,
		Assurance:  manifestv2.AssuranceAuthenticated,
		Deliveries: []string{DeliveryDirect, DeliveryShared},
	}

	tests := []struct {
		name    string
		decl    Declaration
		wantErr bool
	}{
		{name: "valid declaration", decl: valid},
		{
			name:    "missing version",
			decl:    Declaration{Assurance: manifestv2.AssuranceAuthenticated, Deliveries: []string{DeliveryDirect}},
			wantErr: true,
		},
		{
			name:    "unrecognized assurance",
			decl:    Declaration{Version: ExtensionVersion, Assurance: "super-sure", Deliveries: []string{DeliveryDirect}},
			wantErr: true,
		},
		{
			name: "weak assurance is valid",
			decl: Declaration{Version: ExtensionVersion, Assurance: manifestv2.AssuranceWeak, Deliveries: []string{DeliveryDirect}},
		},
		{
			name:    "empty deliveries refused",
			decl:    Declaration{Version: ExtensionVersion, Assurance: manifestv2.AssuranceAuthenticated},
			wantErr: true,
		},
		{
			name:    "unrecognized delivery",
			decl:    Declaration{Version: ExtensionVersion, Assurance: manifestv2.AssuranceAuthenticated, Deliveries: []string{"dm"}},
			wantErr: true,
		},
		{
			name: "one recognized delivery is enough",
			decl: Declaration{Version: ExtensionVersion, Assurance: manifestv2.AssuranceAuthenticated, Deliveries: []string{DeliveryShared}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.decl.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestDecodeNotification(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantErr    bool
		wantTarget Target
		wantMsg    string
	}{
		{
			name:       "good",
			body:       `{"target":{"delivery":"shared","address":"space-42"},"message":"run r-1 finished"}`,
			wantTarget: Target{Delivery: DeliveryShared, Address: "space-42"},
			wantMsg:    "run r-1 finished",
		},
		{
			name:    "bad delivery",
			body:    `{"target":{"delivery":"dm","address":"u1"},"message":"x"}`,
			wantErr: true,
		},
		{
			name:    "missing address",
			body:    `{"target":{"delivery":"direct"},"message":"x"}`,
			wantErr: true,
		},
		{
			name:       "extra fields tolerated",
			body:       `{"target":{"delivery":"direct","address":"u1","futureField":true},"message":"x","futureTopLevel":"whatever","_meta":{"anything":1}}`,
			wantTarget: Target{Delivery: DeliveryDirect, Address: "u1"},
			wantMsg:    "x",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, err := decodeNotification(json.RawMessage(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatal("decodeNotification() = nil error, want one")
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeNotification(): %v", err)
			}
			if n.Target != tc.wantTarget {
				t.Errorf("target = %+v, want %+v", n.Target, tc.wantTarget)
			}
			if n.Message != tc.wantMsg {
				t.Errorf("message = %q, want %q", n.Message, tc.wantMsg)
			}
		})
	}
}

func validDeclaration() Declaration {
	return Declaration{
		Version:    ExtensionVersion,
		Assurance:  manifestv2.AssuranceAuthenticated,
		Deliveries: []string{DeliveryDirect, DeliveryShared},
	}
}

func TestNew(t *testing.T) {
	noop := func(context.Context, Notification) error { return nil }

	if _, err := New(validDeclaration(), noop); err != nil {
		t.Fatalf("New() with a valid declaration and handler: %v", err)
	}
	if _, err := New(Declaration{}, noop); err == nil {
		t.Fatal("New() with an invalid declaration succeeded, want an error")
	}
	if _, err := New(validDeclaration(), nil); err == nil {
		t.Fatal("New() with a nil notify handler succeeded, want an error")
	}
}

func TestExtension_IdentityAndDeclaration(t *testing.T) {
	decl := validDeclaration()
	ext, err := New(decl, func(context.Context, Notification) error { return nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ext.ExtensionID() != ExtensionChannel {
		t.Errorf("ExtensionID() = %q, want %q", ext.ExtensionID(), ExtensionChannel)
	}
	wire, ok := ext.Declaration().(declarationWire)
	if !ok {
		t.Fatalf("Declaration() = %T, want declarationWire", ext.Declaration())
	}
	if wire.Version != decl.Version || wire.Assurance != decl.Assurance || len(wire.Deliveries) != len(decl.Deliveries) {
		t.Errorf("Declaration() = %+v, want it to mirror %+v", wire, decl)
	}
}

func TestExtension_Methods_OnlyNotify(t *testing.T) {
	ext, err := New(validDeclaration(), func(context.Context, Notification) error { return nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	methods := ext.Methods()
	if len(methods) != 1 {
		t.Fatalf("Methods() = %v, want exactly one method (channel/notify)", methods)
	}
	if _, ok := methods[methodChannelNotify]; !ok {
		t.Fatalf("Methods() = %v, want it to include %q", methods, methodChannelNotify)
	}
}

// modernHeaders builds the A4 headers every method on the 2026-07-28
// transport requires.
func modernHeaders(method string) map[string]string {
	return map[string]string{
		"MCP-Protocol-Version": mcpserver.ProtocolVersion,
		"Mcp-Method":           method,
	}
}

func postJSON(t *testing.T, handler http.Handler, body map[string]any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

type jsonrpcEnvelope struct {
	Result map[string]any `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) jsonrpcEnvelope {
	t.Helper()
	var env jsonrpcEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return env
}

func notifyBody(target map[string]any, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  methodChannelNotify,
		"params": map[string]any{
			"target":  target,
			"message": message,
		},
	}
}

func TestMountedExtension_NotifySuccess(t *testing.T) {
	var got Notification
	called := false
	ext, err := New(validDeclaration(), func(_ context.Context, n Notification) error {
		called = true
		got = n
		return nil
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	srv := mcpserver.NewServer("test-plugin", "0.1.0")
	if err := srv.Mount(ext); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	body := notifyBody(map[string]any{"delivery": DeliveryShared, "address": "space-42"}, "run r-1 finished")
	w := postJSON(t, srv, body, modernHeaders(methodChannelNotify))
	env := decodeEnvelope(t, w)
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	if !called {
		t.Fatal("the notify handler was never invoked")
	}
	if got.Target.Delivery != DeliveryShared || got.Target.Address != "space-42" || got.Message != "run r-1 finished" {
		t.Errorf("handler received %+v, did not round-trip", got)
	}
}

func TestMountedExtension_NotifyBadTargetIsInvalidParams(t *testing.T) {
	called := false
	ext, err := New(validDeclaration(), func(context.Context, Notification) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	srv := mcpserver.NewServer("test-plugin", "0.1.0")
	if err := srv.Mount(ext); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	body := notifyBody(map[string]any{"delivery": "dm", "address": "u1"}, "x")
	w := postJSON(t, srv, body, modernHeaders(methodChannelNotify))
	env := decodeEnvelope(t, w)
	if env.Error == nil || env.Error.Code != mcpserver.ErrCodeInvalidParams {
		t.Fatalf("error = %+v, want ErrCodeInvalidParams for an unknown delivery", env.Error)
	}
	if called {
		t.Fatal("the notify handler ran despite an invalid target")
	}
}

func TestMountedExtension_NotifyHandlerErrorIsJSONRPCError(t *testing.T) {
	ext, err := New(validDeclaration(), func(context.Context, Notification) error {
		return errUnknownAddress
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	srv := mcpserver.NewServer("test-plugin", "0.1.0")
	if err := srv.Mount(ext); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	body := notifyBody(map[string]any{"delivery": DeliveryDirect, "address": "does-not-exist"}, "x")
	w := postJSON(t, srv, body, modernHeaders(methodChannelNotify))
	env := decodeEnvelope(t, w)
	if env.Error == nil {
		t.Fatal("a failing notify handler produced no JSON-RPC error, want one, not a silent success")
	}
}

// TestMountedExtension_RequestAbsent proves the contract's worked example C:
// a server that mounts channelext with no request handler answers
// channel/request with method-not-found, because Methods() never claimed it.
func TestMountedExtension_RequestAbsent(t *testing.T) {
	ext, err := New(validDeclaration(), func(context.Context, Notification) error { return nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	srv := mcpserver.NewServer("test-plugin", "0.1.0")
	if err := srv.Mount(ext); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "channel/request",
		"params": map[string]any{
			"target":  map[string]any{"delivery": "direct", "address": "u1"},
			"message": "approve?",
			"options": []map[string]any{{"id": "approve", "label": "Approve"}},
		},
	}
	w := postJSON(t, srv, body, modernHeaders("channel/request"))
	env := decodeEnvelope(t, w)
	if env.Error == nil || env.Error.Code != mcpserver.ErrCodeMethodNotFound {
		t.Fatalf("error = %+v, want ErrCodeMethodNotFound for channel/request with no request handler", env.Error)
	}
}

// errUnknownAddress stands in for a plugin author's own "I don't know this
// address" failure.
var errUnknownAddress = errors.New("unknown address")
