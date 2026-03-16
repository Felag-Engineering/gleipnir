import { describe, it, expect, vi, afterEach } from 'vitest'
import { fmtDur, fmtTok, fmtAbs, fmtRel, timeLeft } from './styles'

describe('fmtDur', () => {
  it('returns — for null', () => {
    expect(fmtDur(null)).toBe('—')
  })

  it('formats 0 seconds', () => {
    expect(fmtDur(0)).toBe('0s')
  })

  it('formats seconds below 60', () => {
    expect(fmtDur(45)).toBe('45s')
    expect(fmtDur(59)).toBe('59s')
  })

  it('formats exactly 60 seconds as 1m 0s', () => {
    expect(fmtDur(60)).toBe('1m 0s')
  })

  it('formats 90 seconds as 1m 30s', () => {
    expect(fmtDur(90)).toBe('1m 30s')
  })

  it('formats 3661 seconds as 61m 1s', () => {
    expect(fmtDur(3661)).toBe('61m 1s')
  })
})

describe('fmtTok', () => {
  it('formats 0 as "0"', () => {
    expect(fmtTok(0)).toBe('0')
  })

  it('formats 999 as "999"', () => {
    expect(fmtTok(999)).toBe('999')
  })

  it('formats 1000 as "1.0k"', () => {
    expect(fmtTok(1000)).toBe('1.0k')
  })

  it('formats 1500 as "1.5k"', () => {
    expect(fmtTok(1500)).toBe('1.5k')
  })

  it('formats 15000 as "15.0k"', () => {
    expect(fmtTok(15000)).toBe('15.0k')
  })

  it('formats 100000 as "100.0k"', () => {
    expect(fmtTok(100000)).toBe('100.0k')
  })
})

describe('fmtAbs', () => {
  it('produces a string containing the month and day of the date', () => {
    // 2024-06-15T12:00:00Z — timezone-independent assertions on month and day
    const result = fmtAbs('2024-06-15T12:00:00Z')
    expect(result).toContain('Jun')
    expect(result).toContain('15')
  })
})

describe('fmtRel', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('returns "just now" for time less than 1 minute ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 30_000).toISOString() // 30 seconds ago
    expect(fmtRel(iso)).toBe('just now')
  })

  it('returns "5m ago" for 5 minutes ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 5 * 60_000).toISOString()
    expect(fmtRel(iso)).toBe('5m ago')
  })

  it('returns "59m ago" for 59 minutes ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 59 * 60_000).toISOString()
    expect(fmtRel(iso)).toBe('59m ago')
  })

  it('returns "2h ago" for 2 hours ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 2 * 60 * 60_000).toISOString()
    expect(fmtRel(iso)).toBe('2h ago')
  })

  it('returns "23h ago" for 23 hours ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 23 * 60 * 60_000).toISOString()
    expect(fmtRel(iso)).toBe('23h ago')
  })

  it('returns a date string (not "Xh ago") for 25 hours ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 25 * 60 * 60_000).toISOString()
    const result = fmtRel(iso)
    // At 25h, fmtRel returns toLocaleDateString, not "Xh ago"
    expect(result).not.toMatch(/h ago$/)
    expect(result).not.toBe('just now')
  })
})

describe('timeLeft', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('returns 5:00 and urgent:false for 300 seconds remaining', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-01-01T12:00:00Z'))
    const iso = new Date(Date.now() + 300 * 1000).toISOString()
    expect(timeLeft(iso)).toEqual({ str: '5:00', urgent: false })
  })

  it('returns 4:59 and urgent:true for 299 seconds remaining', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-01-01T12:00:00Z'))
    const iso = new Date(Date.now() + 299 * 1000).toISOString()
    expect(timeLeft(iso)).toEqual({ str: '4:59', urgent: true })
  })

  it('returns 0:00 and urgent:true for past/zero time', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-01-01T12:00:00Z'))
    const iso = new Date(Date.now() - 1000).toISOString() // 1 second in the past
    expect(timeLeft(iso)).toEqual({ str: '0:00', urgent: true })
  })

  it('returns 10:00 and urgent:false for 600 seconds remaining', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-01-01T12:00:00Z'))
    const iso = new Date(Date.now() + 600 * 1000).toISOString()
    expect(timeLeft(iso)).toEqual({ str: '10:00', urgent: false })
  })
})
