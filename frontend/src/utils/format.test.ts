import { describe, it, expect, vi, afterEach } from 'vitest'
import { formatDuration, formatDurationMs, formatTokens, formatTimestamp, formatTimeAgo, formatCountdown, computeRunDuration, formatDate, getPreferredTimezone } from '@/utils/format'

describe('formatDuration', () => {
  it('returns — for null', () => {
    expect(formatDuration(null)).toBe('—')
  })

  it('formats 0 seconds', () => {
    expect(formatDuration(0)).toBe('0s')
  })

  it('formats seconds below 60', () => {
    expect(formatDuration(45)).toBe('45s')
    expect(formatDuration(59)).toBe('59s')
  })

  it('formats exactly 60 seconds as 1m 0s', () => {
    expect(formatDuration(60)).toBe('1m 0s')
  })

  it('formats 90 seconds as 1m 30s', () => {
    expect(formatDuration(90)).toBe('1m 30s')
  })

  it('formats 3661 seconds as 61m 1s', () => {
    expect(formatDuration(3661)).toBe('61m 1s')
  })
})

describe('readStorageKey (via getPreferredTimezone)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('logs console.warn when localStorage.getItem throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('storage unavailable')
    })
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})

    getPreferredTimezone()

    expect(warnSpy).toHaveBeenCalledOnce()
    expect(warnSpy).toHaveBeenCalledWith(
      'localStorage read failed for key',
      'gleipnir-timezone',
      expect.any(Error),
    )
  })
})

describe('formatDurationMs', () => {
  it('formats 0ms as 0s', () => {
    expect(formatDurationMs(0)).toBe('0s')
  })

  it('formats 45000ms as 45s', () => {
    expect(formatDurationMs(45000)).toBe('45s')
  })

  it('formats 90000ms as 1m 30s', () => {
    expect(formatDurationMs(90000)).toBe('1m 30s')
  })
})

describe('formatTokens', () => {
  it('formats 0 as "0"', () => {
    expect(formatTokens(0)).toBe('0')
  })

  it('formats 999 as "999"', () => {
    expect(formatTokens(999)).toBe('999')
  })

  it('formats 1000 as "1.0k"', () => {
    expect(formatTokens(1000)).toBe('1.0k')
  })

  it('formats 1500 as "1.5k"', () => {
    expect(formatTokens(1500)).toBe('1.5k')
  })

  it('formats 15000 as "15.0k"', () => {
    expect(formatTokens(15000)).toBe('15.0k')
  })

  it('formats 100000 as "100.0k"', () => {
    expect(formatTokens(100000)).toBe('100.0k')
  })

  it('formats 1500000 as "1.5M"', () => {
    expect(formatTokens(1500000)).toBe('1.5M')
  })
})

describe('formatTimestamp', () => {
  it('produces a string containing the month and day of the date', () => {
    // 2024-06-15T12:00:00Z — timezone-independent assertions on month and day
    const result = formatTimestamp('2024-06-15T12:00:00Z')
    expect(result).toContain('Jun')
    expect(result).toContain('15')
  })

  it('returns "Invalid Date" for an invalid ISO input', () => {
    // new Date('not-a-date').toLocaleString() returns "Invalid Date" rather than throwing,
    // so the try/catch passthrough never fires — the function returns "Invalid Date"
    expect(formatTimestamp('not-a-date')).toBe('Invalid Date')
  })
})

describe('formatTimeAgo', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('returns "just now" for time less than 1 minute ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 30_000).toISOString() // 30 seconds ago
    expect(formatTimeAgo(iso)).toBe('just now')
  })

  it('returns "5m ago" for 5 minutes ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 5 * 60_000).toISOString()
    expect(formatTimeAgo(iso)).toBe('5m ago')
  })

  it('returns "59m ago" for 59 minutes ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 59 * 60_000).toISOString()
    expect(formatTimeAgo(iso)).toBe('59m ago')
  })

  it('returns "2h ago" for 2 hours ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 2 * 60 * 60_000).toISOString()
    expect(formatTimeAgo(iso)).toBe('2h ago')
  })

  it('returns "23h ago" for 23 hours ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 23 * 60 * 60_000).toISOString()
    expect(formatTimeAgo(iso)).toBe('23h ago')
  })

  it('returns a date string (not "Xh ago") for 25 hours ago', () => {
    vi.useFakeTimers()
    const now = new Date('2024-01-01T12:00:00Z')
    vi.setSystemTime(now)
    const iso = new Date(Date.now() - 25 * 60 * 60_000).toISOString()
    const result = formatTimeAgo(iso)
    // At 25h, formatTimeAgo returns toLocaleDateString, not "Xh ago"
    expect(result).not.toMatch(/h ago$/)
    expect(result).not.toBe('just now')
  })

  it('returns "Invalid Date" for an invalid ISO string', () => {
    const result = formatTimeAgo('not-a-date')
    expect(result).toBe('Invalid Date')
  })
})

describe('formatCountdown', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('returns 5:00 and urgent:false for 300 seconds remaining', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-01-01T12:00:00Z'))
    const iso = new Date(Date.now() + 300 * 1000).toISOString()
    expect(formatCountdown(iso)).toEqual({ str: '5:00', urgent: false })
  })

  it('returns 4:59 and urgent:true for 299 seconds remaining', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-01-01T12:00:00Z'))
    const iso = new Date(Date.now() + 299 * 1000).toISOString()
    expect(formatCountdown(iso)).toEqual({ str: '4:59', urgent: true })
  })

  it('returns 0:00 and urgent:true for past/zero time', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-01-01T12:00:00Z'))
    const iso = new Date(Date.now() - 1000).toISOString() // 1 second in the past
    expect(formatCountdown(iso)).toEqual({ str: '0:00', urgent: true })
  })

  it('returns 10:00 and urgent:false for 600 seconds remaining', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-01-01T12:00:00Z'))
    const iso = new Date(Date.now() + 600 * 1000).toISOString()
    expect(formatCountdown(iso)).toEqual({ str: '10:00', urgent: false })
  })
})

describe('formatDate', () => {
  it('returns a string containing the year and day', () => {
    const result = formatDate('2026-01-15T10:00:00Z')
    expect(result).toContain('2026')
    expect(result).toContain('15')
  })

  it('does not include time components', () => {
    const result = formatDate('2026-06-01T23:59:59Z')
    // Should not contain hour:minute patterns
    expect(result).not.toMatch(/\d{1,2}:\d{2}/)
  })
})

describe('computeRunDuration', () => {
  it('returns null when completed_at is null', () => {
    const run = { started_at: '2024-01-01T12:00:00Z', completed_at: null }
    expect(computeRunDuration(run)).toBeNull()
  })

  it('returns seconds difference when both timestamps are present', () => {
    const run = {
      started_at: '2024-01-01T12:00:00Z',
      completed_at: '2024-01-01T12:01:30Z',
    }
    expect(computeRunDuration(run)).toBe(90)
  })

  it('returns 0 for identical timestamps', () => {
    const run = {
      started_at: '2024-01-01T12:00:00Z',
      completed_at: '2024-01-01T12:00:00Z',
    }
    expect(computeRunDuration(run)).toBe(0)
  })

  it('returns NaN for invalid timestamps', () => {
    const run = { started_at: 'bad', completed_at: 'also-bad' }
    expect(computeRunDuration(run)).toBeNaN()
  })
})
