import { describe, it, expect } from 'vitest'
import { parseStep } from './types'
import type { ApiRunStep } from '@/api/types'

function makeRawStep(type: string, content: unknown): ApiRunStep {
  return {
    id: 'step-1',
    run_id: 'run-1',
    step_number: 1,
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
