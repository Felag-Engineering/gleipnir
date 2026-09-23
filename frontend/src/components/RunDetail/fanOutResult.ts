// Detects and parses Relay's per-Node fan-out execution result, returned by
// run_operation, raw_exec and get_job. These types mirror Relay's PerNodeResult /
// ExecutionOutput / RunOrPlanOutput (relay/internal/controlplane/mcp/wire.go).
export interface FanOutRow {
  node_id: string
  outcome: string
  exit_code?: number
  stdout: string
  stderr: string
  stdout_truncated: boolean
  stderr_truncated: boolean
  duration_ms: number
  refusal_explanation?: string
}

export interface FanOutResult {
  job_id: string
  results: FanOutRow[]
}

const REQUIRED_ROW_KEYS = [
  'node_id',
  'outcome',
  'stdout',
  'stderr',
  'stdout_truncated',
  'stderr_truncated',
  'duration_ms',
] as const

const OPTIONAL_ROW_KEYS = ['exit_code', 'refusal_explanation'] as const

const ALLOWED_ROW_KEYS = new Set<string>([...REQUIRED_ROW_KEYS, ...OPTIONAL_ROW_KEYS])

const ALLOWED_TOP_LEVEL_KEYS = new Set(['job_id', 'results'])

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isFanOutRow(value: unknown): value is FanOutRow {
  if (!isPlainObject(value)) return false

  // Any unknown key rejects the whole result, so no field is silently dropped
  // by the table.
  for (const key of Object.keys(value)) {
    if (!ALLOWED_ROW_KEYS.has(key)) return false
  }

  if (
    typeof value.node_id !== 'string' ||
    typeof value.outcome !== 'string' ||
    typeof value.stdout !== 'string' ||
    typeof value.stderr !== 'string'
  ) {
    return false
  }
  if (typeof value.stdout_truncated !== 'boolean' || typeof value.stderr_truncated !== 'boolean') {
    return false
  }
  if (typeof value.duration_ms !== 'number' || !Number.isFinite(value.duration_ms)) {
    return false
  }
  if ('exit_code' in value && !Number.isInteger(value.exit_code)) {
    return false
  }
  if ('refusal_explanation' in value && typeof value.refusal_explanation !== 'string') {
    return false
  }

  return true
}

// parseFanOutResult detects Relay's fan-out shape in the value parseToolOutput
// already returns. Detection is shape-only, not tool-name-based, and every
// predicate closes over its key set: an unrecognized field anywhere sends the
// whole result to the fallback JSON rendering instead of being dropped.
export function parseFanOutResult(output: unknown): FanOutResult | null {
  let candidate: unknown
  if (typeof output === 'string') {
    const trimmed = output.trim()
    if (!trimmed.startsWith('{')) return null
    try {
      candidate = JSON.parse(trimmed)
    } catch {
      return null
    }
  } else if (isPlainObject(output)) {
    candidate = output
  } else {
    return null
  }

  if (!isPlainObject(candidate)) return null

  for (const key of Object.keys(candidate)) {
    if (!ALLOWED_TOP_LEVEL_KEYS.has(key)) return null
  }

  // job_id is present only when something was actually dispatched. Plan and
  // parked responses omit it, so they fall back to today's rendering.
  if (typeof candidate.job_id !== 'string' || candidate.job_id === '') return null

  if (!Array.isArray(candidate.results)) return null
  if (!candidate.results.every(isFanOutRow)) return null

  return { job_id: candidate.job_id, results: candidate.results as FanOutRow[] }
}

export type OutcomeTone = 'ok' | 'policy' | 'failed' | 'notRun' | 'unknown'

// outcomeTone is not restricted to the outcomes Relay documents today. An
// unrecognized outcome string maps to 'unknown' and is shown verbatim, so
// accepting it hides nothing and a new Relay outcome doesn't break detection.
export function outcomeTone(outcome: string): OutcomeTone {
  switch (outcome) {
    case 'success':
      return 'ok'
    case 'denied_by_policy':
      return 'policy'
    case 'failure':
    case 'timeout':
      return 'failed'
    case 'unreachable':
    case 'version_refused':
    case 'unsupported':
    case 'cancelled':
      return 'notRun'
    default:
      return 'unknown'
  }
}

export function outcomeLabel(outcome: string): string {
  switch (outcome) {
    case 'denied_by_policy':
      return 'Denied by policy'
    case 'version_refused':
      return 'Version refused'
    case 'success':
      return 'Success'
    case 'failure':
      return 'Failed'
    case 'timeout':
      return 'Timed out'
    case 'unreachable':
      return 'Unreachable'
    case 'unsupported':
      return 'Unsupported'
    case 'cancelled':
      return 'Cancelled'
    default:
      return outcome
  }
}

// OUTCOME_ORDER puts exceptions first, so "here are the 3" reads at the top
// of the table.
export const OUTCOME_ORDER = [
  'denied_by_policy',
  'failure',
  'timeout',
  'unreachable',
  'version_refused',
  'unsupported',
  'cancelled',
  'success',
] as const

// groupByOutcome buckets rows by outcome in OUTCOME_ORDER, with unknown
// outcomes surfaced before 'success' in first-seen order. Relay's node_id
// order (already sorted) is kept within each group, so the sort is stable.
// Empty groups are omitted.
export function groupByOutcome(rows: FanOutRow[]): { outcome: string; rows: FanOutRow[] }[] {
  const byOutcome = new Map<string, FanOutRow[]>()
  const unknownOrder: string[] = []

  for (const row of rows) {
    if (!byOutcome.has(row.outcome)) {
      byOutcome.set(row.outcome, [])
      if (!(OUTCOME_ORDER as readonly string[]).includes(row.outcome)) {
        unknownOrder.push(row.outcome)
      }
    }
    byOutcome.get(row.outcome)!.push(row)
  }

  const order = [
    ...OUTCOME_ORDER.filter((o) => o !== 'success'),
    ...unknownOrder,
    'success',
  ]

  return order
    .filter((outcome) => byOutcome.has(outcome))
    .map((outcome) => ({ outcome, rows: byOutcome.get(outcome)! }))
}
