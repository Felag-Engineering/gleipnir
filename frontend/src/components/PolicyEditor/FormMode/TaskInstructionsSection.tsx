import shared from './FormSections.module.css';
import styles from './TaskInstructionsSection.module.css';
import type { TaskInstructionsFormState } from './types';

export interface TaskInstructionsSectionProps {
  value: TaskInstructionsFormState;
  onChange: (next: TaskInstructionsFormState) => void;
}

export function TaskInstructionsSection({ value, onChange }: TaskInstructionsSectionProps) {
  return (
    <div className={shared.section}>
      <div className={shared.heading}>Task Instructions</div>

      <div className={shared.field}>
        <label className={shared.label}>Task</label>
        <textarea
          className={styles.textarea}
          value={value.task}
          placeholder="Describe what the agent should do…"
          onChange={(e) => onChange({ ...value, task: e.target.value })}
        />
      </div>
      <div className={styles.hint}>
        The trigger payload (webhook body, poll result) is delivered as the agent's first message.
      </div>
    </div>
  );
}
