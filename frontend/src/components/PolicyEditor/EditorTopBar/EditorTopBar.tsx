import { Link } from 'react-router-dom';
import styles from './EditorTopBar.module.css';

export interface EditorTopBarProps {
  policyName: string;
  isDirty: boolean;
  mode: 'form' | 'yaml';
  canSave: boolean;
  isEditMode: boolean;
  onModeChange: (m: 'form' | 'yaml') => void;
  onSave: () => void;
  onDeleteClick: () => void;
}

export function EditorTopBar({
  policyName,
  isDirty,
  mode,
  canSave,
  isEditMode,
  onModeChange,
  onSave,
  onDeleteClick,
}: EditorTopBarProps) {
  return (
    <div className={styles.topbar}>
      <div className={styles.breadcrumb}>
        <span className={`${styles.crumb} ${styles.crumbGleipnir}`}>GLEIPNIR</span>
        <span className={styles.separator}>›</span>
        <Link to="/agents" className={`${styles.crumb} ${styles.crumbPolicies} ${styles.crumbLink}`}>Agents</Link>
        <span className={styles.separator}>›</span>
        <span className={`${styles.crumb} ${styles.crumbPolicy}`}>{policyName}</span>
        {isDirty && <span className={styles.dirtyDot} />}
      </div>

      <div className={styles.actions}>
        <div className={styles.modeToggle}>
          <button
            className={mode === 'form' ? styles.toggleActive : styles.toggleInactive}
            onClick={() => onModeChange('form')}
          >
            Form
          </button>
          <button
            className={mode === 'yaml' ? styles.toggleActive : styles.toggleInactive}
            onClick={() => onModeChange('yaml')}
          >
            YAML
          </button>
        </div>

        <button
          className={styles.saveButton}
          onClick={onSave}
          disabled={!canSave}
        >
          Save
        </button>

        {isEditMode && (
          <button
            className={styles.deleteButton}
            onClick={onDeleteClick}
          >
            Delete
          </button>
        )}
      </div>
    </div>
  );
}
