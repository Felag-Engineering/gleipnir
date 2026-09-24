//go:build relaysmoke

package relaysmoke

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// relayAPI is a small typed client for Relay's Control API
// (relay/internal/controlplane/api), used only for setup, cross-checks, and
// the out-of-band approval decision -- everything an agent actually does
// goes through the real Gleipnir MCP stack instead, in harness_test.go and
// relaysmoke_test.go.
type relayAPI struct {
	base  string // e.g. "https://relay:19443"
	token string // Bearer credential; never logged
	hc    *http.Client
}

// newRelayAPI builds a relayAPI verifying the control plane's certificate
// against pool alone -- InsecureSkipVerify is never an option here, the same
// posture internal/mcp.WithRootCAs enforces.
func newRelayAPI(base, token string, pool *x509.CertPool) relayAPI {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return relayAPI{
		base:  base,
		token: token,
		hc:    &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}
}

// maxRelayErrorBodyBytes bounds how much of a non-2xx Relay response body
// this client retains for the error message -- a Relay problem+json body is
// small diagnostic text, not a secret, but an unbounded read is still an
// unforced memory-exhaustion risk against a server this test does not
// control.
const maxRelayErrorBodyBytes = 2 << 10

// relayStatusError names a non-2xx Control API response, carrying enough of
// the body for a human to see why.
type relayStatusError struct {
	Method, Path string
	StatusCode   int
	Body         []byte
}

func (e *relayStatusError) Error() string {
	return fmt.Sprintf("relay control api: %s %s: status %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// do sends one Control API request. body is marshaled as the JSON request
// body when non-nil; out, when non-nil, receives the JSON-decoded response
// body on a 2xx status. It returns the HTTP status code so a caller can
// branch on 409 (accounts.go's create-or-lookup) without inspecting an error
// string.
func (a relayAPI) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("marshal %s %s request body: %w", method, path, err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.base+path, reqBody)
	if err != nil {
		return 0, fmt.Errorf("build %s %s request: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, maxRelayErrorBodyBytes)) //nolint:errcheck
		return resp.StatusCode, &relayStatusError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: limited}
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// --- Accounts (relay/internal/controlplane/api/accounts.go) ---

type relayAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Role string `json:"role"`
}

type listAccountsResponse struct {
	Accounts []relayAccount `json:"accounts"`
}

type mintAPITokenResponse struct {
	Token string `json:"token"`
}

// mintMachineToken creates (or reuses) a machine/operator Account named name
// and mints a fresh API Token for it, following the same create-or-lookup
// steps as the Relay runbook's own copy-paste block: a 409 on create means
// the Account already exists, so the lookup after it always succeeds. The
// returned token is used in-memory only by the caller and must never be
// logged.
func mintMachineToken(ctx context.Context, admin relayAPI, name string) (accountID, token string, err error) {
	status, createErr := admin.do(ctx, http.MethodPost, "/api/v1/accounts",
		map[string]any{"name": name, "kind": "machine", "role": "operator"}, nil)
	if createErr != nil && status != http.StatusConflict {
		return "", "", fmt.Errorf("create machine account %q: %w", name, createErr)
	}

	var accounts listAccountsResponse
	if _, err := admin.do(ctx, http.MethodGet, "/api/v1/accounts", nil, &accounts); err != nil {
		return "", "", fmt.Errorf("list accounts looking for %q: %w", name, err)
	}
	for _, acc := range accounts.Accounts {
		if acc.Name == name {
			accountID = acc.ID
			break
		}
	}
	if accountID == "" {
		return "", "", fmt.Errorf("account %q not found after create-or-lookup", name)
	}

	var minted mintAPITokenResponse
	if _, err := admin.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%s/tokens", accountID),
		// ttl_seconds is required by Relay; 0 means never-expire, so never send
		// it here -- a smoke run's credential should not outlive the run.
		map[string]any{"name": "relaysmoke", "ttl_seconds": 3600}, &minted); err != nil {
		return "", "", fmt.Errorf("mint token for account %s: %w", accountID, err)
	}
	if minted.Token == "" {
		return "", "", fmt.Errorf("mint token for account %s: empty token in response", accountID)
	}
	return accountID, minted.Token, nil
}

// --- Approvals (relay/internal/controlplane/api/approvals.go) ---

type approval struct {
	RequestID    string   `json:"request_id"`
	State        string   `json:"state"`
	PlanHash     string   `json:"plan_hash"`
	RequestedBy  string   `json:"requested_by"`
	Channel      string   `json:"channel"`
	MatchedRules []string `json:"matched_rules"`
}

type listApprovalsResponse struct {
	Approvals []approval `json:"approvals"`
}

func pendingApprovals(ctx context.Context, a relayAPI) ([]approval, error) {
	var resp listApprovalsResponse
	if _, err := a.do(ctx, http.MethodGet, "/api/v1/approvals", nil, &resp); err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	return resp.Approvals, nil
}

// decide answers a parked approval request as approver, over the Control
// API's out-of-band decision endpoint. Approving does not itself dispatch:
// the original requester re-sends the identical tools/call, and Relay's
// capability layer releases it by plan hash.
func decide(ctx context.Context, approver relayAPI, requestID, planHash string, approve bool, reason string) error {
	status, err := approver.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/approvals/%s/decision", requestID),
		map[string]any{"approve": approve, "plan_hash": planHash, "reason": reason}, nil)
	if err != nil {
		return fmt.Errorf("decide approval %s (approve=%v): %w", requestID, approve, err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("decide approval %s: unexpected status %d", requestID, status)
	}
	return nil
}

// --- Relay MCP tool payload decoding ---
//
// Gleipnir's mcp.Client.CallTool returns ToolResult.Output as the JSON of the
// MCP `content` array; the tool's actual structured payload lives in
// content[0].text as a JSON string (structuredContent, which Relay also
// sends, is dropped by Gleipnir's client). decodeToolPayload undoes that one
// layer of indirection for the shapes below.

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// decodeToolPayload unmarshals output (a Gleipnir mcp.ToolResult.Output
// value: the JSON-encoded MCP content array) into dst, by finding the single
// text content item and unmarshaling its .Text. output must carry exactly one
// text item -- Relay's tools never send more than one -- and the error names
// the raw text (truncated) so a shape mismatch is diagnosable without a
// debugger.
func decodeToolPayload(output []byte, dst any) error {
	var items []contentItem
	if err := json.Unmarshal(output, &items); err != nil {
		return fmt.Errorf("unmarshal content array: %w", err)
	}
	var texts []string
	for _, item := range items {
		if item.Type == "text" {
			texts = append(texts, item.Text)
		}
	}
	if len(texts) != 1 {
		return fmt.Errorf("content array carried %d text item(s), want exactly 1: %s", len(texts), truncateForError(string(output)))
	}
	if err := json.Unmarshal([]byte(texts[0]), dst); err != nil {
		return fmt.Errorf("unmarshal tool payload %s: %w", truncateForError(texts[0]), err)
	}
	return nil
}

// maxDecodeErrorTextBytes bounds how much server-controlled text a decode
// error message repeats back, so a misbehaving Relay build cannot balloon a
// test failure's own output.
const maxDecodeErrorTextBytes = 1024

func truncateForError(s string) string {
	if len(s) <= maxDecodeErrorTextBytes {
		return s
	}
	return s[:maxDecodeErrorTextBytes] + "...(truncated)"
}

// runOrPlanOutput mirrors relay/internal/controlplane/mcp/wire.go's
// RunOrPlanOutput -- run_operation and raw_exec's shared result shape.
type runOrPlanOutput struct {
	JobID           string           `json:"job_id,omitempty"`
	Results         []perNodeResult  `json:"results"`
	PendingApproval *pendingApproval `json:"pending_approval,omitempty"`
}

// perNodeResult mirrors wire.go's PerNodeResult.
type perNodeResult struct {
	NodeID             string `json:"node_id"`
	Outcome            string `json:"outcome"`
	ExitCode           *int32 `json:"exit_code,omitempty"`
	Stdout             string `json:"stdout"`
	Stderr             string `json:"stderr"`
	RefusalExplanation string `json:"refusal_explanation,omitempty"`
}

// pendingApproval mirrors wire.go's PendingApprovalOutput -- the universal
// "parked, no answerable question" fallback every Relay build renders when it
// has nothing better (relay-646, section 1).
type pendingApproval struct {
	Status       string   `json:"status"`
	RequestID    string   `json:"request_id"`
	PlanHash     string   `json:"plan_hash"`
	Summary      string   `json:"summary"`
	MatchedRules []string `json:"matched_rules"`
}

// listNodesOutput mirrors wire.go's ListNodesOutput.
type listNodesOutput struct {
	Nodes []struct {
		NodeID    string `json:"node_id"`
		Connected bool   `json:"connected"`
	} `json:"nodes"`
}
