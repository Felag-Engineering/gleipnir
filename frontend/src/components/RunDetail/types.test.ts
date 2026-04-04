import { describe, it, expect } from 'vitest'
import {
  parseStep,
  pairToolBlocks,
  isToolBlock,
  isThoughtContent,
  isThinkingContent,
  isToolCallContent,
  isToolResultContent,
  isErrorContent,
  isCompleteContent,
  isCapabilitySnapshotContent,
  isApprovalRequestContent,
  isFeedbackRequestContent,
  isFeedbackResponseContent,
} from './types'
import type { ApiRunStep } from '@/api/types'

let stepIdCounter = 0

function makeRawStep(type: string, content: unknown, stepNumber?: number): ApiRunStep {
  stepIdCounter++
  return {
    id: `step-${stepIdCounter}`,
    run_id: 'run-1',
    step_number: stepNumber ?? stepIdCounter,
    type,
    content: JSON.stringify(content),
    token_cost: 0,
    created_at: '2024-01-01T00:00:00Z',
  }
}

describe('parseStep', () => {
  describe('thinking steps', () => {
    it('parses a thinking step with redacted: false', () => {
      const raw = makeRawStep('thinking', { text: 'Let me reason through this.', redacted: false })
      const step = parseStep(raw)
      expect(step.type).toBe('thinking')
      if (step.type === 'thinking') {
        expect(step.content.text).toBe('Let me reason through this.')
        expect(step.content.redacted).toBe(false)
      }
    })

    it('parses a thinking step with redacted: true', () => {
      const raw = makeRawStep('thinking', { text: '', redacted: true })
      const step = parseStep(raw)
      expect(step.type).toBe('thinking')
      if (step.type === 'thinking') {
        expect(step.content.redacted).toBe(true)
      }
    })
  })

  describe('regression guards', () => {
    it('still parses thought steps correctly', () => {
      const raw = makeRawStep('thought', { text: 'I should check the file.' })
      const step = parseStep(raw)
      expect(step.type).toBe('thought')
      if (step.type === 'thought') {
        expect(step.content.text).toBe('I should check the file.')
      }
    })

    it('falls through to unknown for unrecognised step types', () => {
      const raw = makeRawStep('future_type', { some: 'data' })
      const step = parseStep(raw)
      expect(step.type).toBe('unknown')
    })
  })

  describe('feedback steps', () => {
    it('parses feedback_request with new native format', () => {
      const raw = makeRawStep('feedback_request', { feedback_id: 'fb-1', tool: 'gleipnir.ask_operator', message: 'Reason\n\nContext' })
      const step = parseStep(raw)
      expect(step.type).toBe('feedback_request')
      if (step.type === 'feedback_request') {
        expect(step.content.feedback_id).toBe('fb-1')
        expect(step.content.tool).toBe('gleipnir.ask_operator')
        expect(step.content.message).toBe('Reason\n\nContext')
      }
    })

    it('parses feedback_request with old MCP format (no feedback_id)', () => {
      const raw = makeRawStep('feedback_request', { tool: 'slack.ask_human', message: 'Please confirm' })
      const step = parseStep(raw)
      expect(step.type).toBe('feedback_request')
      if (step.type === 'feedback_request') {
        expect(step.content.feedback_id).toBeUndefined()
        expect(step.content.tool).toBe('slack.ask_human')
        expect(step.content.message).toBe('Please confirm')
      }
    })

    it('parses feedback_response with feedback_id', () => {
      const raw = makeRawStep('feedback_response', { feedback_id: 'fb-1', response: 'Yes' })
      const step = parseStep(raw)
      expect(step.type).toBe('feedback_response')
      if (step.type === 'feedback_response') {
        expect(step.content.feedback_id).toBe('fb-1')
        expect(step.content.response).toBe('Yes')
      }
    })

    it('parses feedback_response without feedback_id (old format)', () => {
      const raw = makeRawStep('feedback_response', { response: 'Looks good' })
      const step = parseStep(raw)
      expect(step.type).toBe('feedback_response')
      if (step.type === 'feedback_response') {
        expect(step.content.feedback_id).toBeUndefined()
        expect(step.content.response).toBe('Looks good')
      }
    })
  })
})

describe('pairToolBlocks', () => {
  it('pairs adjacent tool_call and tool_result by tool_name', () => {
    const steps = [
      parseStep(makeRawStep('tool_call', { tool_name: 'read_file', server_id: 'fs', input: { path: '/tmp/a' } })),
      parseStep(makeRawStep('tool_result', { tool_name: 'read_file', output: '"content"', is_error: false })),
    ]
    const result = pairToolBlocks(steps)
    expect(result).toHaveLength(1)
    expect(isToolBlock(result[0])).toBe(true)
    if (isToolBlock(result[0])) {
      expect(result[0].call?.content.tool_name).toBe('read_file')
      expect(result[0].result?.content.tool_name).toBe('read_file')
      expect(result[0].approval).toBeNull()
    }
  })

  it('leaves tool_call unpaired when no result follows', () => {
    const steps = [
      parseStep(makeRawStep('tool_call', { tool_name: 'read_file', server_id: 'fs', input: {} })),
    ]
    const result = pairToolBlocks(steps)
    expect(result).toHaveLength(1)
    expect(isToolBlock(result[0])).toBe(true)
    if (isToolBlock(result[0])) {
      expect(result[0].call).not.toBeNull()
      expect(result[0].result).toBeNull()
      expect(result[0].approval).toBeNull()
    }
  })

  it('pairs tool_call with error tool_result', () => {
    const steps = [
      parseStep(makeRawStep('tool_call', { tool_name: 'write_file', server_id: 'fs', input: { path: '/tmp/x' } })),
      parseStep(makeRawStep('tool_result', { tool_name: 'write_file', output: 'permission denied', is_error: true })),
    ]
    const result = pairToolBlocks(steps)
    expect(result).toHaveLength(1)
    if (isToolBlock(result[0])) {
      expect(result[0].result?.content.is_error).toBe(true)
    }
  })

  it('pairs approval_request -> tool_call -> tool_result (actual backend ordering)', () => {
    const steps = [
      parseStep(makeRawStep('approval_request', { tool: 'send_slack', input: { channel: '#ops' } })),
      parseStep(makeRawStep('tool_call', { tool_name: 'send_slack', server_id: 'slack', input: { channel: '#ops' } })),
      parseStep(makeRawStep('tool_result', { tool_name: 'send_slack', output: '"ok"', is_error: false })),
    ]
    const result = pairToolBlocks(steps)
    expect(result).toHaveLength(1)
    if (isToolBlock(result[0])) {
      expect(result[0].approval?.content.tool).toBe('send_slack')
      expect(result[0].call?.content.tool_name).toBe('send_slack')
      expect(result[0].result?.content.tool_name).toBe('send_slack')
    }
  })

  it('handles denied/timed-out approval (standalone approval_request, no tool_call)', () => {
    const steps = [
      parseStep(makeRawStep('approval_request', { tool: 'send_slack', input: {} })),
      parseStep(makeRawStep('thought', { text: 'Approval was denied.' })),
    ]
    const result = pairToolBlocks(steps)
    expect(result).toHaveLength(2)
    expect(isToolBlock(result[0])).toBe(true)
    if (isToolBlock(result[0])) {
      expect(result[0].approval?.content.tool).toBe('send_slack')
      expect(result[0].call).toBeNull()
      expect(result[0].result).toBeNull()
    }
    expect(isToolBlock(result[1])).toBe(false)
    if (!isToolBlock(result[1])) {
      expect(result[1].type).toBe('thought')
    }
  })

  it('handles approval_request followed by non-matching tool_call', () => {
    const steps = [
      parseStep(makeRawStep('approval_request', { tool: 'send_slack', input: {} })),
      parseStep(makeRawStep('tool_call', { tool_name: 'read_file', server_id: 'fs', input: {} })),
      parseStep(makeRawStep('tool_result', { tool_name: 'read_file', output: '"content"', is_error: false })),
    ]
    const result = pairToolBlocks(steps)
    expect(result).toHaveLength(2)
    // First block: approval denied (no matching call)
    expect(isToolBlock(result[0])).toBe(true)
    if (isToolBlock(result[0])) {
      expect(result[0].approval?.content.tool).toBe('send_slack')
      expect(result[0].call).toBeNull()
    }
    // Second block: read_file call+result
    expect(isToolBlock(result[1])).toBe(true)
    if (isToolBlock(result[1])) {
      expect(result[1].call?.content.tool_name).toBe('read_file')
      expect(result[1].result?.content.tool_name).toBe('read_file')
    }
  })

  it('passes non-tool steps through unchanged', () => {
    const steps = [
      parseStep(makeRawStep('thought', { text: 'thinking...' })),
      parseStep(makeRawStep('tool_call', { tool_name: 'read_file', server_id: 'fs', input: {} })),
      parseStep(makeRawStep('tool_result', { tool_name: 'read_file', output: '"x"', is_error: false })),
      parseStep(makeRawStep('complete', { message: 'done' })),
    ]
    const result = pairToolBlocks(steps)
    expect(result).toHaveLength(3)
    expect(isToolBlock(result[0])).toBe(false)
    if (!isToolBlock(result[0])) expect(result[0].type).toBe('thought')
    expect(isToolBlock(result[1])).toBe(true)
    expect(isToolBlock(result[2])).toBe(false)
    if (!isToolBlock(result[2])) expect(result[2].type).toBe('complete')
  })

  it('handles multiple calls of the same tool without mispairing', () => {
    const steps = [
      parseStep(makeRawStep('tool_call', { tool_name: 'read_file', server_id: 'fs', input: { path: '/a' } })),
      parseStep(makeRawStep('tool_result', { tool_name: 'read_file', output: '"a"', is_error: false })),
      parseStep(makeRawStep('tool_call', { tool_name: 'read_file', server_id: 'fs', input: { path: '/b' } })),
      parseStep(makeRawStep('tool_result', { tool_name: 'read_file', output: '"b"', is_error: false })),
    ]
    const result = pairToolBlocks(steps)
    expect(result).toHaveLength(2)
    expect(isToolBlock(result[0])).toBe(true)
    expect(isToolBlock(result[1])).toBe(true)
    if (isToolBlock(result[0]) && isToolBlock(result[1])) {
      // Each block has its own call and result — not cross-paired.
      expect(result[0].call?.raw.id).not.toBe(result[1].call?.raw.id)
      expect(result[0].result?.raw.id).not.toBe(result[1].result?.raw.id)
    }
  })

  it('does not pair across a thinking boundary', () => {
    const steps = [
      parseStep(makeRawStep('tool_call', { tool_name: 'read_file', server_id: 'fs', input: {} })),
      parseStep(makeRawStep('thinking', { text: 'reasoning...', redacted: false })),
      parseStep(makeRawStep('tool_result', { tool_name: 'read_file', output: '"x"', is_error: false })),
    ]
    const result = pairToolBlocks(steps)
    expect(result).toHaveLength(3)
    // tool_call stays unpaired because the next unconsumed step is thinking (not a tool_result)
    expect(isToolBlock(result[0])).toBe(true)
    if (isToolBlock(result[0])) {
      expect(result[0].call).not.toBeNull()
      expect(result[0].result).toBeNull()
    }
    expect(isToolBlock(result[1])).toBe(false)
    if (!isToolBlock(result[1])) expect(result[1].type).toBe('thinking')
    // The orphan tool_result passes through as a ParsedStep
    expect(isToolBlock(result[2])).toBe(false)
    if (!isToolBlock(result[2])) expect(result[2].type).toBe('tool_result')
  })

  it('orphan tool_result passes through as ParsedStep', () => {
    const steps = [
      parseStep(makeRawStep('tool_result', { tool_name: 'read_file', output: '"x"', is_error: false })),
    ]
    const result = pairToolBlocks(steps)
    expect(result).toHaveLength(1)
    expect(isToolBlock(result[0])).toBe(false)
    if (!isToolBlock(result[0])) {
      expect(result[0].type).toBe('tool_result')
    }
  })
})

describe('isToolBlock', () => {
  it('returns true for a ToolBlockData object', () => {
    const block = {
      approval: null,
      call: parseStep(makeRawStep('tool_call', { tool_name: 'read_file', server_id: 'fs', input: {} })) as (ReturnType<typeof parseStep> & { type: 'tool_call' }),
      result: null,
    }
    expect(isToolBlock(block)).toBe(true)
  })

  it('returns false for a ParsedStep object', () => {
    const step = parseStep(makeRawStep('thought', { text: 'hello' }))
    expect(isToolBlock(step)).toBe(false)
  })
})

describe('parseStep type guards', () => {
  it('returns unknown when thought content lacks text field', () => {
    const raw = makeRawStep('thought', { notText: 123 })
    const step = parseStep(raw)
    expect(step.type).toBe('unknown')
  })

  it('returns unknown when thinking content has wrong redacted type', () => {
    const raw = makeRawStep('thinking', { text: 'ok', redacted: 'yes' })
    const step = parseStep(raw)
    expect(step.type).toBe('unknown')
  })

  it('returns unknown when tool_call content lacks tool_name', () => {
    const raw = makeRawStep('tool_call', { server_id: 'fs', input: {} })
    const step = parseStep(raw)
    expect(step.type).toBe('unknown')
  })

  it('returns unknown when tool_result content lacks is_error', () => {
    const raw = makeRawStep('tool_result', { tool_name: 'read_file', output: '"x"' })
    const step = parseStep(raw)
    expect(step.type).toBe('unknown')
  })

  it('returns unknown when error content lacks message', () => {
    const raw = makeRawStep('error', { code: 'E001' })
    const step = parseStep(raw)
    expect(step.type).toBe('unknown')
  })

  it('returns unknown when complete content lacks message', () => {
    const raw = makeRawStep('complete', { result: 'done' })
    const step = parseStep(raw)
    expect(step.type).toBe('unknown')
  })

  it('returns unknown when approval_request content lacks input', () => {
    const raw = makeRawStep('approval_request', { tool: 'send_slack' })
    const step = parseStep(raw)
    expect(step.type).toBe('unknown')
  })

  it('returns unknown when feedback_request content lacks tool', () => {
    const raw = makeRawStep('feedback_request', { message: 'hello' })
    const step = parseStep(raw)
    expect(step.type).toBe('unknown')
  })

  it('returns unknown when feedback_response content is not an object', () => {
    const raw = makeRawStep('feedback_response', null)
    // null is JSON-parseable but isFeedbackResponseContent rejects it
    const step = parseStep(raw)
    expect(step.type).toBe('unknown')
  })

  it('still parses all valid content types correctly after adding guards', () => {
    expect(parseStep(makeRawStep('thought', { text: 'hi' })).type).toBe('thought')
    expect(parseStep(makeRawStep('thinking', { text: 'hi', redacted: false })).type).toBe('thinking')
    expect(parseStep(makeRawStep('tool_call', { tool_name: 'f', server_id: 's', input: {} })).type).toBe('tool_call')
    expect(parseStep(makeRawStep('tool_result', { tool_name: 'f', output: '"x"', is_error: false })).type).toBe('tool_result')
    expect(parseStep(makeRawStep('error', { message: 'oops', code: 'E1' })).type).toBe('error')
    expect(parseStep(makeRawStep('complete', { message: 'done' })).type).toBe('complete')
    expect(parseStep(makeRawStep('approval_request', { tool: 't', input: {} })).type).toBe('approval_request')
    expect(parseStep(makeRawStep('feedback_request', { tool: 't' })).type).toBe('feedback_request')
    expect(parseStep(makeRawStep('feedback_response', {})).type).toBe('feedback_response')
  })
})

describe('type guard functions', () => {
  describe('isThoughtContent', () => {
    it('returns false for non-objects', () => {
      expect(isThoughtContent(null)).toBe(false)
      expect(isThoughtContent(42)).toBe(false)
      expect(isThoughtContent('string')).toBe(false)
      expect(isThoughtContent(undefined)).toBe(false)
    })

    it('returns false when text field is missing', () => {
      expect(isThoughtContent({ notText: 'hi' })).toBe(false)
    })

    it('returns true for valid shape', () => {
      expect(isThoughtContent({ text: 'hello' })).toBe(true)
    })
  })

  describe('isThinkingContent', () => {
    it('returns false when redacted is not boolean', () => {
      expect(isThinkingContent({ text: 'ok', redacted: 'yes' })).toBe(false)
      expect(isThinkingContent({ text: 'ok', redacted: 1 })).toBe(false)
    })

    it('returns true for valid shape', () => {
      expect(isThinkingContent({ text: 'ok', redacted: false })).toBe(true)
    })
  })

  describe('isCapabilitySnapshotContent', () => {
    it('returns true for V1 array shape', () => {
      expect(isCapabilitySnapshotContent([])).toBe(true)
      expect(isCapabilitySnapshotContent([{ ServerName: 's', ToolName: 't' }])).toBe(true)
    })

    it('returns true for V2 object shape with model and tools', () => {
      expect(isCapabilitySnapshotContent({ model: 'claude-3', tools: [] })).toBe(true)
      expect(isCapabilitySnapshotContent({ provider: 'anthropic', model: 'claude-3', tools: [] })).toBe(true)
    })

    it('returns false when neither shape matches', () => {
      expect(isCapabilitySnapshotContent(null)).toBe(false)
      expect(isCapabilitySnapshotContent({ model: 'claude-3' })).toBe(false)
      expect(isCapabilitySnapshotContent({ tools: [] })).toBe(false)
    })
  })

  describe('isToolCallContent', () => {
    it('returns false when required fields are missing', () => {
      expect(isToolCallContent({ server_id: 'fs', input: {} })).toBe(false)
      expect(isToolCallContent({ tool_name: 'f', input: {} })).toBe(false)
      expect(isToolCallContent({ tool_name: 'f', server_id: 'fs' })).toBe(false)
    })

    it('returns true for valid shape', () => {
      expect(isToolCallContent({ tool_name: 'f', server_id: 'fs', input: {} })).toBe(true)
    })
  })

  describe('isErrorContent', () => {
    it('returns false when message or code is missing', () => {
      expect(isErrorContent({ code: 'E1' })).toBe(false)
      expect(isErrorContent({ message: 'oops' })).toBe(false)
    })

    it('returns true for valid shape', () => {
      expect(isErrorContent({ message: 'oops', code: 'E1' })).toBe(true)
    })
  })

  describe('isApprovalRequestContent', () => {
    it('returns false when tool or input is missing', () => {
      expect(isApprovalRequestContent({ tool: 'send_slack' })).toBe(false)
      expect(isApprovalRequestContent({ input: {} })).toBe(false)
    })

    it('returns true for valid shape', () => {
      expect(isApprovalRequestContent({ tool: 'send_slack', input: {} })).toBe(true)
    })
  })

  describe('isFeedbackRequestContent', () => {
    it('returns false when tool is missing', () => {
      expect(isFeedbackRequestContent({ message: 'hello' })).toBe(false)
    })

    it('returns true when tool is present (other fields are optional)', () => {
      expect(isFeedbackRequestContent({ tool: 'ask_operator' })).toBe(true)
      expect(isFeedbackRequestContent({ tool: 'ask_operator', message: 'hi', feedback_id: 'fb-1' })).toBe(true)
    })
  })

  describe('isFeedbackResponseContent', () => {
    it('returns false for null and non-objects', () => {
      expect(isFeedbackResponseContent(null)).toBe(false)
      expect(isFeedbackResponseContent(42)).toBe(false)
    })

    it('returns true for any non-null object (both fields are optional)', () => {
      expect(isFeedbackResponseContent({})).toBe(true)
      expect(isFeedbackResponseContent({ feedback_id: 'fb-1', response: 'yes' })).toBe(true)
    })
  })

  describe('isToolResultContent', () => {
    it('returns false when required fields are missing', () => {
      expect(isToolResultContent({ tool_name: 'f', output: '"x"' })).toBe(false)
      expect(isToolResultContent({ output: '"x"', is_error: false })).toBe(false)
    })

    it('returns true for valid shape', () => {
      expect(isToolResultContent({ tool_name: 'f', output: '"x"', is_error: false })).toBe(true)
    })
  })

  describe('isCompleteContent', () => {
    it('returns false when message is missing', () => {
      expect(isCompleteContent({ result: 'done' })).toBe(false)
    })

    it('returns true for valid shape', () => {
      expect(isCompleteContent({ message: 'done' })).toBe(true)
    })
  })
})
