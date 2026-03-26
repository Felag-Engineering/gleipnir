import { Link } from 'react-router-dom'
import styles from './StatsBar.module.css'

interface StatsBarProps {
  activeRuns: number
  pendingApprovals: number
  mcpServerCount: number
  mcpServersLoading: boolean
}

export function StatsBar({ activeRuns, pendingApprovals, mcpServerCount, mcpServersLoading }: StatsBarProps) {
  const hasApprovals = pendingApprovals > 0

  return (
    <div className={styles.grid}>
      <div className={styles.card}>
        <div className={styles.label}>Active Runs</div>
        <div className={styles.value}>{activeRuns}</div>
        <div className={styles.sub}>active</div>
      </div>

      <div className={`${styles.card}${hasApprovals ? ` ${styles.cardActionable}` : ''}`}>
        <div className={styles.label}>Pending Approvals</div>
        <div className={`${styles.value}${hasApprovals ? ` ${styles.valueHighlight}` : ''}`}>
          {pendingApprovals}
        </div>
        <div className={styles.sub}>
          {hasApprovals ? (
            <Link to="/runs?status=waiting_for_approval" className={styles.reviewLink}>
              Review →
            </Link>
          ) : (
            'none'
          )}
        </div>
      </div>

      <div className={styles.card}>
        <div className={styles.label}>System Health</div>
        <div className={styles.value}>
          {mcpServersLoading ? '—' : `${mcpServerCount} ${mcpServerCount === 1 ? 'server' : 'servers'}`}
        </div>
        <div className={styles.sub}>configured</div>
      </div>
    </div>
  )
}
