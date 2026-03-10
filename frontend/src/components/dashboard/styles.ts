// Shared inline styles for dashboard mockup components.
// These will migrate to CSS Modules during v0.1 implementation.
// For now they faithfully reproduce the mockup's visual design.

export const FONT = {
  mono: 'IBM Plex Mono, monospace',
  sans: 'IBM Plex Sans, system-ui, sans-serif',
};

export const TRIGGER_COLORS: Record<string, string> = {
  webhook: '#60a5fa',
  cron: '#a78bfa',
  poll: '#34d399',
};

export const GLOBAL_STYLES = `
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
`;

// Format helpers
export const fmtDur = (s: number | null) => s == null ? '—' : s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
export const fmtTok = (n: number) => n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n);
export const fmtAbs = (iso: string) => new Date(iso).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });
export const fmtRel = (iso: string) => {
  const m = Math.floor((Date.now() - new Date(iso).getTime()) / 60000);
  if (m < 1) return 'just now';
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  return h < 24 ? `${h}h ago` : new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
};
export const timeLeft = (expiresAt: string) => {
  const secs = Math.max(0, Math.floor((new Date(expiresAt).getTime() - Date.now()) / 1000));
  const m = Math.floor(secs / 60), s = secs % 60;
  return { str: `${m}:${String(s).padStart(2, '0')}`, urgent: secs < 300 };
};
