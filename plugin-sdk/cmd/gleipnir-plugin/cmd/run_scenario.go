package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	handshakev1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/handshake/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/internal/fakehost"
	"github.com/felag-engineering/gleipnir/plugin-sdk/internal/hostwire"
)

// ── Scenario YAML schema ─────────────────────────────────────────────────────
//
// Scenario YAML format:
//
//   steps:
//     - rpc: Handshake.Negotiate
//       request:
//         host_version: "0.0.0-dev"
//         expected_capabilities: []
//       assert_response:
//         ok: true
//
//     - rpc: Tool.ListTools
//       request: {}
//       assert_response:
//         min_tools: 1
//
//     - rpc: Tool.Call
//       request:
//         tool_name: echo
//         input_json: '{"text":"hello"}'
//       assert_response:
//         result_contains: "hello"
//
//     - assert_host:
//         min_events: 0
//         min_metrics: 0
//         min_audit_steps: 0

// scenario is the top-level structure of a scenario YAML file.
type scenario struct {
	Steps []scenarioStep `yaml:"steps"`
}

// scenarioStep is a single step in a scenario. Exactly one of the RPC fields
// or assert_host is expected to be non-zero, but validation is intentionally
// lenient to give useful error messages at execution time.
type scenarioStep struct {
	// RPC is the dotted service.method to call, e.g. "Handshake.Negotiate".
	RPC string `yaml:"rpc"`

	// Request is the RPC request fields as a free-form map. The fields are
	// mapped to the appropriate proto message at execution time.
	Request map[string]interface{} `yaml:"request"`

	// AssertResponse checks fields in the RPC response.
	AssertResponse map[string]interface{} `yaml:"assert_response"`

	// AssertHost checks the fake host's recorded state after this step.
	AssertHost *assertHost `yaml:"assert_host"`
}

// assertHost holds assertions against fake host recorder state.
type assertHost struct {
	MinEvents     int `yaml:"min_events"`
	MinMetrics    int `yaml:"min_metrics"`
	MinAuditSteps int `yaml:"min_audit_steps"`
	MinLogs       int `yaml:"min_logs"`
}

// loadScenario reads and parses a scenario YAML file. KnownFields(true) is
// used so typos in field names cause an immediate, actionable error rather than
// silent no-ops.
func loadScenario(path string) (*scenario, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open scenario file: %w", err)
	}
	defer f.Close()

	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read scenario file: %w", err)
	}

	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)

	var s scenario
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse scenario YAML: %w", err)
	}
	return &s, nil
}

// runScenario is the entry point for --scenario mode. It launches the plugin,
// runs each step in the scenario file, and prints a pass/fail summary.
func runScenario(ctx context.Context, cmd *cobra.Command, binary, scenarioFile string) error {
	sc, err := loadScenario(scenarioFile)
	if err != nil {
		return err
	}

	host := newFakeHost(fakehost.Options{})

	pluginClient, teardown, err := launchPlugin(ctx, binary, host, hostwire.Options{})
	if err != nil {
		return fmt.Errorf("launch plugin: %w", err)
	}
	defer teardown()

	for i, step := range sc.Steps {
		stepNum := i + 1

		if step.AssertHost != nil {
			if err := evalAssertHost(step.AssertHost, host); err != nil {
				return fmt.Errorf("step %d assert_host: %w", stepNum, err)
			}
			continue
		}

		if err := execScenarioStep(ctx, step, pluginClient, host); err != nil {
			return fmt.Errorf("step %d (%s): %w", stepNum, step.RPC, err)
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "OK: scenario passed (%d steps)\n", len(sc.Steps))
	return nil
}

// execScenarioStep dispatches a single RPC step and evaluates assert_response.
func execScenarioStep(ctx context.Context, step scenarioStep, client *hostwire.Client, _ *fakehost.Host) error {
	switch step.RPC {
	case "Handshake.Negotiate":
		req := &handshakev1.NegotiateRequest{}
		if v, ok := step.Request["host_version"].(string); ok {
			req.HostVersion = v
		}
		resp, err := client.Handshake.Negotiate(ctx, req)
		if err != nil {
			return fmt.Errorf("Negotiate RPC failed: %w", err)
		}
		return evalResponseFields(step.AssertResponse, map[string]interface{}{
			"ok":           resp.GetOk(),
			"error_detail": resp.GetErrorDetail(),
			"sdk_version":  resp.GetSdkVersion(),
		})

	case "Tool.ListTools":
		resp, err := client.Tool.ListTools(ctx, &toolv1.ListToolsRequest{})
		if err != nil {
			return fmt.Errorf("ListTools RPC failed: %w", err)
		}
		assertions := map[string]interface{}{
			"tool_count": len(resp.GetTools()),
		}
		// min_tools check handled in evalResponseFields
		if minRaw, ok := step.AssertResponse["min_tools"]; ok {
			minInt, ok := minRaw.(int)
			if !ok {
				return fmt.Errorf("assert_response.min_tools must be an integer")
			}
			if len(resp.GetTools()) < minInt {
				return fmt.Errorf("min_tools assertion failed: got %d tools, want at least %d", len(resp.GetTools()), minInt)
			}
			return nil
		}
		return evalResponseFields(step.AssertResponse, assertions)

	case "Tool.Call":
		toolName, _ := step.Request["tool_name"].(string)
		inputJSON, _ := step.Request["input_json"].(string)
		resp, err := client.Tool.Call(ctx, &toolv1.CallRequest{
			ToolName:  toolName,
			InputJson: inputJSON,
		})
		if err != nil {
			return fmt.Errorf("Tool.Call RPC failed: %w", err)
		}
		if rc, ok := step.AssertResponse["result_contains"].(string); ok {
			if !strings.Contains(resp.GetOutputJson(), rc) {
				return fmt.Errorf("result_contains assertion failed: %q not found in %q", rc, resp.GetOutputJson())
			}
			return nil
		}
		return evalResponseFields(step.AssertResponse, map[string]interface{}{
			"output_json": resp.GetOutputJson(),
			"ok":          resp.GetError() == nil,
		})

	default:
		return fmt.Errorf("unknown RPC %q — supported: Handshake.Negotiate, Tool.ListTools, Tool.Call", step.RPC)
	}
}

// evalResponseFields checks that each key in assertions matches the expected
// value from the response fields map. An empty assertions map (assert_response:
// {}) means "no assertions" and always passes — use this when you only want to
// verify the RPC does not return an error.
func evalResponseFields(assertions map[string]interface{}, responseFields map[string]interface{}) error {
	for key, want := range assertions {
		got, exists := responseFields[key]
		if !exists {
			return fmt.Errorf("assertion key %q is not a supported response field", key)
		}
		if !valuesEqual(got, want) {
			return fmt.Errorf("assertion %q failed: got %v, want %v", key, got, want)
		}
	}
	return nil
}

// valuesEqual compares two values with a tolerance for YAML's int representation.
func valuesEqual(got, want interface{}) bool {
	if got == want {
		return true
	}
	// YAML decodes integers as int; proto booleans are bool. Allow a
	// straightforward string comparison as a fallback.
	return fmt.Sprintf("%v", got) == fmt.Sprintf("%v", want)
}

// evalAssertHost checks fake host recorder state against the assertHost spec.
func evalAssertHost(a *assertHost, h *fakehost.Host) error {
	if n := len(h.Events()); n < a.MinEvents {
		return fmt.Errorf("min_events: got %d, want at least %d", n, a.MinEvents)
	}
	if n := len(h.Metrics()); n < a.MinMetrics {
		return fmt.Errorf("min_metrics: got %d, want at least %d", n, a.MinMetrics)
	}
	if n := len(h.AuditSteps()); n < a.MinAuditSteps {
		return fmt.Errorf("min_audit_steps: got %d, want at least %d", n, a.MinAuditSteps)
	}
	if n := len(h.Logs()); n < a.MinLogs {
		return fmt.Errorf("min_logs: got %d, want at least %d", n, a.MinLogs)
	}
	return nil
}
