//go:build relaysmoke

package relaysmoke

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// expectedProtocolVersion is the MCP revision this suite requires Relay to
// negotiate. Anything else fails t.Run("protocol") loudly: a silent
// downgrade to the legacy transport would keep every other subtest green
// while quietly disabling Act 2's in-band approval flow.
const expectedProtocolVersion = "2026-07-28"

// relaysmokeTools is Relay's fixed eight-tool catalog (go-sdk v1.8.0). A
// future Relay build is free to add more (t.Run("discover") logs, not fails,
// on an extra tool); it must never lose one of these eight.
var relaysmokeTools = []string{
	"list_nodes", "describe_node", "list_operations", "run_operation",
	"raw_exec", "get_job", "cancel_job", "approve_request",
}

func readerPolicyYAML() string {
	return `
name: relaysmoke-reader
trigger:
  type: manual
capabilities:
  tools:
    - tool: relay.list_nodes
    - tool: relay.run_operation
agent:
  model: claude-opus-4-5
  task: "read the fleet's state"
`
}

// mutatePolicyYAML grants relay.run_operation WITHOUT Gleipnir's own
// approval:required -- Relay owns the gate for this scenario (the lane's
// testdata/approval-gates.json, or Relay's own demo gate in strict mode),
// exactly like #937's mrtrPolicyYAML. It deliberately does NOT grant
// relay.approve_request, matching the demo policy: the only way this run's
// approval question can be answered is Gleipnir's operator tool-input path.
func mutatePolicyYAML() string {
	return `
name: relaysmoke-mutate
trigger:
  type: manual
capabilities:
  tools:
    - tool: relay.run_operation
agent:
  model: claude-opus-4-5
  task: "restart nginx on the production web fleet"
`
}

// lastToolResult returns the LAST tool_result step matching toolName, or nil.
// A run's trace can carry more than one call to the same tool (the mutate
// scenario's original call plus its MRTR retry); the last one is the fake's
// -- here, Relay's -- own final verdict.
func lastToolResult(results []toolResultStep, toolName string) *toolResultStep {
	for i := len(results) - 1; i >= 0; i-- {
		if results[i].ToolName == toolName {
			return &results[i]
		}
	}
	return nil
}

// TestRelaySmoke drives one in-process Gleipnir through a real, compose-started
// Relay demo fleet. Ordered subtests share one harness; there is no
// t.Parallel() anywhere; register's failure aborts the whole test (nothing
// else can do anything without a registered server), and every other subtest
// runs independently so one failure does not hide the rest.
func TestRelaySmoke(t *testing.T) {
	e := loadEnv(t)
	h := newHarness(t, e)
	ctx := context.Background()

	rules := rulesFor(e.RequireMRTR)
	rep := &report{
		RelayRef:        e.RelayRef,
		StrictMRTR:      e.RequireMRTR,
		ApprovalGate:    rules.File,
		RawExecOutcomes: map[string]int{},
	}
	t.Logf("RELAYSMOKE strict MRTR (pre-demo gate) = %v; approval gate = %s", e.RequireMRTR, rules.File)
	t.Cleanup(func() {
		if err := rep.writeTo(e.ReportPath); err != nil {
			t.Errorf("write report to %s: %v", e.ReportPath, err)
		}
	})

	_, pool, err := mcp.ParseCACertBundle(e.CAPEM)
	if err != nil {
		t.Fatalf("parse RELAYSMOKE_CA_FILE: %v", err)
	}

	admin := newRelayAPI(e.APIBase, e.AdminToken, pool)
	approver := newRelayAPI(e.APIBase, e.ApproverToken, pool)

	_, machineToken, err := mintMachineToken(ctx, admin, "gleipnir-smoke")
	if err != nil {
		t.Fatalf("mint machine token for Gleipnir's registration: %v", err)
	}

	adminMCP := mcp.NewClient(e.MCPURL,
		mcp.WithRootCAs(pool),
		mcp.WithAuthHeaders([]mcp.AuthHeader{{Name: "Authorization", Value: "Bearer " + e.AdminToken}}),
		mcp.WithTimeout(120*time.Second),
	)
	waitForFleet(t, ctx, adminMCP, e.ExpectedNodes)

	var serverID, protocolVersion string

	registerOK := t.Run("register", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"name": "relay",
			"url":  e.MCPURL,
			// call_timeout_seconds is #939's per-server override, live-checked
			// here: 120s matches Relay's own approved-retry budget (the
			// approved Job runs synchronously inside the retried tools/call),
			// and this suite asserts effective_call_timeout_seconds below
			// rather than only exercising the field incidentally.
			"call_timeout_seconds": 120,
			"ca_cert_pem":          e.CAPEM,
			"auth_headers": []map[string]string{
				{"key": "Authorization", "value": "Bearer " + machineToken},
			},
			// run_attribution: relay (#943) is the live proof that Relay
			// accepts the asserted attribution headers with no 400 — the
			// point of this manual-only lane.
			"run_attribution": map[string]any{"mode": "relay"},
		})
		if err != nil {
			t.Fatalf("marshal register request: %v", err)
		}

		w := h.do(t, http.MethodPost, "/api/v1/mcp/servers", string(body), model.RoleAdmin)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /api/v1/mcp/servers status = %d, body %s", w.Code, w.Body.String())
		}

		var resp struct {
			Data struct {
				ID                          string  `json:"id"`
				ProtocolVersion             *string `json:"protocol_version"`
				DiscoveryError              *string `json:"discovery_error"`
				EffectiveCallTimeoutSeconds int64   `json:"effective_call_timeout_seconds"`
				RunAttribution              struct {
					Mode string `json:"mode"`
				} `json:"run_attribution"`
			} `json:"data"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode register response: %v", err)
		}
		if resp.Data.DiscoveryError != nil {
			t.Fatalf("registering Relay failed — cause: %s; detail: %s",
				diagnoseRegistration(*resp.Data.DiscoveryError), *resp.Data.DiscoveryError)
		}
		if resp.Data.EffectiveCallTimeoutSeconds != 120 {
			t.Errorf("effective_call_timeout_seconds = %d, want 120 (issue #939's per-server override)", resp.Data.EffectiveCallTimeoutSeconds)
		}
		if resp.Data.RunAttribution.Mode != "relay" {
			t.Errorf("run_attribution.mode = %q, want %q (issue #943)", resp.Data.RunAttribution.Mode, "relay")
		}
		serverID = resp.Data.ID
		if resp.Data.ProtocolVersion != nil {
			protocolVersion = *resp.Data.ProtocolVersion
		}
	})
	if !registerOK {
		t.Fatal("t.Run(\"register\") failed; nothing downstream can run without a registered server")
	}

	t.Run("discover", func(t *testing.T) {
		rows, err := h.store.Queries().ListMCPToolsByServer(ctx, serverID)
		if err != nil {
			t.Fatalf("ListMCPToolsByServer: %v", err)
		}
		byName := make(map[string]db.McpTool, len(rows))
		for _, row := range rows {
			byName[row.Name] = row
		}

		var missing []string
		for _, want := range relaysmokeTools {
			tool, ok := byName[want]
			if !ok {
				missing = append(missing, want)
				continue
			}
			if tool.CanonicalSchema == nil {
				t.Errorf("tool %q has a NULL canonical_schema — schemanorm refused this schema at discovery (fail-open)", want)
				continue
			}
			if !json.Valid([]byte(*tool.CanonicalSchema)) {
				t.Errorf("tool %q canonical_schema is not valid JSON: %s", want, *tool.CanonicalSchema)
			}
		}
		if len(missing) > 0 {
			t.Errorf("Relay is missing expected tools: %v", missing)
		}

		var extra []string
		for name := range byName {
			found := false
			for _, want := range relaysmokeTools {
				if want == name {
					found = true
					break
				}
			}
			if !found {
				extra = append(extra, name)
			}
		}
		if len(extra) > 0 {
			t.Logf("Relay exposes additional tools beyond the fixed eight: %v", extra)
		}
		for name := range byName {
			rep.Tools = append(rep.Tools, name)
		}

		diff, err := h.registry.RefreshTools(ctx, serverID)
		if err != nil {
			t.Fatalf("RefreshTools: %v", err)
		}
		if len(diff.Added) > 0 || len(diff.Removed) > 0 || len(diff.Modified) > 0 {
			t.Errorf("RefreshTools diff = %+v, want empty — canonical form should be stable across two real fetches", diff)
		}
	})

	t.Run("protocol", func(t *testing.T) {
		srv, err := h.store.Queries().GetMCPServer(ctx, serverID)
		if err != nil {
			t.Fatalf("GetMCPServer: %v", err)
		}
		if srv.ProtocolVersion == nil {
			t.Fatal("server has no protocol_version pinned")
		}
		protocolVersion = *srv.ProtocolVersion
		rep.ProtocolVersion = protocolVersion
		t.Logf("RELAYSMOKE negotiated MCP protocol version: %s (relay %s)", protocolVersion, e.RelayRef)

		if protocolVersion != expectedProtocolVersion {
			t.Errorf("negotiated protocol version = %q, want %q — a silent downgrade to the legacy path would keep this lane green while quietly changing behavior, and the gated_mutate step's in-band approval cannot reach a human at all on a legacy pin",
				protocolVersion, expectedProtocolVersion)
		}
	})

	t.Run("reader", func(t *testing.T) {
		llm := testutil.NewMockLLMClient(
			testutil.MakeToolCallResponse("relay.list_nodes", "call-1", map[string]any{
				"selector": "relay:env=dev",
			}),
			testutil.MakeToolCallResponse("relay.run_operation", "call-2", map[string]any{
				"selector":  "relay:env=dev",
				"operation": "file.read",
				"args":      map[string]any{"path": "/etc/os-release"},
			}),
			testutil.MakeTextResponse("done"),
		)
		runID := h.launch(t, "p-relaysmoke-reader", readerPolicyYAML(), llm)

		select {
		case <-h.awaitDone(runID, 3*time.Minute):
		case <-time.After(3 * time.Minute):
			t.Fatalf("reader run did not finish within 3m")
		}
		if status := runStatus(t, h.store, runID); status != "complete" {
			t.Fatalf("reader run status = %s, want complete; error steps: %v", status, errorSteps(t, h.store, runID))
		}

		results := toolResults(t, h.store, runID)
		listResult := lastToolResult(results, "relay.list_nodes")
		if listResult == nil || listResult.IsError {
			t.Fatalf("relay.list_nodes tool_result missing or is_error; results=%+v", results)
		}
		var nodes listNodesOutput
		if err := decodeToolPayload([]byte(listResult.Output), &nodes); err != nil {
			t.Fatalf("decode list_nodes payload: %v", err)
		}
		if len(nodes.Nodes) != e.ExpectedNodes {
			t.Errorf("list_nodes returned %d Nodes, want %d", len(nodes.Nodes), e.ExpectedNodes)
		}
		rep.ReaderNodes = len(nodes.Nodes)

		runResult := lastToolResult(results, "relay.run_operation")
		if runResult == nil || runResult.IsError {
			t.Fatalf("relay.run_operation tool_result missing or is_error; results=%+v", results)
		}
		var out runOrPlanOutput
		if err := decodeToolPayload([]byte(runResult.Output), &out); err != nil {
			t.Fatalf("decode run_operation payload: %v", err)
		}
		if out.JobID == "" {
			t.Error("run_operation job_id is empty")
		}
		if len(out.Results) != e.ExpectedNodes {
			t.Errorf("run_operation returned %d per-node results, want %d", len(out.Results), e.ExpectedNodes)
		}
		seen := make(map[string]bool, len(out.Results))
		var failures []string
		for _, r := range out.Results {
			if seen[r.NodeID] {
				t.Errorf("duplicate node_id %s in run_operation results", r.NodeID)
			}
			seen[r.NodeID] = true
			if r.Outcome != "success" {
				failures = append(failures, fmt.Sprintf("%s->%s (stderr=%q)", r.NodeID, r.Outcome, r.Stderr))
			}
		}
		if len(failures) > 0 {
			t.Errorf("not every Node returned success reading /etc/os-release: %v", failures)
		}
	})

	t.Run("gated_mutate", func(t *testing.T) {
		// The demo's own repair beat (Relay's docs/operations/demo-fleet.md):
		// `node:role=web, node:env=prod` resolves to daemon-4 and daemon-5 of
		// the `demo` compose profile scripts/relaysmoke.sh brings up — a
		// fan-out of 2, so the fleet-wide-mutate rule parks it. Both are
		// role=web Nodes, so daemon-entrypoint.sh links the DEV-ONLY systemctl
		// shim and an approved restart exits 0 rather than failing to spawn
		// systemctl; policy.web.toml allows service.restart of nginx.
		llm := testutil.NewMockLLMClient(
			testutil.MakeToolCallResponse("relay.run_operation", "call-1", map[string]any{
				"selector":  "node:role=web, node:env=prod",
				"operation": "service.restart",
				"args":      map[string]any{"unit": "nginx"},
			}),
			testutil.MakeTextResponse("done"),
		)

		// Subscribed BEFORE launch, so the pause's own event cannot be missed
		// (signal-don't-poll).
		parkedCh := h.pub.subscribe("tool_input.created")
		runID := h.launch(t, "p-relaysmoke-mutate", mutatePolicyYAML(), llm)
		done := h.awaitDone(runID, 4*time.Minute)

		select {
		case <-parkedCh:
			gatedMutateBranchA(t, ctx, h, approver, runID, done, e.RequireMRTR, rules, rep)

		case <-done:
			// gatedMutateBranchB ends in t.Skipf (or, in strict mode,
			// t.Fatalf), both of which call runtime.Goexit() and never return
			// to this call site — rep.MRTR is therefore set INSIDE the branch
			// function, not after this call.
			gatedMutateBranchB(t, ctx, h, approver, runID, e.RelayRef, e.RequireMRTR, rules, rep)

		case <-time.After(4 * time.Minute):
			rep.MRTR = "failed: neither a pause nor run completion within 4m"
			t.Fatalf("gated_mutate: neither the tool_input.created pause nor run completion arrived within 4m")
		}
	})

	t.Run("raw_exec_denied_by_policy", func(t *testing.T) {
		// Called directly through a Gleipnir mcp.Client, never through an
		// agent: the demo policies above never grant raw_exec (ADR-001), and
		// this step needs the founding admin's credential -- Raw Exec is
		// admin-only in Relay.
		pinnedAdmin := mcp.NewClient(e.MCPURL,
			mcp.WithRootCAs(pool),
			mcp.WithAuthHeaders([]mcp.AuthHeader{{Name: "Authorization", Value: "Bearer " + e.AdminToken}}),
			mcp.WithProtocolVersion(protocolVersion),
			mcp.WithTimeout(120*time.Second),
		)

		args := map[string]any{
			"selector": "relay:env=dev",
			"argv":     []string{"/bin/rm", "-rf", "/var/lib/postgresql"},
		}

		r1, err := pinnedAdmin.CallTool(ctx, "raw_exec", args, mcp.CallOptions{})
		if err != nil {
			t.Fatalf("raw_exec (first call): %v", err)
		}
		// This client declares no elicitation capability (CallOptions{} —
		// only a policy-resolved tool gets one, registry.go), so Relay must
		// never ask it a question: under the default lane's out-of-band rule
		// it may not ask anyone (gleipnir-relay#646), and under the demo
		// gate's in-band rule it may ask only a client that can answer.
		if r1.ResultType == mcp.ResultTypeInputRequired {
			t.Fatalf("raw_exec parked with input_required for a client that declared no elicitation capability (rule %s) — Relay must render pending_approval here", rules.RawExec)
		}
		var parked runOrPlanOutput
		if err := decodeToolPayload(r1.Output, &parked); err != nil {
			t.Fatalf("decode raw_exec (first call) payload: %v", err)
		}
		if parked.PendingApproval == nil {
			t.Fatalf("raw_exec did not park — want pending_approval, got %+v", parked)
		}
		if !containsString(parked.PendingApproval.MatchedRules, rules.RawExec) {
			t.Errorf("raw_exec matched_rules = %v, want it to contain %s (gate %s)", parked.PendingApproval.MatchedRules, rules.RawExec, rules.File)
		}

		if err := decide(ctx, approver, parked.PendingApproval.RequestID, parked.PendingApproval.PlanHash, true,
			"relaysmoke: approve a raw_exec every Node Policy refuses"); err != nil {
			t.Fatalf("approve the parked raw_exec: %v", err)
		}

		// Relay releases the identical call by plan hash.
		r2, err := pinnedAdmin.CallTool(ctx, "raw_exec", args, mcp.CallOptions{})
		if err != nil {
			t.Fatalf("raw_exec (retry after approval): %v", err)
		}
		if r2.IsError {
			t.Fatalf("raw_exec retry is_error, output=%s", r2.Output)
		}
		var dispatched runOrPlanOutput
		if err := decodeToolPayload(r2.Output, &dispatched); err != nil {
			t.Fatalf("decode raw_exec retry payload: %v", err)
		}
		if len(dispatched.Results) != e.ExpectedNodes {
			t.Errorf("raw_exec retry returned %d results, want %d", len(dispatched.Results), e.ExpectedNodes)
		}
		for _, r := range dispatched.Results {
			rep.RawExecOutcomes[r.Outcome]++
			if r.Outcome != "denied_by_policy" {
				t.Errorf("node %s outcome = %s, want denied_by_policy — every Node Policy fixture denies this argv", r.NodeID, r.Outcome)
				continue
			}
			if r.ExitCode != nil {
				t.Errorf("node %s has exit_code set on a denied_by_policy result, want absent (nothing executed)", r.NodeID)
			}
			if r.Stderr == "" {
				t.Errorf("node %s denied_by_policy result has empty stderr", r.NodeID)
			}
			if r.RefusalExplanation == "" {
				t.Errorf("node %s denied_by_policy result has empty refusal_explanation", r.NodeID)
			}
		}
		t.Logf("RELAYSMOKE raw_exec outcomes: %+v", rep.RawExecOutcomes)
	})

	t.Run("negative_controls", func(t *testing.T) {
		wrongCAPEM := generateThrowawayCAPEM(t)
		body, err := json.Marshal(map[string]any{
			"name":        "relay-wrong-ca",
			"url":         e.MCPURL,
			"ca_cert_pem": wrongCAPEM,
			"auth_headers": []map[string]string{
				{"key": "Authorization", "value": "Bearer " + machineToken},
			},
		})
		if err != nil {
			t.Fatalf("marshal wrong-CA request: %v", err)
		}
		w := h.do(t, http.MethodPost, "/api/v1/mcp/servers", string(body), model.RoleAdmin)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /api/v1/mcp/servers (wrong CA) status = %d, body %s", w.Code, w.Body.String())
		}
		var wrongCAResp struct {
			Data struct {
				DiscoveryError *string `json:"discovery_error"`
			} `json:"data"`
		}
		if err := json.NewDecoder(w.Body).Decode(&wrongCAResp); err != nil {
			t.Fatalf("decode wrong-CA response: %v", err)
		}
		if wrongCAResp.Data.DiscoveryError == nil || !strings.Contains(*wrongCAResp.Data.DiscoveryError, "TLS certificate verification failed") {
			t.Errorf("discovery_error = %v, want it to contain \"TLS certificate verification failed\"", wrongCAResp.Data.DiscoveryError)
		} else if cause := diagnoseRegistration(*wrongCAResp.Data.DiscoveryError); cause != "CA configuration: the ca_cert_pem registered for Relay does not verify its control-plane certificate" {
			t.Errorf("diagnoseRegistration(%q) = %q, want the CA-configuration cause", *wrongCAResp.Data.DiscoveryError, cause)
		}

		body, err = json.Marshal(map[string]any{
			"name":        "relay-bad-token",
			"url":         e.MCPURL,
			"ca_cert_pem": e.CAPEM,
			"auth_headers": []map[string]string{
				{"key": "Authorization", "value": "Bearer glp_relaysmoke_invalid"},
			},
		})
		if err != nil {
			t.Fatalf("marshal bad-token request: %v", err)
		}
		w = h.do(t, http.MethodPost, "/api/v1/mcp/servers", string(body), model.RoleAdmin)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /api/v1/mcp/servers (bad token) status = %d, body %s", w.Code, w.Body.String())
		}
		var badTokenResp struct {
			Data struct {
				DiscoveryError *string `json:"discovery_error"`
			} `json:"data"`
		}
		if err := json.NewDecoder(w.Body).Decode(&badTokenResp); err != nil {
			t.Fatalf("decode bad-token response: %v", err)
		}
		// If Relay ever answers 403 instead of 401 here, adjust this assertion
		// to the observed code and note it in docs/developer/relay-smoke.md --
		// do not loosen it to accept either silently.
		if badTokenResp.Data.DiscoveryError == nil || !strings.Contains(*badTokenResp.Data.DiscoveryError, "status 401") {
			t.Errorf("discovery_error = %v, want it to contain \"status 401\"", badTokenResp.Data.DiscoveryError)
		} else if cause := diagnoseRegistration(*badTokenResp.Data.DiscoveryError); cause != "bearer token: Relay rejected the machine Account's API Token" {
			t.Errorf("diagnoseRegistration(%q) = %q, want the bearer-token cause", *badTokenResp.Data.DiscoveryError, cause)
		}
	})
}

// gatedMutateBranchA is taken when Relay emitted `input_required` for the
// gated mutate call: an MRTR pause a human can actually answer
// (gleipnir-relay#646 present in this build).
//
// In strict mode (requireMRTR) it additionally proves the demo beat end to
// end: the answer travels through Gleipnir's operator tool-input path (never
// approve_request), Relay accepted the retry's responder assertion and
// recorded an in-band decision, the matched rule is the demo gate's, and the
// restart succeeded on every Node.
func gatedMutateBranchA(t *testing.T, ctx context.Context, h *harness, approver relayAPI, runID string, done <-chan struct{}, requireMRTR bool, rules gateRules, rep *report) {
	t.Helper()
	rep.MRTR = "exercised"

	w := h.do(t, http.MethodGet, "/api/v1/runs/"+runID+"/tool-input", "", model.RoleApprover)
	if w.Code != http.StatusOK {
		t.Fatalf("GET tool-input status = %d, body %s", w.Code, w.Body.String())
	}
	var getBody struct {
		Data run.ToolInputRequestResponse `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&getBody); err != nil {
		t.Fatalf("decode GET tool-input response: %v", err)
	}
	if getBody.Data.ServerName != "relay" {
		t.Errorf("server_name = %q, want relay", getBody.Data.ServerName)
	}
	if getBody.Data.ElicitationKind != string(model.ElicitationKindPermission) {
		t.Errorf("elicitation_kind = %q, want permission", getBody.Data.ElicitationKind)
	}
	if requireMRTR && getBody.Data.RequiredRole != string(model.RoleApprover) {
		t.Errorf("required_role = %q, want %q — a permission ask is answered by an approver", getBody.Data.RequiredRole, model.RoleApprover)
	}
	if len(getBody.Data.Requests) != 1 || getBody.Data.Requests[0].Message == "" {
		t.Fatalf("requests = %+v, want one request with a non-empty Relay-authored message", getBody.Data.Requests)
	}
	t.Logf("RELAYSMOKE Relay's elicitation message: %s", getBody.Data.Requests[0].Message)

	// The exact body the run detail page's Approve button sends for a
	// consent-only ask (frontend ToolInputCard: `{confirmed: true}` when the
	// requested schema has no fields). The responder identity is NOT in this
	// body — the handler stamps it from the session (ADR-061), which is
	// precisely the property strict mode checks on Relay's side below.
	w = h.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/tool-input",
		`{"responses":[{"action":"accept","content":{"confirmed":true}}]}`, model.RoleApprover)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST tool-input status = %d, body %s", w.Code, w.Body.String())
	}

	select {
	case <-done:
	case <-time.After(4 * time.Minute):
		t.Fatal("gated_mutate: run did not complete within 4m after the pause was answered")
	}
	if status := runStatus(t, h.store, runID); status != "complete" {
		t.Fatalf("gated_mutate run status = %s, want complete; error steps: %v", status, errorSteps(t, h.store, runID))
	}

	results := toolResults(t, h.store, runID)
	if requireMRTR {
		// Structurally impossible today (the policy does not grant it, so it
		// is never registered — ADR-001), and asserted anyway: the demo's
		// claim is that a HUMAN answered in Gleipnir, and an approve_request
		// call in this trace would mean the model did instead.
		if r := lastToolResult(results, "relay.approve_request"); r != nil {
			t.Errorf("the run called relay.approve_request (%+v) — the approval must arrive through Gleipnir's operator tool-input path, never a model-called tool", *r)
		}
	}
	last := lastToolResult(results, "relay.run_operation")
	if last == nil || last.IsError {
		t.Fatalf("final relay.run_operation tool_result missing or is_error; results=%+v", results)
	}
	var out runOrPlanOutput
	if err := decodeToolPayload([]byte(last.Output), &out); err != nil {
		t.Fatalf("decode run_operation payload: %v", err)
	}
	if out.AnswerRefused != nil {
		// Relay refuses an API-token session's answer that carries no
		// io.gleipnir/responder permission assertion (AP-64) with exactly
		// this block — the most likely reason for it is that Gleipnir's
		// retry stopped stamping the responder.
		t.Fatalf("Relay refused the answered retry: reason=%q state=%q next_step=%q — check that the retry carries _meta[\"io.gleipnir/responder\"] = {gate: permission, username} (internal/execution/agent/inputrequired.go)",
			out.AnswerRefused.Reason, out.AnswerRefused.State, out.AnswerRefused.NextStep)
	}
	if len(out.Results) != 2 {
		t.Errorf("run_operation returned %d per-node results, want 2 (node:role=web, node:env=prod)", len(out.Results))
	}
	rep.MutateOutcomes = map[string]int{}
	seen := make(map[string]bool, len(out.Results))
	for _, r := range out.Results {
		rep.MutateOutcomes[r.Outcome]++
		if r.NodeID == "" || r.Outcome == "" {
			t.Errorf("per-node result missing node_id or outcome: %+v", r)
		}
		if seen[r.NodeID] {
			t.Errorf("duplicate node_id %s in run_operation results", r.NodeID)
		}
		seen[r.NodeID] = true
		t.Logf("RELAYSMOKE gated_mutate outcome: %s -> %s", r.NodeID, r.Outcome)

		// Asserted only in strict mode: the default lane predates the web
		// Nodes' systemctl shim and treats parking as the only property under
		// test. Strict mode tracks the demo, where the restart must succeed.
		if requireMRTR {
			if r.Outcome != "success" {
				t.Errorf("node %s outcome = %s (stderr=%q), want success — the demo's web Nodes restart nginx through the DEV-ONLY systemctl shim", r.NodeID, r.Outcome, r.Stderr)
			} else if r.ExitCode == nil || *r.ExitCode != 0 {
				t.Errorf("node %s exit_code = %v, want 0", r.NodeID, r.ExitCode)
			}
		}
	}

	if requireMRTR {
		assertInBandDecision(t, ctx, approver, out, rules)
	}

	w = h.do(t, http.MethodGet, "/api/v1/runs/"+runID+"/decisions", "", model.RoleAuditor)
	if w.Code != http.StatusOK {
		t.Fatalf("GET decisions status = %d, body %s", w.Code, w.Body.String())
	}
	var decBody struct {
		Data []run.DecisionSummary `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&decBody); err != nil {
		t.Fatalf("decode decisions response: %v", err)
	}
	if len(decBody.Data) != 1 || decBody.Data[0].Outcome != string(decision.OutcomeAnswered) {
		t.Fatalf("decisions = %+v, want exactly one answered record", decBody.Data)
	}
	if decBody.Data[0].ActorUsername != approverUsername {
		t.Errorf("actor_username = %q, want %s", decBody.Data[0].ActorUsername, approverUsername)
	}

	pending, err := pendingApprovals(ctx, approver)
	if err != nil {
		t.Fatalf("cross-check Relay's pending approvals: %v", err)
	}
	for _, a := range pending {
		if a.RequestedBy == "gleipnir-smoke" {
			t.Errorf("Relay still lists a pending approval requested by gleipnir-smoke: %+v — the in-band decision did not land on Relay's own record", a)
		}
	}
}

// assertInBandDecision checks, from Relay's side, that the answered retry was
// accepted as an in-band decision naming Gleipnir's responder — which, on an
// API-token session, Relay grants only when the retry carried
// _meta["io.gleipnir/responder"] = {gate: "permission", username: <non-blank>}
// (AP-64). The decision block echoes that username as on_behalf_of, so it is
// the wire-level proof that the assertion arrived intact. The Control API
// read-back then pins which gate the Relay actually loaded.
func assertInBandDecision(t *testing.T, ctx context.Context, approver relayAPI, out runOrPlanOutput, rules gateRules) {
	t.Helper()

	d := out.Decision
	if d == nil {
		t.Fatalf("the answered retry's result carries no `decision` block — Relay did not treat the retry as an answer to its question; result=%+v", out)
	}
	if d.Channel != "in-band" {
		t.Errorf("decision.channel = %q, want in-band", d.Channel)
	}
	if d.OnBehalfOf != approverUsername {
		t.Errorf("decision.on_behalf_of = %q, want %q — the responder assertion Gleipnir stamps on the retry did not reach Relay as sent", d.OnBehalfOf, approverUsername)
	}
	if d.State != "approved" {
		t.Errorf("decision.state = %q, want approved", d.State)
	}
	if d.RequestID == "" {
		t.Fatal("decision.request_id is empty; cannot cross-check Relay's approval record")
	}

	a, err := getApproval(ctx, approver, d.RequestID)
	if err != nil {
		t.Fatalf("read back Relay's approval record: %v", err)
	}
	if !containsString(a.MatchedRules, rules.FleetWideMutate) {
		t.Errorf("approval matched_rules = %v, want it to contain %s — the Relay is not running the demo gate (%s)", a.MatchedRules, rules.FleetWideMutate, rules.File)
	}
	if a.Channel != "in-band" || !a.RequesterAllowed || len(a.Audience) != 0 {
		t.Errorf("approval requirement = {channel %q, requester_allowed %v, audience %v}, want {in-band, true, none} — the demo gate's semantics", a.Channel, a.RequesterAllowed, a.Audience)
	}
	if a.State != "approved" {
		t.Errorf("approval state = %q, want approved", a.State)
	}
	if out.JobID == "" || a.JobID != out.JobID {
		t.Errorf("approval job_id = %q, run_operation job_id = %q — want the same, non-empty Job", a.JobID, out.JobID)
	}
	if len(a.Decisions) != 1 {
		t.Fatalf("approval decisions = %+v, want exactly one", a.Decisions)
	}
	got := a.Decisions[0]
	if !got.Approve || got.Channel != "in-band" || got.Account != "gleipnir-smoke" || got.Human {
		t.Errorf("approval decision = %+v, want an in-band approval by the gleipnir-smoke machine Account (human=false; the human is the ASSERTED responder)", got)
	}
}

// gatedMutateBranchB is taken when the run completed on its own: Relay never
// emitted input_required, so the model received the universal
// `pending_approval` fallback text instead. This is the expected outcome on
// a Relay build without gleipnir-relay#646 -- assert the fallback, clean up
// Relay's own parked request, then skip (never silently pass) naming the
// build and the tracked issue.
//
// In strict mode the same fallback is a FAILURE: the demo depends on the
// question reaching a human in Gleipnir, and a pending_approval result means
// it would not have.
func gatedMutateBranchB(t *testing.T, ctx context.Context, h *harness, approver relayAPI, runID, relayRef string, requireMRTR bool, rules gateRules, rep *report) {
	t.Helper()

	status := runStatus(t, h.store, runID)
	results := toolResults(t, h.store, runID)
	last := lastToolResult(results, "relay.run_operation")

	var out runOrPlanOutput
	decodeErr := error(nil)
	if last != nil && !last.IsError {
		decodeErr = decodeToolPayload([]byte(last.Output), &out)
	}

	parkedAsFallback := status == "complete" && last != nil && !last.IsError && decodeErr == nil &&
		out.PendingApproval != nil && out.PendingApproval.Status == "pending_approval"
	if !parkedAsFallback {
		rep.MRTR = "failed: neither input_required nor pending_approval"
		t.Fatalf("mutate run ended without parking and without pending_approval — cause: Gleipnir's parked-result (input_required) handling; run status=%s, last tool_result=%+v, error steps=%v",
			status, last, errorSteps(t, h.store, runID))
	}

	if !containsString(out.PendingApproval.MatchedRules, rules.FleetWideMutate) {
		t.Errorf("pending_approval.matched_rules = %v, want it to contain %s (gate %s)", out.PendingApproval.MatchedRules, rules.FleetWideMutate, rules.File)
	}
	if out.PendingApproval.RequestID == "" || out.PendingApproval.PlanHash == "" {
		t.Fatalf("pending_approval request_id/plan_hash empty: %+v", out.PendingApproval)
	}

	if err := decide(ctx, approver, out.PendingApproval.RequestID, out.PendingApproval.PlanHash, false,
		"relaysmoke: cleanup of fallback park"); err != nil {
		t.Fatalf("cleanup decision on Relay's fallback park: %v", err)
	}

	// rep.MRTR is set before t.Fatalf/t.Skipf: both call runtime.Goexit(), so
	// nothing after them in this goroutine (including the call site back in
	// TestRelaySmoke) runs.
	if requireMRTR {
		rep.MRTR = "failed: pending_approval fallback (strict)"
		t.Fatalf("STRICT: Relay %s answered the gated mutate with pending_approval, not input_required — on stage the approval question would never reach a human in Gleipnir. "+
			"Relay asks in-band only when ALL hold: the matched rule is in-band with requester_allowed and an audience admitting the requester (gate %s, matched %v); "+
			"the client declares name `gleipnir` and the elicitation capability on the 2026-07-28 transport; and the build carries gleipnir-relay#646",
			relayRef, rules.File, out.PendingApproval.MatchedRules)
	}
	rep.MRTR = "skipped: pending_approval fallback"
	t.Skipf("Relay %s answered the in-band gated call with pending_approval, not input_required — gleipnir-relay#646 is not in this build, or the gate's rule is not askable by the requesting Account. Asserted the universal fallback instead. Run `make relaysmoke-demo` to make this a failure.", relayRef)
}

// waitForFleet is the one unavoidable wall-clock poll in this suite: there is
// no event to subscribe to for "the fleet finished enrolling". The 3m
// deadline is well over 5x the few seconds compose health typically takes
// once the containers are already healthy (CLAUDE.md's signal-don't-poll
// rule for unavoidable external waits).
func waitForFleet(t *testing.T, ctx context.Context, adminMCP *mcp.Client, expectedNodes int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	var lastErr error
	var lastUnconnected []string

	for time.Now().Before(deadline) {
		res, err := adminMCP.CallTool(ctx, "list_nodes", map[string]any{"selector": "relay:env=dev"}, mcp.CallOptions{})
		if err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}
		lastErr = nil

		var nodes listNodesOutput
		if err := decodeToolPayload(res.Output, &nodes); err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}

		var unconnected []string
		for _, n := range nodes.Nodes {
			if !n.Connected {
				unconnected = append(unconnected, n.NodeID)
			}
		}
		if len(nodes.Nodes) >= expectedNodes && len(unconnected) == 0 {
			return
		}
		lastUnconnected = unconnected
		time.Sleep(2 * time.Second)
	}

	if lastErr != nil {
		t.Fatalf("waiting for the fleet to come up: last error calling list_nodes: %v", lastErr)
	}
	t.Fatalf("fleet did not reach %d connected Nodes within 3m; still unconnected: %v", expectedNodes, lastUnconnected)
}

// generateThrowawayCAPEM builds a self-signed CA certificate this test uses
// as a deliberately WRONG pin (t.Run("negative_controls")) -- it never
// verifies anything against Relay's real certificate.
func generateThrowawayCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate throwaway CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "relaysmoke throwaway CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create throwaway CA certificate: %v", err)
	}
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("PEM-encode throwaway CA: %v", err)
	}
	return buf.String()
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestDiagnoseRegistration is a table-driven unit test of the classifier
// t.Run("register") and t.Run("negative_controls") both depend on. It needs
// no fleet and runs (and compiles) with the rest of this build-tagged suite.
func TestDiagnoseRegistration(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want string
	}{
		{"TLS verification failure", "TLS certificate verification failed for relay: the server's certificate is signed by an unknown authority", "CA configuration: the ca_cert_pem registered for Relay does not verify its control-plane certificate"},
		{"401", "post tools/list: mcp server returned status 401", "bearer token: Relay rejected the machine Account's API Token"},
		{"403", "post tools/list: mcp server returned status 403", "bearer token: Relay rejected the machine Account's API Token"},
		{"no such host", "post tools/list: dial tcp: lookup relay: no such host", "reachability: is the fleet up, and does `relay` resolve to 127.0.0.1?"},
		{"connection refused", "post tools/list: dial tcp 127.0.0.1:19443: connect: connection refused", "reachability: is the fleet up, and does `relay` resolve to 127.0.0.1?"},
		{"unrecognized", "post tools/list: some other transport error", "unclassified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diagnoseRegistration(tc.msg); got != tc.want {
				t.Errorf("diagnoseRegistration(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}
