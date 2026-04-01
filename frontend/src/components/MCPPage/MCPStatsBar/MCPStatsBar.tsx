import styles from './MCPStatsBar.module.css'

interface Props {
  totalTools: number
  isLoading: boolean
}

export function MCPStatsBar({ totalTools, isLoading }: Props) {
  const dash = '–'
  return (
    <div className={styles.grid}>
      <div className={styles.card}>
        <div className={styles.label}>Total tools</div>
        <div className={styles.value}>{isLoading ? dash : totalTools}</div>
      </div>
    </div>
  )
}
