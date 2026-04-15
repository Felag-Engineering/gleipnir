import { Check } from 'lucide-react';
import styles from '../TriggerSection.module.css';
import type { TriggerFormState } from '../types';

export interface TriggerCardProps {
  type: TriggerFormState['type'];
  selected: TriggerFormState['type'];
  onSelect: (type: TriggerFormState['type']) => void;
  title: string;
  desc: string;
}

export function TriggerCard({ type, selected, onSelect, title, desc }: TriggerCardProps) {
  const active = type === selected;
  return (
    <button
      className={active ? `${styles.card} ${styles.cardActive}` : styles.card}
      onClick={() => onSelect(type)}
    >
      {active && (
        <span className={styles.checkmark} aria-hidden="true">
          <Check size={10} color="var(--bg-surface)" strokeWidth={2.5} aria-hidden="true" />
        </span>
      )}
      <div className={active ? `${styles.cardTitle} ${styles.cardTitleActive}` : styles.cardTitle}>
        {title}
      </div>
      <div className={styles.cardDesc}>{desc}</div>
    </button>
  );
}
