package channelext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

func setClock(t *testing.T, now time.Time) *time.Time {
	t.Helper()
	cur := now
	orig := timeNow
	timeNow = func() time.Time { return cur }
	t.Cleanup(func() { timeNow = orig })
	return &cur
}

// storeFactories runs a test body against both TaskStore implementations.
func storeFactories(t *testing.T) map[string]func(t *testing.T) TaskStore {
	return map[string]func(t *testing.T) TaskStore{
		"memory": func(t *testing.T) TaskStore { return NewMemoryTaskStore() },
		"file": func(t *testing.T) TaskStore {
			s, err := NewFileTaskStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewFileTaskStore: %v", err)
			}
			return s
		},
	}
}

func requestBody(params map[string]any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": 1, "method": methodChannelRequest, "params": params}
}

func TestDecodeRequest(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "options", body: `{"target":{"delivery":"direct","address":"u1"},"message":"ok?","options":[{"id":"a","label":"A"}]}`},
		{name: "schema", body: `{"target":{"delivery":"shared","address":"s1"},"message":"ticket?","requestedSchema":{"type":"object"},"kind":"information"}`},
		{name: "neither options nor schema", body: `{"target":{"delivery":"direct","address":"u1"},"message":"fyi"}`, wantErr: true},
		{name: "null schema counts as absent", body: `{"target":{"delivery":"direct","address":"u1"},"message":"fyi","requestedSchema":null}`, wantErr: true},
		{name: "empty message", body: `{"target":{"delivery":"direct","address":"u1"},"message":"","options":[{"id":"a"}]}`, wantErr: true},
		{name: "bad delivery", body: `{"target":{"delivery":"dm","address":"u1"},"message":"x","options":[{"id":"a"}]}`, wantErr: true},
		{name: "missing address", body: `{"target":{"delivery":"direct"},"message":"x","options":[{"id":"a"}]}`, wantErr: true},
		{name: "empty option id", body: `{"target":{"delivery":"direct","address":"u1"},"message":"x","options":[{"id":""}]}`, wantErr: true},
		{name: "duplicate option id", body: `{"target":{"delivery":"direct","address":"u1"},"message":"x","options":[{"id":"a"},{"id":"a"}]}`, wantErr: true},
		{name: "unknown fields tolerated", body: `{"target":{"delivery":"direct","address":"u1"},"message":"x","options":[{"id":"a"}],"future":1,"_meta":{}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeRequest(json.RawMessage(tc.body))
			if tc.wantErr != (err != nil) {
				t.Fatalf("decodeRequest() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

type requestHarness struct {
	srv       *mcpserver.Server
	ext       *Extension
	delivered []string
	cancelled []string
	mu        sync.Mutex
}

func newRequestHarness(t *testing.T, requestErr error, opts ...Option) *requestHarness {
	t.Helper()
	h := &requestHarness{}
	all := append([]Option{
		WithRequest(func(_ context.Context, taskID string, _ Request) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.delivered = append(h.delivered, taskID)
			return requestErr
		}),
		WithOnCancel(func(_ context.Context, taskID, _ string) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.cancelled = append(h.cancelled, taskID)
		}),
	}, opts...)
	ext, err := New(validDeclaration(), func(context.Context, Notification) error { return nil }, all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.ext = ext
	h.srv = mcpserver.NewServer("test-plugin", "0.1.0")
	if err := h.srv.Mount(ext); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return h
}

func (h *requestHarness) call(t *testing.T, method string, params map[string]any) jsonrpcEnvelope {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
	return decodeEnvelope(t, postJSON(t, h.srv, body, modernHeaders(method)))
}

func (h *requestHarness) open(t *testing.T) string {
	t.Helper()
	env := h.call(t, methodChannelRequest, map[string]any{
		"target":  map[string]any{"delivery": "direct", "address": "u1"},
		"message": "approve?",
		"options": []map[string]any{{"id": "approve", "label": "Approve"}},
	})
	if env.Error != nil {
		t.Fatalf("channel/request: %+v", env.Error)
	}
	return env.Result["taskId"].(string)
}

func TestChannelRequest_EnvelopeAndValidation(t *testing.T) {
	setClock(t, time.Unix(1_700_000_000, 0))
	h := newRequestHarness(t, nil)

	env := h.call(t, methodChannelRequest, map[string]any{
		"target":  map[string]any{"delivery": "direct", "address": "u1"},
		"message": "approve?",
		"options": []map[string]any{{"id": "approve", "label": "Approve"}},
	})
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	if id, _ := env.Result["taskId"].(string); !taskIDPattern.MatchString(id) {
		t.Errorf("taskId = %q, want a 128-bit hex id", id)
	}
	if env.Result["status"] != "working" {
		t.Errorf("status = %v, want working", env.Result["status"])
	}
	if env.Result["pollIntervalMs"] != float64(DefaultPollInterval.Milliseconds()) {
		t.Errorf("pollIntervalMs = %v, want default", env.Result["pollIntervalMs"])
	}
	if env.Result["ttlMs"] != float64(DefaultTaskTTL.Milliseconds()) {
		t.Errorf("ttlMs = %v, want default", env.Result["ttlMs"])
	}

	bad := h.call(t, methodChannelRequest, map[string]any{
		"target":  map[string]any{"delivery": "direct", "address": "u1"},
		"message": "just fyi",
	})
	if bad.Error == nil || bad.Error.Code != mcpserver.ErrCodeInvalidParams {
		t.Fatalf("error = %+v, want invalid params for a request with neither options nor requestedSchema", bad.Error)
	}
	if len(h.delivered) != 1 {
		t.Errorf("RequestFunc ran %d times, want 1 (the refused request must not reach it)", len(h.delivered))
	}
}

func TestChannelRequest_DeliveryFailureFailsTheTask(t *testing.T) {
	h := newRequestHarness(t, errors.New("no such user"))
	env := h.call(t, methodChannelRequest, map[string]any{
		"target":  map[string]any{"delivery": "direct", "address": "u1"},
		"message": "approve?",
		"options": []map[string]any{{"id": "approve"}},
	})
	if env.Error == nil {
		t.Fatal("a failing RequestFunc produced no JSON-RPC error")
	}
	task, err := h.ext.Tasks().Get(context.Background(), h.delivered[0])
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if task.Status != TaskFailed {
		t.Errorf("status = %q, want failed so no task is left open for a question nobody saw", task.Status)
	}
}

func TestTasksGet_BeforeAndAfterCompletion(t *testing.T) {
	setClock(t, time.Unix(1_700_000_000, 0))
	h := newRequestHarness(t, nil)
	id := h.open(t)

	before := h.call(t, methodTasksGet, map[string]any{"taskId": id})
	if before.Result["status"] != "working" || before.Result["result"] != nil {
		t.Fatalf("before completion = %v, want working with no result", before.Result)
	}

	if _, err := h.ext.Tasks().Complete(context.Background(), id, Resolution{OptionID: "approve", ActorExternalID: "U1"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	after := h.call(t, methodTasksGet, map[string]any{"taskId": id})
	if after.Result["status"] != "completed" {
		t.Fatalf("status = %v, want completed", after.Result["status"])
	}
	result, _ := after.Result["result"].(map[string]any)
	if result["optionId"] != "approve" || result["actorExternalId"] != "U1" {
		t.Errorf("result = %v, want optionId approve by U1", result)
	}
}

func TestTasksGet_UnknownTask(t *testing.T) {
	h := newRequestHarness(t, nil)
	for _, id := range []string{"0123456789abcdef0123456789abcdef", "../../etc/passwd"} {
		env := h.call(t, methodTasksGet, map[string]any{"taskId": id})
		if env.Error == nil || env.Error.Code != mcpserver.ErrCodeInvalidParams {
			t.Errorf("tasks/get %q error = %+v, want invalid params", id, env.Error)
		}
	}
}

func TestTasksGet_TTLExpiryIsTerminalNotCompleted(t *testing.T) {
	now := setClock(t, time.Unix(1_700_000_000, 0))
	h := newRequestHarness(t, nil, WithTaskTiming(time.Second, time.Minute))
	id := h.open(t)

	*now = now.Add(time.Minute)
	env := h.call(t, methodTasksGet, map[string]any{"taskId": id})
	if env.Result["status"] != "failed" || env.Result["result"] != nil {
		t.Fatalf("after TTL = %v, want failed with no fabricated result", env.Result)
	}

	// An answer arriving after expiry must not resurrect the task.
	_, err := h.ext.Tasks().Complete(context.Background(), id, Resolution{OptionID: "approve"})
	if !errors.Is(err, ErrTaskTerminal) {
		t.Errorf("Complete after expiry = %v, want ErrTaskTerminal", err)
	}
}

func TestTasksCancel_HookRunsOnlyWhenCancelWins(t *testing.T) {
	setClock(t, time.Unix(1_700_000_000, 0))

	t.Run("cancel wins", func(t *testing.T) {
		h := newRequestHarness(t, nil)
		id := h.open(t)
		env := h.call(t, methodTasksCancel, map[string]any{"taskId": id})
		if env.Result["status"] != "cancelled" {
			t.Fatalf("status = %v, want cancelled", env.Result["status"])
		}
		if len(h.cancelled) != 1 || h.cancelled[0] != id {
			t.Errorf("cancel hook calls = %v, want exactly [%s]", h.cancelled, id)
		}
		// A second cancel is idempotent and does not re-run the hook.
		h.call(t, methodTasksCancel, map[string]any{"taskId": id})
		if len(h.cancelled) != 1 {
			t.Errorf("cancel hook ran %d times, want 1", len(h.cancelled))
		}
	})

	t.Run("cancel loses to completion", func(t *testing.T) {
		h := newRequestHarness(t, nil)
		id := h.open(t)
		if _, err := h.ext.Tasks().Complete(context.Background(), id, Resolution{OptionID: "approve"}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		env := h.call(t, methodTasksCancel, map[string]any{"taskId": id})
		if env.Error != nil {
			t.Fatalf("cancel of a completed task errored: %+v", env.Error)
		}
		if env.Result["status"] != "completed" {
			t.Errorf("status = %v, want completed (not resurrected or overwritten)", env.Result["status"])
		}
		if len(h.cancelled) != 0 {
			t.Errorf("cancel hook ran for a task that was already answered: %v", h.cancelled)
		}
	})
}

func TestWithOptions_Validation(t *testing.T) {
	noop := func(context.Context, Notification) error { return nil }
	req := WithRequest(func(context.Context, string, Request) error { return nil })
	hook := WithOnCancel(func(context.Context, string, string) {})

	tests := []struct {
		name string
		opts []Option
	}{
		{name: "nil request", opts: []Option{WithRequest(nil)}},
		{name: "nil store", opts: []Option{req, WithTaskStore(nil)}},
		{name: "nil cancel hook", opts: []Option{req, WithOnCancel(nil)}},
		{name: "cancel hook without request", opts: []Option{hook}},
		{name: "non-positive timing", opts: []Option{req, WithTaskTiming(0, time.Hour)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(validDeclaration(), noop, tc.opts...); err == nil {
				t.Fatal("New() succeeded, want an error")
			}
		})
	}

	ext, err := New(validDeclaration(), noop, req, hook)
	if err != nil {
		t.Fatalf("New with request and hook: %v", err)
	}
	for _, m := range []string{methodChannelNotify, methodChannelRequest, methodTasksGet, methodTasksCancel} {
		if _, ok := ext.Methods()[m]; !ok {
			t.Errorf("Methods() missing %q", m)
		}
	}
}

func TestTaskStore_Lifecycle(t *testing.T) {
	for name, newStore := range storeFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			ctx := context.Background()
			created, err := store.Create(ctx, NewTask{CreatedAt: timeNow(), TTL: time.Hour})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			other, err := store.Create(ctx, NewTask{CreatedAt: timeNow(), TTL: time.Hour})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if created.ID == other.ID {
				t.Fatal("two tasks share an id")
			}

			tests := []struct {
				name string
				res  Resolution
				want error
			}{
				{name: "neither field", res: Resolution{ActorExternalID: "U1"}, want: ErrInvalidResolution},
				{name: "empty content only", res: Resolution{Content: json.RawMessage{}}, want: ErrInvalidResolution},
			}
			for _, tc := range tests {
				if _, err := store.Complete(ctx, created.ID, tc.res); !errors.Is(err, tc.want) {
					t.Errorf("%s: Complete = %v, want %v", tc.name, err, tc.want)
				}
			}
			if got, _ := store.Get(ctx, created.ID); got.Status != TaskWorking {
				t.Fatalf("a refused resolution changed the task to %q", got.Status)
			}

			done, err := store.Complete(ctx, created.ID, Resolution{Content: json.RawMessage(`{"ticket":"OPS-1"}`), ActorExternalID: "U1"})
			if err != nil || done.Status != TaskCompleted {
				t.Fatalf("Complete = %+v, %v", done, err)
			}
			for _, step := range []func() (Task, error){
				func() (Task, error) { return store.Complete(ctx, created.ID, Resolution{OptionID: "x"}) },
				func() (Task, error) { return store.Cancel(ctx, created.ID) },
				func() (Task, error) { return store.Fail(ctx, created.ID, "late") },
			} {
				got, err := step()
				if !errors.Is(err, ErrTaskTerminal) {
					t.Errorf("write after terminal = %v, want ErrTaskTerminal", err)
				}
				if got.Status != TaskCompleted || got.Result == nil || got.Result.ActorExternalID != "U1" {
					t.Errorf("loser was handed %+v, want the winning completed task", got)
				}
			}

			if _, err := store.Get(ctx, "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrTaskNotFound) {
				t.Errorf("Get unknown = %v, want ErrTaskNotFound", err)
			}
			if _, err := store.Cancel(ctx, "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrTaskNotFound) {
				t.Errorf("Cancel unknown = %v, want ErrTaskNotFound", err)
			}
		})
	}
}

// TestTaskStore_CompleteVsCancelRace proves exactly one terminal write wins
// under contention; run with -race.
func TestTaskStore_CompleteVsCancelRace(t *testing.T) {
	for name, newStore := range storeFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			ctx := context.Background()
			task, err := store.Create(ctx, NewTask{CreatedAt: timeNow(), TTL: time.Hour})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			const writers = 16
			var wins atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range writers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					var err error
					if i%2 == 0 {
						_, err = store.Complete(ctx, task.ID, Resolution{OptionID: "approve", ActorExternalID: "U1"})
					} else {
						_, err = store.Cancel(ctx, task.ID)
					}
					switch {
					case err == nil:
						wins.Add(1)
					case !errors.Is(err, ErrTaskTerminal):
						t.Errorf("writer %d: unexpected error %v", i, err)
					}
				}()
			}
			close(start)
			wg.Wait()

			if got := wins.Load(); got != 1 {
				t.Fatalf("%d writers won, want exactly 1", got)
			}
			final, err := store.Get(ctx, task.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !final.Status.Terminal() {
				t.Fatalf("final status %q is not terminal", final.Status)
			}
			if (final.Status == TaskCompleted) != (final.Result != nil) {
				t.Errorf("status %q and result %v disagree", final.Status, final.Result)
			}
		})
	}
}

func TestFileTaskStore_SurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	first, err := NewFileTaskStore(dir)
	if err != nil {
		t.Fatalf("NewFileTaskStore: %v", err)
	}
	open, err := first.Create(ctx, NewTask{CreatedAt: timeNow(), TTL: time.Hour})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	answered, err := first.Create(ctx, NewTask{CreatedAt: timeNow(), TTL: time.Hour})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := first.Complete(ctx, answered.ID, Resolution{OptionID: "approve", ActorExternalID: "U1"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	reopened, err := NewFileTaskStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Get(ctx, open.ID)
	if err != nil || got.Status != TaskWorking || got.TTL != time.Hour || !got.CreatedAt.Equal(open.CreatedAt) {
		t.Errorf("open task after reopen = %+v, %v", got, err)
	}
	got, err = reopened.Get(ctx, answered.ID)
	if err != nil || got.Status != TaskCompleted || got.Result == nil || got.Result.OptionID != "approve" {
		t.Errorf("answered task after reopen = %+v, %v", got, err)
	}
	if _, err := reopened.Cancel(ctx, answered.ID); !errors.Is(err, ErrTaskTerminal) {
		t.Errorf("Cancel after reopen = %v, want ErrTaskTerminal (terminal state is durable)", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != taskFileExt {
			t.Errorf("stray file %q left behind in the store directory", e.Name())
		}
	}
}

func TestFileTaskStore_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTaskStore(filepath.Join(dir, "tasks"))
	if err != nil {
		t.Fatalf("NewFileTaskStore: %v", err)
	}
	// A file one level up that a traversing id would address.
	if err := os.WriteFile(filepath.Join(dir, "secret.json"), []byte(`{"id":"x","status":"working"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, id := range []string{"../secret", "..", "", "a/b", `..\secret`, "ABCDEF0123456789ABCDEF0123456789"} {
		if _, err := store.Get(context.Background(), id); !errors.Is(err, ErrTaskNotFound) {
			t.Errorf("Get(%q) = %v, want ErrTaskNotFound", id, err)
		}
		if _, err := store.Cancel(context.Background(), id); !errors.Is(err, ErrTaskNotFound) {
			t.Errorf("Cancel(%q) = %v, want ErrTaskNotFound", id, err)
		}
	}
}

func TestMountedExtension_RequestServedOverHTTP(t *testing.T) {
	h := newRequestHarness(t, nil)
	rec := postJSON(t, h.srv, requestBody(map[string]any{
		"target":          map[string]any{"delivery": "shared", "address": "s1"},
		"message":         "ticket?",
		"requestedSchema": map[string]any{"type": "object"},
	}), modernHeaders(methodChannelRequest))
	if env := decodeEnvelope(t, rec); env.Error != nil || env.Result["taskId"] == "" {
		t.Fatalf("schema request = %+v / %+v", env.Result, env.Error)
	}
}

// A late answer must not complete a wait whose TTL passed, even when no
// tasks/get observed the expiry first: the store records the expiry instead.
func TestComplete_AfterTTLRecordsExpiry(t *testing.T) {
	for name, newStore := range storeFactories(t) {
		t.Run(name, func(t *testing.T) {
			clock := setClock(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			store := newStore(t)
			ctx := context.Background()

			task, err := store.Create(ctx, NewTask{CreatedAt: *clock, TTL: time.Minute})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			*clock = clock.Add(time.Minute)

			got, err := store.Complete(ctx, task.ID, Resolution{OptionID: "approve", ActorExternalID: "U1"})
			if !errors.Is(err, ErrTaskExpired) || !errors.Is(err, ErrTaskTerminal) {
				t.Fatalf("Complete after TTL: err = %v, want ErrTaskExpired wrapping ErrTaskTerminal", err)
			}
			if got.Status != TaskFailed || got.Result != nil {
				t.Fatalf("returned task = %+v, want failed with no result", got)
			}

			stored, err := store.Get(ctx, task.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if stored.Status != TaskFailed || stored.StatusMessage != expiredMessage {
				t.Fatalf("stored task = %+v, want the expiry persisted", stored)
			}
			if _, err := store.Cancel(ctx, task.ID); !errors.Is(err, ErrTaskTerminal) {
				t.Fatalf("Cancel after expiry: err = %v, want ErrTaskTerminal", err)
			}
		})
	}
}
