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
})
