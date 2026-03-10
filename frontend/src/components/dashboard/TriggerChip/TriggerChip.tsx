import type { TriggerType } from '../types';
import styles from './TriggerChip.module.css';

interface TriggerChipProps {
  type: TriggerType;
}

export function TriggerChip({ type }: TriggerChipProps) {
  return (
    <span className={`${styles.chip} ${styles[type] || ''}`}>{type}</span>
  );
}
