import { useEffect, useState } from 'react';
import { Check } from 'lucide-react';
import styles from './TriggerSection.module.css';
import type { TriggerFormState, ManualTriggerState, ScheduledTriggerState, PollTriggerState, PollCheckState } from './types';

export interface TriggerSectionProps {
  value: TriggerFormState;
  onChange: (next: TriggerFormState) => void;
  policyId?: string;
}

const DEFAULT_MANUAL: ManualTriggerState = { type: 'manual' };
const DEFAULT_SCHEDULED: ScheduledTriggerState = { type: 'scheduled', fireAt: [] };
const DEFAULT_POLL: PollTriggerState = {
  type: 'poll',
  interval: '5m',
  match: 'all',
  checks: [{ tool: '', input: '', path: '', comparator: 'equals', value: '' }],
};

export function TriggerSection({ value, onChange, policyId }: TriggerSectionProps) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  function handleTypeSelect(type: TriggerFormState['type']) {
    if (type === 'webhook') onChange({ type: 'webhook' });
    else if (type === 'manual') onChange(DEFAULT_MANUAL);
    else if (type === 'poll') onChange(DEFAULT_POLL);
    else onChange(DEFAULT_SCHEDULED);
  }

  async function handleCopy() {
    if (!policyId) return;
    const url = `/api/v1/webhooks/${policyId}`;
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
    } catch {
      // clipboard API unavailable or permission denied — silently fail
    }
  }

  return (
    <div className={styles.section}>
      <div className={styles.heading}>Trigger</div>

      <div className={styles.cards}>
        <TriggerCard type="webhook" selected={value.type} onSelect={handleTypeSelect}
          title="Webhook" desc="Triggered by an incoming HTTP request" />
        <TriggerCard type="manual" selected={value.type} onSelect={handleTypeSelect}
          title="Manual" desc="Triggered on demand from the dashboard" />
        <TriggerCard type="scheduled" selected={value.type} onSelect={handleTypeSelect}
          title="Scheduled" desc="Fires once at each specified date and time, then pauses" />
        <TriggerCard type="poll" selected={value.type} onSelect={handleTypeSelect}
          title="Poll" desc="Periodically calls MCP tools and fires when conditions match" />
      </div>

      <div className={styles.config}>
        {value.type === 'webhook' && (
          <WebhookConfig policyId={policyId} copied={copied} onCopy={handleCopy} />
        )}
        {value.type === 'manual' && null}
        {value.type === 'scheduled' && (
          <ScheduledConfig value={value} onChange={onChange} />
        )}
        {value.type === 'poll' && (
          <PollConfig value={value} onChange={onChange} />
        )}
      </div>
    </div>
  );
}

interface TriggerCardProps {
  type: TriggerFormState['type'];
  selected: TriggerFormState['type'];
  onSelect: (type: TriggerFormState['type']) => void;
  title: string;
  desc: string;
}

function TriggerCard({ type, selected, onSelect, title, desc }: TriggerCardProps) {
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

interface WebhookConfigProps {
  policyId?: string;
  copied: boolean;
  onCopy: () => void;
}

function WebhookConfig({ policyId, copied, onCopy }: WebhookConfigProps) {
  const url = policyId ? `POST /api/v1/webhooks/${policyId}` : undefined;
  const displayValue = url ?? 'POST /api/v1/webhooks/<policy-id>';

  return (
    <div className={styles.field}>
      <label className={styles.label}>Webhook URL</label>
      <div className={styles.webhookUrl}>
        <input
          className={policyId ? styles.webhookInput : `${styles.webhookInput} ${styles.webhookInputPlaceholder}`}
          type="text"
          value={displayValue}
          readOnly
        />
        <button
          className={copied ? `${styles.copyButton} ${styles.copyButtonDone}` : styles.copyButton}
          onClick={onCopy}
          disabled={!policyId}
        >
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
    </div>
  );
}

interface ScheduledConfigProps {
  value: ScheduledTriggerState;
  onChange: (next: TriggerFormState) => void;
}

function ScheduledConfig({ value, onChange }: ScheduledConfigProps) {
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
    <div className={styles.field}>
      <label className={styles.label}>Fire at (UTC)</label>
      {value.fireAt.map((ts, i) => (
        <div key={i} className={styles.fieldRow}>
          <input
            className={styles.input}
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
    </div>
  );
}

interface PollConfigProps {
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

function PollConfig({ value, onChange }: PollConfigProps) {
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
      <div className={styles.field}>
        <label className={styles.label}>Interval</label>
        <input
          className={`${styles.input} ${styles.inputMono}`}
          type="text"
          value={value.interval}
          placeholder="5m"
          onChange={(e) => updateInterval(e.target.value)}
        />
      </div>

      <div className={styles.field}>
        <label className={styles.label}>Match mode</label>
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

            <div className={styles.field}>
              <label className={styles.label}>Tool</label>
              <input
                className={`${styles.input} ${styles.inputMono}`}
                type="text"
                value={check.tool}
                placeholder="server.tool_name"
                onChange={(e) => updateCheck(i, { ...check, tool: e.target.value })}
              />
            </div>

            <div className={styles.field}>
              <label className={styles.label}>Input (JSON, optional)</label>
              <textarea
                className={styles.textarea}
                value={check.input}
                placeholder={'{"key": "value"}'}
                onChange={(e) => updateCheck(i, { ...check, input: e.target.value })}
              />
            </div>

            <div className={styles.field}>
              <label className={styles.label}>JSONPath</label>
              <input
                className={`${styles.input} ${styles.inputMono}`}
                type="text"
                value={check.path}
                placeholder="$.field.path"
                onChange={(e) => updateCheck(i, { ...check, path: e.target.value })}
              />
            </div>

            <div className={styles.fieldRow}>
              <div className={styles.field}>
                <label className={styles.label}>Comparator</label>
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

              <div className={styles.field}>
                <label className={styles.label}>Value</label>
                <input
                  className={`${styles.input} ${styles.inputMono}`}
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
