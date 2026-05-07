package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	handshakev1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/handshake/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
	"github.com/felag-engineering/gleipnir/plugin-sdk/internal/fakehost"
)

// ── Scenario YAML parsing ────────────────────────────────────────────────────

func TestLoadScenario_Valid(t *testing.T) {
	const yamlContent = `
steps:
  - rpc: Handshake.Negotiate
    request:
      host_version: "0.0.0-dev"
    assert_response:
      ok: true
  - rpc: Tool.ListTools
    request: {}
    assert_response:
      min_tools: 1
`
	path := writeTempFile(t, "scenario.yaml", yamlContent)
	sc, err := loadScenario(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sc.Steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(sc.Steps))
	}
	if sc.Steps[0].RPC != "Handshake.Negotiate" {
		t.Errorf("step 0 rpc: want Handshake.Negotiate, got %q", sc.Steps[0].RPC)
	}
}

func TestLoadScenario_UnknownField(t *testing.T) {
	const yamlContent = `
steps:
  - rpc: Handshake.Negotiate
    unknown_field: oops
`
	path := writeTempFile(t, "scenario.yaml", yamlContent)
	_, err := loadScenario(path)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestLoadScenario_NotFound(t *testing.T) {
	_, err := loadScenario("/nonexistent/scenario.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadScenario_AssertHost(t *testing.T) {
	const yamlContent = `
steps:
  - assert_host:
      min_events: 2
      min_metrics: 1
`
	path := writeTempFile(t, "scenario.yaml", yamlContent)
	sc, err := loadScenario(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sc.Steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(sc.Steps))
	}
	if sc.Steps[0].AssertHost == nil {
		t.Fatal("expected assert_host to be parsed")
	}
	if sc.Steps[0].AssertHost.MinEvents != 2 {
		t.Errorf("want min_events=2, got %d", sc.Steps[0].AssertHost.MinEvents)
	}
}

// ── Capture JSONL roundtrip ──────────────────────────────────────────────────

func TestWriteAndReadJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	header := captureHeader{
		Sequence:             -1,
		CaptureFormatVersion: 1,
		Binary:               "./myplugin",
		CapturedAt:           time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSONLLine(f, header); err != nil {
		t.Fatalf("write header: %v", err)
	}

	rec := captureRecord{
		CapturedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Sequence:    1,
		EventID:     "evt-abc",
		EventKind:   "github.push",
		PayloadJSON: `{"ref":"main"}`,
	}
	if err := writeJSONLLine(f, rec); err != nil {
		t.Fatalf("write record: %v", err)
	}
	f.Close()

	// Read back and parse.
	rf, _ := os.Open(path)
	defer rf.Close()
	lines, err := readJSONLLines(rf)
	if err != nil {
		t.Fatalf("read lines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}

	var gotHeader captureHeader
	if err := json.Unmarshal([]byte(lines[0]), &gotHeader); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if gotHeader.Sequence != -1 {
		t.Errorf("header sequence: want -1, got %d", gotHeader.Sequence)
	}
	if gotHeader.CaptureFormatVersion != 1 {
		t.Errorf("format version: want 1, got %d", gotHeader.CaptureFormatVersion)
	}

	var gotRec captureRecord
	if err := json.Unmarshal([]byte(lines[1]), &gotRec); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if gotRec.EventID != "evt-abc" {
		t.Errorf("event_id: want evt-abc, got %q", gotRec.EventID)
	}
}

// ── Replay JSONL header validation ──────────────────────────────────────────

func TestRunReplay_HeaderValidation(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "empty file",
			content: "",
			wantErr: "empty",
		},
		{
			name:    "invalid JSON on first line",
			content: "not-json\n",
			wantErr: "invalid JSON",
		},
		{
			name:    "wrong sequence in header",
			content: `{"sequence":0,"capture_format_version":1,"binary":"x","captured_at":""}` + "\n",
			wantErr: "sequence=-1",
		},
		{
			name:    "unsupported format version",
			content: `{"sequence":-1,"capture_format_version":99,"binary":"x","captured_at":""}` + "\n",
			wantErr: "unsupported capture_format_version",
		},
		{
			name:    "valid header no events",
			content: `{"sequence":-1,"capture_format_version":1,"binary":"x","captured_at":""}` + "\n",
			wantErr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempFile(t, "capture.jsonl", tc.content)
			cmd := &cobra.Command{}
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			err := runReplay(cmd, "/bin/true", replayOpts{inFile: path})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
			}
		})
	}
}

func TestRunReplay_LineNumberedErrors(t *testing.T) {
	// Line 2 has invalid JSON.
	content := `{"sequence":-1,"capture_format_version":1,"binary":"x","captured_at":""}` + "\n" +
		`not-json` + "\n"
	path := writeTempFile(t, "capture.jsonl", content)
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	err := runReplay(cmd, "/bin/true", replayOpts{inFile: path})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("expected line number in error, got: %v", err)
	}
}

func TestRunReplay_FilterByKind(t *testing.T) {
	content := buildTestJSONL(t, []captureRecord{
		{Sequence: 1, EventID: "a", EventKind: "github.push", PayloadJSON: "{}"},
		{Sequence: 2, EventID: "b", EventKind: "slack.message", PayloadJSON: "{}"},
		{Sequence: 3, EventID: "c", EventKind: "github.push", PayloadJSON: "{}"},
	})
	path := writeTempFile(t, "capture.jsonl", content)

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)

	// Use /bin/true which always exits 0.
	err := runReplay(cmd, "/bin/true", replayOpts{
		inFile: path,
		filter: "event_kind=github.push",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if strings.Contains(output, "slack.message") {
		t.Error("filtered event slack.message should not appear in output")
	}
	if !strings.Contains(output, "2 OK") {
		t.Errorf("expected 2 OK events in output, got: %q", output)
	}
}

// ── CLI flag-combination validation ─────────────────────────────────────────

func TestRunCmd_FlagValidation(t *testing.T) {
	// Save and restore the launchPlugin stub.
	origLaunch := launchPlugin
	origFakeHost := newFakeHost
	defer func() {
		launchPlugin = origLaunch
		newFakeHost = origFakeHost
	}()
	// Stub that never returns (not called in validation-error tests).
	launchPlugin = func(_ context.Context, _ string, _ hostwire.HostServer, _ hostwire.Options) (*hostwire.Client, func(), error) {
		return nil, nil, fmt.Errorf("should not be called")
	}
	newFakeHost = func(_ fakehost.Options) *fakehost.Host {
		return fakehost.New(fakehost.Options{})
	}

	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "no mode flag",
			args:    []string{"./myplugin"},
			wantErr: "one of --scenario, --capture, or --replay is required",
		},
		{
			name:    "two mode flags",
			args:    []string{"./myplugin", "--scenario", "s.yaml", "--capture", "c.jsonl"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "three mode flags",
			args:    []string{"./myplugin", "--scenario", "s.yaml", "--capture", "c.jsonl", "--replay", "r.jsonl"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "missing binary argument",
			args:    []string{"--scenario", "s.yaml"},
			wantErr: "accepts 1 arg(s)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := &cobra.Command{Use: "gleipnir-plugin"}
			runCmd := NewRunCmd()
			root.AddCommand(runCmd)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			root.SetArgs(append([]string{"run"}, tc.args...))
			err := root.Execute()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ── E2E test using runfixture binary (Linux only) ───────────────────────────

// TestE2E_RunFixture builds the runfixture binary (tagged //go:build runfixture)
// and drives a 2-step scenario + capture + replay roundtrip. The test is
// skipped when:
//   - GOOS is not linux (runfixture uses plugin.Serve which works on any POSIX
//     platform, but we only guarantee the test in CI which is linux/amd64).
//   - the `go` binary is not on PATH.
func TestE2E_RunFixture(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("e2e runfixture test requires linux")
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go binary not on PATH; skipping e2e test")
	}

	// Build the runfixture binary.
	dir := t.TempDir()
	fixtureBin := filepath.Join(dir, "runfixture")
	fixturePkg := "github.com/felag-engineering/gleipnir/plugin-sdk/cmd/gleipnir-plugin/cmd/internal/runfixture"
	buildCmd := exec.Command(goPath, "build", "-tags", "runfixture", "-o", fixtureBin, fixturePkg)
	buildCmd.Dir = filepath.Join(repoRoot(t), "plugin-sdk")
	buildOut, buildErr := buildCmd.CombinedOutput()
	if buildErr != nil {
		t.Fatalf("build runfixture: %v\n%s", buildErr, buildOut)
	}

	// Restore stubs after test.
	origLaunch := launchPlugin
	origFakeHost := newFakeHost
	defer func() {
		launchPlugin = origLaunch
		newFakeHost = origFakeHost
	}()
	launchPlugin = hostwire.Launch
	newFakeHost = fakehost.New

	t.Run("scenario", func(t *testing.T) {
		scenarioYAML := `steps:
  - rpc: Handshake.Negotiate
    request:
      host_version: "0.0.0-dev"
    assert_response:
      ok: true
  - rpc: Tool.ListTools
    request: {}
    assert_response:
      min_tools: 1
`
		scenarioPath := writeTempFile(t, "scenario.yaml", scenarioYAML)

		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&out)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := runScenario(ctx, cmd, fixtureBin, scenarioPath); err != nil {
			t.Fatalf("scenario failed: %v", err)
		}
		if !strings.Contains(out.String(), "OK") {
			t.Errorf("expected OK in output, got: %q", out.String())
		}
	})

	t.Run("capture_and_replay", func(t *testing.T) {
		capturePath := filepath.Join(dir, "capture.jsonl")

		var captureOut bytes.Buffer
		capCmd := &cobra.Command{}
		capCmd.SetOut(&captureOut)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// The runfixture emits exactly one synthetic event via EmitEvent and then
		// the Trigger.Start stream closes. --max-events 1 is redundant but
		// defensive.
		if err := runCapture(ctx, capCmd, fixtureBin, captureOpts{
			outFile:    capturePath,
			watchScope: "{}",
			maxEvents:  1,
		}); err != nil {
			t.Fatalf("capture failed: %v", err)
		}

		// Verify the capture file has a header and at least one event.
		rf, err := os.Open(capturePath)
		if err != nil {
			t.Fatalf("open capture file: %v", err)
		}
		lines, _ := readJSONLLines(rf)
		rf.Close()
		if len(lines) < 2 {
			t.Fatalf("expected at least 2 JSONL lines (header + event), got %d", len(lines))
		}

		// Replay the captured events against the runfixture binary. The
		// runfixture implements --replay-event and exits 0 on success.
		var replayOut bytes.Buffer
		repCmd := &cobra.Command{}
		repCmd.SetOut(&replayOut)

		if err := runReplay(repCmd, fixtureBin, replayOpts{inFile: capturePath}); err != nil {
			t.Fatalf("replay failed: %v\noutput: %s", err, replayOut.String())
		}
		if !strings.Contains(replayOut.String(), "OK") {
			t.Errorf("expected OK in replay output, got: %q", replayOut.String())
		}
	})
}

// ── Fix #1: expected_capabilities parsing ────────────────────────────────────

// TestExecScenarioStep_NegotiateCapabilities verifies that expected_capabilities
// entries in a Handshake.Negotiate request are parsed and sent on the wire,
// and that unknown capability names return a clear error.
func TestExecScenarioStep_NegotiateCapabilities(t *testing.T) {
	// Stub client that captures what NegotiateRequest was sent.
	var capturedReq *handshakev1.NegotiateRequest
	stubClient := &hostwire.Client{
		Handshake: &stubHandshakeClient{
			negotiateFn: func(req *handshakev1.NegotiateRequest) (*handshakev1.NegotiateResponse, error) {
				capturedReq = req
				return &handshakev1.NegotiateResponse{Ok: true}, nil
			},
		},
	}

	t.Run("short names accepted", func(t *testing.T) {
		capturedReq = nil
		step := scenarioStep{
			RPC: "Handshake.Negotiate",
			Request: map[string]interface{}{
				"host_version":          "1.0.0",
				"expected_capabilities": []interface{}{"TOOL", "TRIGGER"},
			},
			AssertResponse: map[string]interface{}{"ok": true},
		}
		if err := execScenarioStep(context.Background(), step, stubClient, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if capturedReq == nil {
			t.Fatal("NegotiateRequest was never sent")
		}
		caps := capturedReq.GetExpectedCapabilities()
		if len(caps) != 2 {
			t.Fatalf("want 2 capabilities, got %d", len(caps))
		}
		if caps[0] != handshakev1.ServiceCapability_SERVICE_CAPABILITY_TOOL {
			t.Errorf("caps[0]: want TOOL, got %v", caps[0])
		}
		if caps[1] != handshakev1.ServiceCapability_SERVICE_CAPABILITY_TRIGGER {
			t.Errorf("caps[1]: want TRIGGER, got %v", caps[1])
		}
	})

	t.Run("full proto names accepted", func(t *testing.T) {
		capturedReq = nil
		step := scenarioStep{
			RPC: "Handshake.Negotiate",
			Request: map[string]interface{}{
				"expected_capabilities": []interface{}{"SERVICE_CAPABILITY_CHANNEL"},
			},
			AssertResponse: map[string]interface{}{"ok": true},
		}
		if err := execScenarioStep(context.Background(), step, stubClient, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		caps := capturedReq.GetExpectedCapabilities()
		if len(caps) != 1 || caps[0] != handshakev1.ServiceCapability_SERVICE_CAPABILITY_CHANNEL {
			t.Errorf("want CHANNEL, got %v", caps)
		}
	})

	t.Run("unknown capability name returns error", func(t *testing.T) {
		step := scenarioStep{
			RPC: "Handshake.Negotiate",
			Request: map[string]interface{}{
				"expected_capabilities": []interface{}{"DOES_NOT_EXIST"},
			},
			AssertResponse: map[string]interface{}{},
		}
		err := execScenarioStep(context.Background(), step, stubClient, nil)
		if err == nil {
			t.Fatal("expected error for unknown capability, got nil")
		}
		if !strings.Contains(err.Error(), "unknown capability") {
			t.Errorf("error %q should mention 'unknown capability'", err.Error())
		}
	})

	t.Run("non-string entry returns error", func(t *testing.T) {
		step := scenarioStep{
			RPC: "Handshake.Negotiate",
			Request: map[string]interface{}{
				"expected_capabilities": []interface{}{42},
			},
			AssertResponse: map[string]interface{}{},
		}
		err := execScenarioStep(context.Background(), step, stubClient, nil)
		if err == nil {
			t.Fatal("expected error for non-string entry, got nil")
		}
	})
}

// ── Fix #3: unrecognized assert_response key after min_tools ─────────────────

// TestExecScenarioStep_ListToolsUnknownAssertKey verifies that an unrecognized
// key in assert_response is rejected even when min_tools is also present.
func TestExecScenarioStep_ListToolsUnknownAssertKey(t *testing.T) {
	stubClient := &hostwire.Client{
		Tool: &stubToolClient{
			listToolsFn: func() (*toolv1.ListToolsResponse, error) {
				return &toolv1.ListToolsResponse{
					Tools: []*toolv1.ToolSchema{{Name: "echo"}},
				}, nil
			},
		},
	}

	step := scenarioStep{
		RPC:     "Tool.ListTools",
		Request: map[string]interface{}{},
		AssertResponse: map[string]interface{}{
			"min_tools":   1,
			"bogus_field": "oops",
		},
	}
	err := execScenarioStep(context.Background(), step, stubClient, nil)
	if err == nil {
		t.Fatal("expected error for unrecognized assert_response key, got nil")
	}
	if !strings.Contains(err.Error(), "bogus_field") {
		t.Errorf("error %q should name the unknown key 'bogus_field'", err.Error())
	}
}

// ── Fix #2: large JSONL line handling ────────────────────────────────────────

// TestReadJSONLLines_LargePayload verifies that lines up to 16MB are read
// without error. The previous bufio.Scanner default of 64KB would have
// returned bufio.ErrTooLong for webhook payloads larger than that.
func TestReadJSONLLines_LargePayload(t *testing.T) {
	// Build a JSONL line that's about 200KB — well above the default 64KB limit.
	bigValue := strings.Repeat("x", 200*1024)
	line := `{"sequence":1,"payload":"` + bigValue + `"}`

	r := strings.NewReader(line + "\n")
	lines, err := readJSONLLines(r)
	if err != nil {
		t.Fatalf("unexpected error for 200KB line: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
}

// TestReadJSONLLines_ExceedsMaxBuffer verifies that a line exceeding 16MB
// returns a human-readable error mentioning the size limit.
func TestReadJSONLLines_ExceedsMaxBuffer(t *testing.T) {
	// Build a line just over 16MB.
	oversize := strings.Repeat("y", 16*1024*1024+1)
	r := strings.NewReader(oversize + "\n")
	_, err := readJSONLLines(r)
	if err == nil {
		t.Fatal("expected error for >16MB line, got nil")
	}
	if !strings.Contains(err.Error(), "16MB") {
		t.Errorf("error %q should mention 16MB", err.Error())
	}
}

// ── Fix #6: empty --filter event_kind= is rejected ───────────────────────────

func TestRunReplay_EmptyFilterKindRejected(t *testing.T) {
	content := buildTestJSONL(t, []captureRecord{
		{Sequence: 1, EventID: "a", EventKind: "github.push", PayloadJSON: "{}"},
	})
	path := writeTempFile(t, "capture.jsonl", content)

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := runReplay(cmd, "/bin/true", replayOpts{inFile: path, filter: "event_kind="})
	if err == nil {
		t.Fatal("expected error for empty event_kind value, got nil")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("error %q should mention non-empty requirement", err.Error())
	}
}

// ── Fix #4: Tool.Call unit test coverage ─────────────────────────────────────

// TestExecScenarioStep_ToolCall covers happy path, result_contains match/mismatch,
// non-string result_contains, and unknown assert_response key.
func TestExecScenarioStep_ToolCall(t *testing.T) {
	cases := []struct {
		name           string
		callFn         func(*toolv1.CallRequest) (*toolv1.CallResponse, error)
		assertResponse map[string]interface{}
		wantErr        string
	}{
		{
			name: "happy path no assertions",
			callFn: func(_ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
				return &toolv1.CallResponse{OutputJson: `{"text":"hello"}`}, nil
			},
			assertResponse: map[string]interface{}{},
		},
		{
			name: "result_contains match",
			callFn: func(_ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
				return &toolv1.CallResponse{OutputJson: `{"text":"hello"}`}, nil
			},
			assertResponse: map[string]interface{}{"result_contains": "hello"},
		},
		{
			name: "result_contains mismatch",
			callFn: func(_ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
				return &toolv1.CallResponse{OutputJson: `{"text":"world"}`}, nil
			},
			assertResponse: map[string]interface{}{"result_contains": "hello"},
			wantErr:        "result_contains assertion failed",
		},
		{
			name: "non-string result_contains",
			callFn: func(_ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
				return &toolv1.CallResponse{OutputJson: `{}`}, nil
			},
			assertResponse: map[string]interface{}{"result_contains": 42},
			wantErr:        "result_contains must be a string",
		},
		{
			name: "unknown assert key",
			callFn: func(_ *toolv1.CallRequest) (*toolv1.CallResponse, error) {
				return &toolv1.CallResponse{OutputJson: `{}`}, nil
			},
			assertResponse: map[string]interface{}{"unknown_key": "oops"},
			wantErr:        "not a supported response field",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubClient := &hostwire.Client{
				Tool: &stubToolClient{callFn: tc.callFn},
			}
			step := scenarioStep{
				RPC:            "Tool.Call",
				Request:        map[string]interface{}{"tool_name": "echo", "input_json": `{"text":"hi"}`},
				AssertResponse: tc.assertResponse,
			}
			err := execScenarioStep(context.Background(), step, stubClient, nil)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
			}
		})
	}
}

// ── Fix #2 (Issue 2): UNSPECIFIED capability rejected ────────────────────────

// TestExecScenarioStep_UnspecifiedCapabilityRejected verifies that listing
// UNSPECIFIED (or its full proto name) as an expected_capability is rejected
// with the same error as an unknown name.
func TestExecScenarioStep_UnspecifiedCapabilityRejected(t *testing.T) {
	stubClient := &hostwire.Client{
		Handshake: &stubHandshakeClient{
			negotiateFn: func(_ *handshakev1.NegotiateRequest) (*handshakev1.NegotiateResponse, error) {
				return &handshakev1.NegotiateResponse{Ok: true}, nil
			},
		},
	}

	for _, capName := range []string{"UNSPECIFIED", "SERVICE_CAPABILITY_UNSPECIFIED"} {
		t.Run(capName, func(t *testing.T) {
			step := scenarioStep{
				RPC: "Handshake.Negotiate",
				Request: map[string]interface{}{
					"expected_capabilities": []interface{}{capName},
				},
				AssertResponse: map[string]interface{}{},
			}
			err := execScenarioStep(context.Background(), step, stubClient, nil)
			if err == nil {
				t.Fatalf("expected error for UNSPECIFIED capability, got nil")
			}
			if !strings.Contains(err.Error(), "unknown capability") {
				t.Errorf("error %q should mention 'unknown capability'", err.Error())
			}
		})
	}
}

// ── Fix #1: replayEvent pipes payload over stdin ──────────────────────────────

// TestReplayEvent_LargePayloadViaStdin verifies that replayEvent can deliver a
// payload larger than 2MB. If the payload were passed as a CLI argument, Linux
// would reject the exec with E2BIG (ARG_MAX ≈ 2MB). Piping over stdin avoids
// this limit entirely.
//
// The test uses /bin/true as the subprocess — it ignores stdin and exits 0,
// which is sufficient to prove no E2BIG occurs at the exec boundary.
func TestReplayEvent_LargePayloadViaStdin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("stdin pipe test requires linux")
	}

	// 3MB payload — well above Linux ARG_MAX (~2MB).
	bigPayload := `{"data":"` + strings.Repeat("x", 3*1024*1024) + `"}`

	var outBuf, errBuf bytes.Buffer
	if err := replayEvent(context.Background(), "/bin/true", bigPayload, &outBuf, &errBuf); err != nil {
		t.Fatalf("replayEvent with 3MB payload failed (E2BIG if passed as arg): %v", err)
	}
}

// ── stub helpers used by unit tests above ────────────────────────────────────

// stubHandshakeClient wraps the handshake client call in a function pointer
// so individual tests can control its behavior without a real gRPC server.
type stubHandshakeClient struct {
	negotiateFn func(*handshakev1.NegotiateRequest) (*handshakev1.NegotiateResponse, error)
}

func (s *stubHandshakeClient) Negotiate(_ context.Context, req *handshakev1.NegotiateRequest, _ ...grpc.CallOption) (*handshakev1.NegotiateResponse, error) {
	return s.negotiateFn(req)
}

// stubToolClient wraps the tool client calls in function pointers.
type stubToolClient struct {
	listToolsFn func() (*toolv1.ListToolsResponse, error)
	callFn      func(*toolv1.CallRequest) (*toolv1.CallResponse, error)
}

func (s *stubToolClient) ListTools(_ context.Context, _ *toolv1.ListToolsRequest, _ ...grpc.CallOption) (*toolv1.ListToolsResponse, error) {
	return s.listToolsFn()
}

func (s *stubToolClient) Call(_ context.Context, req *toolv1.CallRequest, _ ...grpc.CallOption) (*toolv1.CallResponse, error) {
	return s.callFn(req)
}

func (s *stubToolClient) Cancel(_ context.Context, _ *toolv1.CancelRequest, _ ...grpc.CallOption) (*toolv1.CancelResponse, error) {
	return &toolv1.CancelResponse{}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// writeTempFile creates a temp file with the given content and returns its path.
func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

// buildTestJSONL constructs a JSONL string with a valid header and the given
// events for replay tests.
func buildTestJSONL(t *testing.T, events []captureRecord) string {
	t.Helper()
	var sb strings.Builder
	header := captureHeader{
		Sequence:             -1,
		CaptureFormatVersion: 1,
		Binary:               "./myplugin",
		CapturedAt:           time.Now().UTC().Format(time.RFC3339Nano),
	}
	b, _ := json.Marshal(header)
	sb.Write(b)
	sb.WriteByte('\n')
	for _, e := range events {
		b, _ = json.Marshal(e)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// repoRoot walks up from the current file to find the repository root (the
// directory containing plugin-sdk/).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Walk up until we find a directory containing plugin-sdk/.
	for {
		if _, err := os.Stat(filepath.Join(dir, "plugin-sdk")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root from %s", dir)
		}
		dir = parent
	}
}
