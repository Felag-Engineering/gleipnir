import type { RunStatus } from '../types';
import { STATUS_CONFIG } from '../types';
import styles from './StatusBadge.module.css';

interface StatusBadgeProps {
  status: RunStatus;
}

const VARIANT: Record<RunStatus, string> = {
  complete:             styles.complete,
  running:              styles.running,
  waiting_for_approval: styles.waitingForApproval,
  failed:               styles.failed,
  interrupted:          styles.interrupted,
};

export function StatusBadge({ status }: StatusBadgeProps) {
  const config = STATUS_CONFIG[status];
  return (
    <span className={`${styles.badge} ${VARIANT[status]}`}>
      <span className={`${styles.dot}${config.pulse ? ` ${styles.pulse}` : ''}`} />
      {config.label}
    </span>
  );
}
