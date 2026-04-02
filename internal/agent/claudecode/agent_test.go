package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// writeMockClaude writes a shell script that emits fixture NDJSON lines to
// stdout and returns the script's path. The script exits 0 unless the fixture
// contains "exit_nonzero" in its name.
func writeMockClaude(t *testing.T, ndjsonLines []string) string {
	t.Helper()

	dir := t.TempDir()
	script := filepath.Join(dir, "claude")

	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	sb.WriteString("cat <<'FIXTURE'\n")
	for _, line := range ndjsonLines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	sb.WriteString("FIXTURE\n")

	if err := os.WriteFile(script, []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("write mock claude: %v", err)
	}
	return script
}

// writeMockClaudeExitCode writes a mock claude that exits with the given code.
func writeMockClaudeExitCode(t *testing.T, code int) string {
	t.Helper()

	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	content := "#!/bin/sh\n"
	if code != 0 {
		content += "echo 'something went wrong' >&2\n"
	}
	content += "exit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write mock claude: %v", err)
	}
	return script
}

// buildTestAgent creates a ClaudeCodeAgent wired to a real SQLite test DB.
// It returns the agent and a cancel func for the run context.
func buildTestAgent(t *testing.T, claudeBin string, tools []mcp.ResolvedTool) (*ClaudeCodeAgent, *agent.RunStateMachine, string) {
	t.Helper()

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "test-policy", "webhook", "{}")

	runID := model.NewULID()
	testutil.InsertRun(t, s, runID, "p1", model.RunStatusPending)

	queries := s.Queries()
	sm := agent.NewRunStateMachine(runID, model.RunStatusPending, queries)
	aw := agent.NewAuditWriter(queries)

	policy := &model.ParsedPolicy{
		Name: "test",
		Agent: model.AgentConfig{
			ModelConfig: model.ModelConfig{Provider: "anthropic", Name: "claude-sonnet-4-6"},
			Task:        "do something",
		},
	}

	cfg := Config{
		Policy:       policy,
		Tools:        tools,
		Audit:        aw,
		StateMachine: sm,
		ApprovalCh:   make(chan bool),
		FeedbackCh:   make(chan string),
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.claudeBin = claudeBin
	return a, sm, runID
}

// successFixture returns the NDJSON lines for a successful Claude Code run.
var successFixture = []string{
	`{"type":"result","subtype":"success","usage":{"input_tokens":10,"output_tokens":5},"cost_usd":0.001}`,
}

func TestClaudeCodeAgent_Run_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("test requires /bin/sh")
	}

	claudeBin := writeMockClaude(t, successFixture)
	a, sm, runID := buildTestAgent(t, claudeBin, nil)

	ctx := context.Background()
	err := a.Run(ctx, runID, "do the thing")
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}

	if got := sm.Current(); got != model.RunStatusComplete {
		t.Fatalf("expected status complete, got: %s", got)
	}
}

func TestClaudeCodeAgent_Run_NonZeroExit(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("test requires /bin/sh")
	}

	claudeBin := writeMockClaudeExitCode(t, 1)
	a, sm, runID := buildTestAgent(t, claudeBin, nil)

	ctx := context.Background()
	err := a.Run(ctx, runID, "do the thing")
	if err == nil {
		t.Fatal("Run: expected error for non-zero exit, got nil")
	}

	if got := sm.Current(); got != model.RunStatusFailed {
		t.Fatalf("expected status failed, got: %s", got)
	}
}

func TestClaudeCodeAgent_BuildArgs(t *testing.T) {
	p := &model.ParsedPolicy{
		Name: "test",
		Agent: model.AgentConfig{
			ModelConfig: model.ModelConfig{Provider: "anthropic", Name: "claude-sonnet-4-6"},
			Task:        "test task",
			Limits:      model.RunLimits{MaxToolCallsPerRun: 10},
		},
	}

	tools := []mcp.ResolvedTool{
		{
			GrantedTool: model.GrantedTool{
				ServerName: "myserver",
				ToolName:   "read_file",
				Approval:   model.ApprovalModeNone,
			},
			Client: mcp.NewClient("http://localhost:9999"),
		},
		{
			GrantedTool: model.GrantedTool{
				ServerName: "myserver",
				ToolName:   "write_file",
				Approval:   model.ApprovalModeRequired,
			},
			Client: mcp.NewClient("http://localhost:9999"),
		},
	}

	a := &ClaudeCodeAgent{
		policy: p,
		tools:  tools,
	}

	args := a.buildArgs("system prompt text", "trigger payload", "/tmp/mcp.json")

	t.Run("first arg is -p", func(t *testing.T) {
		if len(args) == 0 || args[0] != "-p" {
			t.Fatalf("expected first arg -p, got: %v", args)
		}
	})

	t.Run("trigger payload follows -p", func(t *testing.T) {
		if len(args) < 2 || args[1] != "trigger payload" {
			t.Fatalf("expected trigger payload as second arg, got: %v", args)
		}
	})

	joined := strings.Join(args, " ")

	t.Run("output-format stream-json", func(t *testing.T) {
		if !strings.Contains(joined, "--output-format stream-json") {
			t.Fatalf("expected --output-format stream-json in args: %v", args)
		}
	})

	t.Run("bare flag present", func(t *testing.T) {
		if !strings.Contains(joined, "--bare") {
			t.Fatalf("expected --bare in args: %v", args)
		}
	})

	t.Run("system-prompt present", func(t *testing.T) {
		if !strings.Contains(joined, "--system-prompt") {
			t.Fatalf("expected --system-prompt in args: %v", args)
		}
	})

	t.Run("mcp-config present", func(t *testing.T) {
		if !strings.Contains(joined, "--mcp-config /tmp/mcp.json") {
			t.Fatalf("expected --mcp-config /tmp/mcp.json in args: %v", args)
		}
	})

	t.Run("max-turns present when limit set", func(t *testing.T) {
		if !strings.Contains(joined, "--max-turns 10") {
			t.Fatalf("expected --max-turns 10 in args: %v", args)
		}
	})

	t.Run("permission-prompt-tool present when gate needed", func(t *testing.T) {
		if !strings.Contains(joined, "--permission-prompt-tool mcp__gleipnir_gate__gleipnir_gate") {
			t.Fatalf("expected --permission-prompt-tool in args: %v", args)
		}
	})

	t.Run("allowedTools contains all tools", func(t *testing.T) {
		if !strings.Contains(joined, "mcp__myserver__read_file") {
			t.Fatalf("expected mcp__myserver__read_file in args: %v", args)
		}
		if !strings.Contains(joined, "mcp__myserver__write_file") {
			t.Fatalf("expected mcp__myserver__write_file in args: %v", args)
		}
	})

	t.Run("no max-turns when limit is 0", func(t *testing.T) {
		p2 := &model.ParsedPolicy{
			Agent: model.AgentConfig{
				Limits: model.RunLimits{MaxToolCallsPerRun: 0},
			},
		}
		a2 := &ClaudeCodeAgent{policy: p2, tools: nil}
		args2 := a2.buildArgs("sp", "payload", "/tmp/mcp.json")
		joined2 := strings.Join(args2, " ")
		if strings.Contains(joined2, "--max-turns") {
			t.Fatalf("expected no --max-turns when limit is 0, got: %v", args2)
		}
	})
}

func TestBuildArgs_MaxTurnsFromOptions(t *testing.T) {
	p := &model.ParsedPolicy{
		Agent: model.AgentConfig{
			ModelConfig: model.ModelConfig{
				Options: map[string]any{"max_turns": 10},
			},
		},
	}
	a := &ClaudeCodeAgent{policy: p}
	joined := strings.Join(a.buildArgs("sp", "payload", "/tmp/mcp.json"), " ")
	if !strings.Contains(joined, "--max-turns 10") {
		t.Errorf("expected --max-turns 10 in args: %s", joined)
	}
}

func TestBuildArgs_MaxBudgetFromOptions(t *testing.T) {
	p := &model.ParsedPolicy{
		Agent: model.AgentConfig{
			ModelConfig: model.ModelConfig{
				Options: map[string]any{"max_budget_usd": 1.5},
			},
		},
	}
	a := &ClaudeCodeAgent{policy: p}
	joined := strings.Join(a.buildArgs("sp", "payload", "/tmp/mcp.json"), " ")
	if !strings.Contains(joined, "--max-budget-usd 1.5") {
		t.Errorf("expected --max-budget-usd 1.5 in args: %s", joined)
	}
}

func TestBuildArgs_MaxTurnsOptionOverridesLimits(t *testing.T) {
	// Options["max_turns"] takes priority over Limits.MaxToolCallsPerRun.
	p := &model.ParsedPolicy{
		Agent: model.AgentConfig{
			ModelConfig: model.ModelConfig{
				Options: map[string]any{"max_turns": 5},
			},
			Limits: model.RunLimits{MaxToolCallsPerRun: 10},
		},
	}
	a := &ClaudeCodeAgent{policy: p}
	joined := strings.Join(a.buildArgs("sp", "payload", "/tmp/mcp.json"), " ")
	if !strings.Contains(joined, "--max-turns 5") {
		t.Errorf("expected --max-turns 5 (from options) in args: %s", joined)
	}
	if strings.Contains(joined, "--max-turns 10") {
		t.Errorf("options[max_turns] should override limits.MaxToolCallsPerRun, got: %s", joined)
	}
}

func TestBuildArgs_MaxTurnsFloat64Fallback(t *testing.T) {
	// YAML may decode integers as float64 (e.g. `max_turns: 20.0`).
	p := &model.ParsedPolicy{
		Agent: model.AgentConfig{
			ModelConfig: model.ModelConfig{
				Options: map[string]any{"max_turns": float64(15)},
			},
		},
	}
	a := &ClaudeCodeAgent{policy: p}
	joined := strings.Join(a.buildArgs("sp", "payload", "/tmp/mcp.json"), " ")
	if !strings.Contains(joined, "--max-turns 15") {
		t.Errorf("expected --max-turns 15 from float64 option in args: %s", joined)
	}
}

func TestClaudeCodeAgent_NeedsGate(t *testing.T) {
	cases := []struct {
		name     string
		tools    []mcp.ResolvedTool
		wantGate bool
	}{
		{
			name:     "no tools",
			tools:    nil,
			wantGate: false,
		},
		{
			name: "all tools no approval",
			tools: []mcp.ResolvedTool{
				{GrantedTool: model.GrantedTool{Approval: model.ApprovalModeNone}},
				{GrantedTool: model.GrantedTool{Approval: model.ApprovalModeNone}},
			},
			wantGate: false,
		},
		{
			name: "one tool approval required",
			tools: []mcp.ResolvedTool{
				{GrantedTool: model.GrantedTool{Approval: model.ApprovalModeNone}},
				{GrantedTool: model.GrantedTool{Approval: model.ApprovalModeRequired}},
			},
			wantGate: true,
		},
		{
			name: "all tools approval required",
			tools: []mcp.ResolvedTool{
				{GrantedTool: model.GrantedTool{Approval: model.ApprovalModeRequired}},
			},
			wantGate: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &ClaudeCodeAgent{tools: tc.tools}
			if got := a.needsGate(); got != tc.wantGate {
				t.Fatalf("needsGate() = %v, want %v", got, tc.wantGate)
			}
		})
	}
}

func TestClaudeCodeAgent_MCPConfig(t *testing.T) {
	tools := []mcp.ResolvedTool{
		{
			GrantedTool: model.GrantedTool{ServerName: "server-a", ToolName: "tool1"},
			Client:      mcp.NewClient("http://server-a.example.com:8080"),
		},
		{
			GrantedTool: model.GrantedTool{ServerName: "server-a", ToolName: "tool2"},
			Client:      mcp.NewClient("http://server-a.example.com:8080"),
		},
		{
			GrantedTool: model.GrantedTool{ServerName: "server-b", ToolName: "tool3"},
			Client:      mcp.NewClient("http://server-b.example.com:9090"),
		},
	}

	a := &ClaudeCodeAgent{tools: tools}

	t.Run("without gate", func(t *testing.T) {
		path, err := a.writeMCPConfig("")
		if err != nil {
			t.Fatalf("writeMCPConfig: %v", err)
		}
		defer os.Remove(path)

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}

		var cfg map[string]any
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("parse config: %v", err)
		}

		servers, ok := cfg["mcpServers"].(map[string]any)
		if !ok {
			t.Fatalf("expected mcpServers map, got: %T", cfg["mcpServers"])
		}
		if len(servers) != 2 {
			t.Fatalf("expected 2 unique servers, got %d: %v", len(servers), servers)
		}
		if _, ok := servers["server-a"]; !ok {
			t.Fatalf("expected server-a in config")
		}
		if _, ok := servers["server-b"]; !ok {
			t.Fatalf("expected server-b in config")
		}
		if _, ok := servers["gleipnir_gate"]; ok {
			t.Fatalf("expected no gleipnir_gate when gateURL is empty")
		}
	})

	t.Run("with gate", func(t *testing.T) {
		path, err := a.writeMCPConfig("http://127.0.0.1:12345")
		if err != nil {
			t.Fatalf("writeMCPConfig: %v", err)
		}
		defer os.Remove(path)

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}

		var cfg map[string]any
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("parse config: %v", err)
		}

		servers, ok := cfg["mcpServers"].(map[string]any)
		if !ok {
			t.Fatalf("expected mcpServers map")
		}
		gateEntry, ok := servers["gleipnir_gate"].(map[string]any)
		if !ok {
			t.Fatalf("expected gleipnir_gate entry")
		}
		if gateEntry["url"] != "http://127.0.0.1:12345" {
			t.Fatalf("expected gate URL http://127.0.0.1:12345, got: %v", gateEntry["url"])
		}
	})

	t.Run("server URL matches client", func(t *testing.T) {
		path, err := a.writeMCPConfig("")
		if err != nil {
			t.Fatalf("writeMCPConfig: %v", err)
		}
		defer os.Remove(path)

		data, _ := os.ReadFile(path)
		var cfg map[string]any
		json.Unmarshal(data, &cfg)
		servers := cfg["mcpServers"].(map[string]any)

		serverA := servers["server-a"].(map[string]any)
		if serverA["url"] != "http://server-a.example.com:8080" {
			t.Fatalf("wrong URL for server-a: %v", serverA["url"])
		}
	})
}

func TestClaudeCodeAgent_Run_ContextCancellation(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("test requires /bin/sh")
	}

	// Mock claude that sleeps for 30 seconds — the context cancel should interrupt it.
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	content := "#!/bin/sh\nsleep 30\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write mock claude: %v", err)
	}

	a, sm, runID := buildTestAgent(t, script, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := a.Run(ctx, runID, "slow task")
	if err == nil {
		t.Fatal("expected error after context cancellation, got nil")
	}

	if got := sm.Current(); got != model.RunStatusFailed {
		t.Fatalf("expected status failed after cancellation, got: %s", got)
	}
}
