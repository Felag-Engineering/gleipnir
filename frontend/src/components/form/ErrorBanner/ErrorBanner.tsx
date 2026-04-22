import styles from './ErrorBanner.module.css'

export interface BannerIssue {
  field?: string
  message: string
}

export interface ErrorBannerProps {
  /** Title shown above the issue list. Defaults to "Fix the following before saving". */
  title?: string
  /** Issues to display. Returns null when empty. */
  issues: BannerIssue[]
  /** Called when the dismiss (×) button is clicked. Omit to hide the button. */
  onDismiss?: () => void
  /** Called with the field path when a clickable issue bullet is clicked. */
  onIssueClick?: (field: string) => void
}

/**
 * A top-of-form error banner that lists validation issues. Issues with a
 * `field` render as a button so the user can click to jump to the offending
 * input. Returns null when there are no issues.
 */
export function ErrorBanner({ title = 'Fix the following before saving', issues, onDismiss, onIssueClick }: ErrorBannerProps) {
  if (issues.length === 0) return null

  return (
    <div className={styles.banner} role="alert">
      <div className={styles.bannerContent}>
        <div className={styles.bannerTitle}>{title}</div>
        <ul className={styles.issueList}>
          {issues.map((issue, i) => (
            <li key={i} className={styles.issueItem}>
              {issue.field && onIssueClick ? (
                <button
                  type="button"
                  className={styles.issueButton}
                  onClick={() => onIssueClick(issue.field!)}
                >
                  {issue.message}
                </button>
              ) : (
                <span>{issue.message}</span>
              )}
            </li>
          ))}
        </ul>
      </div>
      {onDismiss && (
        <button
          type="button"
          className={styles.dismissButton}
          onClick={onDismiss}
          aria-label="Dismiss"
        >
          ×
        </button>
      )}
    </div>
  )
}
