import { usePluginInstanceCapabilities } from '@/hooks/queries/plugins'
import { PluginHealthChip } from '@/components/admin/PluginHealthChip/PluginHealthChip'
import type { ApiPluginCapabilityHealth } from '@/api/types'
import styles from './CapabilityHealthPanel.module.css'

interface CapabilityHealthPanelProps {
  pluginId: string
  instanceId: string
}

function capabilityLabel(cap: ApiPluginCapabilityHealth): string {
  return cap.name ? `${cap.profile}/${cap.name}` : cap.profile
}

// CapabilityHealthPanel lists per-capability health for one plugin instance.
// An empty list is normal: instances that report no capability health (the
// current plugin runtime) have nothing to show.
export function CapabilityHealthPanel({ pluginId, instanceId }: CapabilityHealthPanelProps) {
  const { data, status } = usePluginInstanceCapabilities(pluginId, instanceId)

  if (status === 'pending') return null

  return (
    <section className={styles.panel} aria-label="Capabilities">
      <h2 className={styles.heading}>Capabilities</h2>
      {status === 'error' && <p className={styles.empty}>Could not load capability health.</p>}
      {status === 'success' && data.length === 0 && (
        <p className={styles.empty}>No per-capability health has been reported for this instance.</p>
      )}
      {status === 'success' && data.length > 0 && (
        <ul className={styles.list}>
          {data.map((cap) => (
            <li key={capabilityLabel(cap)} className={styles.item}>
              <span className={styles.capName}>{capabilityLabel(cap)}</span>
              <PluginHealthChip state={cap.state} detail={cap.detail || undefined} />
              <span className={styles.source}>
                {cap.source === 'self_report' ? 'reported by plugin' : 'checked by host'}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
