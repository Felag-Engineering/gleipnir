import { useEffect } from 'react';
import shared from './FormSections.module.css';
import styles from './ModelSection.module.css';
import type { ModelFormState } from './types';
import { useModels } from '@/hooks/queries/users';
import { usePublicConfig } from '@/hooks/queries/config';
import { formatProviderName } from '@/utils/format';

export interface ModelSectionProps {
  value: ModelFormState;
  onChange: (next: ModelFormState) => void;
}

export function ModelSection({ value, onChange }: ModelSectionProps) {
  const { data: providers, isLoading, isError } = useModels();
  const { data: publicConfig } = usePublicConfig();

  // When neither provider nor model is set (new policy, no model block in YAML),
  // apply the system default from /api/v1/config if it appears in the enabled
  // model list. An empty state is distinct from "chosen but invalid" — we must
  // not overwrite a value the user has already selected.
  useEffect(() => {
    if (!providers || providers.length === 0) return;
    if (value.provider !== '' || value.model !== '') return;
    const dm = publicConfig?.default_model;
    if (!dm) return;
    const inList = providers.some(
      (g) => g.provider === dm.provider && g.models.some((m) => m.name === dm.name),
    );
    if (!inList) return;
    onChange({ provider: dm.provider, model: dm.name });
  }, [providers, publicConfig, value.provider, value.model, onChange]);

  // If the current (provider, model) is not present in the loaded list and the
  // current value is non-empty, snap to the first available option. This handles
  // the case where an existing policy references a model that was subsequently
  // disabled. Guard against empty-state so we don't overwrite the "not yet
  // chosen" placeholder before the system-default effect has a chance to run.
  useEffect(() => {
    if (!providers || providers.length === 0) return;
    if (!value.provider && !value.model) return;
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
  const isEmpty = !value.provider && !value.model;

  return (
    <div className={shared.section}>
      <div className={shared.heading}>Model</div>
      <select
        className={styles.select}
        value={selected}
        onChange={handleChange}
        disabled={isLoading || isError || providers?.length === 0}
      >
        {isLoading && <option value={selected}>Loading models…</option>}
        {isError && <option value={selected}>Failed to load models</option>}
        {providers?.length === 0 && <option value="">No models enabled — go to Admin → Models</option>}
        {/* Placeholder shown when no model has been chosen yet (new policy, no system default) */}
        {isEmpty && !isLoading && !isError && (providers?.length ?? 0) > 0 && (
          <option value="">Select a model…</option>
        )}
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
