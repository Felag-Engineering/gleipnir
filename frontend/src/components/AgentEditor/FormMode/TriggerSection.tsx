import shared from './FormSections.module.css';
import type {
  TriggerFormState,
  ManualTriggerState,
  ScheduledTriggerState,
  PollTriggerState,
  CronTriggerState,
  SectionIssues,
} from './types';
import { TriggerPicker } from './triggers/TriggerPicker';
import type { TriggerPickerValue } from './triggers/TriggerPicker';
import { WebhookConfig } from './triggers/WebhookConfig';
import { ScheduledConfig } from './triggers/ScheduledConfig';
import { PollConfig } from './triggers/PollConfig';
import { CronConfig } from './triggers/CronConfig';
import { usePluginInstancesForAudience } from '@/hooks/queries/admin';

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
const DEFAULT_CRON: CronTriggerState = { type: 'cron', cronExpr: '0 9 * * *' };

// pickerValueToTriggerState converts a TriggerPickerValue to a full TriggerFormState,
// initializing default sub-form values for built-in types.
function pickerValueToTriggerState(v: TriggerPickerValue): TriggerFormState {
  if (!v) return { type: 'webhook', auth: 'hmac' };
  if (v.kind === 'builtin') {
    switch (v.type) {
      case 'webhook':   return { type: 'webhook', auth: 'hmac' };
      case 'manual':    return DEFAULT_MANUAL;
      case 'scheduled': return DEFAULT_SCHEDULED;
      case 'poll':      return DEFAULT_POLL;
      case 'cron':      return DEFAULT_CRON;
    }
  }
  return { type: 'subscribed', source: v.source, eventKind: v.eventKind, binding: {} };
}

// triggerStateToPickerValue converts the current TriggerFormState back to the
// discriminated picker value so TriggerPicker can show the active selection.
function triggerStateToPickerValue(trigger: TriggerFormState): TriggerPickerValue {
  if (trigger.type === 'subscribed') {
    return { kind: 'subscribed', source: trigger.source, eventKind: trigger.eventKind };
  }
  const builtinTypes = ['webhook', 'manual', 'scheduled', 'poll', 'cron'] as const;
  if (builtinTypes.includes(trigger.type as typeof builtinTypes[number])) {
    return { kind: 'builtin', type: trigger.type as typeof builtinTypes[number] };
  }
  return null;
}

export function TriggerSection({ value, onChange, policyId, errors = [] }: TriggerSectionProps) {
  const pluginQuery = usePluginInstancesForAudience();

  function handlePickerChange(next: TriggerPickerValue) {
    if (!next) return;

    // Preserve existing sub-form config when switching between trigger types of
    // the same kind (e.g. webhook auth mode survives re-selecting webhook).
    if (next.kind === 'builtin' && value.type === next.type) return;

    onChange(pickerValueToTriggerState(next));
  }

  const pickerValue = triggerStateToPickerValue(value);

  return (
    <div className={shared.section}>
      <div className={shared.heading}>Trigger</div>

      <TriggerPicker
        value={pickerValue}
        onChange={handlePickerChange}
        pluginInstances={pluginQuery.data}
        loading={pluginQuery.isLoading}
      />

      <div>
        {value.type === 'webhook' && (
          <WebhookConfig
            policyId={policyId}
            value={value}
            onChange={(next) => onChange(next)}
          />
        )}
        {value.type === 'scheduled' && <ScheduledConfig value={value} onChange={onChange} errors={errors} />}
        {value.type === 'poll' && <PollConfig value={value} onChange={onChange} errors={errors} />}
        {value.type === 'cron' && <CronConfig value={value} onChange={onChange} errors={errors} />}
        {value.type === 'manual' && null}
        {value.type === 'subscribed' && (
          // TODO(#218): binding form — shows the typed binding fields from the manifest's
          // binding_schema. For now, display a placeholder so the section is not empty.
          <div>
            Binding configuration for <strong>{value.source}</strong>: {value.eventKind}
          </div>
        )}
      </div>
    </div>
  );
}
