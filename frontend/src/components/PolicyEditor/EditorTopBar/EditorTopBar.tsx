import { useState } from 'react';
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
  onDelete: () => void;
}

export function EditorTopBar({
  policyName,
  isDirty,
  mode,
  canSave,
  isEditMode,
  onModeChange,
  onSave,
  onDelete,
}: EditorTopBarProps) {
  const [confirmOpen, setConfirmOpen] = useState(false);

  return (
    <>
      <div className={styles.topbar}>
        <div className={styles.breadcrumb}>
          <span className={`${styles.crumb} ${styles.crumbGleipnir}`}>GLEIPNIR</span>
          <span className={styles.separator}>›</span>
          <Link to="/policies" className={`${styles.crumb} ${styles.crumbPolicies} ${styles.crumbLink}`}>Policies</Link>
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
              onClick={() => setConfirmOpen(true)}
            >
              Delete
            </button>
          )}
        </div>
      </div>

      {confirmOpen && (
        <div className={styles.overlay}>
          <div className={styles.modal}>
            <div className={styles.modalTitle}>Delete policy?</div>
            <div className={styles.modalBody}>
              This action cannot be undone. The policy and all associated run history will be
              permanently deleted.
            </div>
            <div className={styles.modalActions}>
              <button
                className={styles.modalCancel}
                onClick={() => setConfirmOpen(false)}
              >
                Cancel
              </button>
              <button
                className={styles.modalConfirm}
                onClick={() => {
                  onDelete();
                  setConfirmOpen(false);
                }}
              >
                Delete
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}
