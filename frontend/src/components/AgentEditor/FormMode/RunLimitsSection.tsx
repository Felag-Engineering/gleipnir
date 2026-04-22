import shared from './FormSections.module.css';
import styles from './RunLimitsSection.module.css';
import type { RunLimitsFormState, SectionIssues } from './types';
import { FieldError } from '@/components/form/FieldError';

export interface RunLimitsSectionProps {
  value: RunLimitsFormState;
  onChange: (next: RunLimitsFormState) => void;
  errors?: SectionIssues;
}

export function RunLimitsSection({ value, onChange, errors = [] }: RunLimitsSectionProps) {
  const tokensErrors = errors.filter(e => e.field === 'agent.limits.max_tokens_per_run').map(e => e.message);
  const toolCallsErrors = errors.filter(e => e.field === 'agent.limits.max_tool_calls_per_run').map(e => e.message);

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
    <div className={shared.section}>
      <div className={shared.heading}>Run Limits</div>

      <div className={styles.fieldRow}>
        <div className={shared.field} data-field="agent.limits.max_tokens_per_run">
          <label className={shared.label}>Max tokens per run</label>
          <input
            className={shared.input}
            type="number"
            min="1"
            value={value.max_tokens_per_run}
            aria-invalid={tokensErrors.length > 0 || undefined}
            onChange={handleTokensChange}
          />
          <FieldError messages={tokensErrors} />
        </div>

        <div className={shared.field} data-field="agent.limits.max_tool_calls_per_run">
          <label className={shared.label}>Max tool calls per run</label>
          <input
            className={shared.input}
            type="number"
            min="1"
            value={value.max_tool_calls_per_run}
            aria-invalid={toolCallsErrors.length > 0 || undefined}
            onChange={handleToolCallsChange}
          />
          <FieldError messages={toolCallsErrors} />
        </div>
      </div>
    </div>
  );
}
