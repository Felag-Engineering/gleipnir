import shared from './FormSections.module.css';
import styles from './TriggerSection.module.css';
import type { TriggerFormState, ManualTriggerState, ScheduledTriggerState, PollTriggerState, SectionIssues } from './types';
import { TriggerCard } from './triggers/TriggerCard';
import { WebhookConfig } from './triggers/WebhookConfig';
import { ScheduledConfig } from './triggers/ScheduledConfig';
import { PollConfig } from './triggers/PollConfig';

export interface TriggerSectionProps {
  value: TriggerFormState;
  onChange: (next: TriggerFormState) => void;
  policyId?: string;
  errors?: SectionIssues;
}

const DEFAULT_MANUAL: ManualTriggerState = { type: 'manual' };
const DEFAULT_SCHEDULED: ScheduledTriggerState = { type: 'scheduled', fireAt: [] };
const DEFAULT_POLL: PollTriggerState = {
  type: 'poll',
  interval: '5m',
  match: 'all',
  checks: [{ tool: '', input: '', path: '', comparator: 'equals', value: '' }],
};

export function TriggerSection({ value, onChange, policyId, errors = [] }: TriggerSectionProps) {
  function handleTypeSelect(type: TriggerFormState['type']) {
    if (type === 'webhook') onChange({ type: 'webhook', auth: 'hmac' });
    else if (type === 'manual') onChange(DEFAULT_MANUAL);
    else if (type === 'poll') onChange(DEFAULT_POLL);
    else onChange(DEFAULT_SCHEDULED);
  }

  return (
    <div className={shared.section}>
      <div className={shared.heading}>Trigger</div>

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
          <WebhookConfig
            policyId={policyId}
            value={value}
            onChange={(next) => onChange(next)}
          />
        )}
        {value.type === 'scheduled' && <ScheduledConfig value={value} onChange={onChange} errors={errors} />}
        {value.type === 'poll' && <PollConfig value={value} onChange={onChange} errors={errors} />}
        {value.type === 'manual' && null}
      </div>
    </div>
  );
}
