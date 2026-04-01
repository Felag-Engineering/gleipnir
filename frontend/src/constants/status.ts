export type RunStatus = 'complete' | 'running' | 'waiting_for_approval' | 'waiting_for_feedback' | 'failed' | 'interrupted' | 'pending'
export type TriggerType = 'webhook' | 'manual' | 'scheduled'

export const KNOWN_STATUSES: Set<string> = new Set([
  'complete', 'running', 'waiting_for_approval', 'waiting_for_feedback', 'failed', 'interrupted', 'pending',
])

// NOTE: KNOWN_TRIGGERS includes 'cron' and 'poll' from the backend, but TriggerType
// only covers 'webhook' | 'manual' | 'scheduled'. The type union should be expanded
// when cron/poll triggers are fully implemented (v0.3).
export const KNOWN_TRIGGERS: Set<string> = new Set([
  'webhook', 'cron', 'poll', 'manual', 'scheduled',
])

export function isRunStatus(s: string): s is RunStatus {
  return KNOWN_STATUSES.has(s)
}

export function isTriggerType(s: string): s is TriggerType {
  return KNOWN_TRIGGERS.has(s)
}

