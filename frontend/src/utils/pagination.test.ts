import { describe, it, expect, vi, afterEach } from 'vitest'
import { computePageNumbers, rangeToSince } from './pagination'

describe('computePageNumbers', () => {
  it('returns [1] for a single page', () => {
    expect(computePageNumbers(1, 1)).toEqual([1])
  })

  it('returns all pages when totalPages <= 7', () => {
    expect(computePageNumbers(1, 5)).toEqual([1, 2, 3, 4, 5])
    expect(computePageNumbers(3, 7)).toEqual([1, 2, 3, 4, 5, 6, 7])
  })

  it('inserts ellipsis when current page is in the middle of many pages', () => {
    const result = computePageNumbers(5, 10)
    expect(result).toEqual([1, 'ellipsis', 4, 5, 6, 'ellipsis', 10])
  })

  it('no leading ellipsis when current page is near the start', () => {
    const result = computePageNumbers(2, 10)
    expect(result[0]).toBe(1)
    expect(result[1]).toBe(2)
    expect(result[2]).toBe(3)
    // Should have trailing ellipsis since 4..9 are skipped
    expect(result).toContain('ellipsis')
    expect(result[result.length - 1]).toBe(10)
  })

  it('no trailing ellipsis when current page is near the end', () => {
    const result = computePageNumbers(9, 10)
    expect(result[0]).toBe(1)
    expect(result[result.length - 1]).toBe(10)
    expect(result[result.length - 2]).toBe(9)
    expect(result[result.length - 3]).toBe(8)
  })

  it('first page is always 1 and last page is always totalPages', () => {
    const result = computePageNumbers(5, 20)
    expect(result[0]).toBe(1)
    expect(result[result.length - 1]).toBe(20)
  })
})

describe('rangeToSince', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('returns undefined for "all"', () => {
    expect(rangeToSince('all')).toBeUndefined()
  })

  it('returns undefined for unrecognized keys', () => {
    expect(rangeToSince('')).toBeUndefined()
    expect(rangeToSince('2w')).toBeUndefined()
  })

  it('returns an ISO string 1 hour in the past for "1h"', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-01-01T12:00:00Z'))
    const result = rangeToSince('1h')
    expect(result).toBe('2024-01-01T11:00:00.000Z')
  })

  it('returns an ISO string 24 hours in the past for "24h"', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-01-02T12:00:00Z'))
    const result = rangeToSince('24h')
    expect(result).toBe('2024-01-01T12:00:00.000Z')
  })

  it('returns an ISO string 7 days in the past for "7d"', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-01-08T12:00:00Z'))
    const result = rangeToSince('7d')
    expect(result).toBe('2024-01-01T12:00:00.000Z')
  })
})
