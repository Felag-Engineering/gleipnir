package hostendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// fakeAuthorizeQuerier is an in-memory AuthorizeActorQuerier with an audit
// recorder — a separate fake from tier1_test.go's fakeTier1Querier because
// the two Deps types carry disjoint interfaces on purpose.
type fakeAuthorizeQuerier struct {
	instances map[string]db.PluginInstance
	audits    []db.InsertPluginAuditEventParams
}

func (f *fakeAuthorizeQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	inst, ok := f.instances[id]
	if !ok {
		return db.PluginInstance{}, errors.New("no such instance")
	}
	return inst, nil
}

func (f *fakeAuthorizeQuerier) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	f.audits = append(f.audits, arg)
	return db.PluginAuditEvent{}, nil
}

// fakeActorDirectory is a hand-rolled ActorDirectory — the #18 seam under
// test: nothing here reaches an actual DB, pinning that AuthorizeActor's
// handler is written against the interface, not against DBActorDirectory.
type fakeActorDirectory struct {
	byExternalID map[string]ActorResolution
	err          error
}

func (f *fakeActorDirectory) Resolve(_ context.Context, actorExternalID string) (ActorResolution, bool, error) {
	if f.err != nil {
		return ActorResolution{}, false, f.err
	}
	res, ok := f.byExternalID[actorExternalID]
	return res, ok, nil
}

// fakePollHint records every PollNow call so a test can assert the poll-now
// hint fired (or did not) with the expected request identifier.
type fakePollHint struct {
	calls []string
	err   error
}

func (f *fakePollHint) PollNow(_ context.Context, requestID string) error {
	f.calls = append(f.calls, requestID)
	return f.err
}

// authorizeFixture mounts host/authorize_actor behind the real ServeHTTP
// path, mirroring tier1_test.go's tier1Fixture without touching that file.
type authorizeFixture struct {
	srv *Server
	q   *fakeAuthorizeQuerier
	dir *fakeActorDirectory
}

func newAuthorizeFixture(t *testing.T, poll PollHint) *authorizeFixture {
	t.Helper()
	q := &fakeAuthorizeQuerier{instances: map[string]db.PluginInstance{
		"inst-a": {ID: "inst-a", PluginID: "plug-a", InstanceName: "slack-prod"},
	}}
	dir := &fakeActorDirectory{byExternalID: map[string]ActorResolution{}}
	srv := NewServer()
	srv.Register(AuthorizeActorTools(AuthorizeActorDeps{
		Querier:   q,
		Directory: dir,
		PollHint:  poll,
	})...)
	return &authorizeFixture{srv: srv, q: q, dir: dir}
}

// callTool performs a host/authorize_actor tools/call over ServeHTTP with the
// caller's instance identity in context, the way the middleware chain
// provides it in production.
func (f *authorizeFixture) callTool(t *testing.T, instanceID string, args any) (isError bool, text string) {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": ToolAuthorizeActor, "arguments": args},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
	req.Header.Set("MCP-Protocol-Version", mcp.ProtocolVersion20260728)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", ToolAuthorizeActor)
	ctx := context.WithValue(req.Context(), identityCtxKey{}, Identity{InstanceID: instanceID})
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req.WithContext(ctx))

	var env struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	if len(env.Result.Content) > 0 {
		text = env.Result.Content[0].Text
	}
	return env.Result.IsError, text
}

func TestAuthorizeActor_AuthorizedActor(t *testing.T) {
	poll := &fakePollHint{}
	f := newAuthorizeFixture(t, poll)
	f.dir.byExternalID["U-APPROVER"] = ActorResolution{
		UserID: "user-1", Username: "ann", Roles: []model.Role{model.RoleApprover},
	}

	isErr, text := f.callTool(t, "inst-a", map[string]any{
		"request_id": "task-123", "actor_external_id": "U-APPROVER",
	})
	if isErr {
		t.Fatalf("error: %s", text)
	}
	res := decodeResult(t, text)
	if res["authorized"] != true {
		t.Errorf("authorized = %v, want true", res["authorized"])
	}
	if res["user_id"] != "user-1" {
		t.Errorf("user_id = %v, want user-1", res["user_id"])
	}
	if len(poll.calls) != 1 || poll.calls[0] != "task-123" {
		t.Fatalf("PollNow calls = %v, want exactly [\"task-123\"] — the poll-now hint must fire with the request's own identifier", poll.calls)
	}
	if len(f.q.audits) != 0 {
		t.Errorf("audits = %d, want 0 for an authorized actor", len(f.q.audits))
	}
}

func TestAuthorizeActor_RolesThatMayAuthorize(t *testing.T) {
	for _, role := range []model.Role{model.RoleApprover, model.RoleOperator, model.RoleAdmin} {
		t.Run(string(role), func(t *testing.T) {
			poll := &fakePollHint{}
			f := newAuthorizeFixture(t, poll)
			f.dir.byExternalID["U-X"] = ActorResolution{UserID: "u", Roles: []model.Role{role}}

			isErr, text := f.callTool(t, "inst-a", map[string]any{
				"request_id": "task-1", "actor_external_id": "U-X",
			})
			if isErr {
				t.Fatalf("error: %s", text)
			}
			if decodeResult(t, text)["authorized"] != true {
				t.Errorf("role %s was not authorized", role)
			}
			if len(poll.calls) != 1 {
				t.Errorf("PollNow calls = %d, want 1", len(poll.calls))
			}
		})
	}
}

func TestAuthorizeActor_UnmappedActorIsRefusedAndAudited(t *testing.T) {
	poll := &fakePollHint{}
	f := newAuthorizeFixture(t, poll)
	// U-UNKNOWN is never registered in f.dir.

	isErr, text := f.callTool(t, "inst-a", map[string]any{
		"request_id": "task-999", "actor_external_id": "U-UNKNOWN",
	})
	if isErr {
		t.Fatalf("an unauthorized actor must be a non-error result, got error: %s", text)
	}
	res := decodeResult(t, text)
	if res["authorized"] != false {
		t.Errorf("authorized = %v, want false", res["authorized"])
	}
	if _, ok := res["user_id"]; ok {
		t.Errorf("result carries user_id %v for an unauthorized actor, want none", res["user_id"])
	}

	if len(poll.calls) != 0 {
		t.Fatalf("PollNow calls = %v, want none — an unauthorized actor must not resolve anything", poll.calls)
	}

	if len(f.q.audits) != 1 {
		t.Fatalf("audits = %d, want exactly 1 unauthorized_approval_attempt", len(f.q.audits))
	}
	audit := f.q.audits[0]
	if audit.EventType != EventTypeUnauthorizedApproval || audit.Severity != "high" {
		t.Errorf("audit = %s/%s, want %s/high", audit.EventType, audit.Severity, EventTypeUnauthorizedApproval)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(audit.PayloadJson), &payload); err != nil {
		t.Fatalf("decode audit payload: %v", err)
	}
	if payload["request_id"] != "task-999" || payload["actor_external_id"] != "U-UNKNOWN" {
		t.Errorf("audit payload = %+v, want request_id=task-999 actor_external_id=U-UNKNOWN", payload)
	}
}

func TestAuthorizeActor_AuditorRoleOnlyIsRefusedAndAudited(t *testing.T) {
	poll := &fakePollHint{}
	f := newAuthorizeFixture(t, poll)
	f.dir.byExternalID["U-AUDITOR"] = ActorResolution{
		UserID: "user-2", Username: "audrey", Roles: []model.Role{model.RoleAuditor},
	}

	isErr, text := f.callTool(t, "inst-a", map[string]any{
		"request_id": "task-1", "actor_external_id": "U-AUDITOR",
	})
	if isErr {
		t.Fatalf("error: %s", text)
	}
	if decodeResult(t, text)["authorized"] != false {
		t.Error("a mapped auditor must not be authorized to settle an approval")
	}
	if len(poll.calls) != 0 {
		t.Errorf("PollNow calls = %v, want none", poll.calls)
	}
	if len(f.q.audits) != 1 || f.q.audits[0].EventType != EventTypeUnauthorizedApproval {
		t.Errorf("audits = %+v, want one unauthorized_approval_attempt", f.q.audits)
	}
}

func TestAuthorizeActor_NilPollHintStillAuthorizes(t *testing.T) {
	// Passing a nil PollHint (not a typed-nil *fakePollHint) exercises the
	// interface's own nil check in the handler.
	f := newAuthorizeFixture(t, nil)
	f.dir.byExternalID["U-APPROVER"] = ActorResolution{UserID: "user-1", Roles: []model.Role{model.RoleApprover}}

	isErr, text := f.callTool(t, "inst-a", map[string]any{
		"request_id": "task-1", "actor_external_id": "U-APPROVER",
	})
	if isErr {
		t.Fatalf("error: %s", text)
	}
	if decodeResult(t, text)["authorized"] != true {
		t.Error("authorization must not depend on a PollHint being wired")
	}
}

func TestAuthorizeActor_PollHintFailureDoesNotFlipAuthorization(t *testing.T) {
	poll := &fakePollHint{err: errors.New("scheduler unavailable")}
	f := newAuthorizeFixture(t, poll)
	f.dir.byExternalID["U-APPROVER"] = ActorResolution{UserID: "user-1", Roles: []model.Role{model.RoleApprover}}

	isErr, text := f.callTool(t, "inst-a", map[string]any{
		"request_id": "task-1", "actor_external_id": "U-APPROVER",
	})
	if isErr {
		t.Fatalf("error: %s", text)
	}
	if decodeResult(t, text)["authorized"] != true {
		t.Error("a failed poll-now hint is a latency regression, not a correctness one")
	}
	if len(poll.calls) != 1 {
		t.Errorf("PollNow calls = %d, want 1 (attempted even though it errors)", len(poll.calls))
	}
}

func TestAuthorizeActor_InvalidArguments(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{"missing request_id", map[string]any{"actor_external_id": "U-X"}},
		{"missing actor_external_id", map[string]any{"request_id": "task-1"}},
		{"empty request_id", map[string]any{"request_id": "", "actor_external_id": "U-X"}},
		{"empty actor_external_id", map[string]any{"request_id": "task-1", "actor_external_id": ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthorizeFixture(t, nil)
			isErr, text := f.callTool(t, "inst-a", tc.args)
			if !isErr || !strings.Contains(text, "invalid_argument") {
				t.Errorf("isError=%v text=%q, want invalid_argument", isErr, text)
			}
		})
	}
}

func TestAuthorizeActor_DirectoryErrorIsInternal(t *testing.T) {
	f := newAuthorizeFixture(t, nil)
	f.dir.err = errors.New("db unavailable")

	isErr, text := f.callTool(t, "inst-a", map[string]any{
		"request_id": "task-1", "actor_external_id": "U-X",
	})
	if !isErr || !strings.Contains(text, "internal") {
		t.Errorf("isError=%v text=%q, want internal", isErr, text)
	}
}

func TestAuthorizeActor_RegisteredInInventory(t *testing.T) {
	f := newAuthorizeFixture(t, nil)
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
	req.Header.Set("MCP-Protocol-Version", mcp.ProtocolVersion20260728)
	req.Header.Set("Mcp-Method", "tools/list")
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req)

	var env struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Result.Tools) != 1 || env.Result.Tools[0].Name != ToolAuthorizeActor {
		t.Errorf("tools = %+v, want exactly [%s]", env.Result.Tools, ToolAuthorizeActor)
	}
}

// TestDBActorDirectory_Resolve pins the ADR-058 Tier-1 source directly,
// separately from the handler tests above (which exercise the interface via
// a fake): an unmapped external id resolves to found=false, and a mapped
// user's roles come back verbatim.
func TestDBActorDirectory_Resolve(t *testing.T) {
	q := &fakeSlackUserQuerier{
		bySlackID: map[string][]db.GetUserBySlackUserIDRow{
			"U-KNOWN": {
				{ID: "user-1", Username: "ann", Role: "operator"},
				{ID: "user-1", Username: "ann", Role: "approver"},
			},
		},
	}
	dir := DBActorDirectory{Querier: q}

	t.Run("known actor resolves with every held role", func(t *testing.T) {
		res, found, err := dir.Resolve(context.Background(), "U-KNOWN")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !found || res.UserID != "user-1" || len(res.Roles) != 2 {
			t.Errorf("resolve = %+v found=%v, want user-1 with 2 roles", res, found)
		}
	})

	t.Run("unknown actor is not found", func(t *testing.T) {
		_, found, err := dir.Resolve(context.Background(), "U-UNKNOWN")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if found {
			t.Error("an unmapped external id must not resolve")
		}
	})

	t.Run("querier error propagates", func(t *testing.T) {
		q.err = errors.New("db down")
		_, _, err := dir.Resolve(context.Background(), "U-KNOWN")
		if err == nil {
			t.Fatal("want a propagated error")
		}
	})
}

type fakeSlackUserQuerier struct {
	bySlackID map[string][]db.GetUserBySlackUserIDRow
	err       error
}

func (f *fakeSlackUserQuerier) GetUserBySlackUserID(_ context.Context, slackUserID *string) ([]db.GetUserBySlackUserIDRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	if slackUserID == nil {
		return nil, nil
	}
	return f.bySlackID[*slackUserID], nil
}
