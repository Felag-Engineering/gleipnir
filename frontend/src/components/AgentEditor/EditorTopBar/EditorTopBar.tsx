import { Link } from 'react-router-dom';
import styles from './EditorTopBar.module.css';

export interface EditorTopBarProps {
  policyName: string;
  canSave: boolean;
  isEditMode: boolean;
  pausedAt?: string | null;
  isPauseResumeLoading?: boolean;
  onSave: () => void;
  onDeleteClick: () => void;
  onRunNowClick?: () => void;
  onPauseClick?: () => void;
  onResumeClick?: () => void;
}

export function EditorTopBar({
  policyName,
  canSave,
  isEditMode,
  pausedAt,
  isPauseResumeLoading,
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
