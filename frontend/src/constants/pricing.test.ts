import { describe, it, expect } from 'vitest'
import { estimateCost, MODEL_PRICING } from '@/constants/pricing'

describe('estimateCost', () => {
  it('returns 0 for unknown model', () => {
    expect(estimateCost('Unknown Model', 1000)).toBe(0)
  })

  it('returns 0 for zero tokens', () => {
    expect(estimateCost('Sonnet 4', 0)).toBe(0)
  })

  it('calculates blended cost for Sonnet 4', () => {
    const rates = MODEL_PRICING['Sonnet 4']
    const blended = (rates.input + rates.output) / 2
    const tokens = 1_000_000
    expect(estimateCost('Sonnet 4', tokens)).toBeCloseTo(blended * tokens, 10)
  })

  it('calculates blended cost for Haiku 3.5', () => {
    const rates = MODEL_PRICING['Haiku 3.5']
    const blended = (rates.input + rates.output) / 2
    const tokens = 500_000
    expect(estimateCost('Haiku 3.5', tokens)).toBeCloseTo(blended * tokens, 10)
  })

  it('calculates blended cost for Opus 4', () => {
    const rates = MODEL_PRICING['Opus 4']
    const blended = (rates.input + rates.output) / 2
    const tokens = 100_000
    expect(estimateCost('Opus 4', tokens)).toBeCloseTo(blended * tokens, 10)
  })

  it('Sonnet 4 is cheaper than Opus 4 per token', () => {
    const cost4 = estimateCost('Sonnet 4', 1_000_000)
    const costOpus = estimateCost('Opus 4', 1_000_000)
    expect(cost4).toBeLessThan(costOpus)
  })

  // Regression tests for issue #669: sub-penny runs were showing $0.00 because
  // new model names had no pricing entries.

  it('Haiku 4.5 with ~2200 tokens returns non-zero cost', () => {
    expect(estimateCost('Haiku 4.5', 2200)).toBeGreaterThan(0)
  })

  it('Sonnet 4.6 with ~1000 tokens returns non-zero cost', () => {
    expect(estimateCost('Sonnet 4.6', 1000)).toBeGreaterThan(0)
  })

  it('Opus 4.8 with ~1000 tokens returns non-zero cost', () => {
    expect(estimateCost('Opus 4.8', 1000)).toBeGreaterThan(0)
  })

  it('Opus 4.7 with ~1000 tokens returns non-zero cost', () => {
    expect(estimateCost('Opus 4.7', 1000)).toBeGreaterThan(0)
  })

  it('GPT-5 Nano with ~1400 tokens returns non-zero cost', () => {
    expect(estimateCost('GPT-5 Nano', 1400)).toBeGreaterThan(0)
  })

  it('Gemini 2.5 Flash-Lite with ~576 tokens returns non-zero cost', () => {
    expect(estimateCost('Gemini 2.5 Flash-Lite', 576)).toBeGreaterThan(0)
  })

  it('Gemini 2.5 Pro with ~5000 tokens returns non-zero cost', () => {
    expect(estimateCost('Gemini 2.5 Pro', 5000)).toBeGreaterThan(0)
  })

  it('GPT-4.1 Mini is cheaper than GPT-4.1 per token', () => {
    expect(estimateCost('GPT-4.1 Mini', 1_000_000)).toBeLessThan(
      estimateCost('GPT-4.1', 1_000_000),
    )
  })

  // Every curated model's display name (internal/http/api/modelnames.go) needs
  // a pricing entry, or its runs show $0.00 on the cost chart (issue #669).
  it.each([
    'Fable 5.1', 'Fable 5', 'Opus 5.5', 'Opus 5', 'Sonnet 5.5', 'Sonnet 5',
    'GPT-6 Astra', 'GPT-6.1 Sol', 'GPT-6 Sol', 'GPT-6 Luna',
    'GPT-5.6 Sol', 'GPT-5.6 Terra', 'GPT-5.6 Luna',
    'GPT-5.5', 'GPT-5.4', 'GPT-5.4 Mini', 'GPT-5.4 Nano',
    'Gemini 3.1 Pro', 'Gemini 3.8 Flash', 'Gemini 3.7 Flash', 'Gemini 3.6 Flash',
    'Gemini 3.5 Flash', 'Gemini 3.5 Flash-Lite', 'Gemini 3.1 Flash-Lite',
  ])('%s has a non-zero price', (name) => {
    expect(estimateCost(name, 1000)).toBeGreaterThan(0)
  })
})
