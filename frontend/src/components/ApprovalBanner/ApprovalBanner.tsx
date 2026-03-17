import styles from './ApprovalBanner.module.css'

interface Props {
  count: number
}

export default function ApprovalBanner({ count }: Props) {
  if (count === 0) return null

  return (
    <div className={styles.banner} role="status">
      {count} {count === 1 ? 'run' : 'runs'} awaiting approval — approval UI available in v0.2
    </div>
  )
}
