import { useEffect, useState } from 'react';
import styles from './TriggerSection.module.css';
import type { TriggerFormState, CronTriggerState, PollTriggerState, ManualTriggerState, ScheduledTriggerState } from './types';

export interface TriggerSectionProps {
  value: TriggerFormState;
  onChange: (next: TriggerFormState) => void;
  policyId?: string;
}

const DEFAULT_CRON: CronTriggerState = { type: 'cron', schedule: '' };
const DEFAULT_MANUAL: ManualTriggerState = { type: 'manual' };
const DEFAULT_POLL: PollTriggerState = {
  type: 'poll',
  interval: '5m',
  request: { url: '', method: 'GET', headers: '', body: undefined },
  filter: '',
};
const DEFAULT_SCHEDULED: ScheduledTriggerState = { type: 'scheduled', fireAt: [] };

export function TriggerSection({ value, onChange, policyId }: TriggerSectionProps) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  function handleTypeSelect(type: TriggerFormState['type']) {
    if (type === 'webhook') onChange({ type: 'webhook' });
    else if (type === 'cron') onChange(DEFAULT_CRON);
    else if (type === 'manual') onChange(DEFAULT_MANUAL);
    else if (type === 'scheduled') onChange(DEFAULT_SCHEDULED);
    else onChange(DEFAULT_POLL);
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
        <button
          className={value.type === 'webhook' ? `${styles.card} ${styles.cardActive}` : styles.card}
          onClick={() => handleTypeSelect('webhook')}
        >
          <div className={value.type === 'webhook' ? `${styles.cardTitle} ${styles.cardTitleActive}` : styles.cardTitle}>
            Webhook
          </div>
          <div className={styles.cardDesc}>Triggered by an incoming HTTP request</div>
        </button>

        <button
          className={value.type === 'cron' ? `${styles.card} ${styles.cardActive}` : styles.card}
          onClick={() => handleTypeSelect('cron')}
        >
          <div className={value.type === 'cron' ? `${styles.cardTitle} ${styles.cardTitleActive}` : styles.cardTitle}>
            Schedule
          </div>
          <div className={styles.cardDesc}>Runs on a cron schedule expression</div>
        </button>

        <button
          className={value.type === 'poll' ? `${styles.card} ${styles.cardActive}` : styles.card}
          onClick={() => handleTypeSelect('poll')}
        >
          <div className={value.type === 'poll' ? `${styles.cardTitle} ${styles.cardTitleActive}` : styles.cardTitle}>
            Poll
          </div>
          <div className={styles.cardDesc}>Periodically fetches a URL and reacts to changes</div>
        </button>

        <button
          className={value.type === 'manual' ? `${styles.card} ${styles.cardActive}` : styles.card}
          onClick={() => handleTypeSelect('manual')}
        >
          <div className={value.type === 'manual' ? `${styles.cardTitle} ${styles.cardTitleActive}` : styles.cardTitle}>
            Manual
          </div>
          <div className={styles.cardDesc}>Triggered on demand from the dashboard</div>
        </button>

        <button
          className={value.type === 'scheduled' ? `${styles.card} ${styles.cardActive}` : styles.card}
          onClick={() => handleTypeSelect('scheduled')}
        >
          <div className={value.type === 'scheduled' ? `${styles.cardTitle} ${styles.cardTitleActive}` : styles.cardTitle}>
            Scheduled
          </div>
          <div className={styles.cardDesc}>Fires once at each specified date and time, then pauses</div>
        </button>
      </div>

      <div className={styles.config}>
        {value.type === 'webhook' && (
          <WebhookConfig policyId={policyId} copied={copied} onCopy={handleCopy} />
        )}
        {value.type === 'cron' && (
          <CronConfig value={value} onChange={onChange} />
        )}
        {value.type === 'poll' && (
          <PollConfig value={value} onChange={onChange} />
        )}
        {value.type === 'manual' && null}
        {value.type === 'scheduled' && (
          <ScheduledConfig value={value} onChange={onChange} />
        )}
      </div>
    </div>
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

interface CronConfigProps {
  value: CronTriggerState;
  onChange: (next: TriggerFormState) => void;
}

function CronConfig({ value, onChange }: CronConfigProps) {
  return (
    <div className={styles.field}>
      <label className={styles.label}>Schedule</label>
      <input
        className={`${styles.input} ${styles.inputMono}`}
        type="text"
        value={value.schedule}
        placeholder="0 * * * *"
        onChange={(e) => onChange({ ...value, schedule: e.target.value })}
      />
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

function PollConfig({ value, onChange }: PollConfigProps) {
  function updateRequest(patch: Partial<PollTriggerState['request']>) {
    onChange({ ...value, request: { ...value.request, ...patch } });
  }

  return (
    <>
      <div className={styles.fieldRow}>
        <div className={styles.field}>
          <label className={styles.label}>Interval</label>
          <input
            className={`${styles.input} ${styles.inputMono}`}
            type="text"
            value={value.interval}
            placeholder="5m"
            onChange={(e) => onChange({ ...value, interval: e.target.value })}
          />
        </div>
        <div className={styles.field}>
          <label className={styles.label}>Method</label>
          <select
            className={styles.select}
            value={value.request.method}
            onChange={(e) => updateRequest({ method: e.target.value as 'GET' | 'POST' })}
          >
            <option value="GET">GET</option>
            <option value="POST">POST</option>
          </select>
        </div>
      </div>

      <div className={styles.field}>
        <label className={styles.label}>URL</label>
        <input
          className={`${styles.input} ${styles.inputMono}`}
          type="text"
          value={value.request.url}
          placeholder="https://example.com/api/resource"
          onChange={(e) => updateRequest({ url: e.target.value })}
        />
      </div>

      <div className={styles.field}>
        <label className={styles.label}>Headers</label>
        <textarea
          className={styles.textarea}
          value={value.request.headers}
          placeholder="Authorization: Bearer token"
          onChange={(e) => updateRequest({ headers: e.target.value })}
        />
      </div>

      <div className={styles.field}>
        <label className={styles.label}>Filter</label>
        <input
          className={`${styles.input} ${styles.inputMono}`}
          type="text"
          value={value.filter}
          placeholder="jq expression or empty to always trigger"
          onChange={(e) => onChange({ ...value, filter: e.target.value })}
        />
      </div>
    </>
  );
}
