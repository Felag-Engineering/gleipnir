import type { RunStatus } from '@/components/dashboard/types';
import { runStatusLabel } from '@/components/dashboard/types';
import styles from './StatusBadge.module.css';

interface StatusBadgeProps {
  status: RunStatus;
  // True when the run is waiting_for_feedback on a tool-initiated permission
  // ask: the badge then reads, and looks like, an approval (see runStatusLabel).
  awaitingPermission?: boolean;
}

const VARIANT: Record<RunStatus, string> = {
  complete:             styles.complete,
  running:              styles.running,
  waiting_for_approval: styles.waitingForApproval,
  waiting_for_feedback: styles.waitingForFeedback,
  failed:               styles.failed,
  interrupted:          styles.interrupted,
  pending:              styles.pending,
};

export function StatusBadge({ status, awaitingPermission = false }: StatusBadgeProps) {
  const variant = status === 'waiting_for_feedback' && awaitingPermission
    ? styles.waitingForApproval
    : VARIANT[status];
  return (
    <span className={`${styles.badge} ${variant}`}>
      {runStatusLabel(status, awaitingPermission)}
    </span>
  );
}
