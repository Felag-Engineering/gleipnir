import shared from './FormSections.module.css';
import type { IdentityFormState, SectionIssues } from './types';
import { FieldError } from '@/components/form/FieldError';

export interface PolicyIdentitySectionProps {
  value: IdentityFormState;
  onChange: (next: IdentityFormState) => void;
  existingFolders?: string[];
  errors?: SectionIssues;
}

export function PolicyIdentitySection({ value, onChange, existingFolders = [], errors = [] }: PolicyIdentitySectionProps) {
  const datalistId = 'policy-folder-suggestions';
  const nameErrors = errors.filter(e => e.field === 'name').map(e => e.message);
  const nameInvalid = nameErrors.length > 0;

  return (
    <div className={shared.section}>
      <div className={shared.heading}>Identity</div>

      <div className={shared.field} data-field="name">
        <label className={shared.label}>Name</label>
        <input
          className={`${shared.input} ${shared.inputMono}`}
          type="text"
          value={value.name}
          aria-invalid={nameInvalid || undefined}
          aria-describedby={nameInvalid ? 'field-name-error' : undefined}
          onChange={(e) => onChange({ ...value, name: e.target.value })}
        />
        <FieldError id="field-name-error" messages={nameErrors} />
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
