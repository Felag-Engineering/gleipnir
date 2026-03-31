package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
)

// Compile-time assertion: *ClaudeCodeAgent must satisfy agent.Runner.
var _ agent.Runner = (*ClaudeCodeAgent)(nil)

// Config holds the dependencies needed to construct a ClaudeCodeAgent.
type Config struct {
	Policy       *model.ParsedPolicy
	Tools        []mcp.ResolvedTool
	Audit        *agent.AuditWriter
	StateMachine *agent.RunStateMachine
	ApprovalCh   <-chan bool
	FeedbackCh   <-chan string
}

// ClaudeCodeAgent implements agent.Runner by spawning a `claude -p` subprocess,
// consuming its stream-json output through ParseStream, and writing audit steps
// through AuditWriter. It manages a gate MCP server co-process for
// approval-gated tools.
type ClaudeCodeAgent struct {
	policy      *model.ParsedPolicy
	tools       []mcp.ResolvedTool
	audit       *agent.AuditWriter
	sm          *agent.RunStateMachine
	approvalCh  <-chan bool
	feedbackCh  <-chan string
	claudeBin   string // "claude" by default; overridable in tests
}

// New returns a ClaudeCodeAgent ready to run.
func New(cfg Config) (*ClaudeCodeAgent, error) {
	if cfg.StateMachine == nil {
		return nil, fmt.Errorf("config.StateMachine is required")
	}
	return &ClaudeCodeAgent{
		policy:     cfg.Policy,
		tools:      cfg.Tools,
		audit:      cfg.Audit,
		sm:         cfg.StateMachine,
		approvalCh: cfg.ApprovalCh,
		feedbackCh: cfg.FeedbackCh,
		claudeBin:  "claude",
	}, nil
}

// needsGate returns true when any granted tool requires operator approval.
// When true, the agent starts an in-process HTTP gate server and registers it
// as --permission-prompt-tool.
func (a *ClaudeCodeAgent) needsGate() bool {
	for _, t := range a.tools {
		if t.Approval == model.ApprovalModeRequired {
			return true
		}
	}
	return false
}

// mcpToolName returns the Claude Code MCP tool name for a server/tool pair.
// Claude Code names MCP tools as "mcp__<server>__<tool>".
func mcpToolName(serverName, toolName string) string {
	return fmt.Sprintf("mcp__%s__%s", serverName, toolName)
}

// buildToolGrants returns the map of tool grants passed to the gate server.
// Keys are Claude Code tool names (mcp__server__tool format).
func (a *ClaudeCodeAgent) buildToolGrants() map[string]ToolGrant {
	grants := make(map[string]ToolGrant, len(a.tools))
	for _, t := range a.tools {
		name := mcpToolName(t.ServerName, t.ToolName)
		grants[name] = ToolGrant{
			Approval: t.Approval,
			Timeout:  t.Timeout,
		}
	}
	return grants
}

// buildAllowedTools returns Claude Code tool names for all granted tools.
// Approval-gated tools are included so Claude Code will attempt them; the
// gate server intercepts and approves/denies at invocation time.
func (a *ClaudeCodeAgent) buildAllowedTools() []string {
	names := make([]string, len(a.tools))
	for i, t := range a.tools {
		names[i] = mcpToolName(t.ServerName, t.ToolName)
	}
	return names
}

// writeMCPConfig writes the --mcp-config JSON file and returns its path.
// The caller is responsible for calling os.Remove on the returned path.
func (a *ClaudeCodeAgent) writeMCPConfig(gateURL string) (string, error) {
	// Collect unique servers by name. Multiple tools may share one server.
	type serverEntry struct{ url string }
	servers := make(map[string]serverEntry)
	for _, t := range a.tools {
		if _, seen := servers[t.ServerName]; !seen {
			servers[t.ServerName] = serverEntry{url: t.Client.ServerURL()}
		}
	}

	mcpServers := make(map[string]map[string]string, len(servers)+1)
	for name, entry := range servers {
		mcpServers[name] = map[string]string{"url": entry.url}
	}
	if gateURL != "" {
		mcpServers["gleipnir_gate"] = map[string]string{"url": gateURL}
	}

	cfg := map[string]any{
		"mcpServers": mcpServers,
	}

	f, err := os.CreateTemp("", "gleipnir-mcp-*.json")
	if err != nil {
		return "", fmt.Errorf("create mcp config temp file: %w", err)
	}

	enc := json.NewEncoder(f)
	encErr := enc.Encode(cfg)
	closeErr := f.Close()

	if encErr != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write mcp config: %w", encErr)
	}
	if closeErr != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("close mcp config: %w", closeErr)
	}

	return f.Name(), nil
}

// buildArgs assembles the CLI arguments for `claude -p`.
func (a *ClaudeCodeAgent) buildArgs(systemPrompt, triggerPayload, mcpConfigPath string) []string {
	args := []string{
		"-p", triggerPayload,
		"--output-format", "stream-json",
		"--verbose",
		// --bare suppresses Claude Code's auto-discovery of MCP servers. Without this,
		// Claude Code would discover tools on its own, bypassing Gleipnir's hard
		// capability enforcement (ADR-001).
		"--bare",
		"--system-prompt", systemPrompt,
		"--mcp-config", mcpConfigPath,
	}

	// MaxToolCallsPerRun maps to --max-turns as a coarse approximation.
	// --max-turns counts conversational turns (each may contain multiple tool calls),
	// so this is an upper bound, not an exact match.
	if a.policy.Agent.Limits.MaxToolCallsPerRun > 0 {
		args = append(args, "--max-turns", strconv.Itoa(a.policy.Agent.Limits.MaxToolCallsPerRun))
	}

	allowedTools := a.buildAllowedTools()
	if len(allowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowedTools, ","))
	}

	if a.needsGate() {
		args = append(args, "--permission-prompt-tool", "mcp__gleipnir_gate__gleipnir_gate")
	}

	return args
}

// startGate starts the in-process HTTP gate server for approval-gated tools.
// It wires the gate's ApprovalBridge to the agent's AuditWriter and RunStateMachine.
func (a *ClaudeCodeAgent) startGate(ctx context.Context, runID string) (gateURL string, shutdown func(), err error) {
	grants := a.buildToolGrants()

	bridge := ApprovalBridge{
		RequestApproval: func(ctx context.Context, toolName string, proposedInput map[string]any) (string, error) {
			approvalID := model.NewULID()

			rawInput, err := json.Marshal(proposedInput)
			if err != nil {
				return "", fmt.Errorf("marshalling proposed input for approval request: %w", err)
			}

			// Default 1-hour expiry when no tool-specific timeout is set.
			expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
			if grant, ok := grants[toolName]; ok && grant.Timeout > 0 {
				expiresAt = time.Now().UTC().Add(grant.Timeout).Format(time.RFC3339Nano)
			}

			if err := a.audit.Write(ctx, agent.Step{
				RunID:   runID,
				Type:    model.StepTypeApprovalRequest,
				Content: map[string]any{"tool": toolName, "input": proposedInput},
			}); err != nil {
				return "", fmt.Errorf("writing approval_request step: %w", err)
			}

			if err := a.sm.Transition(ctx, model.RunStatusWaitingForApproval, "",
				agent.WithApprovalPayload(agent.ApprovalPayload{
					ApprovalID:    approvalID,
					ToolName:      toolName,
					ProposedInput: string(rawInput),
					ExpiresAt:     expiresAt,
				})); err != nil {
				return "", fmt.Errorf("transitioning run to waiting_for_approval: %w", err)
			}

			return approvalID, nil
		},
		ResumeRunning: func(ctx context.Context) error {
			return a.sm.Transition(ctx, model.RunStatusRunning, "")
		},
	}

	gateCfg := GateConfig{
		RunID:      runID,
		ToolGrants: grants,
		Bridge:     bridge,
		ApprovalCh: a.approvalCh,
	}
	// NewGateServer requires io.Reader/io.Writer for its stdio path, but when
	// dispatching via HTTP those are unused. bytes.NewReader(nil) and io.Discard
	// satisfy the constructor without allocating meaningful resources.
	gateServer := NewGateServer(bytes.NewReader(nil), noopWriter{}, gateCfg)

	url, shutdownFn, err := StartHTTPGate(ctx, gateServer)
	if err != nil {
		return "", nil, fmt.Errorf("starting HTTP gate: %w", err)
	}
	return url, shutdownFn, nil
}

// noopWriter satisfies io.Writer without doing anything. Used as the stdio
// stdout for the GateServer when it is dispatching via HTTP.
type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// failRun transitions the run to failed. If the provided context is already
// cancelled, a background context is used so the DB write still lands.
func (a *ClaudeCodeAgent) failRun(ctx context.Context, runErr error) error {
	transCtx := ctx
	if ctx.Err() != nil {
		transCtx = context.Background()
	}
	if tErr := a.sm.Transition(transCtx, model.RunStatusFailed, runErr.Error()); tErr != nil {
		slog.ErrorContext(transCtx, "failed to persist run failure status",
			"run_err", runErr, "transition_err", tErr)
	}
	return runErr
}

// Run executes the Claude Code subprocess for a single run.
//
// Lifecycle:
//  1. Transition pending → running.
//  2. Render system prompt and write capability snapshot.
//  3. Optionally start the gate HTTP server for approval-gated tools.
//  4. Write --mcp-config JSON to a temp file.
//  5. Spawn `claude -p` and stream its NDJSON output to the audit trail.
//  6. Map exit code to complete/failed run status.
func (a *ClaudeCodeAgent) Run(ctx context.Context, runID string, triggerPayload string) error {
	defer func() {
		if err := a.audit.Close(); err != nil {
			slog.ErrorContext(ctx, "audit writer drain error", "run_id", runID, "err", err)
		}
	}()

	// Use context.Background() for the initial transition: if the caller's context
	// is already cancelled we still want the DB write to land before returning.
	if err := a.sm.Transition(context.Background(), model.RunStatusRunning, ""); err != nil {
		a.failRun(context.Background(), err) //nolint:errcheck
		return fmt.Errorf("transitioning run to running: %w", err)
	}

	grantedTools := make([]model.GrantedTool, len(a.tools))
	for i, rt := range a.tools {
		grantedTools[i] = rt.GrantedTool
	}

	systemPrompt := policy.RenderSystemPrompt(a.policy, grantedTools, time.Now().UTC())

	if err := a.sm.PersistSystemPrompt(ctx, systemPrompt); err != nil {
		slog.WarnContext(ctx, "failed to persist system prompt", "run_id", runID, "err", err)
	}

	type capabilitySnapshot struct {
		Provider string              `json:"provider"`
		Model    string              `json:"model"`
		Tools    []model.GrantedTool `json:"tools"`
	}
	if err := a.audit.Write(context.Background(), agent.Step{
		RunID: runID,
		Type:  model.StepTypeCapabilitySnapshot,
		Content: capabilitySnapshot{
			Provider: a.policy.Agent.ModelConfig.Provider,
			Model:    a.policy.Agent.ModelConfig.Name,
			Tools:    grantedTools,
		},
	}); err != nil {
		return a.failRun(ctx, fmt.Errorf("writing capability snapshot: %w", err))
	}

	var gateURL string
	if a.needsGate() {
		url, shutdownGate, err := a.startGate(ctx, runID)
		if err != nil {
			return a.failRun(ctx, fmt.Errorf("starting gate server: %w", err))
		}
		defer shutdownGate()
		gateURL = url
	}

	configPath, err := a.writeMCPConfig(gateURL)
	if err != nil {
		return a.failRun(ctx, fmt.Errorf("writing mcp config: %w", err))
	}
	defer os.Remove(configPath)

	args := a.buildArgs(systemPrompt, triggerPayload, configPath)

	cmd := exec.CommandContext(ctx, a.claudeBin, args...)
	cmd.Stdin = nil
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return a.failRun(ctx, fmt.Errorf("creating stdout pipe: %w", err))
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	// Setpgid places the subprocess into its own process group. Sending SIGTERM
	// (or SIGKILL) to the negative PID terminates the group, including any
	// child processes that Claude Code itself spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return a.failRun(ctx, fmt.Errorf("starting claude subprocess: %w", err))
	}

	// done is closed after cmd.Wait() returns. The cancellation goroutine selects
	// on both ctx.Done() and done to avoid sending SIGTERM/SIGKILL after the PID
	// has been reclaimed by the OS.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
				// Process already exited; SIGKILL not needed.
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
			// Process exited normally; goroutine exits cleanly.
		}
	}()

	// Consume the stream and write audit steps. The loop exits when ParseStream
	// closes the channel (on EOF or ctx cancellation). cmd.Wait() is called
	// after reading completes, per StdoutPipe documentation.
	sawComplete := false
	sawError := false
	ch := ParseStream(ctx, stdout)
	for ev := range ch {
		switch ev.Type {
		case model.StepTypeComplete:
			sawComplete = true
		case model.StepTypeError:
			sawError = true
		}
		if wErr := a.audit.Write(ctx, agent.Step{
			RunID:     runID,
			Type:      ev.Type,
			Content:   ev.Content,
			TokenCost: ev.TokenCost,
		}); wErr != nil {
			slog.WarnContext(ctx, "audit write failed", "run_id", runID, "err", wErr)
		}
	}

	waitErr := cmd.Wait()
	close(done)

	if waitErr != nil {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if stderrStr != "" {
			slog.ErrorContext(ctx, "claude subprocess stderr", "run_id", runID, "stderr", stderrStr)
			if wErr := a.audit.Write(ctx, agent.Step{
				RunID:   runID,
				Type:    model.StepTypeError,
				Content: model.ErrorStepContent{Message: stderrStr, Code: model.ErrorCodeAPIError},
			}); wErr != nil {
				slog.WarnContext(ctx, "audit write failed for stderr", "run_id", runID, "err", wErr)
			}
		}

		// Context cancellation is the root cause; propagate it clearly.
		if ctx.Err() != nil {
			return a.failRun(ctx, fmt.Errorf("run cancelled: %w", ctx.Err()))
		}
		return a.failRun(ctx, fmt.Errorf("claude subprocess: %w", waitErr))
	}

	_ = sawError // ParseStream already emitted an error step; the exit code governs the outcome.

	if sawComplete || !sawError {
		if tErr := a.sm.Transition(ctx, model.RunStatusComplete, ""); tErr != nil {
			return fmt.Errorf("transitioning run to complete: %w", tErr)
		}
		return nil
	}

	return a.failRun(ctx, fmt.Errorf("claude subprocess exited without a complete event"))
}
