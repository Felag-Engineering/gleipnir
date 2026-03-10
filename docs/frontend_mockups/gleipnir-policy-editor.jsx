import { useState } from "react";

const SAMPLE_YAML = `name: vikunja-triage
description: >
  Triage newly opened Vikunja tasks in the DevOps project. Check Grafana
  for related alerts and kubectl for pod health before commenting.

trigger:
  type: webhook

capabilities:
  sensors:
    - tool: vikunja.task_get
    - tool: vikunja.task_list
    - tool: grafana.get_alerts
    - tool: kubectl.get_pods

  actuators:
    - tool: vikunja.task_comment
    - tool: vikunja.task_close
      approval: required
      timeout: 30m
      on_timeout: reject

agent:
  task: |
    A new task has been opened in the DevOps Vikunja project.
    1. Read the task details from the trigger payload.
    2. Check Grafana for any active alerts that might be related.
    3. Check kubectl for pod health in the relevant namespace.
    4. Post a comment summarising what you found and recommended priority.
    5. If clearly a duplicate, close it — but this requires approval.

  limits:
    max_tokens_per_run: 20000
    max_tool_calls_per_run: 50

  concurrency: skip`;

const MOCK_TOOLS = {
  "vikunja": [
    { name: "task_get", role: "sensor", desc: "Get a task by ID" },
    { name: "task_list", role: "sensor", desc: "List tasks in a project" },
    { name: "task_comment", role: "actuator", desc: "Post a comment on a task" },
    { name: "task_close", role: "actuator", desc: "Close a task" },
    { name: "task_create", role: "actuator", desc: "Create a new task" },
    { name: "project_list", role: "sensor", desc: "List all projects" },
  ],
  "grafana": [
    { name: "get_alerts", role: "sensor", desc: "Get active alerts" },
    { name: "get_dashboard", role: "sensor", desc: "Get dashboard data" },
  ],
  "kubectl": [
    { name: "get_pods", role: "sensor", desc: "List pods in namespace" },
    { name: "get_events", role: "sensor", desc: "Get recent cluster events" },
  ]
};

const ROLE_COLORS = {
  sensor: { color: "#60a5fa", bg: "rgba(96,165,250,0.1)", border: "rgba(96,165,250,0.25)" },
  actuator: { color: "#fb923c", bg: "rgba(251,146,60,0.1)", border: "rgba(251,146,60,0.25)" },
  feedback: { color: "#a78bfa", bg: "rgba(167,139,250,0.1)", border: "rgba(167,139,250,0.25)" }
};

function RoleBadge({ role }) {
  const cfg = ROLE_COLORS[role] || ROLE_COLORS.sensor;
  return (
    <span style={{
      fontSize: "10px",
      fontFamily: "'IBM Plex Mono', monospace",
      color: cfg.color,
      background: cfg.bg,
      border: `1px solid ${cfg.border}`,
      padding: "1px 6px",
      borderRadius: "3px",
      letterSpacing: "0.03em"
    }}>{role}</span>
  );
}

function SectionLabel({ children, hint }) {
  return (
    <div style={{ marginBottom: "8px" }}>
      <span style={{ fontSize: "11px", color: "#64748b", textTransform: "uppercase", letterSpacing: "0.08em", fontWeight: 500 }}>
        {children}
      </span>
      {hint && <span style={{ fontSize: "11px", color: "#334155", marginLeft: "8px" }}>{hint}</span>}
    </div>
  );
}

function ToolRow({ server, tool, onRemove, showApproval, onToggleApproval }) {
  return (
    <div style={{
      display: "flex",
      alignItems: "center",
      gap: "8px",
      padding: "8px 12px",
      background: "#0d1018",
      border: "1px solid #1e2330",
      borderRadius: "6px",
      marginBottom: "6px"
    }}>
      <RoleBadge role={tool.role} />
      <span style={{ fontFamily: "'IBM Plex Mono', monospace", fontSize: "12px", color: "#cbd5e1", flex: 1 }}>
        {server}.{tool.name}
      </span>
      <span style={{ fontSize: "11px", color: "#475569", flex: 2 }}>{tool.desc}</span>
      {tool.role === "actuator" && (
        <label style={{ display: "flex", alignItems: "center", gap: "5px", cursor: "pointer", flexShrink: 0 }}>
          <div
            onClick={onToggleApproval}
            style={{
              width: "28px", height: "16px", borderRadius: "8px",
              background: showApproval ? "rgba(245,158,11,0.5)" : "#1e2330",
              border: showApproval ? "1px solid rgba(245,158,11,0.5)" : "1px solid #334155",
              position: "relative", cursor: "pointer", transition: "all 0.2s",
              flexShrink: 0
            }}
          >
            <div style={{
              position: "absolute", top: "2px",
              left: showApproval ? "13px" : "2px",
              width: "10px", height: "10px",
              borderRadius: "50%",
              background: showApproval ? "#f59e0b" : "#475569",
              transition: "left 0.2s"
            }} />
          </div>
          <span style={{ fontSize: "10px", color: showApproval ? "#f59e0b" : "#475569", whiteSpace: "nowrap" }}>
            {showApproval ? "approval req." : "no approval"}
          </span>
        </label>
      )}
      <span onClick={onRemove} style={{ color: "#334155", cursor: "pointer", fontSize: "16px", lineHeight: 1, marginLeft: "4px" }}>×</span>
    </div>
  );
}

export default function PolicyEditor() {
  const [mode, setMode] = useState("form");
  const [yaml, setYaml] = useState(SAMPLE_YAML);
  const [policyName, setPolicyName] = useState("vikunja-triage");
  const [description, setDescription] = useState("Triage newly opened Vikunja tasks in the DevOps project.");
  const [triggerType, setTriggerType] = useState("webhook");
  const [task, setTask] = useState(`A new task has been opened in the DevOps Vikunja project.
1. Read the task details from the trigger payload.
2. Check Grafana for any active alerts that might be related.
3. Check kubectl for pod health in the relevant namespace.
4. Post a comment summarising what you found and recommended priority.
5. If clearly a duplicate, close it — but this requires approval.`);
  const [maxTokens, setMaxTokens] = useState(20000);
  const [maxToolCalls, setMaxToolCalls] = useState(50);
  const [concurrency, setConcurrency] = useState("skip");
  const [addingTool, setAddingTool] = useState(false);
  const [searchTool, setSearchTool] = useState("");
  const [savedTools, setSavedTools] = useState([
    { server: "vikunja", name: "task_get", role: "sensor", desc: "Get a task by ID", approval: false },
    { server: "vikunja", name: "task_list", role: "sensor", desc: "List tasks in a project", approval: false },
    { server: "grafana", name: "get_alerts", role: "sensor", desc: "Get active alerts", approval: false },
    { server: "kubectl", name: "get_pods", role: "sensor", desc: "List pods in namespace", approval: false },
    { server: "vikunja", name: "task_comment", role: "actuator", desc: "Post a comment on a task", approval: false },
    { server: "vikunja", name: "task_close", role: "actuator", desc: "Close a task", approval: true },
  ]);
  const [dirty, setDirty] = useState(false);

  const allTools = Object.entries(MOCK_TOOLS).flatMap(([server, tools]) =>
    tools.map(t => ({ ...t, server }))
  );
  const filteredSearch = allTools.filter(t => {
    const q = searchTool.toLowerCase();
    return (
      !savedTools.find(st => st.server === t.server && st.name === t.name) &&
      (`${t.server}.${t.name}`.includes(q) || t.desc.toLowerCase().includes(q))
    );
  });

  const sensors = savedTools.filter(t => t.role === "sensor");
  const actuators = savedTools.filter(t => t.role === "actuator");

  return (
    <div style={{
      fontFamily: "'IBM Plex Sans', system-ui, sans-serif",
      background: "#0f1117",
      minHeight: "100vh",
      color: "#e2e8f0",
    }}>
      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=IBM+Plex+Sans:wght@300;400;500;600&display=swap');
        * { box-sizing: border-box; margin: 0; padding: 0; }
        textarea { resize: vertical; }
        ::-webkit-scrollbar { width: 4px; }
        ::-webkit-scrollbar-track { background: #1e2330; }
        ::-webkit-scrollbar-thumb { background: #334155; border-radius: 2px; }
        input:focus, textarea:focus, select:focus { outline: none; border-color: #3b82f6 !important; }
        .tool-search-result:hover { background: #131720 !important; cursor: pointer; }
        .mode-btn { transition: all 0.15s; }
        .trigger-btn { transition: all 0.15s; cursor: pointer; }
        .trigger-btn:hover { border-color: #334155 !important; }
      `}</style>

      {/* Top bar */}
      <div style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        padding: "0 24px",
        height: "52px",
        borderBottom: "1px solid #1e2330",
        background: "#0d1018",
      }}>
        <div style={{ display: "flex", alignItems: "center", gap: "16px" }}>
          <div style={{ display: "flex", alignItems: "center", gap: "10px" }}>
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none">
              <path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2z" fill="#3b82f6" opacity="0.2"/>
              <path d="M12 6v6l4 2" stroke="#3b82f6" strokeWidth="2" strokeLinecap="round"/>
              <path d="M2 12h3M19 12h3M12 2v3M12 19v3" stroke="#3b82f6" strokeWidth="1.5" strokeLinecap="round" opacity="0.5"/>
            </svg>
            <span style={{ fontSize: "15px", fontWeight: 600, letterSpacing: "0.05em", color: "#f1f5f9" }}>GLEIPNIR</span>
          </div>
          <span style={{ color: "#1e2330" }}>›</span>
          <span style={{ fontSize: "13px", color: "#475569" }}>Policies</span>
          <span style={{ color: "#1e2330" }}>›</span>
          <span style={{ fontSize: "13px", color: "#e2e8f0", fontFamily: "'IBM Plex Mono', monospace" }}>{policyName || "new-policy"}</span>
          {dirty && <span style={{ width: "6px", height: "6px", borderRadius: "50%", background: "#f59e0b" }} />}
        </div>

        <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
          {/* Mode toggle */}
          <div style={{
            display: "flex",
            background: "#0d1018",
            border: "1px solid #1e2330",
            borderRadius: "6px",
            padding: "2px",
            gap: "2px"
          }}>
            {[
              { id: "form", label: "Form", icon: <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M9 21V9"/></svg> },
              { id: "yaml", label: "YAML", icon: <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg> }
            ].map(m => (
              <button key={m.id} className="mode-btn" onClick={() => setMode(m.id)} style={{
                padding: "5px 12px",
                borderRadius: "4px",
                border: "none",
                background: mode === m.id ? "#1e2330" : "transparent",
                color: mode === m.id ? "#e2e8f0" : "#475569",
                fontSize: "12px",
                cursor: "pointer",
                display: "flex",
                alignItems: "center",
                gap: "5px",
                fontFamily: "'IBM Plex Sans', sans-serif"
              }}>
                {m.icon} {m.label}
              </button>
            ))}
          </div>

          <button style={{
            padding: "7px 16px",
            background: dirty ? "#3b82f6" : "#1e2330",
            border: "none",
            borderRadius: "6px",
            color: dirty ? "#fff" : "#475569",
            fontSize: "13px",
            cursor: dirty ? "pointer" : "default",
            fontFamily: "'IBM Plex Sans', sans-serif",
            fontWeight: 500,
            transition: "all 0.15s"
          }} onClick={() => setDirty(false)}>
            {dirty ? "Save Policy" : "Saved"}
          </button>
        </div>
      </div>

      {mode === "yaml" ? (
        /* YAML mode */
        <div style={{ padding: "24px", height: "calc(100vh - 52px)", display: "flex", flexDirection: "column" }}>
          <div style={{
            display: "flex", alignItems: "center", justifyContent: "space-between",
            marginBottom: "12px"
          }}>
            <div style={{ fontSize: "11px", color: "#475569" }}>
              Editing raw YAML — changes here sync to the form view on switch.
            </div>
            <div style={{ display: "flex", gap: "16px", fontSize: "11px", color: "#334155" }}>
              <span style={{ color: "#4ade80" }}>● Valid YAML</span>
            </div>
          </div>
          <textarea
            value={yaml}
            onChange={e => { setYaml(e.target.value); setDirty(true); }}
            style={{
              flex: 1,
              fontFamily: "'IBM Plex Mono', monospace",
              fontSize: "13px",
              lineHeight: "1.7",
              background: "#0d1018",
              border: "1px solid #1e2330",
              borderRadius: "8px",
              padding: "20px 24px",
              color: "#cbd5e1",
              width: "100%",
            }}
          />
        </div>
      ) : (
        /* Form mode */
        <div style={{ display: "flex", height: "calc(100vh - 52px)" }}>
          {/* Main form */}
          <div style={{ flex: 1, overflow: "auto", padding: "24px 32px" }}>
            <div style={{ maxWidth: "680px" }}>

              {/* Meta */}
              <div style={{ marginBottom: "32px" }}>
                <SectionLabel>Policy Identity</SectionLabel>
                <div style={{ display: "flex", gap: "12px", marginBottom: "10px" }}>
                  <div style={{ flex: 1 }}>
                    <label style={{ fontSize: "11px", color: "#475569", display: "block", marginBottom: "5px" }}>Name</label>
                    <input
                      value={policyName}
                      onChange={e => { setPolicyName(e.target.value); setDirty(true); }}
                      placeholder="my-policy-name"
                      style={{
                        width: "100%", padding: "8px 12px",
                        background: "#0d1018", border: "1px solid #1e2330",
                        borderRadius: "6px", color: "#e2e8f0",
                        fontFamily: "'IBM Plex Mono', monospace", fontSize: "13px"
                      }}
                    />
                  </div>
                </div>
                <div>
                  <label style={{ fontSize: "11px", color: "#475569", display: "block", marginBottom: "5px" }}>Description</label>
                  <input
                    value={description}
                    onChange={e => { setDescription(e.target.value); setDirty(true); }}
                    placeholder="What does this policy do?"
                    style={{
                      width: "100%", padding: "8px 12px",
                      background: "#0d1018", border: "1px solid #1e2330",
                      borderRadius: "6px", color: "#e2e8f0", fontSize: "13px"
                    }}
                  />
                </div>
              </div>

              {/* Trigger */}
              <div style={{ marginBottom: "32px" }}>
                <SectionLabel hint="When should this agent run?">Trigger</SectionLabel>
                <div style={{ display: "flex", gap: "8px" }}>
                  {[
                    { id: "webhook", label: "Webhook", desc: "Triggered by an external HTTP call", icon: "⚡" },
                    { id: "cron", label: "Schedule", desc: "Runs on a recurring schedule", icon: "🕐" },
                    { id: "poll", label: "Poll", desc: "Watches a URL for changes", icon: "🔄" }
                  ].map(t => (
                    <div
                      key={t.id}
                      className="trigger-btn"
                      onClick={() => { setTriggerType(t.id); setDirty(true); }}
                      style={{
                        flex: 1,
                        padding: "12px 14px",
                        background: triggerType === t.id ? "rgba(59,130,246,0.08)" : "#0d1018",
                        border: triggerType === t.id ? "1px solid rgba(59,130,246,0.4)" : "1px solid #1e2330",
                        borderRadius: "8px",
                        cursor: "pointer"
                      }}
                    >
                      <div style={{ fontSize: "16px", marginBottom: "4px" }}>{t.icon}</div>
                      <div style={{ fontSize: "13px", fontWeight: 500, color: triggerType === t.id ? "#93c5fd" : "#94a3b8", marginBottom: "2px" }}>{t.label}</div>
                      <div style={{ fontSize: "11px", color: "#475569" }}>{t.desc}</div>
                    </div>
                  ))}
                </div>
                {triggerType === "webhook" && (
                  <div style={{
                    marginTop: "10px", padding: "10px 14px",
                    background: "#0d1018", border: "1px solid #1e2330",
                    borderRadius: "6px", display: "flex", alignItems: "center", gap: "8px"
                  }}>
                    <span style={{ fontSize: "11px", color: "#475569" }}>Endpoint:</span>
                    <span style={{ fontFamily: "'IBM Plex Mono', monospace", fontSize: "12px", color: "#60a5fa" }}>
                      POST /api/v1/webhooks/{policyName || "<policy-id>"}
                    </span>
                    <span style={{ marginLeft: "auto", fontSize: "11px", color: "#334155", cursor: "pointer" }}>Copy ↗</span>
                  </div>
                )}
                {triggerType === "cron" && (
                  <div style={{ marginTop: "10px" }}>
                    <input placeholder='Cron expression, e.g. "0 8 * * 1-5"' style={{
                      width: "100%", padding: "8px 12px",
                      background: "#0d1018", border: "1px solid #1e2330",
                      borderRadius: "6px", color: "#e2e8f0",
                      fontFamily: "'IBM Plex Mono', monospace", fontSize: "13px"
                    }} />
                  </div>
                )}
              </div>

              {/* Capabilities */}
              <div style={{ marginBottom: "32px" }}>
                <SectionLabel hint="What tools can this agent use?">Capabilities</SectionLabel>

                {/* Legend */}
                <div style={{ display: "flex", gap: "12px", marginBottom: "12px" }}>
                  {["sensor", "actuator"].map(role => (
                    <div key={role} style={{ display: "flex", alignItems: "center", gap: "6px" }}>
                      <RoleBadge role={role} />
                      <span style={{ fontSize: "11px", color: "#475569" }}>
                        {role === "sensor" ? "read-only, called freely" : "world-affecting, optionally gated"}
                      </span>
                    </div>
                  ))}
                </div>

                {/* Tool list */}
                {savedTools.length === 0 && (
                  <div style={{
                    padding: "24px", textAlign: "center", border: "1px dashed #1e2330",
                    borderRadius: "8px", color: "#334155", fontSize: "13px", marginBottom: "8px"
                  }}>
                    No tools added yet. Add tools from the registry below.
                  </div>
                )}
                {savedTools.map((t, i) => (
                  <ToolRow
                    key={`${t.server}.${t.name}`}
                    server={t.server}
                    tool={t}
                    showApproval={t.approval}
                    onToggleApproval={() => {
                      const updated = [...savedTools];
                      updated[i] = { ...t, approval: !t.approval };
                      setSavedTools(updated);
                      setDirty(true);
                    }}
                    onRemove={() => {
                      setSavedTools(savedTools.filter((_, j) => j !== i));
                      setDirty(true);
                    }}
                  />
                ))}

                {/* Add tool */}
                {!addingTool ? (
                  <button
                    onClick={() => setAddingTool(true)}
                    style={{
                      display: "flex", alignItems: "center", gap: "6px",
                      padding: "7px 14px", marginTop: "4px",
                      background: "transparent", border: "1px dashed #1e2330",
                      borderRadius: "6px", color: "#475569", fontSize: "12px",
                      cursor: "pointer", width: "100%", justifyContent: "center"
                    }}
                  >
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                      <line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>
                    </svg>
                    Add tool from registry
                  </button>
                ) : (
                  <div style={{
                    border: "1px solid #1e2330", borderRadius: "8px", overflow: "hidden", marginTop: "4px"
                  }}>
                    <div style={{ padding: "8px 12px", borderBottom: "1px solid #1e2330", background: "#0d1018", display: "flex", gap: "8px" }}>
                      <input
                        autoFocus
                        value={searchTool}
                        onChange={e => setSearchTool(e.target.value)}
                        placeholder="Search tools by name or server..."
                        style={{
                          flex: 1, padding: "6px 10px",
                          background: "#131720", border: "1px solid #1e2330",
                          borderRadius: "5px", color: "#e2e8f0", fontSize: "12px",
                          fontFamily: "'IBM Plex Mono', monospace"
                        }}
                      />
                      <button onClick={() => { setAddingTool(false); setSearchTool(""); }} style={{
                        padding: "6px 10px", background: "transparent", border: "1px solid #1e2330",
                        borderRadius: "5px", color: "#475569", cursor: "pointer", fontSize: "12px"
                      }}>Cancel</button>
                    </div>
                    <div style={{ maxHeight: "180px", overflow: "auto" }}>
                      {filteredSearch.map(t => (
                        <div
                          key={`${t.server}.${t.name}`}
                          className="tool-search-result"
                          style={{
                            display: "flex", alignItems: "center", gap: "10px",
                            padding: "9px 14px", borderBottom: "1px solid #1a1f2e",
                            background: "transparent", transition: "background 0.1s"
                          }}
                          onClick={() => {
                            setSavedTools([...savedTools, { ...t, approval: false }]);
                            setSearchTool("");
                            setAddingTool(false);
                            setDirty(true);
                          }}
                        >
                          <RoleBadge role={t.role} />
                          <span style={{ fontFamily: "'IBM Plex Mono', monospace", fontSize: "12px", color: "#94a3b8" }}>
                            {t.server}.{t.name}
                          </span>
                          <span style={{ fontSize: "11px", color: "#475569" }}>{t.desc}</span>
                        </div>
                      ))}
                      {filteredSearch.length === 0 && (
                        <div style={{ padding: "16px", textAlign: "center", fontSize: "12px", color: "#334155" }}>
                          No tools match your search
                        </div>
                      )}
                    </div>
                  </div>
                )}
              </div>

              {/* Task instructions */}
              <div style={{ marginBottom: "32px" }}>
                <SectionLabel hint="What should the agent do?">Task Instructions</SectionLabel>
                <textarea
                  value={task}
                  onChange={e => { setTask(e.target.value); setDirty(true); }}
                  rows={8}
                  style={{
                    width: "100%", padding: "12px 14px",
                    background: "#0d1018", border: "1px solid #1e2330",
                    borderRadius: "8px", color: "#cbd5e1", fontSize: "13px",
                    lineHeight: "1.7", fontFamily: "'IBM Plex Sans', sans-serif"
                  }}
                />
                <div style={{ fontSize: "11px", color: "#334155", marginTop: "6px" }}>
                  The trigger payload (webhook body, poll result) is delivered as the agent's first message — reference it as needed.
                </div>
              </div>

              {/* Limits */}
              <div style={{ marginBottom: "32px" }}>
                <SectionLabel hint="Guard rails on each run">Run Limits</SectionLabel>
                <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "12px" }}>
                  <div>
                    <label style={{ fontSize: "11px", color: "#475569", display: "block", marginBottom: "5px" }}>
                      Max tokens per run
                    </label>
                    <input
                      type="number"
                      value={maxTokens}
                      onChange={e => { setMaxTokens(e.target.value); setDirty(true); }}
                      style={{
                        width: "100%", padding: "8px 12px",
                        background: "#0d1018", border: "1px solid #1e2330",
                        borderRadius: "6px", color: "#e2e8f0",
                        fontFamily: "'IBM Plex Mono', monospace", fontSize: "13px"
                      }}
                    />
                  </div>
                  <div>
                    <label style={{ fontSize: "11px", color: "#475569", display: "block", marginBottom: "5px" }}>
                      Max tool calls per run
                    </label>
                    <input
                      type="number"
                      value={maxToolCalls}
                      onChange={e => { setMaxToolCalls(e.target.value); setDirty(true); }}
                      style={{
                        width: "100%", padding: "8px 12px",
                        background: "#0d1018", border: "1px solid #1e2330",
                        borderRadius: "6px", color: "#e2e8f0",
                        fontFamily: "'IBM Plex Mono', monospace", fontSize: "13px"
                      }}
                    />
                  </div>
                </div>
              </div>

              {/* Concurrency */}
              <div style={{ marginBottom: "32px" }}>
                <SectionLabel hint="When a trigger fires while a run is already active">Concurrency Behaviour</SectionLabel>
                <div style={{ display: "grid", gridTemplateColumns: "repeat(4, 1fr)", gap: "8px" }}>
                  {[
                    { id: "skip", label: "Skip", desc: "Discard the new trigger" },
                    { id: "queue", label: "Queue", desc: "Run after current finishes" },
                    { id: "parallel", label: "Parallel", desc: "Run concurrently" },
                    { id: "replace", label: "Replace", desc: "Cancel current, start fresh" },
                  ].map(c => (
                    <div
                      key={c.id}
                      onClick={() => { setConcurrency(c.id); setDirty(true); }}
                      style={{
                        padding: "10px 12px",
                        background: concurrency === c.id ? "rgba(59,130,246,0.08)" : "#0d1018",
                        border: concurrency === c.id ? "1px solid rgba(59,130,246,0.4)" : "1px solid #1e2330",
                        borderRadius: "6px", cursor: "pointer"
                      }}
                    >
                      <div style={{ fontSize: "12px", fontWeight: 500, color: concurrency === c.id ? "#93c5fd" : "#94a3b8", marginBottom: "3px" }}>{c.label}</div>
                      <div style={{ fontSize: "10px", color: "#475569" }}>{c.desc}</div>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          </div>

          {/* Right sidebar — context panel */}
          <div style={{
            width: "260px",
            borderLeft: "1px solid #1e2330",
            background: "#0d1018",
            padding: "24px 20px",
            overflow: "auto",
            flexShrink: 0
          }}>
            <div style={{ marginBottom: "24px" }}>
              <div style={{ fontSize: "11px", color: "#334155", textTransform: "uppercase", letterSpacing: "0.08em", marginBottom: "12px" }}>
                Capability Envelope
              </div>
              <div style={{ display: "flex", gap: "12px", marginBottom: "14px" }}>
                <div style={{ textAlign: "center" }}>
                  <div style={{ fontSize: "22px", fontFamily: "'IBM Plex Mono', monospace", color: "#60a5fa", fontWeight: 500 }}>{sensors.length}</div>
                  <div style={{ fontSize: "10px", color: "#475569" }}>sensors</div>
                </div>
                <div style={{ textAlign: "center" }}>
                  <div style={{ fontSize: "22px", fontFamily: "'IBM Plex Mono', monospace", color: "#fb923c", fontWeight: 500 }}>{actuators.length}</div>
                  <div style={{ fontSize: "10px", color: "#475569" }}>actuators</div>
                </div>
                <div style={{ textAlign: "center" }}>
                  <div style={{ fontSize: "22px", fontFamily: "'IBM Plex Mono', monospace", color: "#f59e0b", fontWeight: 500 }}>
                    {actuators.filter(t => t.approval).length}
                  </div>
                  <div style={{ fontSize: "10px", color: "#475569" }}>gated</div>
                </div>
              </div>

              {actuators.filter(t => t.approval).length > 0 && (
                <div style={{
                  padding: "10px 12px",
                  background: "rgba(245,158,11,0.06)", border: "1px solid rgba(245,158,11,0.15)",
                  borderRadius: "6px", fontSize: "11px", color: "#92400e", lineHeight: "1.6"
                }}>
                  {actuators.filter(t => t.approval).map(t => (
                    <div key={`${t.server}.${t.name}`} style={{ color: "#f59e0b", fontFamily: "'IBM Plex Mono', monospace", fontSize: "10px" }}>
                      ⚠ {t.server}.{t.name} requires approval
                    </div>
                  ))}
                </div>
              )}
            </div>

            <div style={{ marginBottom: "24px" }}>
              <div style={{ fontSize: "11px", color: "#334155", textTransform: "uppercase", letterSpacing: "0.08em", marginBottom: "10px" }}>
                Run Limits
              </div>
              <div style={{ fontSize: "12px", color: "#475569", lineHeight: "2", fontFamily: "'IBM Plex Mono', monospace" }}>
                <div>{maxTokens.toLocaleString()} <span style={{ color: "#334155" }}>tokens</span></div>
                <div>{maxToolCalls} <span style={{ color: "#334155" }}>tool calls</span></div>
                <div>{concurrency} <span style={{ color: "#334155" }}>concurrency</span></div>
              </div>
            </div>

            <div>
              <div style={{ fontSize: "11px", color: "#334155", textTransform: "uppercase", letterSpacing: "0.08em", marginBottom: "10px" }}>
                Quick Actions
              </div>
              {[
                { label: "Test trigger", icon: "▶" },
                { label: "View run history", icon: "↗" },
                { label: "Duplicate policy", icon: "⎘" },
                { label: "Delete policy", icon: "✕", danger: true }
              ].map(action => (
                <div key={action.label} style={{
                  padding: "8px 10px", borderRadius: "5px",
                  color: action.danger ? "#f87171" : "#64748b",
                  fontSize: "12px", cursor: "pointer",
                  display: "flex", alignItems: "center", gap: "8px",
                  marginBottom: "2px"
                }}>
                  <span style={{ fontSize: "10px", opacity: 0.7 }}>{action.icon}</span>
                  {action.label}
                </div>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
