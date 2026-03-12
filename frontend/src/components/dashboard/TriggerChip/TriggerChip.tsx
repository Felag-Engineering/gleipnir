import type { TriggerType } from '../types';
import styles from './TriggerChip.module.css';

interface TriggerChipProps {
  type: TriggerType;
  pausedAt?: string | null;
}

export function TriggerChip({ type, pausedAt }: TriggerChipProps) {
  const isPaused = type === 'scheduled' && pausedAt != null;
  const label = isPaused ? `${type} (paused)` : type;
  const className = [styles.chip, styles[type] || '', isPaused ? styles.paused : '']
    .filter(Boolean)
    .join(' ');
  return (
    <span className={className}>{label}</span>
  );
}
