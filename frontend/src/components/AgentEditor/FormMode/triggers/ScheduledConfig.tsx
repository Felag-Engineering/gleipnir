import shared from '../FormSections.module.css';
import styles from '../TriggerSection.module.css';
import type { ScheduledTriggerState, TriggerFormState, SectionIssues } from '../types';
import { FieldError } from '@/components/form/FieldError';

export interface ScheduledConfigProps {
  value: ScheduledTriggerState;
  onChange: (next: TriggerFormState) => void;
  errors?: SectionIssues;
}

export function ScheduledConfig({ value, onChange, errors = [] }: ScheduledConfigProps) {
  const fireAtErrors = errors.filter(e => e.field === 'trigger.fire_at').map(e => e.message);
  function addEntry() {
    // Default to one hour from now, formatted as a datetime-local value (no seconds).
    const soon = new Date(Date.now() + 60 * 60 * 1000);
    const isoLocal = soon.toISOString().slice(0, 16); // "YYYY-MM-DDTHH:MM"
    onChange({ ...value, fireAt: [...value.fireAt, isoLocal + ':00Z'] });
  }

  function updateEntry(index: number, raw: string) {
    // datetime-local input gives "YYYY-MM-DDTHH:MM" — append UTC offset so it
    // round-trips as a valid RFC3339 timestamp.
    const asRFC3339 = raw.length === 16 ? raw + ':00Z' : raw;
    const next = value.fireAt.slice();
    next[index] = asRFC3339;
    onChange({ ...value, fireAt: next });
  }

  function removeEntry(index: number) {
    onChange({ ...value, fireAt: value.fireAt.filter((_, i) => i !== index) });
  }

  // Convert a stored RFC3339 value to the "YYYY-MM-DDTHH:MM" format expected
  // by datetime-local inputs. Falls back to the raw string on parse failure.
  function toInputValue(ts: string): string {
    try {
      const d = new Date(ts);
      if (isNaN(d.getTime())) return ts;
      return d.toISOString().slice(0, 16);
    } catch {
      return ts;
    }
  }

  return (
    <div className={shared.field} data-field="trigger.fire_at">
      <label className={shared.label}>Fire at (UTC)</label>
      {value.fireAt.map((ts, i) => (
        <div key={i} className={styles.fieldRow}>
          <input
            className={shared.input}
            type="datetime-local"
            value={toInputValue(ts)}
            onChange={(e) => updateEntry(i, e.target.value)}
          />
          <button
            className={styles.copyButton}
            type="button"
            onClick={() => removeEntry(i)}
          >
            Remove
          </button>
        </div>
      ))}
      <button
        className={styles.copyButton}
        type="button"
        onClick={addEntry}
      >
        + Add time
      </button>
      <FieldError messages={fireAtErrors} />
    </div>
  );
}
