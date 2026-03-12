import styles from './ModelSection.module.css';
import type { ModelFormState, ModelValue } from './types';

export interface ModelSectionProps {
  value: ModelFormState;
  onChange: (next: ModelFormState) => void;
}

const MODEL_OPTIONS: { value: ModelValue; label: string; desc: string }[] = [
  { value: 'claude-opus-4-6',           label: 'Opus 4.6',        desc: 'Most capable, highest cost' },
  { value: 'claude-sonnet-4-6',         label: 'Sonnet 4.6',      desc: 'Balanced capability and cost' },
  { value: 'claude-haiku-4-5-20251001', label: 'Haiku 4.5',       desc: 'Fastest, lowest cost' },
];

export function ModelSection({ value, onChange }: ModelSectionProps) {
  return (
    <div className={styles.section}>
      <div className={styles.heading}>Model</div>
      <select
        className={styles.select}
        value={value.model}
        onChange={(e) => onChange({ model: e.target.value as ModelValue })}
      >
        {MODEL_OPTIONS.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label} — {option.desc}
          </option>
        ))}
      </select>
    </div>
  );
}
