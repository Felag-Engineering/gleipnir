// Detects and parses Relay's per-Node fan-out execution result, returned by
// run_operation, raw_exec and get_job. These types mirror Relay's PerNodeResult /
// ExecutionOutput / RunOrPlanOutput (relay/internal/controlplane/mcp/wire.go),
// plus the decision / answer_refused blocks an answered approval retry carries
// (ApprovalDecisionOutput / AnswerRefusalOutput, tools_approval.go).
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

// FanOutDecision mirrors Relay's ApprovalDecisionOutput: the decision block an
// answered MRTR retry carries beside the Job it released. Relay always emits
// every field (none are omitempty), so all six are required here.
export interface FanOutDecision {
  request_id: string
  state: string
  progress: string
  channel: string
  on_behalf_of: string
  next_step: string
}

// FanOutAnswerRefusal mirrors Relay's AnswerRefusalOutput: an answered retry
// whose answer Relay refused before (or instead of) reaching a decision.
// reason and next_step are always sent; the rest are omitempty on Relay's side.
export interface FanOutAnswerRefusal {
  reason: string
  next_step: string
  request_id?: string
  state?: string
  job_id?: string
  expires_at?: string
}

// FanOutResult is either a dispatch (job_id set, one row per Node) or, on the
// answered retry of an in-band approval, an outcome where nothing was
// dispatched (job_id absent, results empty). Relay's own contract: "an absent
// job_id always means nothing was dispatched, never a failed id".
export interface FanOutResult {
  job_id?: string
  results: FanOutRow[]
  // Present only on the answered retry of an in-band approval: what the human
  // decided, beside the Job it released (approved) or with nothing dispatched
  // (denied).
  decision?: FanOutDecision
  // Present only when that answered retry's answer was refused. Never beside
  // a job_id.
  answer_refused?: FanOutAnswerRefusal
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

const ALLOWED_TOP_LEVEL_KEYS = new Set(['job_id', 'results', 'decision', 'answer_refused'])

const DECISION_KEYS = [
  'request_id',
  'state',
  'progress',
  'channel',
  'on_behalf_of',
  'next_step',
] as const

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

// isFanOutDecision holds the decision block to the same closed-key rule as a
// row: an unrecognized or missing field sends the whole result to the
// fallback rendering rather than being dropped.
function isFanOutDecision(value: unknown): value is FanOutDecision {
  if (!isPlainObject(value)) return false
  const keys = Object.keys(value)
  if (keys.length !== DECISION_KEYS.length) return false
  return DECISION_KEYS.every((key) => typeof value[key] === 'string')
}

const REQUIRED_REFUSAL_KEYS = ['reason', 'next_step'] as const
const OPTIONAL_REFUSAL_KEYS = ['request_id', 'state', 'job_id', 'expires_at'] as const
const ALLOWED_REFUSAL_KEYS = new Set<string>([...REQUIRED_REFUSAL_KEYS, ...OPTIONAL_REFUSAL_KEYS])

// isFanOutAnswerRefusal holds answer_refused to the same closed-key rule.
// Every field Relay sends is a string.
function isFanOutAnswerRefusal(value: unknown): value is FanOutAnswerRefusal {
  if (!isPlainObject(value)) return false
  for (const key of Object.keys(value)) {
    if (!ALLOWED_REFUSAL_KEYS.has(key)) return false
    if (typeof value[key] !== 'string') return false
  }
  return REQUIRED_REFUSAL_KEYS.every((key) => key in value)
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

  if (!Array.isArray(candidate.results)) return null
  if ('decision' in candidate && !isFanOutDecision(candidate.decision)) return null
  if ('answer_refused' in candidate && !isFanOutAnswerRefusal(candidate.answer_refused)) return null

  if ('job_id' in candidate) {
    // A dispatch. Relay omits job_id rather than sending it empty, so an empty
    // or non-string one is not a shape we know.
    if (typeof candidate.job_id !== 'string' || candidate.job_id === '') return null
    if (!candidate.results.every(isFanOutRow)) return null
    // Relay renders answer_refused only on a call that dispatched nothing.
    if ('answer_refused' in candidate) return null

    const parsed: FanOutResult = { job_id: candidate.job_id, results: candidate.results as FanOutRow[] }
    if ('decision' in candidate) parsed.decision = candidate.decision as FanOutDecision
    return parsed
  }

  // No job_id: nothing was dispatched. The only such shapes rendered here are
  // an answered retry's outcome: a decision (for example a denial) or a
  // refused answer, exactly one of them, with an empty results array. Plan
  // and parked responses carry keys outside the allowed set and were already
  // sent to the fallback rendering above.
  if (candidate.results.length !== 0) return null
  const hasDecision = 'decision' in candidate
  const hasRefusal = 'answer_refused' in candidate
  if (hasDecision === hasRefusal) return null

  return hasDecision
    ? { results: [], decision: candidate.decision as FanOutDecision }
    : { results: [], answer_refused: candidate.answer_refused as FanOutAnswerRefusal }
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
