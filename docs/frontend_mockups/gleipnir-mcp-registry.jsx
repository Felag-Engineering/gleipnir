import { useState, useEffect, useRef } from "react";

// ─── Design tokens (consistent with dashboard + timeline) ──────────────────────

const T = {
  bgCanvas:    "#0F1117",
  bgSurface:   "#131720",
  bgElevated:  "#1E2330",
  bgTopbar:    "#0D1018",
  bgCode:      "#090C12",
  borderSubtle:"#1E2330",
  borderMid:   "#253044",
  textPrimary: "#E2E8F0",
  textSecond:  "#94A3B8",
  textMuted:   "#475569",
  textFaint:   "#334155",
  blueLight:   "#60A5FA",
  green:       "#4ADE80",
  amber:       "#F59E0B",
  red:         "#F87171",
  purple:      "#A78BFA",
  teal:        "#34D399",
  sensor:      "#60A5FA",
  actuator:    "#FB923C",
  feedback:    "#A78BFA",
};

// ─── Mock data ─────────────────────────────────────────────────────────────────

const MOCK_SERVERS = [
  {
    id: "srv-01", name: "kubectl-mcp", url: "http://kubectl-mcp:8080",
    status: "reachable", lastDiscoveredAt: new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString(),
    tools: [
      { id:"t1", name:"kubectl.get_pods",    description:"List pods across namespaces with status and restart counts.", role:"sensor",
        inputSchema:{ namespace:{type:"string",required:true,scopeable:true}, label_selector:{type:"string",required:false,scopeable:false} },
        usedInPolicies:[{name:"worker-pod-watcher",scoped:true},{name:"daily-cluster-digest",scoped:false}] },
      { id:"t2", name:"kubectl.get_events",  description:"Fetch recent events for a pod or namespace.", role:"sensor",
        inputSchema:{ namespace:{type:"string",required:true,scopeable:true}, pod:{type:"string",required:false,scopeable:false} },
        usedInPolicies:[{name:"worker-pod-watcher",scoped:true}] },
      { id:"t3", name:"kubectl.get_logs",    description:"Retrieve container logs for a given pod.", role:"sensor",
        inputSchema:{ namespace:{type:"string",required:true,scopeable:true}, pod:{type:"string",required:true,scopeable:false}, lines:{type:"integer",required:false,scopeable:false} },
        usedInPolicies:[] },
      { id:"t4", name:"kubectl.delete_pod",  description:"Delete a pod, triggering a restart via its controller.", role:"actuator",
        inputSchema:{ namespace:{type:"string",required:true,scopeable:true}, pod:{type:"string",required:true,scopeable:false} },
        usedInPolicies:[{name:"worker-pod-watcher",scoped:true}] },
      { id:"t5", name:"kubectl.scale",       description:"Scale a deployment to the specified replica count.", role:"actuator",
        inputSchema:{ namespace:{type:"string",required:true,scopeable:true}, deployment:{type:"string",required:true,scopeable:false}, replicas:{type:"integer",required:true,scopeable:false} },
        usedInPolicies:[] },
    ],
    affectedPolicies: ["worker-pod-watcher", "daily-cluster-digest"],
  },
  {
    id: "srv-02", name: "vikunja-mcp", url: "http://vikunja-mcp:8080",
    status: "reachable", lastDiscoveredAt: new Date(Date.now() - 1 * 60 * 60 * 1000).toISOString(),
    tools: [
      { id:"t6",  name:"vikunja.task_get",     description:"Retrieve a single task by ID.", role:"sensor",
        inputSchema:{ task_id:{type:"integer",required:true,scopeable:false} },
        usedInPolicies:[{name:"vikunja-triage",scoped:false}] },
      { id:"t7",  name:"vikunja.task_list",    description:"List tasks in a project with optional filters.", role:"sensor",
        inputSchema:{ project_id:{type:"integer",required:true,scopeable:true}, filter:{type:"string",required:false,scopeable:false} },
        usedInPolicies:[{name:"vikunja-triage",scoped:false},{name:"daily-task-digest",scoped:false}] },
      { id:"t8",  name:"vikunja.project_list", description:"List all accessible Vikunja projects.", role:"sensor",
        inputSchema:{},
        usedInPolicies:[{name:"daily-task-digest",scoped:false}] },
      { id:"t9",  name:"vikunja.task_create",  description:"Create a new task in the specified project.", role:"actuator",
        inputSchema:{ title:{type:"string",required:true,scopeable:false}, project_id:{type:"integer",required:true,scopeable:true}, priority:{type:"integer",required:false,scopeable:false} },
        usedInPolicies:[{name:"worker-pod-watcher",scoped:false}] },
      { id:"t10", name:"vikunja.task_comment", description:"Post a comment on an existing task.", role:"actuator",
        inputSchema:{ task_id:{type:"integer",required:true,scopeable:false}, comment:{type:"string",required:true,scopeable:false} },
        usedInPolicies:[{name:"vikunja-triage",scoped:false}] },
      { id:"t11", name:"vikunja.task_close",   description:"Mark a task as complete.", role:"actuator",
        inputSchema:{ task_id:{type:"integer",required:true,scopeable:false} },
        usedInPolicies:[{name:"vikunja-triage",scoped:false}] },
    ],
    affectedPolicies: ["worker-pod-watcher", "vikunja-triage", "daily-task-digest"],
  },
  {
    id: "srv-03", name: "grafana-mcp", url: "http://grafana-mcp:8080",
    status: "unreachable", lastDiscoveredAt: new Date(Date.now() - 26 * 60 * 60 * 1000).toISOString(),
    tools: [
      { id:"t12", name:"grafana.get_alerts",    description:"Fetch currently firing alerts from Grafana.", role:"sensor",
        inputSchema:{ severity:{type:"string",required:false,scopeable:true} },
        usedInPolicies:[{name:"grafana-alert-responder",scoped:false}] },
      { id:"t13", name:"grafana.get_dashboard", description:"Retrieve dashboard panels and current metric values.", role:"sensor",
        inputSchema:{ uid:{type:"string",required:true,scopeable:false} },
        usedInPolicies:[{name:"grafana-alert-responder",scoped:false}] },
      { id:"t14", name:"grafana.silence_alert", description:"Silence a firing alert for a given duration.", role:"actuator",
        inputSchema:{ alert_id:{type:"string",required:true,scopeable:false}, duration:{type:"string",required:true,scopeable:false} },
        usedInPolicies:[] },
      { id:"t15", name:"grafana.annotate",      description:"Post an annotation to a dashboard at the current time.", role:null,
        inputSchema:{ dashboard_uid:{type:"string",required:true,scopeable:false}, text:{type:"string",required:true,scopeable:false} },
        usedInPolicies:[] },
    ],
    affectedPolicies: ["grafana-alert-responder"],
  },
];

// Simulated re-discovery diff for kubectl-mcp
const MOCK_DIFF = {
  serverId: "srv-01",
  added:   [{ name:"kubectl.rollout_restart", description:"Trigger a rollout restart for a deployment.", suggestedRole:"actuator" }],
  removed: [{ name:"kubectl.get_logs",        description:"Retrieve container logs for a given pod." }],
  modified:[{ name:"kubectl.scale",           description:"Scale a deployment or statefulset to the specified replica count.", prev:"Scale a deployment to the specified replica count." }],
};

// ─── Helpers ───────────────────────────────────────────────────────────────────

const fmtAgo = iso => {
  const s = Math.floor((Date.now() - new Date(iso)) / 1000);
  if (s < 60)   return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s/60)}m ago`;
  if (s < 86400) return `${Math.floor(s/3600)}h ago`;
  return `${Math.floor(s/86400)}d ago`;
};

const ROLES = ["sensor","actuator","feedback"];
const ROLE_COLOR = { sensor:T.sensor, actuator:T.actuator, feedback:T.feedback, null:T.textMuted };

// ─── Global styles ─────────────────────────────────────────────────────────────

const STYLES = `
  @import url('https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=IBM+Plex+Sans:wght@300;400;500;600&display=swap');
  *{box-sizing:border-box;margin:0;padding:0}
  body{background:${T.bgCanvas};color:${T.textPrimary};font-family:'IBM Plex Sans',sans-serif}
  @keyframes gPulse{0%,100%{opacity:1}50%{opacity:.35}}
  @keyframes gSpin{from{transform:rotate(0deg)}to{transform:rotate(360deg)}}
  @keyframes fadeIn{from{opacity:0}to{opacity:1}}
  @keyframes slideUp{from{opacity:0;transform:translateY(16px)}to{opacity:1;transform:translateY(0)}}
  @keyframes shimmer{from{background-position:-200% 0}to{background-position:200% 0}}
  ::-webkit-scrollbar{width:6px}::-webkit-scrollbar-track{background:transparent}
  ::-webkit-scrollbar-thumb{background:#253044;border-radius:3px}

  .nav-tab{background:none;border:none;cursor:pointer;padding:0 2px 12px;font-family:'IBM Plex Sans',sans-serif;font-size:13px;font-weight:500;color:${T.textMuted};border-bottom:2px solid transparent;transition:all .15s;white-space:nowrap}
  .nav-tab:hover{color:${T.textSecond}}
  .nav-tab.active{color:${T.textPrimary};border-bottom-color:${T.blueLight}}

  .server-card{background:${T.bgSurface};border-radius:8px;overflow:hidden;transition:border-color .15s}

  .tool-row{display:grid;grid-template-columns:1fr 80px 28px;gap:8px;align-items:center;padding:7px 0;border-bottom:1px solid ${T.borderSubtle};transition:background .1s}
  .tool-row:last-child{border-bottom:none}
  .tool-row:hover{background:rgba(255,255,255,.02)}

  .role-select{background:${T.bgElevated};border:1px solid ${T.borderMid};border-radius:4px;padding:3px 6px;font-family:'IBM Plex Mono',monospace;font-size:10px;color:${T.textSecond};cursor:pointer;outline:none;appearance:none;-webkit-appearance:none;width:100%;transition:border-color .15s}
  .role-select:focus{border-color:#475569}
  .role-select.unassigned{color:${T.amber};border-color:rgba(245,158,11,.3);background:rgba(245,158,11,.06)}
  .role-select.sensor{color:${T.sensor}}
  .role-select.actuator{color:${T.actuator}}
  .role-select.feedback{color:${T.feedback}}

  .icon-btn{background:none;border:none;cursor:pointer;display:flex;align-items:center;justify-content:center;width:28px;height:28px;border-radius:4px;color:${T.textMuted};transition:all .15s;flex-shrink:0}
  .icon-btn:hover{background:${T.bgElevated};color:${T.textSecond}}
  .icon-btn.danger:hover{background:rgba(248,113,113,.1);color:${T.red}}

  .pill-btn{display:inline-flex;align-items:center;gap:5px;padding:5px 12px;border-radius:5px;border:none;cursor:pointer;font-family:'IBM Plex Sans',sans-serif;font-size:12px;font-weight:500;transition:all .15s}
  .pill-btn.primary{background:rgba(59,130,246,.15);border:1px solid rgba(59,130,246,.3);color:${T.blueLight}}
  .pill-btn.primary:hover{background:rgba(59,130,246,.25)}
  .pill-btn.ghost{background:transparent;border:1px solid ${T.borderMid};color:${T.textMuted}}
  .pill-btn.ghost:hover{color:${T.textSecond};border-color:#334155}
  .pill-btn.danger{background:rgba(248,113,113,.08);border:1px solid rgba(248,113,113,.2);color:${T.red}}
  .pill-btn.danger:hover{background:rgba(248,113,113,.16)}
  .pill-btn.confirm{background:rgba(248,113,113,.18);border:1px solid rgba(248,113,113,.4);color:${T.red}}
  .pill-btn:disabled{opacity:.4;cursor:not-allowed}

  .modal-overlay{position:fixed;inset:0;background:rgba(0,0,0,.7);display:flex;align-items:center;justify-content:center;z-index:200;animation:fadeIn .15s ease;padding:24px}
  .modal-box{background:${T.bgSurface};border:1px solid ${T.borderMid};border-radius:10px;width:100%;max-width:520px;animation:slideUp .2s ease;overflow:hidden}
  .modal-box.wide{max-width:680px}

  .modal-header{display:flex;align-items:center;justify-content:space-between;padding:18px 20px 16px;border-bottom:1px solid ${T.borderSubtle}}
  .modal-body{padding:20px}
  .modal-footer{display:flex;justify-content:flex-end;gap:8px;padding:14px 20px;border-top:1px solid ${T.borderSubtle}}

  .field-label{font-family:'IBM Plex Mono',monospace;font-size:10px;color:${T.textMuted};text-transform:uppercase;letter-spacing:.06em;margin-bottom:6px}
  .field-input{width:100%;padding:8px 12px;background:${T.bgCanvas};border:1px solid ${T.borderMid};border-radius:5px;color:${T.textPrimary};font-family:'IBM Plex Mono',monospace;font-size:12px;outline:none;transition:border-color .15s}
  .field-input:focus{border-color:#475569}
  .field-input::placeholder{color:${T.textFaint}}

  .diff-added{background:rgba(74,222,128,.06);border-left:2px solid ${T.green};padding:8px 12px;border-radius:0 4px 4px 0;margin-bottom:4px}
  .diff-removed{background:rgba(248,113,113,.06);border-left:2px solid ${T.red};padding:8px 12px;border-radius:0 4px 4px 0;margin-bottom:4px}
  .diff-modified{background:rgba(245,158,11,.06);border-left:2px solid ${T.amber};padding:8px 12px;border-radius:0 4px 4px 0;margin-bottom:4px}

  .ping-dot{width:7px;height:7px;border-radius:50%;flex-shrink:0}
  .ping-dot.reachable{background:${T.green};animation:gPulse 2.4s ease-in-out infinite}
  .ping-dot.unreachable{background:${T.red}}
  .ping-dot.checking{background:${T.amber};animation:gPulse 1s ease-in-out infinite}

  .unassigned-banner{display:flex;align-items:center;gap:8px;padding:7px 12px;background:rgba(245,158,11,.07);border:1px solid rgba(245,158,11,.2);border-radius:5px;margin-bottom:12px;font-size:11px;color:${T.amber};font-family:'IBM Plex Mono',monospace}

  .discover-btn{display:inline-flex;align-items:center;gap:5px;padding:4px 10px;border-radius:4px;border:1px solid ${T.borderMid};background:transparent;cursor:pointer;font-family:'IBM Plex Mono',monospace;font-size:10px;color:${T.textMuted};transition:all .15s}
  .discover-btn:hover{color:${T.textSecond};border-color:#334155}
  .discover-btn.spinning svg{animation:gSpin .9s linear infinite}
`;

// ─── Atoms ─────────────────────────────────────────────────────────────────────

const Spinner = ({size=13,color=T.blueLight}) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" style={{animation:"gSpin .9s linear infinite",flexShrink:0}}>
    <circle cx="12" cy="12" r="10" stroke={color} strokeWidth="2.5" strokeOpacity=".2"/>
    <path d="M12 2a10 10 0 0 1 10 10" stroke={color} strokeWidth="2.5" strokeLinecap="round"/>
  </svg>
);

const RoleChip = ({role}) => {
  if (!role) return <span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.amber,background:"rgba(245,158,11,.1)",border:"1px solid rgba(245,158,11,.25)",padding:"1px 5px",borderRadius:3}}>unassigned</span>;
  const col = ROLE_COLOR[role]||T.textMuted;
  return <span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:col,background:`${col}14`,border:`1px solid ${col}30`,padding:"1px 5px",borderRadius:3}}>{role}</span>;
};

const StatusDot = ({status}) => <span className={`ping-dot ${status}`}/>;

// ─── Top bar ───────────────────────────────────────────────────────────────────

const TopBar = ({activeTab, setActiveTab}) => {
  const tabs = ["Runs","Policies","Servers"];
  return (
    <div style={{height:48,background:T.bgTopbar,borderBottom:`1px solid ${T.borderSubtle}`,display:"flex",alignItems:"center",padding:"0 24px",gap:0,flexShrink:0,position:"sticky",top:0,zIndex:100}}>
      <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:13,fontWeight:500,color:T.textPrimary,letterSpacing:".04em",marginRight:32}}>GLEIPNIR</span>
      <div style={{display:"flex",gap:24,alignSelf:"stretch",alignItems:"flex-end"}}>
        {tabs.map(t=>(
          <button key={t} className={`nav-tab${activeTab===t?" active":""}`} onClick={()=>setActiveTab(t)}>{t}</button>
        ))}
      </div>
    </div>
  );
};

// ─── Register / Edit modal ─────────────────────────────────────────────────────

const ServerModal = ({initial, onClose, onSave}) => {
  const [name, setName] = useState(initial?.name??"");
  const [url,  setUrl]  = useState(initial?.url??"");
  const isEdit = !!initial;
  const valid  = name.trim().length>0 && url.trim().length>0;

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal-box" onClick={e=>e.stopPropagation()}>
        <div className="modal-header">
          <span style={{fontFamily:"'IBM Plex Sans',sans-serif",fontSize:14,fontWeight:600,color:T.textPrimary}}>
            {isEdit?"Edit MCP server":"Register MCP server"}
          </span>
          <button className="icon-btn" onClick={onClose}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
          </button>
        </div>
        <div className="modal-body" style={{display:"flex",flexDirection:"column",gap:16}}>
          <div>
            <div className="field-label">Server name</div>
            <input className="field-input" placeholder="e.g. kubectl-mcp" value={name} onChange={e=>setName(e.target.value)}/>
            <div style={{marginTop:5,fontSize:11,color:T.textMuted}}>Used in policy tool references: <span style={{fontFamily:"'IBM Plex Mono',monospace",color:T.textSecond}}>{name||"server-name"}.tool_name</span></div>
          </div>
          <div>
            <div className="field-label">HTTP URL</div>
            <input className="field-input" placeholder="http://mcp-server:8080" value={url} onChange={e=>setUrl(e.target.value)}/>
            <div style={{marginTop:5,fontSize:11,color:T.textMuted}}>Gleipnir will call <span style={{fontFamily:"'IBM Plex Mono',monospace",color:T.textSecond}}>{url||"http://…"}/tools/list</span> and <span style={{fontFamily:"'IBM Plex Mono',monospace",color:T.textSecond}}>/tools/call</span> on this server.</div>
          </div>
          {!isEdit && (
            <div style={{padding:"10px 12px",background:T.bgElevated,border:`1px solid ${T.borderSubtle}`,borderRadius:5,fontSize:11,color:T.textMuted,lineHeight:1.6}}>
              After registering, Gleipnir will run an initial tool discovery. You'll assign capability roles (sensor / actuator / feedback) to each discovered tool before they can be used in policies.
            </div>
          )}
        </div>
        <div className="modal-footer">
          <button className="pill-btn ghost" onClick={onClose}>Cancel</button>
          <button className="pill-btn primary" disabled={!valid} onClick={()=>onSave({name:name.trim(),url:url.trim()})}>
            {isEdit?"Save changes":"Register & discover"}
          </button>
        </div>
      </div>
    </div>
  );
};

// ─── Delete confirmation modal ─────────────────────────────────────────────────

const DeleteModal = ({server, onClose, onConfirm}) => (
  <div className="modal-overlay" onClick={onClose}>
    <div className="modal-box" onClick={e=>e.stopPropagation()}>
      <div className="modal-header">
        <span style={{fontFamily:"'IBM Plex Sans',sans-serif",fontSize:14,fontWeight:600,color:T.red}}>Delete server</span>
        <button className="icon-btn" onClick={onClose}>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>
      </div>
      <div className="modal-body" style={{display:"flex",flexDirection:"column",gap:14}}>
        <p style={{fontSize:13,color:T.textSecond,lineHeight:1.6}}>
          Are you sure you want to delete <span style={{fontFamily:"'IBM Plex Mono',monospace",color:T.textPrimary}}>{server.name}</span>? All {server.tools.length} registered tools will be removed from the registry.
        </p>
        {server.affectedPolicies.length>0 && (
          <div style={{background:"rgba(248,113,113,.06)",border:"1px solid rgba(248,113,113,.2)",borderRadius:6,padding:"10px 14px"}}>
            <div style={{fontSize:11,fontFamily:"'IBM Plex Mono',monospace",color:T.red,marginBottom:8,textTransform:"uppercase",letterSpacing:".06em"}}>
              {server.affectedPolicies.length} {server.affectedPolicies.length===1?"policy":"policies"} affected
            </div>
            {server.affectedPolicies.map(p=>(
              <div key={p} style={{display:"flex",alignItems:"center",gap:6,padding:"4px 0",borderBottom:`1px solid rgba(248,113,113,.1)`}}>
                <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke={T.red} strokeWidth="2.5" style={{flexShrink:0,opacity:.7}}><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
                <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:11,color:T.textSecond}}>{p}</span>
                <span style={{fontSize:10,color:T.textMuted,marginLeft:"auto"}}>will fail at run start</span>
              </div>
            ))}
          </div>
        )}
      </div>
      <div className="modal-footer">
        <button className="pill-btn ghost" onClick={onClose}>Cancel</button>
        <button className="pill-btn confirm" onClick={onConfirm}>Delete server</button>
      </div>
    </div>
  </div>
);

// ─── Diff modal ────────────────────────────────────────────────────────────────

const DiffModal = ({diff, serverName, onClose, onApply}) => {
  const total = diff.added.length+diff.removed.length+diff.modified.length;
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal-box wide" onClick={e=>e.stopPropagation()}>
        <div className="modal-header">
          <div>
            <span style={{fontFamily:"'IBM Plex Sans',sans-serif",fontSize:14,fontWeight:600,color:T.textPrimary}}>Re-discovery results</span>
            <span style={{marginLeft:8,fontSize:11,fontFamily:"'IBM Plex Mono',monospace",color:T.textMuted}}>{serverName}</span>
          </div>
          <button className="icon-btn" onClick={onClose}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
          </button>
        </div>
        <div className="modal-body" style={{display:"flex",flexDirection:"column",gap:16,maxHeight:"60vh",overflowY:"auto"}}>
          {/* Summary */}
          <div style={{display:"flex",gap:12}}>
            {diff.added.length>0&&<span style={{fontSize:11,fontFamily:"'IBM Plex Mono',monospace",color:T.green,background:"rgba(74,222,128,.08)",border:"1px solid rgba(74,222,128,.2)",padding:"3px 8px",borderRadius:4}}>+{diff.added.length} added</span>}
            {diff.removed.length>0&&<span style={{fontSize:11,fontFamily:"'IBM Plex Mono',monospace",color:T.red,background:"rgba(248,113,113,.08)",border:"1px solid rgba(248,113,113,.2)",padding:"3px 8px",borderRadius:4}}>−{diff.removed.length} removed</span>}
            {diff.modified.length>0&&<span style={{fontSize:11,fontFamily:"'IBM Plex Mono',monospace",color:T.amber,background:"rgba(245,158,11,.08)",border:"1px solid rgba(245,158,11,.2)",padding:"3px 8px",borderRadius:4}}>~{diff.modified.length} modified</span>}
          </div>

          {/* Added */}
          {diff.added.length>0&&(
            <div>
              <div style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.green,textTransform:"uppercase",letterSpacing:".06em",marginBottom:6}}>Added</div>
              {diff.added.map(t=>(
                <div key={t.name} className="diff-added">
                  <div style={{display:"flex",alignItems:"center",gap:8,marginBottom:3}}>
                    <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:12,fontWeight:500,color:T.green}}>{t.name}</span>
                    <span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.amber,background:"rgba(245,158,11,.1)",border:"1px solid rgba(245,158,11,.25)",padding:"1px 5px",borderRadius:3}}>unassigned</span>
                    <span style={{fontSize:10,color:T.textMuted,marginLeft:"auto"}}>suggested: {t.suggestedRole}</span>
                  </div>
                  <p style={{fontSize:11,color:T.textMuted}}>{t.description}</p>
                </div>
              ))}
            </div>
          )}

          {/* Removed */}
          {diff.removed.length>0&&(
            <div>
              <div style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.red,textTransform:"uppercase",letterSpacing:".06em",marginBottom:6}}>Removed</div>
              {diff.removed.map(t=>(
                <div key={t.name} className="diff-removed">
                  <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:12,color:T.red}}>{t.name}</span>
                  <p style={{fontSize:11,color:T.textMuted,marginTop:3}}>{t.description}</p>
                </div>
              ))}
            </div>
          )}

          {/* Modified */}
          {diff.modified.length>0&&(
            <div>
              <div style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.amber,textTransform:"uppercase",letterSpacing:".06em",marginBottom:6}}>Modified</div>
              {diff.modified.map(t=>(
                <div key={t.name} className="diff-modified">
                  <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:12,fontWeight:500,color:T.amber}}>{t.name}</span>
                  <div style={{marginTop:6,display:"flex",flexDirection:"column",gap:3}}>
                    <div style={{fontSize:11,color:T.textMuted}}><span style={{color:T.red,opacity:.7}}>−</span> {t.prev}</div>
                    <div style={{fontSize:11,color:T.textSecond}}><span style={{color:T.green,opacity:.7}}>+</span> {t.description}</div>
                  </div>
                </div>
              ))}
            </div>
          )}

          {diff.added.length>0&&(
            <div style={{padding:"9px 12px",background:"rgba(245,158,11,.06)",border:"1px solid rgba(245,158,11,.15)",borderRadius:5,fontSize:11,color:T.amber,lineHeight:1.6}}>
              {diff.added.length} new {diff.added.length===1?"tool":"tools"} will be added as <strong>unassigned</strong>. Assign a capability role before using in policies.
            </div>
          )}
        </div>
        <div className="modal-footer">
          <button className="pill-btn ghost" onClick={onClose}>Discard</button>
          <button className="pill-btn primary" onClick={onApply}>Apply {total} {total===1?"change":"changes"}</button>
        </div>
      </div>
    </div>
  );
};

// ─── Tool row with inline role selector ────────────────────────────────────────

const TYPE_COLOR = { string: T.teal, integer: T.purple, boolean: T.amber };

const SchemaFieldPill = ({name, def}) => (
  <div style={{display:"flex",alignItems:"center",gap:6,padding:"3px 0"}}>
    <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:10,color:T.textSecond,minWidth:110,flexShrink:0}}>{name}</span>
    <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:10,color:TYPE_COLOR[def.type]??T.textMuted,background:`${TYPE_COLOR[def.type]??T.textMuted}12`,border:`1px solid ${TYPE_COLOR[def.type]??T.textMuted}25`,padding:"1px 6px",borderRadius:3,flexShrink:0}}>{def.type}</span>
    {!def.required && <span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint}}>optional</span>}
    {def.scopeable && (
      <span title="This field can be restricted via params in a policy" style={{display:"inline-flex",alignItems:"center",gap:3,fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.blueLight,background:"rgba(96,165,250,.08)",border:"1px solid rgba(96,165,250,.18)",padding:"1px 6px",borderRadius:3,cursor:"default"}}>
        <svg width="8" height="8" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
        scopeable
      </span>
    )}
  </div>
);

const ToolRow = ({tool, onRoleChange}) => {
  const [open, setOpen] = useState(false);
  const schemaEntries = Object.entries(tool.inputSchema??{});
  const scopeableFields = schemaEntries.filter(([,d])=>d.scopeable);
  const scopedPolicies = (tool.usedInPolicies??[]).filter(p=>p.scoped);
  const usedIn = tool.usedInPolicies??[];

  return (
    <div>
      <div className="tool-row" style={{paddingLeft:0,paddingRight:0,gridTemplateColumns:"1fr 80px 28px"}}>
        {/* Name + expand toggle */}
        <div style={{minWidth:0}}>
          <div style={{display:"flex",alignItems:"center",gap:6}}>
            <button onClick={()=>setOpen(o=>!o)} style={{background:"none",border:"none",cursor:"pointer",display:"flex",alignItems:"center",padding:0,flexShrink:0}}>
              <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke={T.textFaint} strokeWidth="2.5"
                style={{flexShrink:0,transition:"transform .15s",transform:open?"rotate(90deg)":"rotate(0deg)",marginRight:4}}>
                <polyline points="9 18 15 12 9 6"/>
              </svg>
            </button>
            <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:11,color:T.textSecond,overflow:"hidden",textOverflow:"ellipsis",whiteSpace:"nowrap"}}>{tool.name}</span>
            {/* Scoped indicator — shown when this tool is param-scoped in at least one policy */}
            {scopedPolicies.length>0&&(
              <span title={`Param-scoped in: ${scopedPolicies.map(p=>p.name).join(", ")}`}
                style={{display:"inline-flex",alignItems:"center",gap:3,fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.blueLight,background:"rgba(96,165,250,.08)",border:"1px solid rgba(96,165,250,.18)",padding:"1px 5px",borderRadius:3,flexShrink:0,cursor:"default"}}>
                <svg width="8" height="8" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
                {scopedPolicies.length} scoped
              </span>
            )}
            {/* Usage count */}
            {usedIn.length>0&&(
              <span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint,flexShrink:0}}>{usedIn.length} {usedIn.length===1?"policy":"policies"}</span>
            )}
          </div>
        </div>

        {/* Role selector */}
        <div>
          <select className={`role-select ${tool.role??"unassigned"}`} value={tool.role??""} onChange={e=>onRoleChange(tool.id, e.target.value||null)}>
            <option value="">unassigned</option>
            {ROLES.map(r=><option key={r} value={r}>{r}</option>)}
          </select>
        </div>

        <div style={{width:28}}/>
      </div>

      {/* Expanded detail */}
      {open&&(
        <div style={{background:T.bgCode,border:`1px solid ${T.borderSubtle}`,borderRadius:5,margin:"2px 0 6px 14px",padding:"12px 14px",display:"flex",flexDirection:"column",gap:12}}>
          {/* Description */}
          <p style={{fontSize:11,color:T.textMuted,lineHeight:1.55}}>{tool.description}</p>

          {/* Input schema */}
          {schemaEntries.length>0 ? (
            <div>
              <div style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint,textTransform:"uppercase",letterSpacing:".07em",marginBottom:7}}>
                input schema
                {scopeableFields.length>0&&(
                  <span style={{marginLeft:8,color:T.blueLight,opacity:.7}}>· {scopeableFields.length} field{scopeableFields.length!==1?"s":""} scopeable via policy params</span>
                )}
              </div>
              <div style={{display:"flex",flexDirection:"column",gap:1}}>
                {schemaEntries.map(([name,def])=><SchemaFieldPill key={name} name={name} def={def}/>)}
              </div>
            </div>
          ) : (
            <div style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint}}>no input parameters</div>
          )}

          {/* Policy usage */}
          {usedIn.length>0&&(
            <div>
              <div style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint,textTransform:"uppercase",letterSpacing:".07em",marginBottom:7}}>used in</div>
              <div style={{display:"flex",flexDirection:"column",gap:4}}>
                {usedIn.map(p=>(
                  <div key={p.name} style={{display:"flex",alignItems:"center",gap:8}}>
                    <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:11,color:T.textSecond}}>{p.name}</span>
                    {p.scoped ? (
                      <span style={{display:"inline-flex",alignItems:"center",gap:3,fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.blueLight,background:"rgba(96,165,250,.08)",border:"1px solid rgba(96,165,250,.18)",padding:"1px 6px",borderRadius:3}}>
                        <svg width="7" height="7" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
                        param-scoped
                      </span>
                    ) : (
                      <span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint}}>unscoped</span>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
};

// ─── Server card ───────────────────────────────────────────────────────────────

const ServerCard = ({server, onEdit, onDelete, onDiscover}) => {
  const [expanded,  setExpanded]  = useState(server.status==="unreachable");
  const [tools,     setTools]     = useState(server.tools);
  const [testing,   setTesting]   = useState(false);
  const [testResult,setTestResult]= useState(null); // null | "ok" | "fail"
  const [discovering, setDiscovering] = useState(false);

  const unassigned = tools.filter(t=>!t.role).length;
  const byRole = {
    sensor:   tools.filter(t=>t.role==="sensor"),
    actuator: tools.filter(t=>t.role==="actuator"),
    feedback: tools.filter(t=>t.role==="feedback"),
    unassigned: tools.filter(t=>!t.role),
  };

  const handleTest = () => {
    setTesting(true); setTestResult(null);
    setTimeout(()=>{ setTesting(false); setTestResult(server.status==="reachable"?"ok":"fail"); setTimeout(()=>setTestResult(null),3000); }, 1200);
  };

  const handleDiscover = () => {
    setDiscovering(true);
    setTimeout(()=>{ setDiscovering(false); onDiscover(server); }, 1400);
  };

  const handleRoleChange = (toolId, newRole) => {
    setTools(ts=>ts.map(t=>t.id===toolId?{...t,role:newRole}:t));
  };

  const borderColor = server.status==="unreachable" ? "rgba(248,113,113,.3)" : unassigned>0 ? "rgba(245,158,11,.2)" : T.borderSubtle;

  return (
    <div className="server-card" style={{border:`1px solid ${borderColor}`}}>
      {/* Card header */}
      <div style={{display:"flex",alignItems:"center",gap:12,padding:"14px 16px"}}>
        <StatusDot status={testing?"checking":server.status}/>
        <div style={{flex:1,minWidth:0}}>
          <div style={{display:"flex",alignItems:"center",gap:8}}>
            <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:13,fontWeight:500,color:T.textPrimary}}>{server.name}</span>
            {unassigned>0&&<span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.amber,background:"rgba(245,158,11,.1)",border:"1px solid rgba(245,158,11,.25)",padding:"1px 6px",borderRadius:3}}>{unassigned} unassigned</span>}
            {server.status==="unreachable"&&<span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.red,background:"rgba(248,113,113,.08)",border:"1px solid rgba(248,113,113,.2)",padding:"1px 6px",borderRadius:3}}>unreachable</span>}
          </div>
          <div style={{display:"flex",alignItems:"center",gap:10,marginTop:2}}>
            <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:10,color:T.textMuted}}>{server.url}</span>
            <span style={{fontSize:10,color:T.textFaint}}>·</span>
            <span style={{fontSize:10,color:T.textFaint}}>{tools.length} tools</span>
            <span style={{fontSize:10,color:T.textFaint}}>·</span>
            <span style={{fontSize:10,color:T.textFaint}}>discovered {fmtAgo(server.lastDiscoveredAt)}</span>
          </div>
        </div>

        {/* Actions */}
        <div style={{display:"flex",alignItems:"center",gap:4}}>
          {/* Test connectivity */}
          <button className="icon-btn" onClick={handleTest} title="Test connectivity"
            style={{color:testing?"#60A5FA":testResult==="ok"?T.green:testResult==="fail"?T.red:T.textMuted}}>
            {testing ? <Spinner size={13}/>
              : testResult==="ok" ? <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><polyline points="20 6 9 17 4 12"/></svg>
              : testResult==="fail" ? <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
              : <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="10"/><path d="M12 8v4m0 4h.01"/></svg>}
          </button>

          {/* Re-discover */}
          <button className={`icon-btn${discovering?" spinning":""}`} onClick={handleDiscover} title="Re-discover tools">
            {discovering ? <Spinner size={13}/> : <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><polyline points="1 4 1 10 7 10"/><path d="M3.51 15a9 9 0 1 0 .49-3.44"/></svg>}
          </button>

          {/* Edit */}
          <button className="icon-btn" onClick={()=>onEdit(server)} title="Edit server">
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
          </button>

          {/* Delete */}
          <button className="icon-btn danger" onClick={()=>onDelete(server)} title="Delete server">
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/><path d="M10 11v6M14 11v6"/><path d="M9 6V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"/></svg>
          </button>

          {/* Expand toggle */}
          <div style={{width:1,height:18,background:T.borderMid,margin:"0 2px"}}/>
          <button className="icon-btn" onClick={()=>setExpanded(e=>!e)} title={expanded?"Collapse":"Expand"}>
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" style={{transition:"transform .18s",transform:expanded?"rotate(180deg)":"rotate(0deg)"}}>
              <polyline points="6 9 12 15 18 9"/>
            </svg>
          </button>
        </div>
      </div>

      {/* Expanded tool list */}
      {expanded&&(
        <div style={{borderTop:`1px solid ${T.borderSubtle}`,padding:"12px 16px 14px"}}>
          {unassigned>0&&(
            <div className="unassigned-banner">
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" style={{flexShrink:0}}><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
              {unassigned} {unassigned===1?"tool":"tools"} unassigned — assign a role before using in policies
            </div>
          )}

          {/* Column headers */}
          <div style={{display:"grid",gridTemplateColumns:"1fr 80px 28px",gap:8,padding:"0 0 6px",marginBottom:4,borderBottom:`1px solid ${T.borderSubtle}`}}>
            <span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint,textTransform:"uppercase",letterSpacing:".06em"}}>Tool</span>
            <span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint,textTransform:"uppercase",letterSpacing:".06em"}}>Role</span>
            <span/>
          </div>

          {tools.map(t=><ToolRow key={t.id} tool={t} onRoleChange={handleRoleChange}/>)}

          {/* Role summary footer */}
          <div style={{display:"flex",gap:14,marginTop:12,paddingTop:10,borderTop:`1px solid ${T.borderSubtle}`}}>
            {[["sensor",T.sensor],["actuator",T.actuator],["feedback",T.feedback]].map(([r,c])=>(
              <div key={r} style={{display:"flex",alignItems:"center",gap:5}}>
                <span style={{width:6,height:6,borderRadius:"50%",background:c,flexShrink:0}}/>
                <span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.textMuted}}>{byRole[r].length} {r}</span>
              </div>
            ))}
            {unassigned>0&&(
              <div style={{display:"flex",alignItems:"center",gap:5}}>
                <span style={{width:6,height:6,borderRadius:"50%",background:T.amber,flexShrink:0,animation:"gPulse 1.6s ease-in-out infinite"}}/>
                <span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.amber}}>{unassigned} unassigned</span>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
};

// ─── Main ──────────────────────────────────────────────────────────────────────

export default function MCPRegistry() {
  const [activeTab,  setActiveTab]  = useState("Servers");
  const [servers,    setServers]    = useState(MOCK_SERVERS);
  const [modal,      setModal]      = useState(null); // null | "register" | "edit" | "delete" | "diff"
  const [target,     setTarget]     = useState(null); // server being acted on
  const [pendingDiff,setPendingDiff]= useState(null);

  const totalTools     = servers.reduce((a,s)=>a+s.tools.length,0);
  const totalUnassigned= servers.reduce((a,s)=>a+s.tools.filter(t=>!t.role).length,0);
  const unreachable    = servers.filter(s=>s.status==="unreachable").length;
  const totalScoped    = servers.reduce((a,s)=>a+s.tools.filter(t=>(t.usedInPolicies??[]).some(p=>p.scoped)).length,0);

  const openEdit   = s => { setTarget(s); setModal("edit"); };
  const openDelete = s => { setTarget(s); setModal("delete"); };
  const openDiff   = s => { setPendingDiff(MOCK_DIFF); setTarget(s); setModal("diff"); };
  const closeModal = () => { setModal(null); setTarget(null); setPendingDiff(null); };

  const handleSave = ({name,url}) => {
    if (modal==="edit") {
      setServers(ss=>ss.map(s=>s.id===target.id?{...s,name,url}:s));
    } else {
      const newSrv = { id:`srv-${Date.now()}`, name, url, status:"reachable", lastDiscoveredAt:new Date().toISOString(), tools:[], affectedPolicies:[] };
      setServers(ss=>[...ss,newSrv]);
    }
    closeModal();
  };

  const handleDelete = () => {
    setServers(ss=>ss.filter(s=>s.id!==target.id));
    closeModal();
  };

  const handleApplyDiff = () => {
    // In real app: apply diff and update tool list; here just close
    closeModal();
  };

  return (
    <>
      <style>{STYLES}</style>
      <div style={{minHeight:"100vh",background:T.bgCanvas,display:"flex",flexDirection:"column"}}>
        <TopBar activeTab={activeTab} setActiveTab={setActiveTab}/>

        <div style={{flex:1,maxWidth:900,width:"100%",margin:"0 auto",padding:"28px 24px"}}>

          {/* Page header */}
          <div style={{display:"flex",alignItems:"flex-start",justifyContent:"space-between",marginBottom:24}}>
            <div>
              <h1 style={{fontFamily:"'IBM Plex Sans',sans-serif",fontSize:18,fontWeight:600,color:T.textPrimary,marginBottom:4}}>MCP Servers</h1>
              <p style={{fontSize:12,color:T.textMuted}}>Register and manage the MCP server containers your agents can call tools from.</p>
            </div>
            <button className="pill-btn primary" onClick={()=>setModal("register")}>
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
              Register server
            </button>
          </div>

          {/* Stats bar */}
          <div style={{display:"grid",gridTemplateColumns:"repeat(5,1fr)",gap:10,marginBottom:24}}>
            {[
              {label:"Servers",      value:servers.length,  color:T.textPrimary},
              {label:"Tools",        value:totalTools,       color:T.textPrimary},
              {label:"Param-scoped", value:totalScoped,      color:totalScoped>0?T.blueLight:T.textFaint},
              {label:"Unassigned",   value:totalUnassigned,  color:totalUnassigned>0?T.amber:T.green},
              {label:"Unreachable",  value:unreachable,      color:unreachable>0?T.red:T.green},
            ].map(({label,value,color})=>(
              <div key={label} style={{background:T.bgSurface,border:`1px solid ${T.borderSubtle}`,borderRadius:7,padding:"12px 14px"}}>
                <div style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.textMuted,textTransform:"uppercase",letterSpacing:".06em",marginBottom:4}}>{label}</div>
                <div style={{fontSize:20,fontFamily:"'IBM Plex Mono',monospace",fontWeight:500,color}}>{value}</div>
              </div>
            ))}
          </div>

          {/* Unreachable banner */}
          {unreachable>0&&(
            <div style={{display:"flex",alignItems:"center",gap:10,padding:"10px 14px",background:"rgba(248,113,113,.06)",border:"1px solid rgba(248,113,113,.2)",borderRadius:6,marginBottom:18,fontSize:12,color:T.textSecond}}>
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke={T.red} strokeWidth="2.5" style={{flexShrink:0}}><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
              <span><span style={{color:T.red,fontWeight:500}}>{unreachable} server{unreachable>1?"s":""} unreachable.</span> Runs depending on these tools will fail at execution time.</span>
            </div>
          )}

          {/* Server list */}
          <div style={{display:"flex",flexDirection:"column",gap:10}}>
            {servers.map(s=>(
              <ServerCard key={s.id} server={s}
                onEdit={openEdit} onDelete={openDelete} onDiscover={openDiff}/>
            ))}
          </div>

          {servers.length===0&&(
            <div style={{textAlign:"center",padding:"64px 0",color:T.textMuted}}>
              <div style={{fontSize:32,marginBottom:12,opacity:.3}}>⬡</div>
              <p style={{fontSize:13,marginBottom:16}}>No MCP servers registered yet.</p>
              <button className="pill-btn primary" onClick={()=>setModal("register")}>Register your first server</button>
            </div>
          )}
        </div>
      </div>

      {/* Modals */}
      {(modal==="register"||modal==="edit")&&(
        <ServerModal initial={modal==="edit"?target:null} onClose={closeModal} onSave={handleSave}/>
      )}
      {modal==="delete"&&target&&(
        <DeleteModal server={target} onClose={closeModal} onConfirm={handleDelete}/>
      )}
      {modal==="diff"&&pendingDiff&&target&&(
        <DiffModal diff={pendingDiff} serverName={target.name} onClose={closeModal} onApply={handleApplyDiff}/>
      )}
    </>
  );
}
