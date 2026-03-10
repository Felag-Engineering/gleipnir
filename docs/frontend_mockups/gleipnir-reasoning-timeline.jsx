import { useState, useEffect, useRef, useCallback } from "react";

// ─── Design tokens ─────────────────────────────────────────────────────────────

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

const STATUS = {
  complete:             { label:"Complete",          color:T.green,     bg:"rgba(74,222,128,0.08)",  border:"rgba(74,222,128,0.2)"  },
  running:              { label:"Running",           color:T.blueLight, bg:"rgba(96,165,250,0.08)",  border:"rgba(96,165,250,0.2)",  pulse:true },
  waiting_for_approval: { label:"Awaiting Approval", color:T.amber,     bg:"rgba(245,158,11,0.08)",  border:"rgba(245,158,11,0.2)",  pulse:true },
  failed:               { label:"Failed",            color:T.red,       bg:"rgba(248,113,113,0.08)", border:"rgba(248,113,113,0.2)" },
  interrupted:          { label:"Interrupted",       color:T.purple,    bg:"rgba(167,139,250,0.08)", border:"rgba(167,139,250,0.2)" },
};

// ─── Mock data ─────────────────────────────────────────────────────────────────

const MOCK_RUN = {
  id:"r502", policyName:"kubectl-pod-watcher", folder:"Infrastructure",
  triggerType:"poll", status:"waiting_for_approval",
  startedAt: new Date(Date.now()-7*60*1000).toISOString(),
  tokenCost:4100, toolCalls:6, duration:null,
};

const CAPABILITY_SNAPSHOT = {
  id:"s0", type:"capability_snapshot", stepNumber:0,
  timestamp: new Date(Date.now()-7*60*1000-500).toISOString(),
  tokenCost:0,
  tools:[
    { name:"kubectl.get_pods",    role:"sensor",   approvalRequired:false,
      presentedSchema:{ namespace:{type:"enum",values:["worker-01","worker-02","worker-03"]}, label_selector:{type:"string",optional:true} } },
    { name:"kubectl.get_events",  role:"sensor",   approvalRequired:false,
      presentedSchema:{ namespace:{type:"enum",values:["worker-01","worker-02","worker-03"]}, pod:{type:"string",optional:true} } },
    { name:"kubectl.delete_pod",  role:"actuator", approvalRequired:true, approvalTimeout:"30m", onTimeout:"reject",
      presentedSchema:{ namespace:{type:"enum",values:["worker-01","worker-02","worker-03"]}, pod:{type:"string"} } },
    { name:"vikunja.task_create", role:"actuator", approvalRequired:true, approvalTimeout:"30m", onTimeout:"reject",
      presentedSchema:{ title:{type:"string"}, project:{type:"string"}, priority:{type:"integer",optional:true}, description:{type:"string",optional:true} } },
  ],
};

const INITIAL_STEPS = [
  { id:"s1", type:"thought", stepNumber:1, timestamp:new Date(Date.now()-7*60*1000).toISOString(), tokenCost:312,
    content:"The poll filter matched — CrashLoopBackOff detected somewhere in the worker namespaces. I need to identify which pod before deciding on severity." },
  { id:"s2", type:"tool_call", stepNumber:2, timestamp:new Date(Date.now()-415*1000).toISOString(), tokenCost:0,
    toolName:"kubectl.get_pods", serverName:"kubectl-mcp", capabilityRole:"sensor", input:{namespace:"worker-02"} },
  { id:"s3", type:"tool_result", stepNumber:3, timestamp:new Date(Date.now()-410*1000).toISOString(), tokenCost:0,
    toolName:"kubectl.get_pods", isError:false,
    output:"NAMESPACE   NAME                         READY   STATUS             RESTARTS   AGE\nworker-02   log-shipper-5b8c7d-pq9r2    0/1     CrashLoopBackOff   8          12m\nworker-02   metrics-proxy-4a7b6c-rs3t5  1/1     Running            0          3h" },
  { id:"s4", type:"thought", stepNumber:4, timestamp:new Date(Date.now()-404*1000).toISOString(), tokenCost:284,
    content:"Found it — worker-02/log-shipper, 8 restarts in 12 minutes. That's a clear crash loop. I need the events to understand why before assessing severity." },
  { id:"s5", type:"tool_call", stepNumber:5, timestamp:new Date(Date.now()-401*1000).toISOString(), tokenCost:0,
    toolName:"kubectl.get_events", serverName:"kubectl-mcp", capabilityRole:"sensor",
    input:{namespace:"worker-02", pod:"log-shipper-5b8c7d-pq9r2"} },
  { id:"s6", type:"tool_result", stepNumber:6, timestamp:new Date(Date.now()-398*1000).toISOString(), tokenCost:0,
    toolName:"kubectl.get_events", isError:false,
    output:"LAST SEEN   TYPE      REASON    OBJECT                          MESSAGE\n12m         Warning   BackOff   pod/log-shipper-5b8c7d-pq9r2    Back-off restarting failed container\n12m         Warning   Failed    pod/log-shipper-5b8c7d-pq9r2    Error: failed to connect to log aggregator at 10.0.1.44:5044 — connection refused. Exit code 1.\n11m         Warning   Failed    pod/log-shipper-5b8c7d-pq9r2    Error: failed to connect to log aggregator at 10.0.1.44:5044 — connection refused. Exit code 1." },
  { id:"s7", type:"thought", stepNumber:7, timestamp:new Date(Date.now()-392*1000).toISOString(), tokenCost:401,
    content:"The log aggregator at 10.0.1.44:5044 is refusing connections. This isn't a pod configuration issue — the upstream dependency is likely down. 8 restarts over 12 minutes with no sign of recovery. This warrants a P1 incident task. The vikunja.task_create actuator requires approval — requesting before proceeding." },
  { id:"s8", type:"approval_request", stepNumber:8, timestamp:new Date(Date.now()-4*60*1000).toISOString(), tokenCost:0,
    toolName:"vikunja.task_create", capabilityRole:"actuator",
    proposedInput:{ title:"INC: CrashLoopBackOff — worker-02/log-shipper", project:"Incidents", priority:1, description:"Pod log-shipper in namespace worker-02 has been in CrashLoopBackOff for 12 minutes. Last exit code: 1. Log aggregator at 10.0.1.44:5044 refusing connections." },
    status:"pending", expiresAt:new Date(Date.now()+18*60*1000).toISOString() },
];

const STREAMING_STEPS = [
  { id:"s9",  type:"thought",   stepNumber:9,  timestamp:new Date(Date.now()+2000).toISOString(),  tokenCost:198,
    content:"Approval received. Proceeding to create the incident task." },
  { id:"s10", type:"tool_call", stepNumber:10, timestamp:new Date(Date.now()+3500).toISOString(), tokenCost:0,
    toolName:"vikunja.task_create", serverName:"vikunja-mcp", capabilityRole:"actuator",
    input:{title:"INC: CrashLoopBackOff — worker-02/log-shipper", project:"Incidents", priority:1} },
  { id:"s11", type:"tool_result",stepNumber:11, timestamp:new Date(Date.now()+5000).toISOString(), tokenCost:0,
    toolName:"vikunja.task_create", isError:false,
    output:'{ "task_id": 1042, "title": "INC: CrashLoopBackOff — worker-02/log-shipper", "project": "Incidents", "priority": 1, "url": "https://vikunja.local/tasks/1042" }' },
  { id:"s12", type:"complete",  stepNumber:12, timestamp:new Date(Date.now()+6500).toISOString(), tokenCost:312,
    summary:"Incident task #1042 created in Vikunja. CrashLoopBackOff on worker-02/log-shipper attributed to log aggregator at 10.0.1.44:5044 being unreachable. P1 priority assigned." },
];

// ─── Helpers ───────────────────────────────────────────────────────────────────

const fmtDur = s => { if(s==null)return null; if(s<60)return`${s}s`; return`${Math.floor(s/60)}m ${s%60}s`; };
const fmtTok = n => n>=1000?`${(n/1000).toFixed(1)}k`:String(n);
const fmtTime= iso => new Date(iso).toLocaleTimeString("en-US",{hour:"2-digit",minute:"2-digit",second:"2-digit",hour12:false});
const fmtAbs = iso => new Date(iso).toLocaleString("en-US",{month:"short",day:"numeric",hour:"2-digit",minute:"2-digit",hour12:false});
const timeLeft = expiresAt => {
  const secs=Math.max(0,Math.floor((new Date(expiresAt)-Date.now())/1000));
  return{str:`${Math.floor(secs/60)}:${String(secs%60).padStart(2,"0")}`,urgent:secs<300};
};

const FILTER_TYPES = [
  {key:"all",label:"All"},{key:"thought",label:"Thoughts"},{key:"tool_call",label:"Calls"},
  {key:"tool_result",label:"Results"},{key:"approval_request",label:"Approvals"},{key:"error",label:"Errors"},
];

// ─── Global styles ─────────────────────────────────────────────────────────────

const STYLES = `
  @import url('https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=IBM+Plex+Sans:wght@300;400;500;600&display=swap');
  *{box-sizing:border-box;margin:0;padding:0}
  body{background:${T.bgCanvas};color:${T.textPrimary};font-family:'IBM Plex Sans',sans-serif}
  @keyframes gPulse{0%,100%{opacity:1}50%{opacity:.35}}
  @keyframes gSpin{from{transform:rotate(0deg)}to{transform:rotate(360deg)}}
  @keyframes stepIn{from{opacity:0;transform:translateY(-6px)}to{opacity:1;transform:translateY(0)}}
  @keyframes approvalPulse{0%,100%{box-shadow:0 0 0 0 rgba(245,158,11,.2)}50%{box-shadow:0 0 0 5px rgba(245,158,11,0)}}
  @keyframes cursorBlink{0%,100%{opacity:1}50%{opacity:0}}
  ::-webkit-scrollbar{width:6px}::-webkit-scrollbar-track{background:transparent}
  ::-webkit-scrollbar-thumb{background:#253044;border-radius:3px}
  ::-webkit-scrollbar-thumb:hover{background:#334155}
  .step-card{animation:stepIn .2s ease forwards}
  .tool-result-pre{font-family:'IBM Plex Mono',monospace;font-size:11px;line-height:1.6;color:${T.textSecond};white-space:pre;overflow-x:auto;padding:10px 12px;background:${T.bgCode};border-radius:4px;border:1px solid ${T.borderMid}}
  .copy-wrapper{position:relative}
  .copy-btn{position:absolute;top:6px;right:6px;background:${T.bgElevated};border:1px solid ${T.borderMid};border-radius:3px;padding:3px 7px;cursor:pointer;font-family:'IBM Plex Mono',monospace;font-size:9px;color:${T.textMuted};transition:all .15s;opacity:0}
  .copy-wrapper:hover .copy-btn{opacity:1}
  .copy-btn:hover{color:${T.textPrimary};border-color:#475569}
  .copy-btn.copied{color:${T.green};border-color:rgba(74,222,128,.3)}
  .expand-btn{background:none;border:none;cursor:pointer;display:inline-flex;align-items:center;gap:4px;font-family:'IBM Plex Mono',monospace;font-size:10px;color:${T.textMuted};padding:3px 0;margin-top:6px;transition:color .15s}
  .expand-btn:hover{color:${T.textSecond}}
  .approve-btn{display:inline-flex;align-items:center;gap:5px;padding:5px 12px;border-radius:5px;border:none;cursor:pointer;font-family:'IBM Plex Sans',sans-serif;font-size:12px;font-weight:500;transition:all .15s}
  .approve-btn.approve{background:rgba(74,222,128,.1);border:1px solid rgba(74,222,128,.3);color:${T.green}}
  .approve-btn.approve:hover{background:rgba(74,222,128,.2)}
  .approve-btn.reject{background:rgba(248,113,113,.08);border:1px solid rgba(248,113,113,.2);color:${T.red}}
  .approve-btn.reject:hover{background:rgba(248,113,113,.15)}
  .approve-btn.confirm-approve{background:rgba(74,222,128,.2);border:1px solid rgba(74,222,128,.4);color:${T.green}}
  .approve-btn.confirm-reject{background:rgba(248,113,113,.15);border:1px solid rgba(248,113,113,.35);color:${T.red}}
  .approve-btn.cancel-btn{background:transparent;border:1px solid ${T.borderMid};color:${T.textMuted}}
  .approve-btn.cancel-btn:hover{color:${T.textSecond};border-color:#334155}
  .filter-chip{background:none;border:1px solid ${T.borderMid};border-radius:4px;padding:3px 10px;cursor:pointer;font-family:'IBM Plex Mono',monospace;font-size:10px;color:${T.textMuted};transition:all .15s}
  .filter-chip:hover{color:${T.textSecond};border-color:#334155}
  .filter-chip.active{color:${T.textPrimary};background:${T.bgElevated};border-color:#475569}
  .action-link{background:none;border:none;cursor:pointer;padding:0;font-family:'IBM Plex Mono',monospace;font-size:10px;color:${T.textMuted};transition:color .15s;display:inline-flex;align-items:center;gap:4px}
  .action-link:hover{color:${T.textSecond}}
  .jump-btn{display:inline-flex;align-items:center;gap:5px;background:rgba(245,158,11,.1);border:1px solid rgba(245,158,11,.3);border-radius:5px;padding:4px 10px;cursor:pointer;font-family:'IBM Plex Mono',monospace;font-size:10px;color:${T.amber};transition:all .15s;animation:approvalPulse 2.4s ease-in-out infinite}
  .jump-btn:hover{background:rgba(245,158,11,.18)}
  .back-btn{display:inline-flex;align-items:center;gap:6px;background:none;border:none;cursor:pointer;font-family:'IBM Plex Sans',sans-serif;font-size:12px;color:${T.textMuted};padding:4px 0;transition:color .15s}
  .back-btn:hover{color:${T.textPrimary}}
  .meta-label{font-family:'IBM Plex Mono',monospace;font-size:10px;color:${T.textMuted};text-transform:uppercase;letter-spacing:.06em;margin-bottom:3px}
  .meta-value{font-family:'IBM Plex Sans',sans-serif;font-size:13px;color:${T.textPrimary};font-weight:500}
  .meta-value.mono{font-family:'IBM Plex Mono',monospace;font-size:11px}
`;

// ─── Atoms ─────────────────────────────────────────────────────────────────────

const StatusBadge = ({status}) => {
  const c=STATUS[status]||STATUS.complete;
  return <span style={{display:"inline-flex",alignItems:"center",gap:5,padding:"3px 9px",borderRadius:4,background:c.bg,border:`1px solid ${c.border}`,fontSize:11,fontFamily:"'IBM Plex Mono',monospace",color:c.color,whiteSpace:"nowrap"}}><span style={{width:6,height:6,borderRadius:"50%",background:c.color,flexShrink:0,animation:c.pulse?"gPulse 1.6s ease-in-out infinite":"none"}}/>{c.label}</span>;
};

const TriggerChip = ({type}) => {
  const col={webhook:T.blueLight,cron:T.purple,poll:T.teal,manual:T.textSecond}[type]||T.textSecond;
  return <span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:col,background:"rgba(255,255,255,.04)",border:"1px solid rgba(255,255,255,.08)",padding:"1px 6px",borderRadius:3}}>{type}</span>;
};

const RoleChip = ({role}) => {
  const col={sensor:T.sensor,actuator:T.actuator,feedback:T.feedback}[role]||T.textMuted;
  return <span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:col,background:`${col}14`,border:`1px solid ${col}30`,padding:"1px 5px",borderRadius:3,flexShrink:0}}>{role}</span>;
};

const Spinner = ({size=14,color=T.blueLight}) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" style={{animation:"gSpin 1s linear infinite",flexShrink:0}}>
    <circle cx="12" cy="12" r="10" stroke={color} strokeWidth="2.5" strokeOpacity="0.2"/>
    <path d="M12 2a10 10 0 0 1 10 10" stroke={color} strokeWidth="2.5" strokeLinecap="round"/>
  </svg>
);

const LiveCursor = () => <span style={{display:"inline-block",width:7,height:13,marginLeft:3,background:T.blueLight,borderRadius:1,verticalAlign:"text-bottom",animation:"cursorBlink 1.1s ease-in-out infinite"}}/>;

const CopyBlock = ({text,children}) => {
  const [copied,setCopied]=useState(false);
  const copy = e => { e.stopPropagation(); navigator.clipboard.writeText(text).then(()=>{setCopied(true);setTimeout(()=>setCopied(false),1800)}); };
  return <div className="copy-wrapper">{children}<button className={`copy-btn${copied?" copied":""}`} onClick={copy}>{copied?"✓ copied":"copy"}</button></div>;
};

// ─── Step icon ─────────────────────────────────────────────────────────────────

const StepIcon = ({type,capabilityRole,isError,isLive}) => {
  const base={width:22,height:22,borderRadius:type==="thought"||type==="complete"?"50%":4,display:"flex",alignItems:"center",justifyContent:"center",flexShrink:0};
  if(isLive) return <div style={{...base,background:"rgba(96,165,250,.1)",border:"1px solid rgba(96,165,250,.25)"}}><Spinner size={11}/></div>;
  const cfg={
    thought:          {bg:"rgba(100,116,139,.1)",   border:"rgba(100,116,139,.2)",   color:T.textMuted,  sym:"·"},
    tool_call:        {bg:capabilityRole==="actuator"?"rgba(251,146,60,.1)":"rgba(96,165,250,.1)",border:capabilityRole==="actuator"?"rgba(251,146,60,.25)":"rgba(96,165,250,.25)",color:capabilityRole==="actuator"?T.actuator:T.sensor,sym:"→"},
    tool_result:      {bg:isError?"rgba(248,113,113,.08)":"rgba(74,222,128,.08)",border:isError?"rgba(248,113,113,.2)":"rgba(74,222,128,.2)",color:isError?T.red:T.green,sym:"←"},
    approval_request: {bg:"rgba(245,158,11,.1)",    border:"rgba(245,158,11,.3)",    color:T.amber,  sym:"!"},
    complete:         {bg:"rgba(74,222,128,.1)",     border:"rgba(74,222,128,.25)",   color:T.green,  sym:"✓"},
    error:            {bg:"rgba(248,113,113,.1)",    border:"rgba(248,113,113,.25)",  color:T.red,    sym:"✕"},
    feedback_request: {bg:"rgba(167,139,250,.1)",    border:"rgba(167,139,250,.25)",  color:T.purple, sym:"?"},
    feedback_response:{bg:"rgba(167,139,250,.1)",    border:"rgba(167,139,250,.25)",  color:T.purple, sym:"↩"},
  };
  const c=cfg[type]||cfg.thought;
  return <div style={{...base,background:c.bg,border:`1px solid ${c.border}`}}><span style={{fontSize:type==="thought"?14:11,color:c.color,fontFamily:"'IBM Plex Mono',monospace",lineHeight:1}}>{c.sym}</span></div>;
};

// ─── Summary + label helpers ───────────────────────────────────────────────────

const stepSummary = s => {
  switch(s.type){
    case "thought":          return s.content.length>100?s.content.slice(0,100)+"…":s.content;
    case "tool_call":        return `Called ${s.toolName}`;
    case "tool_result":      return s.isError?`Error from ${s.toolName}`:`${s.toolName} — ${s.output.split("\n")[0].slice(0,60)}`;
    case "approval_request": return `Approval required — ${s.toolName}`;
    case "complete":         return s.summary?.slice(0,100)+(s.summary?.length>100?"…":"");
    case "error":            return `Error — ${s.content?.message?.slice(0,70)??"unknown"}`;
    default:                 return s.type;
  }
};

const typeLabel = s => {
  const m={
    thought:          {text:"thought",        color:T.textMuted},
    tool_call:        {text:s.capabilityRole==="actuator"?"actuator call":"sensor call",color:s.capabilityRole==="actuator"?T.actuator:T.sensor},
    tool_result:      {text:s.isError?"error result":"result",color:s.isError?T.red:T.green},
    approval_request: {text:"approval",       color:T.amber},
    complete:         {text:"complete",       color:T.green},
    error:            {text:"error",          color:T.red},
    feedback_request: {text:"feedback",       color:T.purple},
    feedback_response:{text:"response",       color:T.purple},
  };
  return m[s.type]||{text:s.type,color:T.textMuted};
};

// ─── Detail panels ─────────────────────────────────────────────────────────────

const ToolCallDetail = ({step}) => (
  <div>
    <div style={{display:"flex",alignItems:"center",gap:8,marginBottom:8}}>
      <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:13,fontWeight:500,color:step.capabilityRole==="actuator"?T.actuator:T.sensor}}>{step.toolName}</span>
      <RoleChip role={step.capabilityRole}/>
      <span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint}}>on {step.serverName}</span>
    </div>
    <CopyBlock text={JSON.stringify(step.input,null,2)}>
      <div style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:11,color:T.textSecond,background:T.bgCode,border:`1px solid ${T.borderMid}`,borderRadius:4,padding:"8px 12px",lineHeight:1.6}}>
        {JSON.stringify(step.input,null,2)}
      </div>
    </CopyBlock>
  </div>
);

const ToolResultDetail = ({step}) => {
  const [exp,setExp]=useState(false);
  const lines=step.output.split("\n"),hasMore=lines.length>3;
  return (
    <div>
      {step.isError&&<span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.red,background:"rgba(248,113,113,.08)",border:"1px solid rgba(248,113,113,.2)",padding:"1px 5px",borderRadius:3,display:"inline-block",marginBottom:6}}>error</span>}
      <CopyBlock text={step.output}>
        <pre className="tool-result-pre">{exp?step.output:lines.slice(0,3).join("\n")}{!exp&&hasMore?"\n…":""}</pre>
      </CopyBlock>
      {hasMore&&<button className="expand-btn" onClick={()=>setExp(e=>!e)}>
        <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" style={{transform:exp?"rotate(180deg)":"none",transition:"transform .15s"}}><polyline points="6 9 12 15 18 9"/></svg>
        {exp?"collapse":`show ${lines.length-3} more lines`}
      </button>}
    </div>
  );
};

const ApprovalDetail = ({step,mode,confirm,setConfirm,note,setNote,onConfirm,isSettled,settledColor,settledLabel,timer}) => (
  <div>
    <div style={{display:"flex",alignItems:"center",gap:8,marginBottom:8,flexWrap:"wrap"}}>
      <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:13,fontWeight:500,color:T.actuator}}>{step.toolName}</span>
      <RoleChip role="actuator"/>
      {!isSettled&&timer&&<span style={{marginLeft:"auto",fontSize:11,fontFamily:"'IBM Plex Mono',monospace",color:timer.urgent?T.red:T.textMuted,animation:timer.urgent?"gPulse 1.2s ease-in-out infinite":"none"}}>{timer.str}</span>}
      {isSettled&&<span style={{marginLeft:"auto",fontSize:11,fontFamily:"'IBM Plex Mono',monospace",color:settledColor}}>{settledLabel}</span>}
    </div>
    <CopyBlock text={JSON.stringify(step.proposedInput,null,2)}>
      <div style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:11,color:T.textSecond,background:T.bgCode,border:`1px solid ${T.borderMid}`,borderRadius:4,padding:"8px 12px",lineHeight:1.6,marginBottom:isSettled?0:12}}>
        {JSON.stringify(step.proposedInput,null,2)}
      </div>
    </CopyBlock>
    {!isSettled&&!confirm&&(
      <div style={{display:"flex",gap:8,marginTop:10}}>
        <button className="approve-btn approve" onClick={()=>setConfirm("approve")}><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><polyline points="20 6 9 17 4 12"/></svg>Approve</button>
        <button className="approve-btn reject"  onClick={()=>setConfirm("reject")}><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>Reject</button>
      </div>
    )}
    {!isSettled&&confirm&&(
      <div style={{marginTop:10}}>
        <textarea placeholder="Optional note…" value={note} onChange={e=>setNote(e.target.value)}
          style={{width:"100%",padding:"7px 10px",borderRadius:4,background:T.bgCanvas,border:`1px solid ${T.borderMid}`,color:T.textPrimary,fontFamily:"'IBM Plex Sans',sans-serif",fontSize:12,resize:"vertical",minHeight:52,marginBottom:8,outline:"none"}}/>
        <div style={{display:"flex",gap:8}}>
          <button className={`approve-btn ${confirm==="approve"?"confirm-approve":"confirm-reject"}`} onClick={()=>onConfirm(confirm)}>Confirm {confirm==="approve"?"Approve":"Reject"}</button>
          <button className="approve-btn cancel-btn" onClick={()=>setConfirm(null)}>Cancel</button>
        </div>
      </div>
    )}
    {isSettled&&note&&<div style={{marginTop:8,fontSize:11,color:T.textMuted,fontStyle:"italic"}}>"{note}"</div>}
  </div>
);

// ─── Capability snapshot card ─────────────────────────────────────────────────
// Rendered separately at the bottom of the list (step 0, oldest).
// Never included in filter counts — it's infrastructure, not agent reasoning.

const SchemaField = ({name, def}) => {
  const isEnum = def.type==="enum";
  return (
    <div style={{display:"flex",alignItems:"baseline",gap:8,padding:"3px 0"}}>
      <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:10,color:T.textSecond,minWidth:120,flexShrink:0}}>{name}</span>
      {isEnum ? (
        <div style={{display:"flex",gap:4,flexWrap:"wrap"}}>
          {def.values.map(v=>(
            <span key={v} style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:10,color:T.blueLight,background:"rgba(96,165,250,.1)",border:"1px solid rgba(96,165,250,.2)",padding:"1px 6px",borderRadius:3}}>{v}</span>
          ))}
        </div>
      ) : (
        <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:10,color:T.textMuted}}>
          {def.type}{def.optional?" (optional)":""}
        </span>
      )}
    </div>
  );
};

const CapabilitySnapshotCard = ({snapshot}) => {
  const [open,setOpen] = useState(false);
  const sensors   = snapshot.tools.filter(t=>t.role==="sensor");
  const actuators = snapshot.tools.filter(t=>t.role==="actuator");

  return (
    <div style={{marginBottom:6,background:T.bgCode,border:`1px solid ${T.borderSubtle}`,borderRadius:7,overflow:"hidden",opacity:.85}}>
      {/* Summary row */}
      <div onClick={()=>setOpen(o=>!o)}
        style={{display:"flex",alignItems:"center",gap:10,padding:"9px 14px",cursor:"pointer",userSelect:"none"}}>
        {/* Icon */}
        <div style={{width:22,height:22,borderRadius:4,display:"flex",alignItems:"center",justifyContent:"center",flexShrink:0,background:"rgba(100,116,139,.1)",border:"1px solid rgba(100,116,139,.2)"}}>
          <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke={T.textMuted} strokeWidth="2.5">
            <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>
          </svg>
        </div>
        <span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint,textTransform:"uppercase",letterSpacing:".06em",minWidth:92}}>capability snapshot</span>
        <span style={{fontSize:12,color:T.textFaint,fontFamily:"'IBM Plex Mono',monospace",flex:1}}>
          {snapshot.tools.length} tools — {sensors.length} sensor{sensors.length!==1?"s":""}, {actuators.length} actuator{actuators.length!==1?"s":""}
        </span>
        <span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint,flexShrink:0}}>run start</span>
        <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke={T.textFaint} strokeWidth="2.5"
          style={{flexShrink:0,transition:"transform .18s",transform:open?"rotate(180deg)":"rotate(0deg)"}}>
          <polyline points="6 9 12 15 18 9"/>
        </svg>
      </div>

      {/* Detail */}
      {open&&(
        <div style={{borderTop:`1px solid ${T.borderSubtle}`,padding:"14px 16px",display:"flex",flexDirection:"column",gap:14}}>
          <p style={{fontSize:11,color:T.textMuted,lineHeight:1.55}}>
            The complete tool list presented to the agent at run start. Enum constraints show the exact values the agent could pass — values outside this set were structurally unavailable to the agent.
          </p>

          {[["sensor",sensors],["actuator",actuators]].map(([role,tools])=>tools.length>0&&(
            <div key={role}>
              <div style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:role==="sensor"?T.sensor:T.actuator,textTransform:"uppercase",letterSpacing:".08em",marginBottom:8}}>
                {role}s
              </div>
              <div style={{display:"flex",flexDirection:"column",gap:10}}>
                {tools.map(tool=>(
                  <div key={tool.name} style={{background:T.bgSurface,border:`1px solid ${T.borderSubtle}`,borderRadius:5,padding:"10px 12px"}}>
                    {/* Tool header */}
                    <div style={{display:"flex",alignItems:"center",gap:8,marginBottom:8}}>
                      <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:12,fontWeight:500,color:role==="sensor"?T.sensor:T.actuator}}>{tool.name}</span>
                      {tool.approvalRequired&&(
                        <span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.amber,background:"rgba(245,158,11,.1)",border:"1px solid rgba(245,158,11,.25)",padding:"1px 6px",borderRadius:3}}>
                          approval required · {tool.approvalTimeout}
                        </span>
                      )}
                    </div>
                    {/* Schema fields */}
                    <div style={{borderTop:`1px solid ${T.borderSubtle}`,paddingTop:8}}>
                      {Object.entries(tool.presentedSchema).map(([name,def])=>(
                        <SchemaField key={name} name={name} def={def}/>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
};

// ─── Step card ─────────────────────────────────────────────────────────────────

const cardBorderFor = (type,role) => {
  if(type==="approval_request") return "rgba(245,158,11,.25)";
  if(type==="complete")         return "rgba(74,222,128,.2)";
  if(type==="error")            return "rgba(248,113,113,.2)";
  if(type==="tool_call"&&role==="actuator") return "rgba(251,146,60,.18)";
  return T.borderSubtle;
};

const StepCard = ({step,isFirst,isLive,onDecision,forceOpen,approvalRef}) => {
  const defaultOpen = step.type==="approval_request"||step.type==="complete"||step.type==="error";
  const [open,setOpen]       = useState(defaultOpen);
  const [confirm,setConfirm] = useState(null);
  const [note,setNote]       = useState("");
  const [timer,setTimer]     = useState(null);
  const [mode,setMode]       = useState(step.status==="pending"?"idle":(step.status??null));

  useEffect(()=>{ if(forceOpen!==null) setOpen(forceOpen); },[forceOpen]);

  useEffect(()=>{
    if(step.type!=="approval_request"||step.status!=="pending") return;
    const iv=setInterval(()=>setTimer(timeLeft(step.expiresAt)),500);
    setTimer(timeLeft(step.expiresAt));
    return()=>clearInterval(iv);
  },[step.expiresAt,step.status,step.type]);

  const isSettled    = mode==="approved"||mode==="rejected"||mode==="timeout";
  const settledColor = mode==="approved"?T.green:mode==="rejected"?T.red:T.textMuted;
  const settledLabel = mode==="approved"?"Approved":mode==="rejected"?"Rejected":"Timed out";
  const isApproval   = step.type==="approval_request";
  const isPending    = isApproval&&!isSettled;
  const label        = typeLabel(step);
  const border       = cardBorderFor(step.type,step.capabilityRole);

  const handleConfirm = decision => {
    setMode(decision==="approve"?"approved":"rejected");
    setConfirm(null);
    if(onDecision) onDecision(decision,note);
  };

  return (
    <div
      ref={isPending?approvalRef:null}
      className="step-card"
      style={{marginBottom:6,background:T.bgSurface,border:`1px solid ${border}`,borderRadius:7,overflow:"hidden",animation:isPending&&!open?"approvalPulse 2.4s ease-in-out infinite":"none"}}
    >
      {/* Summary row */}
      <div onClick={()=>!isLive&&setOpen(o=>!o)}
        style={{display:"flex",alignItems:"center",gap:10,padding:"9px 14px",cursor:isLive?"default":"pointer",userSelect:"none"}}>

        <StepIcon type={step.type} capabilityRole={step.capabilityRole} isError={step.isError} isLive={isLive&&isFirst}/>

        <span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:isApproval&&isSettled?settledColor:label.color,textTransform:"uppercase",letterSpacing:".06em",flexShrink:0,minWidth:92}}>
          {isApproval&&isSettled?settledLabel.toLowerCase():label.text}
        </span>

        <span style={{fontSize:12,color:open?T.textMuted:T.textSecond,fontFamily:step.type==="thought"?"'IBM Plex Sans',sans-serif":"'IBM Plex Mono',monospace",fontStyle:step.type==="thought"?"italic":"normal",flex:1,overflow:"hidden",textOverflow:"ellipsis",whiteSpace:"nowrap",transition:"color .15s"}}>
          {isLive&&isFirst?<span style={{animation:"gPulse 1.2s ease-in-out infinite"}}>…</span>:stepSummary(step)}
        </span>

        {/* Inline approve/reject when card is collapsed */}
        {isPending&&!open&&!confirm&&(
          <div style={{display:"flex",gap:6,flexShrink:0}} onClick={e=>e.stopPropagation()}>
            <button className="approve-btn approve" style={{padding:"3px 10px",fontSize:11}} onClick={()=>{setOpen(true);setConfirm("approve")}}>
              <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><polyline points="20 6 9 17 4 12"/></svg>Approve
            </button>
            <button className="approve-btn reject" style={{padding:"3px 10px",fontSize:11}} onClick={()=>{setOpen(true);setConfirm("reject")}}>
              <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>Reject
            </button>
          </div>
        )}

        {step.tokenCost>0&&<span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint,flexShrink:0}}>{fmtTok(step.tokenCost)} tok</span>}
        <span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint,flexShrink:0}}>{fmtTime(step.timestamp)}</span>
        {!isLive&&<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke={T.textFaint} strokeWidth="2.5" style={{flexShrink:0,transition:"transform .18s",transform:open?"rotate(180deg)":"rotate(0deg)"}}><polyline points="6 9 12 15 18 9"/></svg>}
      </div>

      {/* Detail panel */}
      {open&&!isLive&&(
        <div style={{borderTop:`1px solid ${T.borderSubtle}`,padding:"12px 14px 14px"}}>
          {step.type==="thought"&&<p style={{fontSize:13,color:T.textSecond,lineHeight:1.65,fontStyle:"italic"}}>{step.content}</p>}
          {step.type==="tool_call"&&<ToolCallDetail step={step}/>}
          {step.type==="tool_result"&&<ToolResultDetail step={step}/>}
          {step.type==="approval_request"&&<ApprovalDetail step={step} mode={mode} confirm={confirm} setConfirm={setConfirm} note={note} setNote={setNote} onConfirm={handleConfirm} isSettled={isSettled} settledColor={settledColor} settledLabel={settledLabel} timer={timer}/>}
          {step.type==="complete"&&<div style={{background:"rgba(74,222,128,.04)",borderRadius:5,padding:"8px 12px"}}><p style={{fontSize:13,color:T.textSecond,lineHeight:1.65}}>{step.summary}</p></div>}
          {step.type==="error"&&<p style={{fontSize:12,color:T.red,fontFamily:"'IBM Plex Mono',monospace"}}>{step.content?.message}</p>}
        </div>
      )}
    </div>
  );
};

// ─── Meta panel ────────────────────────────────────────────────────────────────

const MetaPanel = ({run,stepCount,liveTokens,onNavigateToPolicy}) => {
  const isLive = run.status==="running"||run.status==="waiting_for_approval";
  const [elapsed,setElapsed] = useState(run.duration??Math.floor((Date.now()-new Date(run.startedAt))/1000));

  useEffect(()=>{
    if(!isLive||run.duration!=null) return;
    const iv=setInterval(()=>setElapsed(Math.floor((Date.now()-new Date(run.startedAt))/1000)),1000);
    return()=>clearInterval(iv);
  },[isLive,run.duration,run.startedAt]);

  useEffect(()=>{ if(run.duration!=null)setElapsed(run.duration); },[run.duration]);

  const pulse = <span style={{marginLeft:5,animation:"gPulse 1.6s ease-in-out infinite"}}>·</span>;

  return (
    <div style={{width:220,flexShrink:0,background:T.bgSurface,border:`1px solid ${T.borderSubtle}`,borderRadius:8,padding:"18px 16px",position:"sticky",top:20,display:"flex",flexDirection:"column",gap:18}}>
      <div><div className="meta-label">Status</div><StatusBadge status={run.status}/></div>
      <div>
        <div className="meta-label">Policy</div>
        <div
          className="meta-value"
          onClick={onNavigateToPolicy}
          style={{fontSize:12,cursor:"pointer",color:T.blueLight,textDecoration:"none",transition:"opacity .15s"}}
          onMouseEnter={e=>e.currentTarget.style.opacity=".75"}
          onMouseLeave={e=>e.currentTarget.style.opacity="1"}
          title="Open policy editor"
        >
          {run.policyName}
          <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" style={{marginLeft:5,verticalAlign:"middle",opacity:.6}}>
            <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><polyline points="16 2 22 2 22 8"/><line x1="10" y1="14" x2="22" y2="2"/>
          </svg>
        </div>
        <div style={{marginTop:3,display:"flex",alignItems:"center",gap:5}}>
          <span style={{fontSize:10,color:T.textFaint}}>{run.folder}</span>
          <TriggerChip type={run.triggerType}/>
        </div>
      </div>
      <div><div className="meta-label">Run ID</div><div className="meta-value mono" style={{color:T.textSecond}}>{run.id}</div></div>
      <div><div className="meta-label">Started</div><div className="meta-value mono" style={{fontSize:11,color:T.textSecond}}>{fmtAbs(run.startedAt)}</div></div>
      <div>
        <div className="meta-label">Duration</div>
        <div className="meta-value mono" style={{color:isLive?T.blueLight:T.textPrimary}}>{fmtDur(elapsed)??"—"}{isLive&&pulse}</div>
      </div>
      <div>
        <div className="meta-label">Tokens</div>
        <div className="meta-value mono" style={{color:isLive?T.blueLight:T.textPrimary}}>{fmtTok(liveTokens)}{isLive&&pulse}</div>
      </div>
      <div>
        <div className="meta-label">Steps</div>
        <div className="meta-value mono" style={{color:isLive?T.blueLight:T.textPrimary}}>{stepCount}{isLive&&pulse}</div>
      </div>
      <div style={{height:1,background:T.borderSubtle}}/>
      <div style={{display:"flex",alignItems:"center",gap:6}}>
        {isLive
          ?<><span style={{width:6,height:6,borderRadius:"50%",background:T.green,flexShrink:0,animation:"gPulse 1.6s ease-in-out infinite"}}/><span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.green}}>live</span></>
          :<><span style={{width:6,height:6,borderRadius:"50%",background:T.textFaint,flexShrink:0}}/><span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint}}>archived</span></>
        }
      </div>
    </div>
  );
};

// ─── Top bar ───────────────────────────────────────────────────────────────────

const TopBar = ({onBack}) => (
  <div style={{height:48,background:T.bgTopbar,borderBottom:`1px solid ${T.borderSubtle}`,display:"flex",alignItems:"center",padding:"0 20px",gap:16,flexShrink:0,position:"sticky",top:0,zIndex:100}}>
    <button className="back-btn" onClick={onBack}>
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><polyline points="15 18 9 12 15 6"/></svg>
      Runs
    </button>
    <div style={{width:1,height:16,background:T.borderMid}}/>
    <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:12,color:T.textMuted}}>Reasoning Timeline</span>
    <span style={{fontFamily:"'IBM Plex Mono',monospace",fontSize:11,color:T.textFaint}}>{MOCK_RUN.policyName} / {MOCK_RUN.id}</span>
  </div>
);

// ─── Main ──────────────────────────────────────────────────────────────────────

export default function ReasoningTimeline() {
  const [steps,setSteps]           = useState([...INITIAL_STEPS].reverse());
  const [snapshot]                 = useState(CAPABILITY_SNAPSHOT);
  const [run,setRun]               = useState(MOCK_RUN);
  const [liveTokens,setLiveTokens] = useState(MOCK_RUN.tokenCost);
  const [streaming,setStreaming]   = useState(false);
  const [streamIdx,setStreamIdx]   = useState(0);
  // activeFilters: empty Set = show all. Non-empty = show only matching types.
  const [activeFilters,setActiveFilters] = useState(new Set());
  const [forceOpen,setForceOpen]   = useState(null);
  const streamTimer = useRef(null);
  const approvalRef = useRef(null);

  const hasPending = steps.some(s=>s.type==="approval_request"&&s.status==="pending");

  const jumpToApproval = () => {
    setActiveFilters(new Set());
    setTimeout(()=>approvalRef.current?.scrollIntoView({behavior:"smooth",block:"center"}),50);
  };

  const startStream = useCallback(()=>{
    if(streaming||streamIdx>=STREAMING_STEPS.length) return;
    setStreaming(true);
    setRun(r=>({...r,status:"running"}));
  },[streaming,streamIdx]);

  useEffect(()=>{
    if(!streaming) return;
    if(streamIdx>=STREAMING_STEPS.length){
      setStreaming(false);
      setRun(r=>({...r,status:"complete",duration:Math.floor((Date.now()-new Date(r.startedAt))/1000)}));
      return;
    }
    const delay=streamIdx===0?600:streamIdx===STREAMING_STEPS.length-1?1800:1200;
    streamTimer.current=setTimeout(()=>{
      const next=STREAMING_STEPS[streamIdx];
      setSteps(s=>[next,...s]);
      if(next.tokenCost>0) setLiveTokens(t=>t+next.tokenCost);
      setStreamIdx(i=>i+1);
    },delay);
    return()=>clearTimeout(streamTimer.current);
  },[streaming,streamIdx]);

  const handleDecision = decision => {
    setSteps(s=>s.map(step=>step.type==="approval_request"?{...step,status:decision==="approve"?"approved":"rejected"}:step));
    if(decision==="approve") setTimeout(startStream,400);
    else setRun(r=>({...r,status:"failed"}));
  };

  // Reset forceOpen so individual cards can toggle after
  useEffect(()=>{ if(forceOpen!==null) setTimeout(()=>setForceOpen(null),80); },[forceOpen]);

  const isLive = run.status==="running"||run.status==="waiting_for_approval";
  const visible = activeFilters.size===0 ? steps : steps.filter(s=>activeFilters.has(s.type));

  const toggleFilter = key => {
    setActiveFilters(prev => {
      const next = new Set(prev);
      if(next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  };

  return (
    <>
      <style>{STYLES}</style>
      <div style={{minHeight:"100vh",background:T.bgCanvas,display:"flex",flexDirection:"column"}}>
        <TopBar onBack={()=>{}}/>
        <div style={{flex:1,display:"flex",gap:24,padding:"20px 24px",maxWidth:1100,width:"100%",margin:"0 auto",alignItems:"flex-start"}}>

          <MetaPanel run={run} stepCount={steps.length} liveTokens={liveTokens}
            onNavigateToPolicy={()=>alert(`Navigate to policy editor: ${run.policyName}`)} />

          <div style={{flex:1,minWidth:0}}>

            {/* Header row */}
            <div style={{display:"flex",alignItems:"center",gap:10,marginBottom:12,flexWrap:"wrap"}}>
              <span style={{fontFamily:"'IBM Plex Sans',sans-serif",fontSize:13,fontWeight:600,color:T.textPrimary}}>Agent reasoning</span>
              <span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint}}>{steps.length} steps</span>
              {isLive&&(
                <div style={{display:"flex",alignItems:"center",gap:5}}>
                  <Spinner size={11}/>
                  <span style={{fontSize:10,fontFamily:"'IBM Plex Mono',monospace",color:T.blueLight}}>
                    {run.status==="waiting_for_approval"?"awaiting approval":"streaming"}
                  </span>
                </div>
              )}
              <div style={{marginLeft:"auto",display:"flex",alignItems:"center",gap:10}}>
                {hasPending&&(
                  <button className="jump-btn" onClick={jumpToApproval}>
                    <span style={{animation:"gPulse 1.4s ease-in-out infinite"}}>!</span>
                    Jump to approval
                  </button>
                )}
                <button className="action-link" onClick={()=>setForceOpen(true)}>expand all</button>
                <span style={{color:T.textFaint,fontSize:10}}>·</span>
                <button className="action-link" onClick={()=>setForceOpen(false)}>collapse all</button>
              </div>
            </div>

            {/* Filter chips */}
            <div style={{display:"flex",gap:6,marginBottom:12,flexWrap:"wrap"}}>
              {/* All chip — active when nothing selected, clears when clicked */}
              <button
                className={`filter-chip${activeFilters.size===0?" active":""}`}
                onClick={()=>setActiveFilters(new Set())}
              >All</button>
              {FILTER_TYPES.filter(f=>f.key!=="all").map(f=>{
                const count = steps.filter(s=>s.type===f.key).length;
                const active = activeFilters.has(f.key);
                return (
                  <button key={f.key} className={`filter-chip${active?" active":""}`} onClick={()=>toggleFilter(f.key)}>
                    {f.label}
                    <span style={{marginLeft:4,opacity:.55}}>{count}</span>
                  </button>
                );
              })}
            </div>

            {/* Live indicator */}
            {streaming&&(
              <div style={{display:"flex",alignItems:"center",gap:8,marginBottom:8,padding:"8px 14px",background:T.bgSurface,border:`1px solid ${T.borderSubtle}`,borderRadius:7}}>
                <Spinner size={12}/><span style={{fontSize:11,fontFamily:"'IBM Plex Mono',monospace",color:T.textMuted}}>agent working…</span>
              </div>
            )}

            {/* Cards */}
            {visible.map((step,i)=>(
              <StepCard key={step.id} step={step} isFirst={i===0} isLive={streaming&&i===0}
                onDecision={handleDecision} forceOpen={forceOpen} approvalRef={approvalRef}/>
            ))}

            {visible.length===0&&(
              <div style={{padding:"32px 0",textAlign:"center",fontSize:12,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint}}>
                no matching steps
              </div>
            )}

            {/* Capability snapshot — always at the bottom, outside filter */}
            {activeFilters.size===0&&(
              <>
                <div style={{display:"flex",alignItems:"center",gap:10,margin:"16px 0 8px"}}>
                  <div style={{flex:1,height:1,background:T.borderSubtle}}/>
                  <span style={{fontSize:9,fontFamily:"'IBM Plex Mono',monospace",color:T.textFaint,textTransform:"uppercase",letterSpacing:".08em",whiteSpace:"nowrap"}}>run start</span>
                  <div style={{flex:1,height:1,background:T.borderSubtle}}/>
                </div>
                <CapabilitySnapshotCard snapshot={snapshot}/>
              </>
            )}

            <div style={{height:32}}/>
          </div>
        </div>
      </div>
    </>
  );
}
