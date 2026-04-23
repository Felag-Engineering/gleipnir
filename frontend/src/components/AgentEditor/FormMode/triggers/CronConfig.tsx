import shared from '../FormSections.module.css';
import type { CronTriggerState, TriggerFormState, SectionIssues } from '../types';
import { FieldError } from '@/components/form/FieldError';

export interface CronConfigProps {
  value: CronTriggerState;
  onChange: (next: TriggerFormState) => void;
  errors?: SectionIssues;
}

export function CronConfig({ value, onChange, errors = [] }: CronConfigProps) {
  const cronExprErrors = errors.filter(e => e.field === 'trigger.cron_expr').map(e => e.message);

  return (
    <div className={shared.field} data-field="trigger.cron_expr">
      <label className={shared.label}>Cron expression</label>
      <input
        className={`${shared.input} ${shared.inputMono}`}
        type="text"
        placeholder="0 9 * * 1"
        value={value.cronExpr}
        onChange={(e) => onChange({ ...value, cronExpr: e.target.value })}
      />
      <p className={shared.label}>
        5-field POSIX format: minute hour day month weekday.
        Example: <code>0 9 * * 1</code> fires every Monday at 09:00.
      </p>
      <FieldError messages={cronExprErrors} />
    </div>
  );
}
