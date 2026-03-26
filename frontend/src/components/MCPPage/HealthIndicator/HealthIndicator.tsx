import styles from './HealthIndicator.module.css'

export type HealthStatus = 'connected' | 'unreachable' | 'discovering'

interface Props {
  status: HealthStatus
}

const LABEL: Record<HealthStatus, string> = {
  connected: 'Connected',
  unreachable: 'Unreachable',
  discovering: 'Discovering',
}

const LABEL_CLASS: Record<HealthStatus, string> = {
  connected: styles.labelConnected,
  unreachable: styles.labelUnreachable,
  discovering: styles.labelDiscovering,
}

export function HealthIndicator({ status }: Props) {
  return (
    <span className={`${styles.label} ${LABEL_CLASS[status]}`}>{LABEL[status]}</span>
  )
}
