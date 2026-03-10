import { useState, useEffect, useCallback, useRef } from "react";

// ─── Approval definitions ─────────────────────────────────────────────────────

const APPROVAL_DEFS = [
  {
    id: "ap1", runId: "r301", policyId: "p3",
    policyName: "vikunja-close-resolved", folder: "Vikunja",
    toolName: "vikunja.task_close",
    proposedInput: { task_id: 1040, reason: "All acceptance criteria met. No open blockers detected." },
    agentSummary: "Wants to close task #1040 — all acceptance criteria met, no open blockers.",
    reasoning: [
      { type: "thought",     text: "The trigger payload indicates task #1040 has had its last sub-task checked off. I should verify the task state before closing." },
      { type: "tool_call",   text: "vikunja.task_get",  detail: '{ "task_id": 1040 }' },
      { type: "tool_result", text: "Task #1040: 'Deploy auth service to prod'. Status: open. Sub-tasks: 4/4 complete. No blockers. Due: 2026-03-07." },
      { type: "thought",     text: "All four sub-tasks complete, due date today, no blockers. Criteria met for closure. Actuator requires approval — requesting before proceeding." },
    ],
    expiresAt:       new Date(Date.now() + 58 * 60 * 1000).toISOString(),
    startedAt:       new Date(Date.now() -  3 * 60 * 1000).toISOString(),
    approvedStatus:  "running",   approvedSettled: "complete",
    rejectedStatus:  "failed",    rejectedSettled: "failed",
    approvedSummary: "Closed task #1040 successfully after approval.",
    rejectedSummary: "Run rejected by operator — task #1040 not closed.",
  },
  {
    id: "ap2", runId: "r502", policyId: "p5",
    policyName: "kubectl-pod-watcher", folder: "Infrastructure",
    toolName: "vikunja.task_create",
    proposedInput: {
      title: "INC: CrashLoopBackOff — worker-02/log-shipper",
      project: "Incidents", priority: 1,
      description: "Pod log-shipper in namespace worker-02 has been in CrashLoopBackOff for 12 minutes. Last exit code: 1.",
    },
    agentSummary: "Wants to create a P1 incident task for a CrashLoopBackOff pod on worker-02.",
    reasoning: [
      { type: "thought",     text: "Polling detected CrashLoopBackOff on worker-02/log-shipper. I need to assess severity before creating an incident task." },
      { type: "tool_call",   text: "kubectl.get_pods",   detail: '{ "namespace": "worker-02" }' },
      { type: "tool_result", text: "NAME: log-shipper | STATUS: CrashLoopBackOff | RESTARTS: 8 | AGE: 12m" },
      { type: "tool_call",   text: "kubectl.get_events", detail: '{ "namespace": "worker-02", "pod": "log-shipper" }' },
      { type: "tool_result", text: "Error: failed to connect to log aggregator at 10.0.1.44:5044 — connection refused. Exit code 1." },
      { type: "thought",     text: "8 restarts over 12 minutes, connection refused to log aggregator. Log aggregator likely down. P1 incident warranted. Requesting approval." },
    ],
    expiresAt:       new Date(Date.now() + 18 * 60 * 1000).toISOString(),
    startedAt:       new Date(Date.now() -  7 * 60 * 1000).toISOString(),
    approvedStatus:  "running",   approvedSettled: "complete",
    rejectedStatus:  "failed",    rejectedSettled: "failed",
    approvedSummary: "Incident task #INC-042 created for worker-02/log-shipper.",
    rejectedSummary: "Run rejected by operator — incident task not created.",
  },
];

// ─── Initial folder data ──────────────────────────────────────────────────────

const INITIAL_FOLDERS = [
  {
    id: "f1", name: "Vikunja",
    policies: [
      {
        id: "p1", name: "vikunja-triage", triggerType: "webhook",
        latestRun: { id: "r101", status: "complete", startedAt: "2026-03-07T14:32:11Z", duration: 47, tokenCost: 8420, toolCalls: 12, summary: "Triaged task #1041 — P2, related Grafana alert resolved." },
        history: [
          { id: "r100", status: "complete",    startedAt: "2026-03-07T13:10:22Z", duration: 62, tokenCost: 11200, toolCalls: 18, summary: "Triaged task #1039 — P1, linked to active Grafana alert." },
          { id: "r099", status: "failed",      startedAt: "2026-03-07T11:55:44Z", duration:  3, tokenCost:   420, toolCalls:  1, summary: "Tool call limit exceeded before completion." },
          { id: "r098", status: "complete",    startedAt: "2026-03-07T09:21:08Z", duration: 38, tokenCost:  7100, toolCalls: 11, summary: "Triaged task #1037 — P3, no related alerts." },
          { id: "r097", status: "complete",    startedAt: "2026-03-06T16:44:55Z", duration: 55, tokenCost:  9300, toolCalls: 14, summary: "Triaged task #1035 — P2, pod restarts on api-gateway." },
          { id: "r096", status: "interrupted", startedAt: "2026-03-06T14:12:00Z", duration:  5, tokenCost:   890, toolCalls:  2, summary: "Interrupted: Gleipnir restarted during execution." },
        ],
      },
      {
        id: "p2", name: "vikunja-daily-digest", triggerType: "cron",
        latestRun: { id: "r201", status: "running", startedAt: "2026-03-07T08:00:00Z", duration: null, tokenCost: 1850, toolCalls: 4, summary: null },
        history: [
          { id: "r200", status: "complete", startedAt: "2026-03-06T08:00:00Z", duration: 84, tokenCost: 10200, toolCalls: 22, summary: "Digest posted — 7 overdue tasks across 3 projects." },
          { id: "r199", status: "complete", startedAt: "2026-03-05T08:00:00Z", duration: 71, tokenCost:  8900, toolCalls: 19, summary: "Digest posted — 4 overdue tasks." },
        ],
      },
      {
        id: "p3", name: "vikunja-close-resolved", triggerType: "poll",
        latestRun: { id: "r301", status: "waiting_for_approval", startedAt: "2026-03-07T14:29:05Z", duration: null, tokenCost: 3210, toolCalls: 7, summary: "Wants to close task #1040 — all criteria met." },
        history: [
          { id: "r300", status: "complete", startedAt: "2026-03-07T12:00:00Z", duration: 29, tokenCost: 4100, toolCalls: 8, summary: "Closed task #1038 after approval." },
          { id: "r299", status: "complete", startedAt: "2026-03-06T18:30:00Z", duration: 33, tokenCost: 4800, toolCalls: 9, summary: "Closed task #1036 after approval." },
        ],
      },
    ],
  },
  {
    id: "f2", name: "Grafana",
    policies: [
      {
        id: "p4", name: "grafana-alert-responder", triggerType: "poll",
        latestRun: { id: "r401", status: "complete", startedAt: "2026-03-07T12:44:00Z", duration: 38, tokenCost: 6100, toolCalls: 9, summary: "Incident task #1038 created for memory-pressure alert on worker-03." },
        history: [
          { id: "r400", status: "complete", startedAt: "2026-03-07T10:12:00Z", duration: 41, tokenCost: 6400, toolCalls: 10, summary: "Incident task created for CPU spike on api-gateway." },
          { id: "r399", status: "failed",   startedAt: "2026-03-07T06:55:00Z", duration:  4, tokenCost:   510, toolCalls:  2, summary: "Grafana MCP server unreachable — connection refused." },
        ],
      },
    ],
  },
  {
    id: "f3", name: "Infrastructure",
    policies: [
      {
        id: "p5", name: "kubectl-pod-watcher", triggerType: "poll",
        latestRun: { id: "r502", status: "waiting_for_approval", startedAt: "2026-03-07T14:38:00Z", duration: null, tokenCost: 4100, toolCalls: 6, summary: "Wants to create P1 incident task — CrashLoopBackOff on worker-02." },
        history: [
          { id: "r501", status: "complete", startedAt: "2026-03-07T14:15:00Z", duration: 22, tokenCost: 3200, toolCalls: 6, summary: "No CrashLoopBackOff pods detected. All namespaces healthy." },
          { id: "r500", status: "complete", startedAt: "2026-03-07T14:00:00Z", duration: 20, tokenCost: 3100, toolCalls: 5, summary: "No issues detected." },
        ],
      },
    ],
  },
];

// ─── Status config ────────────────────────────────────────────────────────────

const STATUS = {
  complete:             { label: "Complete",          color: "#4ade80", bg: "rgba(74,222,128,0.08)",  border: "rgba(74,222,128,0.2)"  },
  running:              { label: "Running",           color: "#60a5fa", bg: "rgba(96,165,250,0.08)",  border: "rgba(96,165,250,0.2)",  pulse: true },
  waiting_for_approval: { label: "Awaiting Approval", color: "#f59e0b", bg: "rgba(245,158,11,0.08)",  border: "rgba(245,158,11,0.2)",  pulse: true },
  failed:               { label: "Failed",            color: "#f87171", bg: "rgba(248,113,113,0.08)", border: "rgba(248,113,113,0.2)" },
  interrupted:          { label: "Interrupted",       color: "#a78bfa", bg: "rgba(167,139,250,0.08)", border: "rgba(167,139,250,0.2)" },
};

// ─── Helpers ──────────────────────────────────────────────────────────────────

const fmtDur = s => s == null ? "—" : s < 60 ? `${s}s` : `${Math.floor(s/60)}m ${s%60}s`;
const fmtTok = n => n >= 1000 ? `${(n/1000).toFixed(1)}k` : String(n);
const fmtAbs = iso => new Date(iso).toLocaleString("en-US", { month:"short", day:"numeric", hour:"2-digit", minute:"2-digit", hour12:false });
const fmtRel = iso => {
  const m = Math.floor((Date.now() - new Date(iso)) / 60000);
  if (m < 1) return "just now";
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  return h < 24 ? `${h}h ago` : new Date(iso).toLocaleDateString("en-US", { month:"short", day:"numeric" });
};
const timeLeft = expiresAt => {
  const secs = Math.max(0, Math.floor((new Date(expiresAt) - Date.now()) / 1000));
  const m = Math.floor(secs / 60), s = secs % 60;
  return { str: `${m}:${String(s).padStart(2,"0")}`, urgent: secs < 300 };
};

// ─── Atoms ────────────────────────────────────────────────────────────────────

const StatusBadge = ({ status }) => {
  const c = STATUS[status] || STATUS.complete;
  return (
    <span style={{ display:"inline-flex", alignItems:"center", gap:5, padding:"2px 8px", borderRadius:4, background:c.bg, border:`1px solid ${c.border}`, fontSize:11, fontFamily:"IBM Plex Mono,monospace", color:c.color, whiteSpace:"nowrap" }}>
      <span style={{ width:6, height:6, borderRadius:"50%", background:c.color, flexShrink:0, animation:c.pulse?"gPulse 1.6s ease-in-out infinite":"none" }} />
      {c.label}
    </span>
  );
};

const TriggerChip = ({ type }) => {
  const col = { webhook:"#60a5fa", cron:"#a78bfa", poll:"#34d399" }[type] || "#94a3b8";
  return <span style={{ fontSize:10, fontFamily:"IBM Plex Mono,monospace", color:col, background:"rgba(255,255,255,0.04)", border:"1px solid rgba(255,255,255,0.08)", padding:"1px 6px", borderRadius:3, flexShrink:0 }}>{type}</span>;
};

const IconChevron = ({ open }) => (
  <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"
    style={{ transition:"transform 0.18s", transform:open?"rotate(90deg)":"rotate(0deg)", flexShrink:0 }}>
    <polyline points="9 18 15 12 9 6"/>
  </svg>
);

const IconHistory = () => (
  <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
    <path d="M3 3v5h5"/><path d="M3.05 13A9 9 0 1 0 6 5.3L3 8"/><path d="M12 7v5l4 2"/>
  </svg>
);

// ─── Reasoning trace ──────────────────────────────────────────────────────────

function ReasoningTrace({ steps }) {
  return (
    <div style={{ display:"flex", flexDirection:"column", gap:0 }}>
      {steps.map((step, i) => {
        const isCall   = step.type === "tool_call";
        const isResult = step.type === "tool_result";
        return (
          <div key={i} style={{ display:"flex", gap:10 }}>
            <div style={{ display:"flex", flexDirection:"column", alignItems:"center", flexShrink:0, width:22 }}>
              <div style={{
                width:20, height:20, borderRadius: isCall||isResult ? 4 : "50%",
                background: isCall ? "rgba(96,165,250,0.12)" : isResult ? "rgba(74,222,128,0.09)" : "rgba(100,116,139,0.09)",
                border: `1px solid ${isCall ? "rgba(96,165,250,0.25)" : isResult ? "rgba(74,222,128,0.2)" : "rgba(100,116,139,0.14)"}`,
                display:"flex", alignItems:"center", justifyContent:"center",
                fontSize:10, color: isCall ? "#60a5fa" : isResult ? "#4ade80" : "#475569",
                flexShrink:0, marginTop:2,
              }}>
                {isCall ? "→" : isResult ? "←" : "·"}
              </div>
              {i < steps.length - 1 && <div style={{ width:1, flex:1, minHeight:6, background:"rgba(255,255,255,0.05)", margin:"2px 0" }} />}
            </div>
            <div style={{ flex:1, paddingBottom: i < steps.length-1 ? 10 : 0, paddingTop:2, minWidth:0 }}>
              {isCall && (
                <div>
                  <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:11, color:"#60a5fa" }}>{step.text}</span>
                  {step.detail && <div style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:10, color:"#334155", marginTop:3, padding:"4px 8px", background:"rgba(96,165,250,0.05)", borderRadius:4, border:"1px solid rgba(96,165,250,0.1)", wordBreak:"break-all" }}>{step.detail}</div>}
                </div>
              )}
              {isResult && <div style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:10, color:"#334155", padding:"4px 8px", background:"rgba(74,222,128,0.04)", borderRadius:4, border:"1px solid rgba(74,222,128,0.1)", wordBreak:"break-all" }}>{step.text}</div>}
              {step.type === "thought" && <p style={{ fontSize:12, color:"#64748b", lineHeight:1.6, margin:0 }}>{step.text}</p>}
            </div>
          </div>
        );
      })}
    </div>
  );
}

// ─── Approval receipt (fades out after 5s) ────────────────────────────────────

function ApprovalReceipt({ receipt, def, onExpired }) {
  // Track whether the 5s fade animation has fired
  const [visible, setVisible] = useState(true);

  useEffect(() => {
    // Start fade after 3.5s (run has settled by then), complete by 5s
    const fadeTimer   = setTimeout(() => setVisible(false), 3500);
    const removeTimer = setTimeout(() => onExpired(receipt.id), 5000);
    return () => { clearTimeout(fadeTimer); clearTimeout(removeTimer); };
  }, []);

  const approved = receipt.decision === "approve";

  return (
    <div style={{
      display:"flex", alignItems:"center", gap:10,
      padding:"9px 14px", borderRadius:7,
      background: approved ? "rgba(74,222,128,0.05)" : "rgba(248,113,113,0.05)",
      border: `1px solid ${approved ? "rgba(74,222,128,0.15)" : "rgba(248,113,113,0.15)"}`,
      // CSS opacity transition handles the fade
      opacity: visible ? 1 : 0,
      transition: "opacity 1.5s ease",
    }}>
      {/* Icon */}
      <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke={approved?"#4ade80":"#f87171"} strokeWidth="2.5">
        {approved
          ? <polyline points="20 6 9 17 4 12"/>
          : <><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></>
        }
      </svg>

      <span style={{ fontSize:12, color:approved?"#4ade80":"#f87171", fontWeight:500 }}>
        {approved ? "Approved" : "Rejected"}
      </span>
      <span style={{ fontSize:12, color:"#334155" }}>
        {def?.policyName} · <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:11 }}>{def?.toolName}</span>
      </span>
      {receipt.note && (
        <span style={{ fontSize:11, color:"#2d3748", fontStyle:"italic" }}>"{receipt.note}"</span>
      )}

      {/* Settling indicator — shown until the poll confirms */}
      {!receipt.settled && (
        <span style={{ display:"flex", alignItems:"center", gap:5, marginLeft:"auto", fontSize:11, color:"#334155" }}>
          <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="#334155" strokeWidth="2"
            style={{ animation:"gSpin 1s linear infinite" }}>
            <path d="M21 12a9 9 0 1 1-9-9"/>
          </svg>
          confirming…
        </span>
      )}
      {receipt.settled && (
        <span style={{ marginLeft:"auto", fontFamily:"IBM Plex Mono,monospace", fontSize:10, color:"#1e2a3a" }}>
          just now
        </span>
      )}
    </div>
  );
}

// ─── Single approval card ─────────────────────────────────────────────────────

function ApprovalCard({ def, onDecide }) {
  const [expanded, setExpanded] = useState(false);
  const [deciding, setDeciding] = useState(null);
  const [note, setNote]         = useState("");
  const tl = timeLeft(def.expiresAt);

  const confirm = decision => {
    setDeciding(null);
    onDecide(def.id, decision, note);
  };

  return (
    <div style={{
      background:"#0f1219",
      border:"1px solid rgba(245,158,11,0.28)",
      borderRadius:10, overflow:"hidden",
      boxShadow:"0 0 0 1px rgba(245,158,11,0.07), 0 4px 24px rgba(0,0,0,0.4)",
    }}>
      <div style={{ height:3, background:"linear-gradient(90deg,#f59e0b,#fb923c)" }} />

      {/* Header */}
      <div style={{ padding:"14px 18px 12px", display:"flex", alignItems:"flex-start", gap:12 }}>
        <div style={{ width:36, height:36, borderRadius:8, flexShrink:0, marginTop:1, background:"rgba(245,158,11,0.1)", border:"1px solid rgba(245,158,11,0.25)", display:"flex", alignItems:"center", justifyContent:"center" }}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#f59e0b" strokeWidth="2">
            <circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
          </svg>
        </div>
        <div style={{ flex:1, minWidth:0 }}>
          <div style={{ display:"flex", alignItems:"center", gap:8, marginBottom:4, flexWrap:"wrap" }}>
            <span style={{ fontSize:13, fontWeight:600, color:"#e2e8f0" }}>{def.policyName}</span>
            <span style={{ fontSize:10, color:"#475569" }}>{def.folder}</span>
            <TriggerChip type="poll" />
          </div>
          <p style={{ fontSize:13, color:"#94a3b8", lineHeight:1.55, margin:0 }}>{def.agentSummary}</p>
        </div>
        <div style={{ display:"flex", flexDirection:"column", alignItems:"flex-end", gap:6, flexShrink:0 }}>
          <div style={{ display:"flex", alignItems:"center", gap:5, padding:"3px 9px", borderRadius:5, background: tl.urgent ? "rgba(248,113,113,0.1)" : "rgba(245,158,11,0.08)", border:`1px solid ${tl.urgent ? "rgba(248,113,113,0.3)" : "rgba(245,158,11,0.2)"}` }}>
            <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke={tl.urgent?"#f87171":"#f59e0b"} strokeWidth="2">
              <circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>
            </svg>
            <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:11, color:tl.urgent?"#f87171":"#f59e0b" }}>{tl.str}</span>
          </div>
          <button onClick={() => setExpanded(e=>!e)} style={{ display:"flex", alignItems:"center", gap:4, background:"transparent", border:"none", cursor:"pointer", color:expanded?"#60a5fa":"#334155", fontSize:11, fontFamily:"IBM Plex Sans,sans-serif", padding:0, transition:"color 0.15s" }}>
            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z"/><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z"/>
            </svg>
            {expanded ? "Hide reasoning" : "Show reasoning"}
          </button>
        </div>
      </div>

      {/* Proposed action */}
      <div style={{ margin:"0 18px 14px", padding:"10px 14px", background:"rgba(0,0,0,0.3)", borderRadius:8, border:"1px solid rgba(255,255,255,0.06)" }}>
        <div style={{ display:"flex", alignItems:"center", gap:8, marginBottom:8 }}>
          <span style={{ fontSize:10, color:"#475569", textTransform:"uppercase", letterSpacing:"0.08em" }}>Proposed action</span>
          <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:12, color:"#fb923c", background:"rgba(251,146,60,0.1)", border:"1px solid rgba(251,146,60,0.25)", padding:"1px 8px", borderRadius:4 }}>{def.toolName}</span>
          <span style={{ fontSize:10, color:"#2d3748", marginLeft:"auto" }}>actuator · approval required</span>
        </div>
        <pre style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:11, color:"#64748b", margin:0, whiteSpace:"pre-wrap", wordBreak:"break-all", lineHeight:1.65 }}>
          {JSON.stringify(def.proposedInput, null, 2)}
        </pre>
      </div>

      {/* Reasoning trace */}
      {expanded && (
        <div style={{ margin:"0 18px 14px", padding:"12px 14px", background:"rgba(0,0,0,0.25)", borderRadius:8, border:"1px solid rgba(255,255,255,0.05)" }}>
          <div style={{ fontSize:10, color:"#334155", textTransform:"uppercase", letterSpacing:"0.08em", marginBottom:12 }}>Agent reasoning</div>
          <ReasoningTrace steps={def.reasoning} />
        </div>
      )}

      {/* Confirm note area */}
      {deciding && (
        <div style={{ margin:"0 18px 14px", padding:"12px 14px", background: deciding==="approve" ? "rgba(74,222,128,0.05)" : "rgba(248,113,113,0.05)", borderRadius:8, border:`1px solid ${deciding==="approve" ? "rgba(74,222,128,0.2)" : "rgba(248,113,113,0.2)"}` }}>
          <div style={{ fontSize:11, color:"#64748b", marginBottom:8 }}>
            {deciding==="approve" ? "Optional note before approving:" : "Optional note before rejecting:"}
          </div>
          <textarea
            value={note} onChange={e=>setNote(e.target.value)}
            placeholder={deciding==="approve" ? "e.g. Confirmed — all criteria verified." : "e.g. Not ready — still waiting on sign-off."}
            rows={2}
            style={{ width:"100%", padding:"8px 10px", borderRadius:6, background:"rgba(0,0,0,0.3)", border:"1px solid rgba(255,255,255,0.08)", color:"#cbd5e1", fontSize:12, fontFamily:"IBM Plex Sans,sans-serif", lineHeight:1.5, resize:"none", marginBottom:10 }}
          />
          <div style={{ display:"flex", gap:8, justifyContent:"flex-end" }}>
            <button onClick={()=>setDeciding(null)} style={{ padding:"6px 14px", borderRadius:5, background:"transparent", border:"1px solid #1e2330", color:"#475569", fontSize:12, cursor:"pointer" }}>Cancel</button>
            <button onClick={()=>confirm(deciding)} style={{ padding:"6px 14px", borderRadius:5, cursor:"pointer", fontSize:12, fontWeight:500, background: deciding==="approve" ? "rgba(74,222,128,0.15)" : "rgba(248,113,113,0.15)", border:`1px solid ${deciding==="approve" ? "rgba(74,222,128,0.4)" : "rgba(248,113,113,0.4)"}`, color: deciding==="approve" ? "#4ade80" : "#f87171" }}>
              Confirm {deciding==="approve" ? "Approve" : "Reject"}
            </button>
          </div>
        </div>
      )}

      {/* Action row */}
      {!deciding && (
        <div style={{ padding:"12px 18px 14px", display:"flex", alignItems:"center", gap:10, borderTop:"1px solid rgba(255,255,255,0.05)" }}>
          <div style={{ fontSize:11, color:"#334155", flex:1 }}>Started {fmtAbs(def.startedAt)} · run paused, waiting for your decision</div>
          <button onClick={()=>setDeciding("reject")} className="btn-reject" style={{ padding:"7px 18px", borderRadius:6, cursor:"pointer", fontSize:13, fontWeight:500, background:"rgba(248,113,113,0.08)", border:"1px solid rgba(248,113,113,0.28)", color:"#f87171", transition:"all 0.15s" }}>Reject</button>
          <button onClick={()=>setDeciding("approve")} className="btn-approve" style={{ padding:"7px 22px", borderRadius:6, cursor:"pointer", fontSize:13, fontWeight:600, background:"rgba(74,222,128,0.12)", border:"1px solid rgba(74,222,128,0.35)", color:"#4ade80", transition:"all 0.15s" }}>Approve</button>
        </div>
      )}
    </div>
  );
}

// ─── Approvals section ────────────────────────────────────────────────────────

function ApprovalsSection({ pendingIds, receipts, onDecide, onReceiptExpired }) {
  const pendingDefs = APPROVAL_DEFS.filter(d => pendingIds.includes(d.id));
  const hasAnything = pendingDefs.length > 0 || receipts.length > 0;
  if (!hasAnything) return null;

  return (
    <div style={{ padding:"20px 20px 0" }}>
      {/* Section header */}
      <div style={{ display:"flex", alignItems:"center", gap:8, marginBottom:14 }}>
        {pendingDefs.length > 0 && (
          <span style={{ width:8, height:8, borderRadius:"50%", background:"#f59e0b", animation:"gPulse 1.6s ease-in-out infinite" }} />
        )}
        <span style={{ fontSize:12, fontWeight:600, color: pendingDefs.length > 0 ? "#f59e0b" : "#475569", letterSpacing:"0.02em" }}>
          Pending Approvals
        </span>
        {pendingDefs.length > 0 && (
          <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:11, color:"#f59e0b", background:"rgba(245,158,11,0.1)", border:"1px solid rgba(245,158,11,0.25)", padding:"0 7px", borderRadius:10, lineHeight:"18px" }}>
            {pendingDefs.length}
          </span>
        )}
        {pendingDefs.length === 0 && receipts.length > 0 && (
          <span style={{ fontSize:11, color:"#334155" }}>— all resolved</span>
        )}
      </div>

      {/* Pending cards */}
      {pendingDefs.length > 0 && (
        <div style={{ display:"flex", flexDirection:"column", gap:12, marginBottom:12 }}>
          {pendingDefs.map(def => (
            <ApprovalCard key={def.id} def={def} onDecide={onDecide} />
          ))}
        </div>
      )}

      {/* Receipts — each manages its own fade timer */}
      {receipts.length > 0 && (
        <div style={{ display:"flex", flexDirection:"column", gap:6, marginBottom:12 }}>
          {receipts.map(r => (
            <ApprovalReceipt
              key={r.id}
              receipt={r}
              def={APPROVAL_DEFS.find(d => d.id === r.id)}
              onExpired={onReceiptExpired}
            />
          ))}
        </div>
      )}

      <div style={{ height:1, background:"rgba(255,255,255,0.04)", marginBottom:0 }} />
    </div>
  );
}

// ─── Run detail panel ─────────────────────────────────────────────────────────

function RunDetailPanel({ run, policyName, onClose }) {
  return (
    <div style={{ position:"fixed", top:52, right:0, bottom:0, width:300, background:"#0d1018", borderLeft:"1px solid #1e2330", display:"flex", flexDirection:"column", zIndex:50, boxShadow:"-12px 0 40px rgba(0,0,0,0.5)" }}>
      <div style={{ padding:"13px 16px", borderBottom:"1px solid #1e2330", display:"flex", alignItems:"center", justifyContent:"space-between" }}>
        <div>
          <div style={{ fontSize:10, color:"#334155", textTransform:"uppercase", letterSpacing:"0.08em", marginBottom:3 }}>Run detail</div>
          <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:12, color:"#64748b" }}>{policyName}</span>
        </div>
        <button onClick={onClose} style={{ background:"none", border:"none", color:"#334155", cursor:"pointer", fontSize:18, lineHeight:1 }}>×</button>
      </div>
      <div style={{ flex:1, overflow:"auto", padding:16, display:"flex", flexDirection:"column", gap:14 }}>
        <StatusBadge status={run.status} />
        {run.summary && <div style={{ fontSize:12, color:"#94a3b8", lineHeight:1.65, padding:"10px 12px", background:"#131720", borderRadius:6, borderLeft:"2px solid #1e2330" }}>{run.summary}</div>}
        <div style={{ display:"grid", gridTemplateColumns:"1fr 1fr", gap:7 }}>
          {[["Run ID",run.id.slice(-8)],["Started",fmtAbs(run.startedAt)],["Duration",fmtDur(run.duration)],["Tokens",fmtTok(run.tokenCost)],["Tool calls",String(run.toolCalls)]].map(([l,v])=>(
            <div key={l} style={{ background:"#131720", borderRadius:6, padding:"8px 10px" }}>
              <div style={{ fontSize:10, color:"#2d3748", textTransform:"uppercase", letterSpacing:"0.08em", marginBottom:3 }}>{l}</div>
              <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:12, color:"#64748b" }}>{v}</span>
            </div>
          ))}
        </div>
        <button style={{ padding:"8px 0", borderRadius:6, cursor:"pointer", background:"#131720", border:"1px solid #1e2330", color:"#475569", fontSize:12, fontFamily:"IBM Plex Sans,sans-serif", display:"flex", alignItems:"center", justifyContent:"center", gap:6 }}>
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z"/><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z"/></svg>
          View reasoning timeline
        </button>
      </div>
    </div>
  );
}

// ─── Policy row ───────────────────────────────────────────────────────────────

const PER_PAGE = 4;

function PolicyRow({ policy, runOverrides }) {
  const [histOpen, setHistOpen]    = useState(false);
  const [page, setPage]            = useState(0);
  const [selectedRun, setSelected] = useState(null);

  // Merge any live overrides into the latest run
  const override = runOverrides[policy.latestRun.id];
  const run = override ? { ...policy.latestRun, ...override } : policy.latestRun;

  const pages = Math.ceil(policy.history.length / PER_PAGE);
  const slice = policy.history.slice(page * PER_PAGE, page * PER_PAGE + PER_PAGE);
  const pick  = r => setSelected(prev => prev?.id === r.id ? null : r);

  return (
    <>
      <div onClick={()=>pick(run)} className="prow" style={{ display:"grid", gridTemplateColumns:"1fr 150px 96px 70px 58px 34px", alignItems:"center", padding:"10px 14px 10px 18px", borderBottom:"1px solid rgba(255,255,255,0.035)", cursor:"pointer", background:selectedRun?.id===run.id?"rgba(59,130,246,0.05)":"transparent", transition:"background 0.12s" }}>
        <div style={{ display:"flex", alignItems:"center", gap:8, minWidth:0 }}>
          <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="#2d3748" strokeWidth="2" style={{ flexShrink:0 }}>
            <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/>
          </svg>
          <span style={{ fontSize:13, fontWeight:500, color:"#c8d3e0", whiteSpace:"nowrap" }}>{policy.name}</span>
          <TriggerChip type={policy.triggerType} />
          {run.summary && <span style={{ fontSize:11, color:"#2d3748", overflow:"hidden", textOverflow:"ellipsis", whiteSpace:"nowrap" }}>{run.summary}</span>}
          {!run.summary && run.status==="running" && (
            <span style={{ fontSize:11, color:"#60a5fa", display:"flex", alignItems:"center", gap:4, flexShrink:0 }}>
              <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" style={{ animation:"gSpin 1s linear infinite" }}><path d="M21 12a9 9 0 1 1-9-9"/></svg>
              Executing…
            </span>
          )}
        </div>
        <StatusBadge status={run.status} />
        <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:11, color:"#2d3748" }}>{fmtRel(run.startedAt)}</span>
        <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:11, color:"#2d3748" }}>{fmtDur(run.duration)}</span>
        <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:11, color:"#2d3748" }}>{fmtTok(run.tokenCost)}</span>
        <button onClick={e=>{e.stopPropagation();setHistOpen(o=>!o);setPage(0);}} title="Show history" className="hbtn" style={{ background:histOpen?"rgba(59,130,246,0.12)":"transparent", border:histOpen?"1px solid rgba(59,130,246,0.3)":"1px solid transparent", borderRadius:5, color:histOpen?"#60a5fa":"#2d3748", cursor:"pointer", padding:"4px 5px", display:"flex", alignItems:"center", justifyContent:"center", transition:"all 0.15s" }}>
          <IconHistory />
        </button>
      </div>

      {histOpen && (
        <div style={{ background:"#090c12", borderBottom:"1px solid rgba(255,255,255,0.04)" }}>
          <div style={{ display:"grid", gridTemplateColumns:"150px 1fr 108px 70px 58px", padding:"6px 14px 6px 18px", borderBottom:"1px solid rgba(255,255,255,0.04)" }}>
            {["Status","Summary","When","Duration","Tokens"].map(h=>(
              <span key={h} style={{ fontSize:10, color:"#1e2a3a", textTransform:"uppercase", letterSpacing:"0.08em" }}>{h}</span>
            ))}
          </div>
          {slice.map(r=>(
            <div key={r.id} onClick={()=>pick(r)} className="hrow" style={{ display:"grid", gridTemplateColumns:"150px 1fr 108px 70px 58px", alignItems:"center", padding:"8px 14px 8px 18px", borderBottom:"1px solid rgba(255,255,255,0.025)", cursor:"pointer", background:selectedRun?.id===r.id?"rgba(59,130,246,0.07)":"transparent", transition:"background 0.1s" }}>
              <StatusBadge status={r.status} />
              <span style={{ fontSize:11, color:"#334155", overflow:"hidden", textOverflow:"ellipsis", whiteSpace:"nowrap", paddingRight:10 }}>{r.summary||"—"}</span>
              <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:11, color:"#2d3748" }}>{fmtAbs(r.startedAt)}</span>
              <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:11, color:"#2d3748" }}>{fmtDur(r.duration)}</span>
              <span style={{ fontFamily:"IBM Plex Mono,monospace", fontSize:11, color:"#2d3748" }}>{fmtTok(r.tokenCost)}</span>
            </div>
          ))}
          {pages > 1 && (
            <div style={{ display:"flex", alignItems:"center", justifyContent:"space-between", padding:"7px 18px", borderTop:"1px solid rgba(255,255,255,0.04)" }}>
              <span style={{ fontSize:11, color:"#1e2a3a", fontFamily:"IBM Plex Mono,monospace" }}>{page*PER_PAGE+1}–{Math.min(page*PER_PAGE+PER_PAGE,policy.history.length)} of {policy.history.length}</span>
              <div style={{ display:"flex", gap:4 }}>
                {[["← Prev",()=>setPage(p=>Math.max(0,p-1)),page===0],["Next →",()=>setPage(p=>Math.min(pages-1,p+1)),page===pages-1]].map(([lbl,fn,dis])=>(
                  <button key={lbl} onClick={fn} disabled={dis} style={{ padding:"3px 9px", borderRadius:4, fontSize:11, background:"transparent", border:"1px solid #1e2330", color:dis?"#1e2330":"#334155", cursor:dis?"default":"pointer", fontFamily:"IBM Plex Mono,monospace" }}>{lbl}</button>
                ))}
              </div>
            </div>
          )}
        </div>
      )}

      {selectedRun && <RunDetailPanel run={selectedRun} policyName={policy.name} onClose={()=>setSelected(null)} />}
    </>
  );
}

// ─── Folder row ───────────────────────────────────────────────────────────────
// Reads live run status from runOverrides before computing dot color.

function FolderRow({ folder, runOverrides }) {
  const [open, setOpen] = useState(false);

  // Derive each policy's effective status — override wins if present
  const effectiveStatuses = folder.policies.map(p => {
    const override = runOverrides[p.latestRun.id];
    return override ? override.status : p.latestRun.status;
  });

  const hasApproval = effectiveStatuses.includes("waiting_for_approval");
  const hasRunning  = effectiveStatuses.includes("running");
  const hasFailed   = effectiveStatuses.includes("failed");
  const dotColor    = hasApproval ? "#f59e0b" : hasFailed ? "#f87171" : hasRunning ? "#60a5fa" : "#4ade80";
  const totalTok    = folder.policies.reduce((s,p) => s + p.latestRun.tokenCost, 0);

  return (
    <div style={{ borderBottom:"1px solid rgba(255,255,255,0.05)" }}>
      <div onClick={()=>setOpen(o=>!o)} className="frow" style={{ display:"flex", alignItems:"center", gap:9, padding:"11px 16px", cursor:"pointer", background:open?"rgba(255,255,255,0.018)":"transparent", transition:"background 0.12s", userSelect:"none" }}>
        <span style={{ color:"#2d3748" }}><IconChevron open={open} /></span>
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke={open?"#60a5fa":"#334155"} strokeWidth="1.8" style={{ flexShrink:0 }}>
          <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/>
        </svg>
        <span style={{ fontSize:13, fontWeight:600, color:open?"#e2e8f0":"#64748b" }}>{folder.name}</span>

        {/* Live status dot — derived from polled overrides */}
        <span style={{
          width:7, height:7, borderRadius:"50%", background:dotColor,
          animation: (hasApproval||hasRunning) ? "gPulse 1.6s ease-in-out infinite" : "none",
          transition: "background 0.4s ease",   // smooth color change as status settles
        }} />

        <span style={{ fontSize:10, fontFamily:"IBM Plex Mono,monospace", color:"#2d3748", background:"rgba(255,255,255,0.03)", border:"1px solid rgba(255,255,255,0.06)", padding:"1px 7px", borderRadius:10 }}>
          {folder.policies.length} {folder.policies.length===1?"policy":"policies"}
        </span>

        {/* "approval pending" chip — only shown when real status says so */}
        {hasApproval && (
          <span style={{ fontSize:10, fontFamily:"IBM Plex Mono,monospace", color:"#f59e0b", background:"rgba(245,158,11,0.08)", border:"1px solid rgba(245,158,11,0.22)", padding:"1px 7px", borderRadius:10 }}>
            approval pending
          </span>
        )}

        <span style={{ marginLeft:"auto", fontFamily:"IBM Plex Mono,monospace", fontSize:11, color:"#1e2a3a" }}>{fmtTok(totalTok)} tok</span>
      </div>

      {open && (
        <div style={{ display:"grid", gridTemplateColumns:"1fr 150px 96px 70px 58px 34px", padding:"5px 14px 5px 18px", background:"rgba(0,0,0,0.18)", borderTop:"1px solid rgba(255,255,255,0.04)", borderBottom:"1px solid rgba(255,255,255,0.04)" }}>
          {["Policy / Latest run","Status","When","Duration","Tokens",""].map((h,i)=>(
            <span key={i} style={{ fontSize:10, color:"#1e2a3a", textTransform:"uppercase", letterSpacing:"0.08em" }}>{h}</span>
          ))}
        </div>
      )}
      {open && folder.policies.map(p => (
        <PolicyRow key={p.id} policy={p} runOverrides={runOverrides} />
      ))}
    </div>
  );
}

// ─── App ──────────────────────────────────────────────────────────────────────

export default function App() {
  const [pendingIds, setPendingIds] = useState(() => APPROVAL_DEFS.map(d => d.id));
  const [receipts,   setReceipts]   = useState([]);

  // runOverrides: { [runId]: { status, summary } }
  // Only updated after the simulated poll confirms (3s after decision).
  // Folder dots read from here, so they stay accurate until backend confirms.
  const [runOverrides, setRunOverrides] = useState({});

  const onDecide = useCallback((approvalId, decision, note) => {
    const def = APPROVAL_DEFS.find(d => d.id === approvalId);
    if (!def) return;

    // Remove from pending immediately
    setPendingIds(prev => prev.filter(id => id !== approvalId));

    // Show receipt (not yet settled)
    setReceipts(prev => [...prev, { id: approvalId, decision, note, settled: false }]);

    // NOTE: We do NOT update runOverrides here.
    // The folder dot and policy row status stay at waiting_for_approval
    // until the simulated poll fires below — matching "wait for real status" spec.

    // Simulate poll confirming ~3s later
    setTimeout(() => {
      const settled = decision === "approve" ? def.approvedSettled  : def.rejectedSettled;
      const summary = decision === "approve" ? def.approvedSummary  : def.rejectedSummary;
      setRunOverrides(prev => ({ ...prev, [def.runId]: { status: settled, summary } }));
      setReceipts(prev => prev.map(r => r.id === approvalId ? { ...r, settled: true } : r));
    }, 3000);
  }, []);

  const onReceiptExpired = useCallback(id => {
    setReceipts(prev => prev.filter(r => r.id !== id));
  }, []);

  const approvalCount = pendingIds.length;

  // Derive stats from live state
  const allPolicies   = INITIAL_FOLDERS.flatMap(f => f.policies);
  const activeRunning = allPolicies.filter(p => {
    const ov = runOverrides[p.latestRun.id];
    return (ov ? ov.status : p.latestRun.status) === "running";
  }).length;

  return (
    <div style={{ fontFamily:"IBM Plex Sans,system-ui,sans-serif", background:"#0f1117", minHeight:"100vh", color:"#e2e8f0", display:"flex", flexDirection:"column" }}>
      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=IBM+Plex+Sans:wght@400;500;600;700&display=swap');
        *,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
        ::-webkit-scrollbar{width:4px}::-webkit-scrollbar-thumb{background:#1e2330;border-radius:2px}
        @keyframes gPulse{0%,100%{opacity:1}50%{opacity:.2}}
        @keyframes gSpin{from{transform:rotate(0deg)}to{transform:rotate(360deg)}}
        .frow:hover{background:rgba(255,255,255,0.025)!important}
        .prow:hover{background:rgba(255,255,255,0.025)!important}
        .hrow:hover{background:rgba(59,130,246,0.07)!important}
        .hbtn:hover{border-color:rgba(59,130,246,0.35)!important;color:#60a5fa!important}
        .btn-approve:hover{background:rgba(74,222,128,0.2)!important}
        .btn-reject:hover{background:rgba(248,113,113,0.15)!important}
        textarea:focus{outline:none;border-color:rgba(59,130,246,0.4)!important}
        button{font-family:IBM Plex Sans,sans-serif}
      `}</style>

      {/* Top bar */}
      <div style={{ display:"flex", alignItems:"center", justifyContent:"space-between", padding:"0 20px", height:52, borderBottom:"1px solid #1e2330", background:"#0d1018", flexShrink:0 }}>
        <div style={{ display:"flex", alignItems:"center", gap:24 }}>
          <div style={{ display:"flex", alignItems:"center", gap:8 }}>
            <svg width="17" height="17" viewBox="0 0 24 24" fill="none">
              <circle cx="12" cy="12" r="10" fill="#3b82f6" opacity="0.15"/>
              <path d="M12 6v6l4 2" stroke="#3b82f6" strokeWidth="2" strokeLinecap="round"/>
              <path d="M2 12h3M19 12h3M12 2v3M12 19v3" stroke="#3b82f6" strokeWidth="1.5" strokeLinecap="round" opacity="0.4"/>
            </svg>
            <span style={{ fontSize:14, fontWeight:700, letterSpacing:"0.06em", color:"#f1f5f9" }}>GLEIPNIR</span>
          </div>
          {["Runs","Policies","Servers"].map(t=>(
            <span key={t} style={{ fontSize:13, color:t==="Runs"?"#e2e8f0":"#2d3748", borderBottom:t==="Runs"?"2px solid #3b82f6":"2px solid transparent", paddingBottom:2, cursor:"pointer" }}>{t}</span>
          ))}
        </div>
        {approvalCount > 0 && (
          <div style={{ display:"flex", alignItems:"center", gap:6, padding:"4px 10px", background:"rgba(245,158,11,0.08)", border:"1px solid rgba(245,158,11,0.25)", borderRadius:6, fontSize:11, color:"#f59e0b", fontFamily:"IBM Plex Mono,monospace" }}>
            <span style={{ width:6, height:6, borderRadius:"50%", background:"#f59e0b", animation:"gPulse 1.6s ease-in-out infinite" }}/>
            {approvalCount} pending
          </div>
        )}
      </div>

      <div style={{ flex:1, overflow:"auto", paddingBottom:48 }}>

        {/* Stats bar */}
        <div style={{ display:"grid", gridTemplateColumns:"repeat(4,1fr)", gap:10, padding:"18px 20px 0" }}>
          {[
            { label:"Active runs",       val:activeRunning,   color:"#60a5fa", sub:"right now",            pulse:activeRunning>0 },
            { label:"Pending approvals", val:approvalCount,   color:"#f59e0b", sub:"agents waiting on you", pulse:approvalCount>0 },
            { label:"Folders",           val:INITIAL_FOLDERS.length, color:"#94a3b8", sub:"configured" },
            { label:"Tokens today",      val:fmtTok(allPolicies.reduce((s,p)=>s+p.latestRun.tokenCost,0)), color:"#94a3b8", sub:"latest run per policy" },
          ].map(s=>(
            <div key={s.label} style={{ background:"#131720", border:"1px solid #1e2330", borderRadius:8, padding:"13px 16px" }}>
              <div style={{ fontSize:10, color:"#2d3748", textTransform:"uppercase", letterSpacing:"0.08em", marginBottom:7 }}>{s.label}</div>
              <div style={{ fontSize:25, fontFamily:"IBM Plex Mono,monospace", fontWeight:500, color:s.color, lineHeight:1, animation:s.pulse?"gPulse 1.6s ease-in-out infinite":"none" }}>{s.val}</div>
              <div style={{ fontSize:10, color:"#1e2a3a", marginTop:5 }}>{s.sub}</div>
            </div>
          ))}
        </div>

        <ApprovalsSection
          pendingIds={pendingIds}
          receipts={receipts}
          onDecide={onDecide}
          onReceiptExpired={onReceiptExpired}
        />

        <div style={{ padding:"18px 20px 10px" }}>
          <span style={{ fontSize:11, color:"#2d3748", textTransform:"uppercase", letterSpacing:"0.08em" }}>Folders</span>
        </div>
        <div style={{ margin:"0 20px", border:"1px solid #1e2330", borderRadius:8, overflow:"hidden" }}>
          {INITIAL_FOLDERS.map(f => (
            <FolderRow key={f.id} folder={f} runOverrides={runOverrides} />
          ))}
        </div>

      </div>
    </div>
  );
}
