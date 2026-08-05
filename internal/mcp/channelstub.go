package mcp

import (
	"encoding/json"
	"net/http"
	"sync"
)

// FakeChannelServer is an in-process MCP server implementing the
// `io.gleipnir/channel` extension, for testing the host client without a real
// plugin (the real one lands in M19). Test-only seam; no production code
// reaches it — same posture as FakeMCPServer, and stdlib-only for the same
// reason: a production-imported package must never be coupled to *testing.T,
// and a handler goroutine that called t.Fatal would be doing so illegally.
//
//	stub := mcp.NewFakeChannelServer()
//	srv := httptest.NewServer(stub); t.Cleanup(srv.Close)
//	client := mcp.NewClient(srv.URL, mcp.WithProtocolVersion(mcp.ProtocolVersion20260728))
//
// It models a channel as a task store: channel/request opens a task, the test
// drives it to a terminal state with Complete/Fail, and tasks/get reports
// whatever state it is in. That is deliberately the whole implementation —
// the extension's design claim is that a channel wait is an ordinary Tasks
// task, and a stub that needed more machinery than this would be evidence
// against the claim.
type FakeChannelServer struct {
	mu sync.Mutex

	// Assurance and Deliveries shape the capability declaration returned by
	// initialize. Defaults are an authenticated channel supporting both
	// delivery targets.
	Assurance  ChannelAssurance
	Deliveries []ChannelDelivery

	// DeclareExtension controls whether the capability entry appears at all.
	// False models a plain MCP server that does no channel work.
	DeclareExtension bool

	// RawCapability, when non-nil, replaces the rendered capability entry
	// verbatim — the seam for testing a malformed declaration.
	RawCapability json.RawMessage

	// Notifications records every channel/notify the server received, in
	// order, so a test can assert on fan-out without inspecting the wire.
	Notifications []ChannelNotification

	// Requests records every channel/request received.
	Requests []ChannelRequestParams

	// PollIntervalMs and TTLMs are echoed on task responses; zero omits them.
	PollIntervalMs int
	TTLMs          int

	tasks  map[string]*fakeChannelTask
	nextID int
}

type fakeChannelTask struct {
	status        TaskStatusValue
	statusMessage string
	result        json.RawMessage
}

// NewFakeChannelServer returns a stub declaring an authenticated channel that
// supports both delivery targets.
func NewFakeChannelServer() *FakeChannelServer {
	return &FakeChannelServer{
		Assurance:        ChannelAssuranceAuthenticated,
		Deliveries:       []ChannelDelivery{ChannelDeliveryDirect, ChannelDeliveryShared},
		DeclareExtension: true,
		tasks:            make(map[string]*fakeChannelTask),
	}
}

// CompleteTask drives an open task to completed with the given resolution —
// what a human clicking a button eventually produces.
func (f *FakeChannelServer) CompleteTask(taskID, optionID, actorExternalID string, content json.RawMessage) {
	result, _ := json.Marshal(channelResolutionWire{ //nolint:errcheck // fixed shapes only
		OptionID:        optionID,
		Content:         content,
		ActorExternalID: actorExternalID,
	})

	f.mu.Lock()
	defer f.mu.Unlock()
	if task, ok := f.tasks[taskID]; ok {
		task.status = TaskStatusCompleted
		task.result = result
	}
}

// ExpireTask drives a task to failed with a TTL-expiry message — the state a
// server settles on when nobody answered before its own clock ran out.
func (f *FakeChannelServer) ExpireTask(taskID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if task, ok := f.tasks[taskID]; ok {
		task.status = TaskStatusFailed
		task.statusMessage = "task ttl expired before the recipient answered"
	}
}

// TaskStatusOf reports a task's current status, for assertions.
func (f *FakeChannelServer) TaskStatusOf(taskID string) TaskStatusValue {
	f.mu.Lock()
	defer f.mu.Unlock()
	if task, ok := f.tasks[taskID]; ok {
		return task.status
	}
	return ""
}

// NotificationCount returns how many channel/notify calls landed.
func (f *FakeChannelServer) NotificationCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Notifications)
}

// LastNotification returns the most recent notification and whether one exists.
func (f *FakeChannelServer) LastNotification() (ChannelNotification, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Notifications) == 0 {
		return ChannelNotification{}, false
	}
	return f.Notifications[len(f.Notifications)-1], true
}

// LastRequest returns the most recent request and whether one exists.
func (f *FakeChannelServer) LastRequest() (ChannelRequestParams, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Requests) == 0 {
		return ChannelRequestParams{}, false
	}
	return f.Requests[len(f.Requests)-1], true
}

func (f *FakeChannelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Mcp-Session-Id", "fake-channel-session")

	switch req.Method {
	case methodServerDiscover:
		// server/discover IS the modern handshake: the 2026-07-28 transport is
		// stateless, so this is the only place a modern server declares
		// anything, extensions included.
		f.writeResult(w, req.ID, f.discoverResult())
	case "initialize":
		f.writeResult(w, req.ID, f.initializeResult())
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case methodChannelNotify:
		f.handleNotify(w, req.ID, req.Params)
	case methodChannelRequest:
		f.handleRequest(w, req.ID, req.Params)
	case methodTasksGet:
		f.handleTasksGet(w, req.ID, req.Params)
	case methodTasksCancel:
		f.handleTasksCancel(w, req.ID, req.Params)
	default:
		f.writeError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

// discoverResult renders the modern handshake response.
func (f *FakeChannelServer) discoverResult() map[string]any {
	return map[string]any{
		"supportedVersions": []string{ProtocolVersion20260728},
		"capabilities":      f.capabilities(),
		"_meta": map[string]any{
			"io.modelcontextprotocol/serverInfo": map[string]any{
				"name": "fake-channel", "version": "0.0.1",
			},
		},
	}
}

func (f *FakeChannelServer) initializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": ProtocolVersion20260728,
		"capabilities":    f.capabilities(),
		"serverInfo":      map[string]any{"name": "fake-channel", "version": "0.0.1"},
	}
}

// capabilities renders the declaration shared by both handshakes.
func (f *FakeChannelServer) capabilities() map[string]any {
	capabilities := map[string]any{}

	f.mu.Lock()
	declare := f.DeclareExtension
	raw := f.RawCapability
	assurance := f.Assurance
	deliveries := append([]ChannelDelivery(nil), f.Deliveries...)
	f.mu.Unlock()

	if declare {
		var entry any
		if raw != nil {
			entry = raw
		} else {
			names := make([]string, len(deliveries))
			for i, d := range deliveries {
				names[i] = string(d)
			}
			entry = map[string]any{
				"version":    ExtensionChannelVersion,
				"assurance":  string(assurance),
				"deliveries": names,
			}
		}
		capabilities["extensions"] = map[string]any{ExtensionChannel: entry}
	}
	return capabilities
}

func (f *FakeChannelServer) handleNotify(w http.ResponseWriter, id, params json.RawMessage) {
	var p channelNotifyParams
	if err := json.Unmarshal(params, &p); err != nil {
		f.writeError(w, id, -32602, "invalid params")
		return
	}

	f.mu.Lock()
	f.Notifications = append(f.Notifications, ChannelNotification{
		Target: ChannelTarget{
			Delivery: ChannelDelivery(p.Target.Delivery),
			Address:  p.Target.Address,
		},
		Message: p.Message,
	})
	f.mu.Unlock()

	// Fire-and-forget still answers the RPC: the acknowledgement says the
	// channel accepted the message, not that anyone read it.
	f.writeResult(w, id, map[string]any{})
}

func (f *FakeChannelServer) handleRequest(w http.ResponseWriter, id, params json.RawMessage) {
	var p channelRequestParamsWire
	if err := json.Unmarshal(params, &p); err != nil {
		f.writeError(w, id, -32602, "invalid params")
		return
	}

	options := make([]ChannelOption, len(p.Options))
	for i, o := range p.Options {
		options[i] = ChannelOption(o)
	}

	f.mu.Lock()
	f.nextID++
	taskID := "task-" + itoa(f.nextID)
	f.tasks[taskID] = &fakeChannelTask{status: TaskStatusWorking}
	f.Requests = append(f.Requests, ChannelRequestParams{
		Target: ChannelTarget{
			Delivery: ChannelDelivery(p.Target.Delivery),
			Address:  p.Target.Address,
		},
		Message:         p.Message,
		RequestedSchema: p.RequestedSchema,
		Options:         options,
		Kind:            ElicitationKind(p.Kind),
	})
	f.mu.Unlock()

	f.writeResult(w, id, f.taskEnvelope(taskID))
}

func (f *FakeChannelServer) handleTasksGet(w http.ResponseWriter, id, params json.RawMessage) {
	var p tasksGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		f.writeError(w, id, -32602, "invalid params")
		return
	}
	f.mu.Lock()
	_, ok := f.tasks[p.TaskID]
	f.mu.Unlock()
	if !ok {
		f.writeError(w, id, -32602, "unknown task")
		return
	}
	f.writeResult(w, id, f.taskEnvelope(p.TaskID))
}

func (f *FakeChannelServer) handleTasksCancel(w http.ResponseWriter, id, params json.RawMessage) {
	var p tasksCancelParams
	if err := json.Unmarshal(params, &p); err != nil {
		f.writeError(w, id, -32602, "invalid params")
		return
	}

	f.mu.Lock()
	task, ok := f.tasks[p.TaskID]
	if ok && !task.status.Terminal() {
		task.status = TaskStatusCancelled
	}
	f.mu.Unlock()
	if !ok {
		f.writeError(w, id, -32602, "unknown task")
		return
	}
	f.writeResult(w, id, f.taskEnvelope(p.TaskID))
}

// taskEnvelope renders a task in the Tasks-extension result shape.
func (f *FakeChannelServer) taskEnvelope(taskID string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	task := f.tasks[taskID]
	out := map[string]any{
		"taskId": taskID,
		"status": string(task.status),
	}
	if task.statusMessage != "" {
		out["statusMessage"] = task.statusMessage
	}
	if task.result != nil {
		out["result"] = task.result
	}
	if f.PollIntervalMs > 0 {
		out["pollIntervalMs"] = f.PollIntervalMs
	}
	if f.TTLMs > 0 {
		out["ttlMs"] = f.TTLMs
	}
	return out
}

func (f *FakeChannelServer) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	body := map[string]any{"jsonrpc": "2.0", "result": result}
	if len(id) > 0 {
		body["id"] = id
	}
	json.NewEncoder(w).Encode(body) //nolint:errcheck // test seam
}

func (f *FakeChannelServer) writeError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	body := map[string]any{
		"jsonrpc": "2.0",
		"error":   map[string]any{"code": code, "message": message},
	}
	if len(id) > 0 {
		body["id"] = id
	}
	json.NewEncoder(w).Encode(body) //nolint:errcheck // test seam
}

// itoa avoids importing strconv for one call in a stdlib-only test seam.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
