import type { FormState } from './agentEditorUtils'
import type { PollTriggerState, ScheduledTriggerState } from './FormMode/types'

export interface FormIssue {
  field: string
  message: string
}

// parseGoDuration parses a Go duration string (e.g. "5m", "1h30m") and returns
// the total duration in milliseconds, or null if the string is not parseable.
// Supports: ms, s, m, h — the units used in Gleipnir policy YAML.
export function parseGoDuration(s: string): number | null {
  if (!s || s.trim() === '') return null
  // All groups are optional, so the regex matches "" — guard against that with match[0] === ''.
  const pattern = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?(?:(\d+)ms)?$/
  const match = s.trim().match(pattern)
  if (!match || match[0] === '') return null
  const [, h, m, sec, ms] = match
  const total =
    (parseInt(h ?? '0') * 3600 +
      parseInt(m ?? '0') * 60 +
      parseInt(sec ?? '0')) *
      1000 +
    parseInt(ms ?? '0')
  return total
}

// isValidToolRef checks that the string uses dot-notation (server.tool).
function isValidToolRef(ref: string): boolean {
  const parts = ref.split('.')
  return parts.length >= 2 && parts[0] !== '' && parts.slice(1).join('.') !== ''
}

const VALID_CONCURRENCY = ['skip', 'queue', 'parallel', 'replace'] as const
const VALID_COMPARATORS = ['equals', 'not_equals', 'greater_than', 'less_than', 'contains'] as const

/**
 * Validates a FormState against the rules mirrored from internal/policy/validator.go.
 * Field paths are identical to the backend so server issues and client issues
 * collapse on the same field in the ErrorBanner.
 *
 * Rules NOT replicated here (backend-only fallback):
 * - claude-code legacy provider (form constrains provider via a select)
 * - trigger.auth enum (form constrains via radio buttons)
 * - capabilities.tools[i].approval enum (form is a boolean toggle)
 * - capabilities.feedback.on_timeout enum (form hard-codes "fail")
 */
export function validateFormState(state: FormState): FormIssue[] {
  const issues: FormIssue[] = []

  function add(field: string, message: string) {
    issues.push({ field, message })
  }

  // --- Identity ---
  if (state.identity.name.trim() === '') {
    add('name', 'name is required')
  }

  // --- Trigger ---
  const trigger = state.trigger
  if (trigger.type === 'scheduled') {
    const scheduled = trigger as ScheduledTriggerState
    if (scheduled.fireAt.length === 0) {
      add(
        'trigger.fire_at',
        'trigger.fire_at is required for scheduled triggers and must contain at least one timestamp',
      )
    }
  }

  if (trigger.type === 'poll') {
    const poll = trigger as PollTriggerState
    const intervalMs = parseGoDuration(poll.interval)
    if (intervalMs === null || intervalMs <= 0) {
      add(
        'trigger.interval',
        'trigger.interval is required for poll triggers and must be a positive duration (e.g. "5m", "1h")',
      )
    } else if (intervalMs < 60 * 1000) {
      add('trigger.interval', 'trigger.interval must be at least 1m to prevent excessive polling')
    }

    if (poll.checks.length === 0) {
      add('trigger.checks', 'trigger.checks is required for poll triggers and must contain at least one check')
    }

    for (let i = 0; i < poll.checks.length; i++) {
      const c = poll.checks[i]
      if (c.tool === '') {
        add(`trigger.checks[${i}].tool`, `trigger.checks[${i}].tool is required`)
      } else if (!isValidToolRef(c.tool)) {
        add(
          `trigger.checks[${i}].tool`,
          `trigger.checks[${i}].tool "${c.tool}" must use dot notation (server_name.tool_name)`,
        )
      }
      if (c.path === '') {
        add(`trigger.checks[${i}].path`, `trigger.checks[${i}].path is required`)
      }
      if (!VALID_COMPARATORS.includes(c.comparator as typeof VALID_COMPARATORS[number])) {
        add(
          `trigger.checks[${i}].comparator`,
          `trigger.checks[${i}] must specify exactly one comparator (equals, not_equals, greater_than, less_than, contains)`,
        )
      }
    }
  }

  // --- Capabilities ---
  const { tools, feedback } = state.capabilities
  if (tools.length === 0 && !feedback.enabled) {
    add('capabilities', 'at least one capability is required (tool or feedback)')
  }

  const seen = new Set<string>()
  for (let i = 0; i < tools.length; i++) {
    const t = tools[i]
    const toolRef = `${t.serverName}.${t.name}`
    if (toolRef === '.' || t.serverName === '' || t.name === '') {
      add(`capabilities.tools[${i}].tool`, `capabilities.tools[${i}].tool is required`)
    } else if (!isValidToolRef(toolRef)) {
      add(
        `capabilities.tools[${i}].tool`,
        `capabilities.tools[${i}].tool "${toolRef}" must use dot notation (server_name.tool_name)`,
      )
    } else if (seen.has(toolRef)) {
      add(
        `capabilities.tools[${i}].tool`,
        `capabilities.tools[${i}].tool "${toolRef}" is a duplicate`,
      )
    }
    seen.add(toolRef)

    if (t.approvalRequired && t.approvalTimeout !== '') {
      if (parseGoDuration(t.approvalTimeout) === null) {
        add(
          `capabilities.tools[${i}].timeout`,
          `capabilities.tools[${i}].timeout "${t.approvalTimeout}" is not a valid duration`,
        )
      }
    }
  }

  if (feedback.enabled && feedback.timeout !== '') {
    if (parseGoDuration(feedback.timeout) === null) {
      add(
        'capabilities.feedback.timeout',
        `capabilities.feedback.timeout "${feedback.timeout}" is not a valid duration`,
      )
    }
  }

  // --- Task ---
  if (state.task.task.trim() === '') {
    add('agent.task', 'agent.task is required')
  }

  // --- Model ---
  if (state.model.provider === '') {
    add(
      'model.provider',
      'model.provider is required (set a default in Admin → Models or specify model.provider in policy YAML)',
    )
  }
  if (state.model.model === '') {
    add(
      'model.name',
      'model.name is required (set a default in Admin → Models or specify model.name in policy YAML)',
    )
  }

  // --- Run Limits ---
  if (state.limits.max_tokens_per_run <= 0) {
    add('agent.limits.max_tokens_per_run', 'agent.limits.max_tokens_per_run must be positive')
  }
  if (state.limits.max_tool_calls_per_run <= 0) {
    add('agent.limits.max_tool_calls_per_run', 'agent.limits.max_tool_calls_per_run must be positive')
  }

  // --- Concurrency ---
  if (!VALID_CONCURRENCY.includes(state.concurrency.concurrency)) {
    add(
      'agent.concurrency',
      `agent.concurrency "${state.concurrency.concurrency}" is invalid; must be skip, queue, parallel, or replace`,
    )
  }
  if (state.concurrency.queueDepth < 0) {
    add('agent.queue_depth', 'agent.queue_depth must not be negative')
  }

  // Cross-validation: replace is incompatible with approval-required tools.
  if (state.concurrency.concurrency === 'replace') {
    if (tools.some(t => t.approvalRequired)) {
      add(
        'agent.concurrency',
        'agent.concurrency "replace" is not valid when any tool has approval: required',
      )
    }
  }

  return issues
}
