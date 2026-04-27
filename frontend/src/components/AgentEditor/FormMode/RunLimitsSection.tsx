import { useEffect, useState } from 'react';
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

  // Local string state lets the user clear the field while editing without
  // immediately coercing a blank entry into the default value. The parent
  // always receives a number — empty string commits as 0 (unlimited).
  const [tokensText, setTokensText] = useState<string>(String(value.max_tokens_per_run));
  const [toolCallsText, setToolCallsText] = useState<string>(String(value.max_tool_calls_per_run));

  // Resync when the parent updates the value for a reason other than our own
  // onChange (e.g. form reset or YAML round-trip). We intentionally omit the
  // local text strings from deps so an in-flight empty string is not clobbered.
  useEffect(() => {
    const localNum = tokensText === '' ? 0 : parseInt(tokensText, 10);
    if (localNum !== value.max_tokens_per_run) {
      setTokensText(String(value.max_tokens_per_run));
    }
  }, [value.max_tokens_per_run]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const localNum = toolCallsText === '' ? 0 : parseInt(toolCallsText, 10);
    if (localNum !== value.max_tool_calls_per_run) {
      setToolCallsText(String(value.max_tool_calls_per_run));
    }
  }, [value.max_tool_calls_per_run]); // eslint-disable-line react-hooks/exhaustive-deps

  function handleTokensChange(e: React.ChangeEvent<HTMLInputElement>) {
    const stripped = e.target.value.replace(/[^0-9]/g, '');
    setTokensText(stripped);
    const num = stripped === '' ? 0 : parseInt(stripped, 10);
    onChange({ ...value, max_tokens_per_run: num });
  }

  function handleToolCallsChange(e: React.ChangeEvent<HTMLInputElement>) {
    const stripped = e.target.value.replace(/[^0-9]/g, '');
    setToolCallsText(stripped);
    const num = stripped === '' ? 0 : parseInt(stripped, 10);
    onChange({ ...value, max_tool_calls_per_run: num });
  }

  function handleTokensBlur() {
    if (tokensText === '') {
      setTokensText('0');
    }
  }

  function handleToolCallsBlur() {
    if (toolCallsText === '') {
      setToolCallsText('0');
    }
  }

  return (
    <div className={shared.section}>
      <div className={shared.heading}>Run Limits</div>

      <div className={styles.fieldRow}>
        <div className={shared.field} data-field="agent.limits.max_tokens_per_run">
          <label className={shared.label}>Max tokens per run (0 = unlimited)</label>
          <input
            className={shared.input}
            type="text"
            inputMode="numeric"
            pattern="[0-9]*"
            value={tokensText}
            aria-invalid={tokensErrors.length > 0 || undefined}
            onChange={handleTokensChange}
            onBlur={handleTokensBlur}
          />
          <div className={styles.hint}>{value.max_tokens_per_run === 0 ? 'Unlimited' : ''}</div>
          <FieldError messages={tokensErrors} />
        </div>

        <div className={shared.field} data-field="agent.limits.max_tool_calls_per_run">
          <label className={shared.label}>Max tool calls per run (0 = unlimited)</label>
          <input
            className={shared.input}
            type="text"
            inputMode="numeric"
            pattern="[0-9]*"
            value={toolCallsText}
            aria-invalid={toolCallsErrors.length > 0 || undefined}
            onChange={handleToolCallsChange}
            onBlur={handleToolCallsBlur}
          />
          <div className={styles.hint}>{value.max_tool_calls_per_run === 0 ? 'Unlimited' : ''}</div>
          <FieldError messages={toolCallsErrors} />
        </div>
      </div>
    </div>
  );
}
