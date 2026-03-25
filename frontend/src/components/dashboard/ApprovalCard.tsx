import { useState } from 'react';
import type { ApprovalDef } from './types';
import { TriggerChip } from './TriggerChip';
import { ReasoningTrace } from './ReasoningTrace';
import { fmtAbs, timeLeft } from './styles';
import styles from './ApprovalCard.module.css';

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
    <div className={styles.card}>
      {/* Accent bar */}
      <div className={styles.accentBar} />

      {/* Header */}
      <div className={styles.header}>
        <div className={styles.alertIcon}>
          <svg role="img" aria-label="Approval required" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="var(--color-amber)" strokeWidth="2">
            <circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
          </svg>
        </div>
        <div className={styles.headerBody}>
          <div className={styles.headerTitle}>
            <span className={styles.policyName}>{def.policyName}</span>
            <span className={styles.folderName}>{def.folder}</span>
            <TriggerChip type="webhook" />
          </div>
          <p className={styles.agentSummary}>{def.agentSummary}</p>
        </div>
        <div className={styles.timerColumn}>
          {/* Countdown timer */}
          <div className={`${styles.timerChip} ${tl.urgent ? styles.timerChipUrgent : styles.timerChipNormal}`}>
            <svg role="img" aria-label="Time remaining" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke={tl.urgent ? 'var(--color-red)' : 'var(--color-amber)'} strokeWidth="2">
              <circle cx="12" cy="12" r="10" /><polyline points="12 6 12 12 16 14" />
            </svg>
            <span className={`${styles.timerText} ${tl.urgent ? styles.timerTextUrgent : styles.timerTextNormal}`}>{tl.str}</span>
          </div>
          <button
            onClick={() => setExpanded(e => !e)}
            className={`${styles.reasoningToggle} ${expanded ? styles.reasoningToggleExpanded : ''}`}
          >
            <svg role="img" aria-label="Reasoning trace" width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z" /><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z" />
            </svg>
            {expanded ? 'Hide reasoning' : 'Show reasoning'}
          </button>
        </div>
      </div>

      {/* Proposed action */}
      <div className={styles.proposedAction}>
        <div className={styles.proposedActionHeader}>
          <span className={styles.sectionLabel}>Proposed action</span>
          <span className={styles.toolNameBadge}>{def.toolName}</span>
          <span className={styles.toolLabel}>tool · approval required</span>
        </div>
        <pre className={styles.proposedJson}>
          {JSON.stringify(def.proposedInput, null, 2)}
        </pre>
      </div>

      {/* Reasoning trace (expandable) */}
      {expanded && (
        <div className={styles.reasoningPanel}>
          <div className={styles.reasoningLabel}>Agent reasoning</div>
          <ReasoningTrace steps={def.reasoning} />
        </div>
      )}

      {/* Confirm note area */}
      {deciding && (
        <div className={`${styles.notePanel} ${deciding === 'approve' ? styles.notePanelApprove : styles.notePanelReject}`}>
          <div className={styles.noteLabel}>
            {deciding === 'approve' ? 'Optional note before approving:' : 'Optional note before rejecting:'}
          </div>
          <textarea
            value={note} onChange={e => setNote(e.target.value)}
            placeholder={deciding === 'approve' ? 'e.g. Confirmed — all criteria verified.' : 'e.g. Not ready — still waiting on sign-off.'}
            rows={2}
            className={styles.noteTextarea}
          />
          <div className={styles.noteActions}>
            <button onClick={() => setDeciding(null)} className={styles.cancelBtn}>Cancel</button>
            <button
              onClick={() => confirm(deciding)}
              className={`${styles.confirmBtn} ${deciding === 'approve' ? styles.confirmBtnApprove : styles.confirmBtnReject}`}
            >
              Confirm {deciding === 'approve' ? 'Approve' : 'Reject'}
            </button>
          </div>
        </div>
      )}

      {/* Action row */}
      {!deciding && (
        <div className={styles.actionRow}>
          <div className={styles.actionInfo}>
            Started {fmtAbs(def.startedAt)} · run paused, waiting for your decision
          </div>
          <button onClick={() => setDeciding('reject')} className={styles.rejectBtn}>Reject</button>
          <button onClick={() => setDeciding('approve')} className={styles.approveBtn}>Approve</button>
        </div>
      )}
    </div>
  );
}
