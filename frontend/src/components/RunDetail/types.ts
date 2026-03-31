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
  ServerName: string
  ToolName: string
  Role: 'tool' | 'feedback'
  Approval: 'none' | 'required'
  Timeout: number
  OnTimeout: string
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
}

export interface FeedbackResponseContent {
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

export function parseStep(raw: ApiRunStep): ParsedStep {
  let content: unknown
  try {
    content = JSON.parse(raw.content)
  } catch {
    content = { text: raw.content }
  }

  switch (raw.type) {
    case 'thought':
      return { type: 'thought', raw, content: content as ThoughtContent }
    case 'thinking':
      return { type: 'thinking', raw, content: content as ThinkingContent }
    case 'tool_call':
      return { type: 'tool_call', raw, content: content as ToolCallContent }
    case 'tool_result':
      return { type: 'tool_result', raw, content: content as ToolResultContent }
    case 'capability_snapshot':
      return { type: 'capability_snapshot', raw, content: content as CapabilitySnapshotContent }
    case 'error':
      return { type: 'error', raw, content: content as ErrorContent }
    case 'complete':
      return { type: 'complete', raw, content: content as CompleteContent }
    case 'approval_request':
      return { type: 'approval_request', raw, content: content as ApprovalRequestContent }
    case 'feedback_request':
      return { type: 'feedback_request', raw, content: content as FeedbackRequestContent }
    case 'feedback_response':
      return { type: 'feedback_response', raw, content: content as FeedbackResponseContent }
    default:
      return { type: 'unknown', raw, content }
  }
}
