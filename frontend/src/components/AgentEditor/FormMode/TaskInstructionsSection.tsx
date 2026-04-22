import shared from './FormSections.module.css';
import styles from './TaskInstructionsSection.module.css';
import type { TaskInstructionsFormState, SectionIssues } from './types';
import { FieldError } from '@/components/form/FieldError';

export interface TaskInstructionsSectionProps {
  value: TaskInstructionsFormState;
  onChange: (next: TaskInstructionsFormState) => void;
  errors?: SectionIssues;
}

export function TaskInstructionsSection({ value, onChange, errors = [] }: TaskInstructionsSectionProps) {
  const taskErrors = errors.filter(e => e.field === 'agent.task').map(e => e.message);

  return (
    <div className={shared.section}>
      <div className={shared.heading}>Task Instructions</div>

      <div className={shared.field} data-field="agent.task">
        <label className={shared.label}>Task</label>
        <textarea
          className={styles.textarea}
          value={value.task}
          placeholder="Describe what the agent should do…"
          aria-invalid={taskErrors.length > 0 || undefined}
          onChange={(e) => onChange({ ...value, task: e.target.value })}
        />
        <FieldError messages={taskErrors} />
      </div>
      <div className={styles.hint}>
        The trigger payload (webhook body, poll result) is delivered as the agent's first message.
      </div>
    </div>
  );
}
