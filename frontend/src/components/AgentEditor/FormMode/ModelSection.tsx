import { useEffect } from 'react';
import shared from './FormSections.module.css';
import styles from './ModelSection.module.css';
import type { ModelFormState } from './types';
import { useModels } from '@/hooks/queries/users';
import { formatProviderName } from '@/utils/format';

export interface ModelSectionProps {
  value: ModelFormState;
  onChange: (next: ModelFormState) => void;
}

export function ModelSection({ value, onChange }: ModelSectionProps) {
  const { data: providers, isLoading, isError } = useModels();

  // If the current (provider, model) is not present in the loaded list — e.g.
  // a new policy whose hardcoded default is "anthropic:claude-sonnet-4-6" but
  // the operator only has Google enabled — silently snap to the first
  // available option. Without this, the <select> visually shows the first
  // option but no change event fires, so the form submits the stale provider
  // and the backend rejects it with "unknown provider".
  useEffect(() => {
    if (!providers || providers.length === 0) return;
    const exists = providers.some(
      (g) => g.provider === value.provider && g.models.some((m) => m.name === value.model),
    );
    if (exists) return;
    const firstGroup = providers.find((g) => g.models.length > 0);
    if (!firstGroup) return;
    onChange({ provider: firstGroup.provider, model: firstGroup.models[0].name });
  }, [providers, value.provider, value.model, onChange]);

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
    <div className={shared.section}>
      <div className={shared.heading}>Model</div>
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
          <optgroup key={group.provider} label={formatProviderName(group.provider)}>
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
