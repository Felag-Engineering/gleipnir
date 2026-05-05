import type { PluginHealthState } from '@/api/types'
import { pluginHealthLabel } from '@/utils/pluginHealth'
import styles from './PluginHealthChip.module.css'

interface PluginHealthChipProps {
  state: PluginHealthState
  detail?: string
  onClick?: () => void
}

// variant maps each state to a CSS module class based on its severity tier.
//   green  — healthy
//   yellow — degraded but operational (unsigned_permissive, pending_*, unhealthy)
//   red    — non-functional (verification_error, signature_invalid, circuit_broken, crashed)
const VARIANT: Record<PluginHealthState, string> = {
  healthy:                    styles.green,
  unsigned_permissive:        styles.yellow,
  pending_key_approval:       styles.yellow,
  pending_manifest_approval:  styles.yellow,
  pending_config_migration:   styles.yellow,
  unhealthy:                  styles.yellow,
  circuit_broken:             styles.red,
  verification_error:         styles.red,
  signature_invalid:          styles.red,
  crashed:                    styles.red,
}

export function PluginHealthChip({ state, detail, onClick }: PluginHealthChipProps) {
  return (
    <button
      type="button"
      className={`${styles.chip} ${VARIANT[state]}`}
      title={detail}
      onClick={onClick}
    >
      {pluginHealthLabel(state)}
    </button>
  )
}
