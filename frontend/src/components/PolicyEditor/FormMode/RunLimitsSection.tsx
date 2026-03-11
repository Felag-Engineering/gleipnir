import styles from './RunLimitsSection.module.css';
import type { RunLimitsFormState } from './types';

export interface RunLimitsSectionProps {
  value: RunLimitsFormState;
  onChange: (next: RunLimitsFormState) => void;
}

export function RunLimitsSection({ value, onChange }: RunLimitsSectionProps) {
  function handleTokensChange(e: React.ChangeEvent<HTMLInputElement>) {
    const parsed = parseInt(e.target.value, 10);
    if (isNaN(parsed) || parsed <= 0) return;
    onChange({ ...value, max_tokens_per_run: parsed });
  }

  function handleToolCallsChange(e: React.ChangeEvent<HTMLInputElement>) {
    const parsed = parseInt(e.target.value, 10);
    if (isNaN(parsed) || parsed <= 0) return;
    onChange({ ...value, max_tool_calls_per_run: parsed });
  }

  return (
    <div className={styles.section}>
      <div className={styles.heading}>Run Limits</div>

      <div className={styles.fieldRow}>
        <div className={styles.field}>
          <label className={styles.label}>Max tokens per run</label>
          <input
            className={styles.input}
            type="number"
            min="1"
            value={value.max_tokens_per_run}
            onChange={handleTokensChange}
          />
        </div>

        <div className={styles.field}>
          <label className={styles.label}>Max tool calls per run</label>
          <input
            className={styles.input}
            type="number"
            min="1"
            value={value.max_tool_calls_per_run}
            onChange={handleToolCallsChange}
          />
        </div>
      </div>
    </div>
  );
}
