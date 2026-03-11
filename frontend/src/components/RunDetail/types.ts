import type { ApiRunStep } from '@/api/types'

export type StepType =
  | 'thought'
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
  Role: 'sensor' | 'actuator' | 'feedback'
  Approval: 'none' | 'required'
  Timeout: number
  OnTimeout: string
}

export type CapabilitySnapshotContent = GrantedToolEntry[]

export interface ApprovalRequestContent {
  tool: string
  input: Record<string, unknown>
}

export type ParsedStep =
  | { type: 'thought'; raw: ApiRunStep; content: ThoughtContent }
  | { type: 'tool_call'; raw: ApiRunStep; content: ToolCallContent }
  | { type: 'tool_result'; raw: ApiRunStep; content: ToolResultContent }
  | { type: 'capability_snapshot'; raw: ApiRunStep; content: CapabilitySnapshotContent }
  | { type: 'error'; raw: ApiRunStep; content: ErrorContent }
  | { type: 'complete'; raw: ApiRunStep; content: CompleteContent }
  | { type: 'approval_request'; raw: ApiRunStep; content: ApprovalRequestContent }
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
    default:
      return { type: 'unknown', raw, content }
  }
}
