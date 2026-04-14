import { Link } from 'react-router-dom';
import styles from './EditorTopBar.module.css';

export interface EditorTopBarProps {
  policyName: string;
  mode: 'form' | 'yaml';
  canSave: boolean;
  isEditMode: boolean;
  pausedAt?: string | null;
  isPauseResumeLoading?: boolean;
  onModeChange: (m: 'form' | 'yaml') => void;
  onSave: () => void;
  onDeleteClick: () => void;
  onRunNowClick?: () => void;
  onPauseClick?: () => void;
  onResumeClick?: () => void;
}

export function EditorTopBar({
  policyName,
  mode,
  canSave,
  isEditMode,
  pausedAt,
  isPauseResumeLoading,
  onModeChange,
  onSave,
  onDeleteClick,
  onRunNowClick,
  onPauseClick,
  onResumeClick,
}: EditorTopBarProps) {
  return (
    <div className={styles.topbar}>
      <div className={styles.breadcrumb}>
        <span className={`${styles.crumb} ${styles.crumbGleipnir}`}>GLEIPNIR</span>
        <span className={styles.separator}>›</span>
        <Link to="/agents" className={`${styles.crumb} ${styles.crumbPolicies} ${styles.crumbLink}`}>Agents</Link>
        <span className={styles.separator}>›</span>
        <span className={`${styles.crumb} ${styles.crumbPolicy}`}>{policyName}</span>
        {pausedAt && <span className={styles.pausedBadge}>Paused</span>}
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

        {isEditMode && onRunNowClick && (
          <button
            className={styles.runNowButton}
            onClick={onRunNowClick}
          >
            Run now
          </button>
        )}

        {isEditMode && (
          pausedAt ? (
            <button
              className={styles.resumeButton}
              onClick={onResumeClick}
              disabled={isPauseResumeLoading}
            >
              Resume
            </button>
          ) : (
            <button
              className={styles.pauseButton}
              onClick={onPauseClick}
              disabled={isPauseResumeLoading}
            >
              Pause
            </button>
          )
        )}

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
