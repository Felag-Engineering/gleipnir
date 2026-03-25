import styles from './MCPStatsBar.module.css'

interface Props {
  totalTools: number
  tools: number
  feedback: number
  isLoading: boolean
}

export function MCPStatsBar({ totalTools, tools, feedback, isLoading }: Props) {
  const dash = '–'
  return (
    <div className={styles.grid}>
      <div className={styles.card}>
        <div className={styles.label}>Total tools</div>
        <div className={styles.value}>{isLoading ? dash : totalTools}</div>
      </div>
      <div className={`${styles.card} ${styles.blue}`}>
        <div className={styles.label}>Tools</div>
        <div className={styles.value}>{isLoading ? dash : tools}</div>
        <div className={styles.sub}>available</div>
      </div>
      <div className={`${styles.card} ${styles.purple}`}>
        <div className={styles.label}>Feedback</div>
        <div className={styles.value}>{isLoading ? dash : feedback}</div>
        <div className={styles.sub}>human-in-the-loop</div>
      </div>
    </div>
  )
}
