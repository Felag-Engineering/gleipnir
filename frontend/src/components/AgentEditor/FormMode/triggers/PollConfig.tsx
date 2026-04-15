import shared from '../FormSections.module.css';
import styles from '../TriggerSection.module.css';
import type { PollCheckState, PollTriggerState, TriggerFormState } from '../types';

export interface PollConfigProps {
  value: PollTriggerState;
  onChange: (next: TriggerFormState) => void;
}

const COMPARATOR_LABELS: Record<PollCheckState['comparator'], string> = {
  equals: 'equals',
  not_equals: 'not equals',
  greater_than: 'greater than',
  less_than: 'less than',
  contains: 'contains',
};

export function PollConfig({ value, onChange }: PollConfigProps) {
  function updateInterval(interval: string) {
    onChange({ ...value, interval });
  }

  function updateMatch(match: 'all' | 'any') {
    onChange({ ...value, match });
  }

  function updateCheck(index: number, updated: PollCheckState) {
    const next = value.checks.slice();
    next[index] = updated;
    onChange({ ...value, checks: next });
  }

  function addCheck() {
    onChange({
      ...value,
      checks: [...value.checks, { tool: '', input: '', path: '', comparator: 'equals', value: '' }],
    });
  }

  function removeCheck(index: number) {
    onChange({ ...value, checks: value.checks.filter((_, i) => i !== index) });
  }

  return (
    <div className={styles.pollConfig}>
      <div className={shared.field}>
        <label className={shared.label}>Interval</label>
        <input
          className={`${shared.input} ${shared.inputMono}`}
          type="text"
          value={value.interval}
          placeholder="5m"
          onChange={(e) => updateInterval(e.target.value)}
        />
      </div>

      <div className={shared.field}>
        <label className={shared.label}>Match mode</label>
        <div className={styles.matchRow}>
          <button
            className={value.match === 'all' ? `${styles.copyButton} ${styles.matchButtonActive}` : styles.copyButton}
            type="button"
            onClick={() => updateMatch('all')}
          >
            All checks (AND)
          </button>
          <button
            className={value.match === 'any' ? `${styles.copyButton} ${styles.matchButtonActive}` : styles.copyButton}
            type="button"
            onClick={() => updateMatch('any')}
          >
            Any check (OR)
          </button>
        </div>
      </div>

      <div className={styles.checksContainer}>
        {value.checks.map((check, i) => (
          <div key={i} className={styles.checkGroup}>
            <div className={styles.checkHeader}>
              <span className={styles.checkNumber}>Check {i + 1}</span>
              {value.checks.length > 1 && (
                <button
                  className={styles.copyButton}
                  type="button"
                  onClick={() => removeCheck(i)}
                >
                  Remove
                </button>
              )}
            </div>

            <div className={shared.field}>
              <label className={shared.label}>Tool</label>
              <input
                className={`${shared.input} ${shared.inputMono}`}
                type="text"
                value={check.tool}
                placeholder="server.tool_name"
                onChange={(e) => updateCheck(i, { ...check, tool: e.target.value })}
              />
            </div>

            <div className={shared.field}>
              <label className={shared.label}>Input (JSON, optional)</label>
              <textarea
                className={styles.textarea}
                value={check.input}
                placeholder={'{"key": "value"}'}
                onChange={(e) => updateCheck(i, { ...check, input: e.target.value })}
              />
            </div>

            <div className={shared.field}>
              <label className={shared.label}>JSONPath</label>
              <input
                className={`${shared.input} ${shared.inputMono}`}
                type="text"
                value={check.path}
                placeholder="$.field.path"
                onChange={(e) => updateCheck(i, { ...check, path: e.target.value })}
              />
            </div>

            <div className={styles.fieldRow}>
              <div className={shared.field}>
                <label className={shared.label}>Comparator</label>
                <select
                  className={styles.select}
                  value={check.comparator}
                  onChange={(e) => updateCheck(i, { ...check, comparator: e.target.value as PollCheckState['comparator'] })}
                >
                  {(Object.keys(COMPARATOR_LABELS) as PollCheckState['comparator'][]).map((comp) => (
                    <option key={comp} value={comp}>{COMPARATOR_LABELS[comp]}</option>
                  ))}
                </select>
              </div>

              <div className={shared.field}>
                <label className={shared.label}>Value</label>
                <input
                  className={`${shared.input} ${shared.inputMono}`}
                  type="text"
                  value={check.value}
                  placeholder="expected value"
                  onChange={(e) => updateCheck(i, { ...check, value: e.target.value })}
                />
              </div>
            </div>
          </div>
        ))}
      </div>

      <button
        className={styles.copyButton}
        type="button"
        onClick={addCheck}
      >
        + Add check
      </button>
    </div>
  );
}
