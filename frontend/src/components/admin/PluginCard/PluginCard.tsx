import type { PluginHealthState } from '@/api/types'
import { PluginHealthChip } from '@/components/admin/PluginHealthChip/PluginHealthChip'
import styles from './PluginCard.module.css'

interface PluginCardProps {
  pluginName: string
  pluginVersion: string
  pluginId: string
  services: string[]
  instanceCount: number
  aggregateHealth: PluginHealthState
  hasSbom: boolean
  isSelected: boolean
  onClick: () => void
}

// SERVICE_LABEL maps a manifest service key to the display label shown in badges.
const SERVICE_LABEL: Record<string, string> = {
  tool: 'Tool',
  trigger: 'Trigger',
  channel: 'Channel',
}

// SERVICE_BADGE_CLASS maps a manifest service key to the CSS module badge class
// for the correct semantic color: tool=blue, trigger=teal, channel=purple.
const SERVICE_BADGE_CLASS: Record<string, string> = {
  tool: styles.serviceTool,
  trigger: styles.serviceTrigger,
  channel: styles.serviceChannel,
}

export function PluginCard({
  pluginName,
  pluginVersion,
  pluginId: _pluginId,
  services,
  instanceCount,
  aggregateHealth,
  hasSbom,
  isSelected,
  onClick,
}: PluginCardProps) {
  const instanceLabel = instanceCount === 1 ? '1 instance' : `${instanceCount} instances`

  return (
    <div
      className={`${styles.card} ${isSelected ? styles.cardSelected : ''}`}
      onClick={onClick}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onClick()
        }
      }}
      role="button"
      tabIndex={0}
      aria-label={`${pluginName} plugin, ${instanceLabel}`}
      aria-pressed={isSelected}
    >
      <div className={styles.topRow}>
        <span className={styles.name}>{pluginName}</span>
        {pluginVersion && (
          <span className={styles.version}>{pluginVersion}</span>
        )}
      </div>

      {(services.length > 0 || hasSbom) && (
        <div className={styles.serviceBadges}>
          {services.map((svc) => (
            <span key={svc} className={SERVICE_BADGE_CLASS[svc] ?? styles.serviceTool}>
              {SERVICE_LABEL[svc] ?? svc}
            </span>
          ))}
          {/* Non-interactive badge: the card has role="button", so an <a> inside
              would be an ARIA nesting violation. The clickable download link lives
              in the right-pane detail panel instead. */}
          {hasSbom && (
            <span className={styles.sbomBadge}>SBOM</span>
          )}
        </div>
      )}

      <div className={styles.bottomRow}>
        <span className={styles.instanceCount}>{instanceLabel}</span>
        <PluginHealthChip state={aggregateHealth} />
      </div>
    </div>
  )
}
