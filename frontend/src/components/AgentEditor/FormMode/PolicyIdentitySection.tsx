import shared from './FormSections.module.css';
import type { IdentityFormState } from './types';

export interface PolicyIdentitySectionProps {
  value: IdentityFormState;
  onChange: (next: IdentityFormState) => void;
  existingFolders?: string[];
}

export function PolicyIdentitySection({ value, onChange, existingFolders = [] }: PolicyIdentitySectionProps) {
  const datalistId = 'policy-folder-suggestions';

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
          list={existingFolders.length > 0 ? datalistId : undefined}
          value={value.folder}
          placeholder="Ungrouped"
          onChange={(e) => onChange({ ...value, folder: e.target.value })}
        />
        {existingFolders.length > 0 && (
          <datalist id={datalistId}>
            {existingFolders.map((folder) => (
              <option key={folder} value={folder} />
            ))}
          </datalist>
        )}
      </div>
    </div>
  );
}
