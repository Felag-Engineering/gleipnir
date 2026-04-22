import { Check } from 'lucide-react';
import shared from './FormSections.module.css';
import styles from './ConcurrencySection.module.css';
import type { ConcurrencyFormState, ConcurrencyValue, SectionIssues } from './types';
import { FieldError } from '@/components/form/FieldError';

export interface ConcurrencySectionProps {
  value: ConcurrencyFormState;
  onChange: (next: ConcurrencyFormState) => void;
  errors?: SectionIssues;
}

const CONCURRENCY_OPTIONS: { value: ConcurrencyValue; label: string; desc: string }[] = [
  { value: 'skip',     label: 'Skip',     desc: 'Discard the new trigger' },
  { value: 'queue',    label: 'Queue',    desc: 'Run after current finishes' },
  { value: 'parallel', label: 'Parallel', desc: 'Run concurrently' },
  { value: 'replace',  label: 'Replace',  desc: 'Cancel current, start fresh' },
];

export function ConcurrencySection({ value, onChange, errors = [] }: ConcurrencySectionProps) {
  const concurrencyErrors = errors.filter(e => e.field === 'agent.concurrency').map(e => e.message);
  const queueDepthErrors = errors.filter(e => e.field === 'agent.queue_depth').map(e => e.message);

  function handleQueueDepthChange(e: React.ChangeEvent<HTMLInputElement>) {
    // Clamp to non-negative integers. Backend rejects negatives (validator.go:200)
    // and 0 means "use default" (model.DefaultQueueDepth).
    const n = Math.max(0, parseInt(e.target.value) || 0);
    onChange({ ...value, concurrency: 'queue', queueDepth: n });
  }

  return (
    <div className={shared.section}>
      <div className={shared.heading}>Concurrency</div>

      <div className={styles.cards} data-field="agent.concurrency">
        {CONCURRENCY_OPTIONS.map((option) => {
          const isActive = value.concurrency === option.value;
          return (
            <button
              key={option.value}
              className={isActive ? `${styles.card} ${styles.cardActive}` : styles.card}
              // Spread value to preserve queueDepth when switching between modes
              onClick={() => onChange({ ...value, concurrency: option.value })}
            >
              {isActive && (
                <span className={styles.checkmark} aria-hidden="true">
                  <Check size={10} color="var(--bg-surface)" strokeWidth={2.5} aria-hidden="true" />
                </span>
              )}
              <div className={isActive ? `${styles.cardTitle} ${styles.cardTitleActive}` : styles.cardTitle}>
                {option.label}
              </div>
              <div className={styles.cardDesc}>{option.desc}</div>
            </button>
          );
        })}
      </div>
      <FieldError messages={concurrencyErrors} />

      {value.concurrency === 'queue' && (
        <div className={styles.queueDepthRow} data-field="agent.queue_depth">
          <label className={styles.queueDepthLabel} htmlFor="queue-depth-input">
            Queue depth
          </label>
          <input
            id="queue-depth-input"
            className={styles.queueDepthInput}
            type="number"
            min={0}
            placeholder="10"
            value={value.queueDepth || ''}
            onChange={handleQueueDepthChange}
          />
          <FieldError messages={queueDepthErrors} />
        </div>
      )}
    </div>
  );
}
