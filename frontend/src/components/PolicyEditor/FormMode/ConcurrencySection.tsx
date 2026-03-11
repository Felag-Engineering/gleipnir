import styles from './ConcurrencySection.module.css';
import type { ConcurrencyFormState, ConcurrencyValue } from './types';

export interface ConcurrencySectionProps {
  value: ConcurrencyFormState;
  onChange: (next: ConcurrencyFormState) => void;
}

const CONCURRENCY_OPTIONS: { value: ConcurrencyValue; label: string; desc: string }[] = [
  { value: 'skip',     label: 'Skip',     desc: 'Discard the new trigger' },
  { value: 'queue',    label: 'Queue',    desc: 'Run after current finishes' },
  { value: 'parallel', label: 'Parallel', desc: 'Run concurrently' },
  { value: 'replace',  label: 'Replace',  desc: 'Cancel current, start fresh' },
];

export function ConcurrencySection({ value, onChange }: ConcurrencySectionProps) {
  return (
    <div className={styles.section}>
      <div className={styles.heading}>Concurrency</div>

      <div className={styles.cards}>
        {CONCURRENCY_OPTIONS.map((option) => {
          const isActive = value.concurrency === option.value;
          return (
            <button
              key={option.value}
              className={isActive ? `${styles.card} ${styles.cardActive}` : styles.card}
              onClick={() => onChange({ concurrency: option.value })}
            >
              <div className={isActive ? `${styles.cardTitle} ${styles.cardTitleActive}` : styles.cardTitle}>
                {option.label}
              </div>
              <div className={styles.cardDesc}>{option.desc}</div>
            </button>
          );
        })}
      </div>
    </div>
  );
}
