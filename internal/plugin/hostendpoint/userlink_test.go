package hostendpoint

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// fakeBinder is a scriptable PendingLinkBinder: one canned result (or error)
// per test case, plus a record of what it was called with so a test can
// assert the handler forwarded the wire arguments unchanged.
type fakeBinder struct {
	result BindResult
	err    error

	calledInstanceID     string
	calledExternalUserID string
	calledCode           string
}

func (f *fakeBinder) BindInboundCode(_ context.Context, instanceID, externalUserID, code string) (BindResult, error) {
	f.calledInstanceID = instanceID
	f.calledExternalUserID = externalUserID
	f.calledCode = code
	return f.result, f.err
}

// fakeUserConfigReader is a scriptable UserConfigReader.
type fakeUserConfigReader struct {
	cfg json.RawMessage
	err error

	calledInstanceID     string
	calledExternalUserID string
}

func (f *fakeUserConfigReader) GetUserConfig(_ context.Context, instanceID, externalUserID string) (json.RawMessage, error) {
	f.calledInstanceID = instanceID
	f.calledExternalUserID = externalUserID
	return f.cfg, f.err
}

// userLinkFixture mounts SubmitIdentityProof and GetUserConfig on a real
// Server, exercised over ServeHTTP — DoD: both methods reachable and
// authenticated, not merely callable as Go functions.
type userLinkFixture struct {
	srv *Server
	q   *fakeTier1Querier // satisfies UserLinkQuerier too; shared fake, no duplication
}

func newUserLinkFixture(t *testing.T, binder PendingLinkBinder, reader UserConfigReader) *userLinkFixture {
	t.Helper()
	q := &fakeTier1Querier{instances: map[string]db.PluginInstance{}}
	srv := NewServer()
	srv.Register(UserLinkTools(UserLinkDeps{
		Querier:      q,
		Binder:       binder,
		ConfigReader: reader,
	})...)
	return &userLinkFixture{srv: srv, q: q}
}

func TestSubmitIdentityProof(t *testing.T) {
	t.Run("nil binder rejects with the structured no-pending-link outcome, not an error", func(t *testing.T) {
		f := newUserLinkFixture(t, nil, nil)
		f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
		fx := (&tier1Fixture{srv: f.srv, q: f.q})

		_, isErr, text := fx.callTool(t, "inst-1", "", ToolSubmitIdentityProof,
			map[string]any{"external_user_id": "U123", "code": "ABCDEF"})
		if isErr {
			t.Fatalf("rejection must be a result, not a tool error: %s", text)
		}
		res := decodeResult(t, text)
		if res["accepted"] != false {
			t.Errorf("accepted = %v, want false", res["accepted"])
		}
		if res["reason"] != ReasonNoPendingLink {
			t.Errorf("reason = %v, want %q", res["reason"], ReasonNoPendingLink)
		}
	})

	t.Run("a configured binder is called with the instance and wire arguments, and its outcome passes through", func(t *testing.T) {
		binder := &fakeBinder{result: BindResult{Accepted: true}}
		f := newUserLinkFixture(t, binder, nil)
		f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
		fx := (&tier1Fixture{srv: f.srv, q: f.q})

		_, isErr, text := fx.callTool(t, "inst-1", "", ToolSubmitIdentityProof,
			map[string]any{"external_user_id": "U123", "code": "ABCDEF"})
		if isErr {
			t.Fatalf("error: %s", text)
		}
		res := decodeResult(t, text)
		if res["accepted"] != true {
			t.Errorf("accepted = %v, want true", res["accepted"])
		}
		if binder.calledInstanceID != "inst-1" || binder.calledExternalUserID != "U123" || binder.calledCode != "ABCDEF" {
			t.Errorf("binder called with (%q, %q, %q), want (inst-1, U123, ABCDEF)",
				binder.calledInstanceID, binder.calledExternalUserID, binder.calledCode)
		}
	})

	t.Run("a binder rejection reason passes through unchanged", func(t *testing.T) {
		binder := &fakeBinder{result: BindResult{Accepted: false, Reason: "code_mismatch"}}
		f := newUserLinkFixture(t, binder, nil)
		f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
		fx := (&tier1Fixture{srv: f.srv, q: f.q})

		_, isErr, text := fx.callTool(t, "inst-1", "", ToolSubmitIdentityProof,
			map[string]any{"external_user_id": "U123", "code": "wrong"})
		if isErr {
			t.Fatalf("rejection must be a result, not a tool error: %s", text)
		}
		res := decodeResult(t, text)
		if res["accepted"] != false || res["reason"] != "code_mismatch" {
			t.Errorf("result = %v, want accepted=false reason=code_mismatch", res)
		}
	})

	t.Run("missing arguments are invalid_argument", func(t *testing.T) {
		f := newUserLinkFixture(t, nil, nil)
		f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
		fx := (&tier1Fixture{srv: f.srv, q: f.q})

		cases := []map[string]any{
			{"external_user_id": "", "code": "ABCDEF"},
			{"external_user_id": "U123", "code": ""},
			{},
		}
		for _, args := range cases {
			_, isErr, text := fx.callTool(t, "inst-1", "", ToolSubmitIdentityProof, args)
			if !isErr || !strings.Contains(text, "invalid_argument") {
				t.Errorf("args %v: isError=%v text=%q, want invalid_argument", args, isErr, text)
			}
		}
	})

	t.Run("an unauthenticated caller is refused", func(t *testing.T) {
		deps := UserLinkDeps{Querier: &fakeTier1Querier{instances: map[string]db.PluginInstance{}}}
		_, err := deps.submitIdentityProof(context.Background(), []byte(`{"external_user_id":"U1","code":"c"}`))
		if err == nil {
			t.Fatal("expected an error with no identity in context")
		}
		var te *ToolError
		if !asToolError(err, &te) || te.Code != "unauthenticated" {
			t.Errorf("err = %v, want ToolError{unauthenticated}", err)
		}
	})

	t.Run("a binder error is an internal tool error, never a rejection result", func(t *testing.T) {
		binder := &fakeBinder{err: context.DeadlineExceeded}
		f := newUserLinkFixture(t, binder, nil)
		f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
		fx := (&tier1Fixture{srv: f.srv, q: f.q})

		_, isErr, text := fx.callTool(t, "inst-1", "", ToolSubmitIdentityProof,
			map[string]any{"external_user_id": "U123", "code": "ABCDEF"})
		if !isErr || !strings.Contains(text, "internal") {
			t.Errorf("isError=%v text=%q, want internal", isErr, text)
		}
	})
}

// TestSubmitIdentityProof_ResultCarriesOnlyOutcome pins the DoD's
// disqualifying property structurally: even when a proof is ACCEPTED, the
// wire result carries nothing beyond the accept/reject vocabulary — no
// user_id, no external_user_id, no role. A self-asserted identity therefore
// cannot flow from this method's response into actor authorization; only
// the host's own durable link record (written by the binder before it
// returns) can do that, and this test cannot see that record at all.
func TestSubmitIdentityProof_ResultCarriesOnlyOutcome(t *testing.T) {
	binder := &fakeBinder{result: BindResult{Accepted: true}}
	f := newUserLinkFixture(t, binder, nil)
	f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
	fx := (&tier1Fixture{srv: f.srv, q: f.q})

	_, isErr, text := fx.callTool(t, "inst-1", "", ToolSubmitIdentityProof,
		map[string]any{"external_user_id": "U123", "code": "ABCDEF"})
	if isErr {
		t.Fatalf("error: %s", text)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	allowed := map[string]bool{"accepted": true, "reason": true}
	for key := range raw {
		if !allowed[key] {
			t.Errorf("result carries field %q outside the accept/reject vocabulary — a self-asserted identity could ride this into authorization", key)
		}
	}
	for _, disqualifying := range []string{"external_user_id", "user_id", "role", "verified", "instance_id"} {
		if _, present := raw[disqualifying]; present {
			t.Errorf("result echoes %q — identity must never flow through this method's response", disqualifying)
		}
	}
}

func TestGetUserConfig(t *testing.T) {
	t.Run("nil reader returns empty config, not an error", func(t *testing.T) {
		f := newUserLinkFixture(t, nil, nil)
		f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
		fx := (&tier1Fixture{srv: f.srv, q: f.q})

		_, isErr, text := fx.callTool(t, "inst-1", "", ToolGetUserConfig, map[string]any{"external_user_id": "U123"})
		if isErr {
			t.Fatalf("error: %s", text)
		}
		if got := decodeResult(t, text)["user_config_json"]; got != "{}" {
			t.Errorf("user_config_json = %v, want {}", got)
		}
	})

	t.Run("a configured reader is called with the instance and external user, and its config passes through", func(t *testing.T) {
		reader := &fakeUserConfigReader{cfg: json.RawMessage(`{"delivery":"direct"}`)}
		f := newUserLinkFixture(t, nil, reader)
		f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
		fx := (&tier1Fixture{srv: f.srv, q: f.q})

		_, isErr, text := fx.callTool(t, "inst-1", "", ToolGetUserConfig, map[string]any{"external_user_id": "U123"})
		if isErr {
			t.Fatalf("error: %s", text)
		}
		if got := decodeResult(t, text)["user_config_json"]; got != `{"delivery":"direct"}` {
			t.Errorf("user_config_json = %v", got)
		}
		if reader.calledInstanceID != "inst-1" || reader.calledExternalUserID != "U123" {
			t.Errorf("reader called with (%q, %q), want (inst-1, U123)", reader.calledInstanceID, reader.calledExternalUserID)
		}
	})

	t.Run("a reader returning nil is normalized to an empty object", func(t *testing.T) {
		reader := &fakeUserConfigReader{cfg: nil}
		f := newUserLinkFixture(t, nil, reader)
		f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
		fx := (&tier1Fixture{srv: f.srv, q: f.q})

		_, isErr, text := fx.callTool(t, "inst-1", "", ToolGetUserConfig, map[string]any{"external_user_id": "U123"})
		if isErr {
			t.Fatalf("error: %s", text)
		}
		if got := decodeResult(t, text)["user_config_json"]; got != "{}" {
			t.Errorf("user_config_json = %v, want {}", got)
		}
	})

	t.Run("missing external_user_id is invalid_argument", func(t *testing.T) {
		f := newUserLinkFixture(t, nil, nil)
		f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
		fx := (&tier1Fixture{srv: f.srv, q: f.q})

		_, isErr, text := fx.callTool(t, "inst-1", "", ToolGetUserConfig, map[string]any{})
		if !isErr || !strings.Contains(text, "invalid_argument") {
			t.Errorf("isError=%v text=%q, want invalid_argument", isErr, text)
		}
	})

	t.Run("a reader error is an internal tool error", func(t *testing.T) {
		reader := &fakeUserConfigReader{err: context.DeadlineExceeded}
		f := newUserLinkFixture(t, nil, reader)
		f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
		fx := (&tier1Fixture{srv: f.srv, q: f.q})

		_, isErr, text := fx.callTool(t, "inst-1", "", ToolGetUserConfig, map[string]any{"external_user_id": "U123"})
		if !isErr || !strings.Contains(text, "internal") {
			t.Errorf("isError=%v text=%q, want internal", isErr, text)
		}
	})

	t.Run("an unauthenticated caller is refused", func(t *testing.T) {
		deps := UserLinkDeps{Querier: &fakeTier1Querier{instances: map[string]db.PluginInstance{}}}
		_, err := deps.getUserConfig(context.Background(), []byte(`{"external_user_id":"U1"}`))
		if err == nil {
			t.Fatal("expected an error with no identity in context")
		}
		var te *ToolError
		if !asToolError(err, &te) || te.Code != "unauthenticated" {
			t.Errorf("err = %v, want ToolError{unauthenticated}", err)
		}
	})
}

func TestUserLinkTools_ReachableThroughRegisteredInventory(t *testing.T) {
	// DoD: both methods reachable and authenticated over the host endpoint,
	// not merely callable as Go functions. TestToolDispatch already pins the
	// tools/list ordering for the six Tier-1 methods; this pins that the two
	// new ones mount cleanly and dispatch through the same path.
	f := newUserLinkFixture(t, &fakeBinder{result: BindResult{Accepted: false, Reason: ReasonNoPendingLink}}, &fakeUserConfigReader{cfg: json.RawMessage(`{}`)})
	f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod"}
	fx := (&tier1Fixture{srv: f.srv, q: f.q})

	for _, tool := range []string{ToolSubmitIdentityProof, ToolGetUserConfig} {
		args := map[string]any{"external_user_id": "U123", "code": "ABCDEF"}
		_, isErr, text := fx.callTool(t, "inst-1", "", tool, args)
		if isErr {
			t.Errorf("tool %s: error: %s", tool, text)
		}
	}
}

// asToolError is a small helper mirroring errors.As without importing
// errors into every test that just wants the *ToolError out.
func asToolError(err error, target **ToolError) bool {
	te, ok := err.(*ToolError)
	if !ok {
		return false
	}
	*target = te
	return true
}
