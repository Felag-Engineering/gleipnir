import type { ApiPolicyListItem } from '@/api/types'

export const BUILTIN_TRIGGER_LABELS: Record<string, string> = {
  webhook: 'Webhook',
  manual: 'Manual',
  scheduled: 'Scheduled',
  poll: 'Poll',
  cron: 'Cron',
}

/**
 * Returns a human-readable pill label for the given policy's trigger type.
 * For subscribed policies, surfaces the event kind and source so the literal
 * word "subscribed" is never shown to operators (ADR-048).
 */
export function triggerPillLabel(
  policy: Pick<ApiPolicyListItem, 'trigger_type' | 'trigger_source' | 'trigger_event_kind'>,
): string {
  if (policy.trigger_type === 'subscribed') {
    if (policy.trigger_event_kind) {
      return policy.trigger_source
        ? `${policy.trigger_event_kind} (${policy.trigger_source})`
        : policy.trigger_event_kind
    }
    return 'Plugin event'
  }
  return BUILTIN_TRIGGER_LABELS[policy.trigger_type] ?? policy.trigger_type
}
