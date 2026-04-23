export type RunStatus = 'complete' | 'running' | 'waiting_for_approval' | 'waiting_for_feedback' | 'failed' | 'interrupted' | 'pending'
export type TriggerType = 'webhook' | 'manual' | 'scheduled' | 'poll' | 'cron'

export const KNOWN_STATUSES: Set<string> = new Set([
  'complete', 'running', 'waiting_for_approval', 'waiting_for_feedback', 'failed', 'interrupted', 'pending',
])

export const KNOWN_TRIGGERS: Set<string> = new Set([
  'webhook', 'cron', 'poll', 'manual', 'scheduled',
])

export function isRunStatus(s: string): s is RunStatus {
  return KNOWN_STATUSES.has(s)
}

export function isTriggerType(s: string): s is TriggerType {
  return KNOWN_TRIGGERS.has(s)
}

