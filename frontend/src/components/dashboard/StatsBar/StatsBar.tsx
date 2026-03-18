import { Link } from 'react-router-dom'
import styles from './StatsBar.module.css'

interface StatsBarProps {
  activeRuns: number
  pendingApprovals: number
  mcpServerCount: number
  mcpServersLoading: boolean
}

export function StatsBar({ activeRuns, pendingApprovals, mcpServerCount, mcpServersLoading }: StatsBarProps) {
  const healthClass = mcpServersLoading
    ? ''
    : mcpServerCount > 0
      ? styles.cardGreen
      : styles.cardRed

  return (
    <div className={styles.grid}>
      <div className={`${styles.card} ${styles.cardBlue}`}>
        <div className={styles.label}>Active Runs</div>
        <div className={`${styles.value}${activeRuns > 0 ? ` ${styles.valueBlue}` : ''}`}>{activeRuns}</div>
        <div className={styles.sub}>active</div>
      </div>

      <div className={`${styles.card} ${styles.cardAmber}`}>
        <div className={styles.label}>Pending Approvals</div>
        <div className={`${styles.value}${pendingApprovals > 0 ? ` ${styles.valueAmber}` : ''}`}>
          {pendingApprovals}
        </div>
        <div className={styles.sub}>
          {pendingApprovals > 0 ? (
            <Link to="/runs?status=waiting_for_approval" className={styles.reviewLink}>
              Review →
            </Link>
          ) : (
            'none'
          )}
        </div>
      </div>

      <div className={`${styles.card} ${healthClass}`}>
        <div className={styles.label}>System Health</div>
        <div className={styles.value}>
          {mcpServersLoading ? '—' : `${mcpServerCount} ${mcpServerCount === 1 ? 'server' : 'servers'}`}
        </div>
        <div className={styles.sub}>configured</div>
      </div>
    </div>
  )
}
