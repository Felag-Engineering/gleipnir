import type { ApiRunStep } from '@/api/types'

export type StepType =
  | 'thought'
  | 'thinking'
  | 'tool_call'
  | 'tool_result'
  | 'capability_snapshot'
  | 'error'
  | 'complete'
  | 'approval_request'
  | 'feedback_request'
  | 'feedback_response'

export interface ThoughtContent {
  text: string
}

export interface ThinkingContent {
  text: string
  redacted: boolean
}

export interface ToolCallContent {
  tool_name: string
  server_id: string
  input: Record<string, unknown>
}

export interface ToolResultContent {
  tool_name: string
  output: string
  is_error: boolean
}

export interface ErrorContent {
  message: string
  code: string
}

export interface CompleteContent {
  message: string
}

export interface GrantedToolEntry {
  server_name: string
  tool_name: string
  approval: 'none' | 'required'
  timeout: number
  on_timeout: string
}

// CapabilitySnapshotV2 is the shape written by agent runs after ADR-023.
// Older snapshots written before this change are plain GrantedToolEntry arrays.
// provider is optional for backward compat: snapshots written before issue #352 have model but no provider.
export interface CapabilitySnapshotV2 {
  provider?: string;
  model: string;
  tools: GrantedToolEntry[];
}

export type CapabilitySnapshotContent = GrantedToolEntry[] | CapabilitySnapshotV2

export interface ApprovalRequestContent {
  tool: string
  input: Record<string, unknown>
}

export interface FeedbackRequestContent {
  tool: string
  message?: string
  feedback_id?: string
  expires_at?: string
}

export interface FeedbackResponseContent {
  feedback_id?: string
  response?: string
}

export type ParsedStep =
  | { type: 'thought'; raw: ApiRunStep; content: ThoughtContent }
  | { type: 'thinking'; raw: ApiRunStep; content: ThinkingContent }
  | { type: 'tool_call'; raw: ApiRunStep; content: ToolCallContent }
  | { type: 'tool_result'; raw: ApiRunStep; content: ToolResultContent }
  | { type: 'capability_snapshot'; raw: ApiRunStep; content: CapabilitySnapshotContent }
  | { type: 'error'; raw: ApiRunStep; content: ErrorContent }
  | { type: 'complete'; raw: ApiRunStep; content: CompleteContent }
  | { type: 'approval_request'; raw: ApiRunStep; content: ApprovalRequestContent }
  | { type: 'feedback_request'; raw: ApiRunStep; content: FeedbackRequestContent }
  | { type: 'feedback_response'; raw: ApiRunStep; content: FeedbackResponseContent }
  | { type: 'unknown'; raw: ApiRunStep; content: unknown }

export function isThoughtContent(x: unknown): x is ThoughtContent {
  return typeof x === 'object' && x !== null && typeof (x as Record<string, unknown>).text === 'string'
}

export function isThinkingContent(x: unknown): x is ThinkingContent {
  if (typeof x !== 'object' || x === null) return false
  const r = x as Record<string, unknown>
  return typeof r.text === 'string' && typeof r.redacted === 'boolean'
}

export function isToolCallContent(x: unknown): x is ToolCallContent {
  if (typeof x !== 'object' || x === null) return false
  const r = x as Record<string, unknown>
  return (
    typeof r.tool_name === 'string' &&
    typeof r.server_id === 'string' &&
    typeof r.input === 'object' &&
    r.input !== null
  )
}

export function isToolResultContent(x: unknown): x is ToolResultContent {
  if (typeof x !== 'object' || x === null) return false
  const r = x as Record<string, unknown>
  return (
    typeof r.tool_name === 'string' &&
    typeof r.output === 'string' &&
    typeof r.is_error === 'boolean'
  )
}

export function isErrorContent(x: unknown): x is ErrorContent {
  if (typeof x !== 'object' || x === null) return false
  const r = x as Record<string, unknown>
  return typeof r.message === 'string' && typeof r.code === 'string'
}

export function isCompleteContent(x: unknown): x is CompleteContent {
  return typeof x === 'object' && x !== null && typeof (x as Record<string, unknown>).message === 'string'
}

export function isCapabilitySnapshotContent(x: unknown): x is CapabilitySnapshotContent {
  if (Array.isArray(x)) return true
  if (typeof x !== 'object' || x === null) return false
  const r = x as Record<string, unknown>
  return typeof r.model === 'string' && Array.isArray(r.tools)
}

export function isApprovalRequestContent(x: unknown): x is ApprovalRequestContent {
  if (typeof x !== 'object' || x === null) return false
  const r = x as Record<string, unknown>
  return typeof r.tool === 'string' && typeof r.input === 'object' && r.input !== null
}

export function isFeedbackRequestContent(x: unknown): x is FeedbackRequestContent {
  return typeof x === 'object' && x !== null && typeof (x as Record<string, unknown>).tool === 'string'
}

// FeedbackResponseContent has no required fields — any non-null object is valid.
export function isFeedbackResponseContent(x: unknown): x is FeedbackResponseContent {
  return typeof x === 'object' && x !== null
}

export function parseStep(raw: ApiRunStep): ParsedStep {
  let content: unknown
  try {
    content = JSON.parse(raw.content)
  } catch {
    content = { text: raw.content }
  }

  switch (raw.type) {
    case 'thought':
      if (isThoughtContent(content)) {
        return { type: 'thought', raw, content }
      }
      return { type: 'unknown', raw, content }
    case 'thinking':
      if (isThinkingContent(content)) {
        return { type: 'thinking', raw, content }
      }
      return { type: 'unknown', raw, content }
    case 'tool_call':
      if (isToolCallContent(content)) {
        return { type: 'tool_call', raw, content }
      }
      return { type: 'unknown', raw, content }
    case 'tool_result':
      if (isToolResultContent(content)) {
        return { type: 'tool_result', raw, content }
      }
      return { type: 'unknown', raw, content }
    case 'capability_snapshot':
      if (isCapabilitySnapshotContent(content)) {
        return { type: 'capability_snapshot', raw, content }
      }
      return { type: 'unknown', raw, content }
    case 'error':
      if (isErrorContent(content)) {
        return { type: 'error', raw, content }
      }
      return { type: 'unknown', raw, content }
    case 'complete':
      if (isCompleteContent(content)) {
        return { type: 'complete', raw, content }
      }
      return { type: 'unknown', raw, content }
    case 'approval_request':
      if (isApprovalRequestContent(content)) {
        return { type: 'approval_request', raw, content }
      }
      return { type: 'unknown', raw, content }
    case 'feedback_request':
      if (isFeedbackRequestContent(content)) {
        return { type: 'feedback_request', raw, content }
      }
      return { type: 'unknown', raw, content }
    case 'feedback_response':
      if (isFeedbackResponseContent(content)) {
        return { type: 'feedback_response', raw, content }
      }
      return { type: 'unknown', raw, content }
    default:
      return { type: 'unknown', raw, content }
  }
}

// ToolBlockData combines a tool_call, its tool_result, and any preceding
// approval_request into a single visual unit. At least one of `approval` or
// `call` will be non-null.
//
// Possible states:
//   - approval = null, call set, result null/set  → non-gated tool call
//   - approval set, call set, result null/set      → approval-gated tool call
//   - approval set, call = null                    → denied/timed-out approval
export interface ToolBlockData {
  approval: (ParsedStep & { type: 'approval_request' }) | null
  call: (ParsedStep & { type: 'tool_call' }) | null
  result: (ParsedStep & { type: 'tool_result' }) | null
}

// isToolBlock distinguishes a ToolBlockData from a ParsedStep. ParsedStep always
// has a `raw` property; ToolBlockData never does.
export function isToolBlock(item: ParsedStep | ToolBlockData): item is ToolBlockData {
  return ('call' in item || 'approval' in item) && !('raw' in item)
}

// pairToolBlocks walks steps in order and merges sequential tool-related steps
// into ToolBlockData values. It uses strict positional adjacency — the look-ahead
// only inspects the immediately next unconsumed step and stops at any boundary type.
//
// Backend step ordering for gated tools: approval_request → tool_call → tool_result.
// When approval is denied/timed-out, tool_call is never written, so a standalone
// approval_request with no following matching tool_call is a denied/timed-out gate.
export function pairToolBlocks(steps: ParsedStep[]): (ParsedStep | ToolBlockData)[] {
  const result: (ParsedStep | ToolBlockData)[] = []
  const consumed = new Set<number>()

  // Returns the index of the next unconsumed step after `fromIndex`, or -1 if none.
  function nextUnconsumed(fromIndex: number): number {
    for (let j = fromIndex + 1; j < steps.length; j++) {
      if (!consumed.has(j)) return j
    }
    return -1
  }

  for (let i = 0; i < steps.length; i++) {
    if (consumed.has(i)) continue

    const step = steps[i]

    if (step.type === 'approval_request') {
      const block: ToolBlockData = {
        approval: step as ParsedStep & { type: 'approval_request' },
        call: null,
        result: null,
      }

      const nextIdx = nextUnconsumed(i)
      if (nextIdx !== -1) {
        const nextStep = steps[nextIdx]
        if (
          nextStep.type === 'tool_call' &&
          nextStep.content.tool_name === step.content.tool
        ) {
          // Matching tool_call follows the approval_request — consume it.
          block.call = nextStep as ParsedStep & { type: 'tool_call' }
          consumed.add(nextIdx)

          const afterCallIdx = nextUnconsumed(nextIdx)
          if (afterCallIdx !== -1) {
            const afterCall = steps[afterCallIdx]
            if (
              afterCall.type === 'tool_result' &&
              afterCall.content.tool_name === step.content.tool
            ) {
              block.result = afterCall as ParsedStep & { type: 'tool_result' }
              consumed.add(afterCallIdx)
            }
          }
        }
        // If the next unconsumed step is not a matching tool_call, the approval
        // was denied or timed out. Leave call and result null.
      }

      result.push(block)
      continue
    }

    if (step.type === 'tool_call') {
      const block: ToolBlockData = {
        approval: null,
        call: step as ParsedStep & { type: 'tool_call' },
        result: null,
      }

      const nextIdx = nextUnconsumed(i)
      if (nextIdx !== -1) {
        const nextStep = steps[nextIdx]
        // Only pair if the very next unconsumed step is a matching tool_result.
        // Any boundary type stops the look-ahead because the next step won't be
        // a tool_result, so the condition naturally fails.
        if (
          nextStep.type === 'tool_result' &&
          nextStep.content.tool_name === step.content.tool_name
        ) {
          block.result = nextStep as ParsedStep & { type: 'tool_result' }
          consumed.add(nextIdx)
        }
      }

      result.push(block)
      continue
    }

    // tool_result not consumed by a preceding call or approval — pass through as-is.
    // All other step types also pass through unchanged.
    result.push(step)
  }

  return result
}
