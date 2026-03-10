import { useState } from 'react';
import type { ApprovalDef } from './types';
import { TriggerChip } from './TriggerChip';
import { ReasoningTrace } from './ReasoningTrace';
import { FONT, fmtAbs, timeLeft } from './styles';

interface ApprovalCardProps {
  def: ApprovalDef;
  onDecide: (approvalId: string, decision: 'approve' | 'reject', note: string) => void;
}

export function ApprovalCard({ def, onDecide }: ApprovalCardProps) {
  const [expanded, setExpanded] = useState(false);
  const [deciding, setDeciding] = useState<'approve' | 'reject' | null>(null);
  const [note, setNote] = useState('');
  const tl = timeLeft(def.expiresAt);

  const confirm = (decision: 'approve' | 'reject') => {
    setDeciding(null);
    onDecide(def.id, decision, note);
  };

  return (
    <div style={{
      background: '#0f1219',
      border: '1px solid rgba(245,158,11,0.28)',
      borderRadius: 10, overflow: 'hidden',
      boxShadow: '0 0 0 1px rgba(245,158,11,0.07), 0 4px 24px rgba(0,0,0,0.4)',
    }}>
      {/* Accent bar */}
      <div style={{ height: 3, background: 'linear-gradient(90deg,#f59e0b,#fb923c)' }} />

      {/* Header */}
      <div style={{ padding: '14px 18px 12px', display: 'flex', alignItems: 'flex-start', gap: 12 }}>
        <div style={{
          width: 36, height: 36, borderRadius: 8, flexShrink: 0, marginTop: 1,
          background: 'rgba(245,158,11,0.1)', border: '1px solid rgba(245,158,11,0.25)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
        }}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#f59e0b" strokeWidth="2">
            <circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
          </svg>
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4, flexWrap: 'wrap' }}>
            <span style={{ fontSize: 13, fontWeight: 600, color: '#e2e8f0' }}>{def.policyName}</span>
            <span style={{ fontSize: 10, color: '#475569' }}>{def.folder}</span>
            <TriggerChip type="poll" />
          </div>
          <p style={{ fontSize: 13, color: '#94a3b8', lineHeight: 1.55, margin: 0 }}>{def.agentSummary}</p>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 6, flexShrink: 0 }}>
          {/* Countdown timer */}
          <div style={{
            display: 'flex', alignItems: 'center', gap: 5, padding: '3px 9px', borderRadius: 5,
            background: tl.urgent ? 'rgba(248,113,113,0.1)' : 'rgba(245,158,11,0.08)',
            border: `1px solid ${tl.urgent ? 'rgba(248,113,113,0.3)' : 'rgba(245,158,11,0.2)'}`,
          }}>
            <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke={tl.urgent ? '#f87171' : '#f59e0b'} strokeWidth="2">
              <circle cx="12" cy="12" r="10" /><polyline points="12 6 12 12 16 14" />
            </svg>
            <span style={{ fontFamily: FONT.mono, fontSize: 11, color: tl.urgent ? '#f87171' : '#f59e0b' }}>{tl.str}</span>
          </div>
          <button
            onClick={() => setExpanded(e => !e)}
            style={{
              display: 'flex', alignItems: 'center', gap: 4,
              background: 'transparent', border: 'none', cursor: 'pointer',
              color: expanded ? '#60a5fa' : '#334155', fontSize: 11,
              fontFamily: FONT.sans, padding: 0, transition: 'color 0.15s',
            }}
          >
            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z" /><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z" />
            </svg>
            {expanded ? 'Hide reasoning' : 'Show reasoning'}
          </button>
        </div>
      </div>

      {/* Proposed action */}
      <div style={{
        margin: '0 18px 14px', padding: '10px 14px',
        background: 'rgba(0,0,0,0.3)', borderRadius: 8,
        border: '1px solid rgba(255,255,255,0.06)',
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
          <span style={{ fontSize: 10, color: '#475569', textTransform: 'uppercase', letterSpacing: '0.08em' }}>Proposed action</span>
          <span style={{
            fontFamily: FONT.mono, fontSize: 12, color: '#fb923c',
            background: 'rgba(251,146,60,0.1)', border: '1px solid rgba(251,146,60,0.25)',
            padding: '1px 8px', borderRadius: 4,
          }}>
            {def.toolName}
          </span>
          <span style={{ fontSize: 10, color: '#2d3748', marginLeft: 'auto' }}>actuator · approval required</span>
        </div>
        <pre style={{
          fontFamily: FONT.mono, fontSize: 11, color: '#64748b', margin: 0,
          whiteSpace: 'pre-wrap', wordBreak: 'break-all', lineHeight: 1.65,
        }}>
          {JSON.stringify(def.proposedInput, null, 2)}
        </pre>
      </div>

      {/* Reasoning trace (expandable) */}
      {expanded && (
        <div style={{
          margin: '0 18px 14px', padding: '12px 14px',
          background: 'rgba(0,0,0,0.25)', borderRadius: 8,
          border: '1px solid rgba(255,255,255,0.05)',
        }}>
          <div style={{ fontSize: 10, color: '#334155', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 12 }}>
            Agent reasoning
          </div>
          <ReasoningTrace steps={def.reasoning} />
        </div>
      )}

      {/* Confirm note area */}
      {deciding && (
        <div style={{
          margin: '0 18px 14px', padding: '12px 14px',
          background: deciding === 'approve' ? 'rgba(74,222,128,0.05)' : 'rgba(248,113,113,0.05)',
          borderRadius: 8,
          border: `1px solid ${deciding === 'approve' ? 'rgba(74,222,128,0.2)' : 'rgba(248,113,113,0.2)'}`,
        }}>
          <div style={{ fontSize: 11, color: '#64748b', marginBottom: 8 }}>
            {deciding === 'approve' ? 'Optional note before approving:' : 'Optional note before rejecting:'}
          </div>
          <textarea
            value={note} onChange={e => setNote(e.target.value)}
            placeholder={deciding === 'approve' ? 'e.g. Confirmed — all criteria verified.' : 'e.g. Not ready — still waiting on sign-off.'}
            rows={2}
            style={{
              width: '100%', padding: '8px 10px', borderRadius: 6,
              background: 'rgba(0,0,0,0.3)', border: '1px solid rgba(255,255,255,0.08)',
              color: '#cbd5e1', fontSize: 12, fontFamily: FONT.sans,
              lineHeight: 1.5, resize: 'none', marginBottom: 10,
            }}
          />
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
            <button onClick={() => setDeciding(null)} style={{
              padding: '6px 14px', borderRadius: 5, background: 'transparent',
              border: '1px solid #1e2330', color: '#475569', fontSize: 12, cursor: 'pointer',
            }}>Cancel</button>
            <button onClick={() => confirm(deciding)} style={{
              padding: '6px 14px', borderRadius: 5, cursor: 'pointer', fontSize: 12, fontWeight: 500,
              background: deciding === 'approve' ? 'rgba(74,222,128,0.15)' : 'rgba(248,113,113,0.15)',
              border: `1px solid ${deciding === 'approve' ? 'rgba(74,222,128,0.4)' : 'rgba(248,113,113,0.4)'}`,
              color: deciding === 'approve' ? '#4ade80' : '#f87171',
            }}>
              Confirm {deciding === 'approve' ? 'Approve' : 'Reject'}
            </button>
          </div>
        </div>
      )}

      {/* Action row */}
      {!deciding && (
        <div style={{
          padding: '12px 18px 14px', display: 'flex', alignItems: 'center', gap: 10,
          borderTop: '1px solid rgba(255,255,255,0.05)',
        }}>
          <div style={{ fontSize: 11, color: '#334155', flex: 1 }}>
            Started {fmtAbs(def.startedAt)} · run paused, waiting for your decision
          </div>
          <button onClick={() => setDeciding('reject')} className="btn-reject" style={{
            padding: '7px 18px', borderRadius: 6, cursor: 'pointer', fontSize: 13, fontWeight: 500,
            background: 'rgba(248,113,113,0.08)', border: '1px solid rgba(248,113,113,0.28)',
            color: '#f87171', transition: 'all 0.15s',
          }}>Reject</button>
          <button onClick={() => setDeciding('approve')} className="btn-approve" style={{
            padding: '7px 22px', borderRadius: 6, cursor: 'pointer', fontSize: 13, fontWeight: 600,
            background: 'rgba(74,222,128,0.12)', border: '1px solid rgba(74,222,128,0.35)',
            color: '#4ade80', transition: 'all 0.15s',
          }}>Approve</button>
        </div>
      )}
    </div>
  );
}
