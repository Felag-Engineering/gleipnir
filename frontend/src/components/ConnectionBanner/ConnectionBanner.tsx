import { ConnectionState } from '../../hooks/useSSE'
import styles from './ConnectionBanner.module.css'

interface Props {
  state: ConnectionState
  compact?: boolean
}

export default function ConnectionBanner({ state, compact = false }: Props) {
  if (compact) {
    const dotClass =
      state === 'connected'
        ? styles.dotConnected
        : state === 'reconnecting'
          ? styles.dotReconnecting
          : styles.dotDisconnected

    const title =
      state === 'connected'
        ? 'Connected'
        : state === 'reconnecting'
          ? 'Connection lost — reconnecting…'
          : 'Connection lost'

    return (
      <span
        className={`${styles.dot} ${dotClass}`}
        title={title}
        role="status"
        aria-label={title}
      />
    )
  }

  if (state === 'connected') {
    return (
      <div className={styles.bannerConnected} role="status">
        <span className={`${styles.dot} ${styles.dotConnected}`} aria-hidden="true" />
        Connected
      </div>
    )
  }

  const message =
    state === 'reconnecting' ? 'Connection lost — reconnecting…' : 'Connection lost'

  return (
    <div className={styles.banner} role="status">
      {message}
    </div>
  )
}
