import styles from './MCPStatsBar.module.css'

interface Props {
  totalTools: number
  sensors: number
  actuators: number
  feedback: number
  isLoading: boolean
}

export function MCPStatsBar({ totalTools, sensors, actuators, feedback, isLoading }: Props) {
  const dash = '–'
  return (
    <div className={styles.grid}>
      <div className={styles.card}>
        <div className={styles.label}>Total tools</div>
        <div className={styles.value}>{isLoading ? dash : totalTools}</div>
      </div>
      <div className={`${styles.card} ${styles.blue}`}>
        <div className={styles.label}>Sensors</div>
        <div className={styles.value}>{isLoading ? dash : sensors}</div>
        <div className={styles.sub}>read-only</div>
      </div>
      <div className={`${styles.card} ${styles.orange}`}>
        <div className={styles.label}>Actuators</div>
        <div className={styles.value}>{isLoading ? dash : actuators}</div>
        <div className={styles.sub}>world-affecting</div>
      </div>
      <div className={`${styles.card} ${styles.purple}`}>
        <div className={styles.label}>Feedback</div>
        <div className={styles.value}>{isLoading ? dash : feedback}</div>
        <div className={styles.sub}>human-in-the-loop</div>
      </div>
    </div>
  )
}
