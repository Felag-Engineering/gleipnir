import shared from './FormSections.module.css';
import type { IdentityFormState } from './types';

export interface PolicyIdentitySectionProps {
  value: IdentityFormState;
  onChange: (next: IdentityFormState) => void;
}

export function PolicyIdentitySection({ value, onChange }: PolicyIdentitySectionProps) {
  return (
    <div className={shared.section}>
      <div className={shared.heading}>Identity</div>

      <div className={shared.field}>
        <label className={shared.label}>Name</label>
        <input
          className={`${shared.input} ${shared.inputMono}`}
          type="text"
          value={value.name}
          onChange={(e) => onChange({ ...value, name: e.target.value })}
        />
      </div>

      <div className={shared.field}>
        <label className={shared.label}>Description</label>
        <textarea
          className={shared.textarea}
          rows={3}
          value={value.description}
          onChange={(e) => onChange({ ...value, description: e.target.value })}
        />
      </div>

      <div className={shared.field}>
        <label className={shared.label}>Folder</label>
        <input
          className={shared.input}
          type="text"
          value={value.folder}
          placeholder="Ungrouped"
          onChange={(e) => onChange({ ...value, folder: e.target.value })}
        />
      </div>
    </div>
  );
}
