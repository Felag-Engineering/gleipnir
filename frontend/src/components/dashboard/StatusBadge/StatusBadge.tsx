import type { RunStatus } from '@/components/dashboard/types';
import { STATUS_CONFIG } from '@/components/dashboard/types';
import styles from './StatusBadge.module.css';

interface StatusBadgeProps {
  status: RunStatus;
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

export function StatusBadge({ status }: StatusBadgeProps) {
  return (
    <span className={`${styles.badge} ${VARIANT[status]}`}>
      {STATUS_CONFIG[status].label}
    </span>
  );
}
