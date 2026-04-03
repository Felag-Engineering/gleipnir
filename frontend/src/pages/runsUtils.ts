import styles from './RunsPage.module.css'

// Maps a run status to the CSS classes for the left stripe and row background.
// Consolidates the two separate lookups into a single call.
export function getRunRowClasses(status: string): { stripe: string; rowBg: string } {
  switch (status) {
    case 'complete':
      return { stripe: styles.stripeComplete, rowBg: styles.rowComplete }
    case 'running':
      return { stripe: styles.stripeRunning, rowBg: styles.rowRunning }
    case 'failed':
      return { stripe: styles.stripeFailed, rowBg: styles.rowFailed }
    case 'waiting_for_approval':
    case 'waiting_for_feedback':
      return { stripe: styles.stripeApproval, rowBg: styles.rowApproval }
    case 'interrupted':
      return { stripe: styles.stripeInterrupted, rowBg: styles.rowInterrupted }
    case 'pending':
      return { stripe: styles.stripePending, rowBg: styles.rowPending }
    default:
      return { stripe: styles.stripePending, rowBg: styles.rowComplete }
  }
}
