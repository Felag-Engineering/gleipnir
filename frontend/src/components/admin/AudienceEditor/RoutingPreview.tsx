import { Bell, MessageSquare } from 'lucide-react'
import type { ApiAudienceEntry } from '@/api/types'
import styles from './RoutingPreview.module.css'

interface Props {
  entries: ApiAudienceEntry[]
  disableInAppFallback?: boolean
}

function entryDisplayName(entry: ApiAudienceEntry): string {
  if (entry.auto) return 'gleipnir.in-app (built-in fallback)'
  return entry.plugin_instance_id || '(unset)'
}

// instanceName is optional so callers without plugin instance data can still use this.
export function RoutingPreview({ entries, disableInAppFallback }: Props) {
  const notifyEntries = entries.filter((e) => e.notify)
  const requestEntry = entries.find((e) => e.request) ?? null

  // If in-app fallback is not disabled and not already in entries, treat it as implicitly appended.
  const hasAutoEntry = entries.some((e) => e.auto)
  const notifyNames = notifyEntries.map((e) => entryDisplayName(e))
  const inAppLabel = 'gleipnir.in-app (built-in fallback)'

  // Append in-app to notify display when it would be auto-injected.
  const notifyDisplay =
    !disableInAppFallback && !hasAutoEntry
      ? [...notifyNames, inAppLabel]
      : notifyNames.length > 0
        ? notifyNames
        : notifyEntries.map((e) => entryDisplayName(e))

  const requestDisplay = requestEntry
    ? entryDisplayName(requestEntry)
    : !disableInAppFallback
      ? inAppLabel
      : null

  return (
    <section className={styles.routingPreview} aria-label="Routing preview">
      <div className={styles.routingRow}>
        <Bell size={14} strokeWidth={1.5} aria-hidden className={styles.routingIcon} />
        <span className={styles.routingLabel}>Notifications fan out to:</span>
        <span className={styles.routingValue}>
          {notifyDisplay.length > 0 ? (
            notifyDisplay.join(', ')
          ) : (
            <em className={styles.muted}>no entries</em>
          )}
        </span>
      </div>
      <div className={styles.routingRow}>
        <MessageSquare size={14} strokeWidth={1.5} aria-hidden className={styles.routingIcon} />
        <span className={styles.routingLabel}>Requests routed to:</span>
        <span className={styles.routingValue}>
          {requestDisplay ?? <em className={styles.muted}>no Request-capable entry</em>}
        </span>
      </div>
    </section>
  )
}
