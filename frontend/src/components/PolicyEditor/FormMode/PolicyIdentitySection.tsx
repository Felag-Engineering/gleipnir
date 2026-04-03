import styles from './PolicyIdentitySection.module.css';
import type { IdentityFormState } from './types';

export interface PolicyIdentitySectionProps {
  value: IdentityFormState;
  onChange: (next: IdentityFormState) => void;
}

export function PolicyIdentitySection({ value, onChange }: PolicyIdentitySectionProps) {
  return (
    <div className={styles.section}>
      <div className={styles.heading}>Identity</div>

      <div className={styles.field}>
        <label className={styles.label}>Name</label>
        <input
          className={`${styles.input} ${styles.inputMono}`}
          type="text"
          value={value.name}
          onChange={(e) => onChange({ ...value, name: e.target.value })}
        />
      </div>

      <div className={styles.inlineRow}>
        <div className={styles.field}>
          <label className={styles.label}>Description</label>
          <input
            className={styles.input}
            type="text"
            value={value.description}
            onChange={(e) => onChange({ ...value, description: e.target.value })}
          />
        </div>

        <div className={styles.field}>
          <label className={styles.label}>Folder</label>
          <input
            className={styles.input}
            type="text"
            value={value.folder}
            placeholder="Ungrouped"
            onChange={(e) => onChange({ ...value, folder: e.target.value })}
          />
        </div>
      </div>
    </div>
  );
}
