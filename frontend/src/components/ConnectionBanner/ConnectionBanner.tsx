import { ConnectionState } from '../../hooks/useSSE'
import styles from './ConnectionBanner.module.css'

interface Props {
  state: ConnectionState
}

export default function ConnectionBanner({ state }: Props) {
  if (state === 'connected') return null

  const message =
    state === 'reconnecting' ? 'Connection lost — reconnecting…' : 'Connection lost'

  return (
    <div className={styles.banner} role="status">
      {message}
    </div>
  )
}
