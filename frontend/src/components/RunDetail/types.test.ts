import { describe, it, expect } from 'vitest'
import { parseStep, pairToolBlocks, isToolBlock } from './types'
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
