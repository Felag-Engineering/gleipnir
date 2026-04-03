import styles from './ModelSection.module.css';
import type { ModelFormState } from './types';
import { useModels } from '@/hooks/queries/users';

export interface ModelSectionProps {
  value: ModelFormState;
  onChange: (next: ModelFormState) => void;
}

export function ModelSection({ value, onChange }: ModelSectionProps) {
  const { data: providers, isLoading, isError } = useModels();

  const handleChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const raw = e.target.value; // format: "provider:model"
    const sep = raw.indexOf(':');
    if (sep < 0) return;
    onChange({
      provider: raw.slice(0, sep),
      model: raw.slice(sep + 1),
    });
  };

  const selected = `${value.provider}:${value.model}`;

  return (
    <div className={styles.section}>
      <div className={styles.heading}>Model</div>
      <select
        className={styles.select}
        value={selected}
        onChange={handleChange}
        disabled={isLoading || isError}
      >
        {isLoading && <option value={selected}>Loading models…</option>}
        {isError && <option value={selected}>Failed to load models</option>}
        {providers?.length === 0 && <option value={selected}>No models available</option>}
        {providers?.map((group) => (
          <optgroup key={group.provider} label={group.provider}>
            {group.models.map((m) => (
              <option key={`${group.provider}:${m.name}`} value={`${group.provider}:${m.name}`}>
                {m.display_name}
              </option>
            ))}
          </optgroup>
        ))}
      </select>
    </div>
  );
}
