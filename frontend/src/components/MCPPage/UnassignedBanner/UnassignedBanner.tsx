import styles from './UnassignedBanner.module.css'

interface Props {
  count: number
}

export function UnassignedBanner({ count }: Props) {
  if (count === 0) return null

  return (
    <div className={styles.banner} role="status" aria-live="polite">
      <span className={styles.icon} aria-hidden="true">⚠</span>
      {count} {count === 1 ? 'tool has' : 'tools have'} no capability role assigned — assign a role to use them in policies.
    </div>
  )
}
