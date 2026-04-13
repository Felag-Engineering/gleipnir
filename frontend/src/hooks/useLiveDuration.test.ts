import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useLiveDuration } from './useLiveDuration'

describe('useLiveDuration', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('returns null when startedAt is null', () => {
    const { result } = renderHook(() => useLiveDuration(null, null, 'pending'))
    expect(result.current).toBeNull()
  })

  it('returns a static duration for a terminal run and does not tick', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2025-01-01T12:01:00Z'))

    const startedAt = '2025-01-01T12:00:00Z'
    const completedAt = '2025-01-01T12:01:00Z' // 60 seconds later

    const { result } = renderHook(() =>
      useLiveDuration(startedAt, completedAt, 'complete'),
    )

    expect(result.current).toBe(60_000)

    // Advancing the clock should not change the value because no interval
    // is running for a terminal run.
    act(() => {
      vi.advanceTimersByTime(5000)
    })

    expect(result.current).toBe(60_000)
  })

  it('ticks every second for a running run', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2025-01-01T12:01:00Z'))

    // Run started 60 seconds before the fake "now"
    const startedAt = '2025-01-01T12:00:00Z'

    const { result } = renderHook(() =>
      useLiveDuration(startedAt, null, 'running'),
    )

    // Initial render: ~60 seconds elapsed
    expect(result.current).toBe(60_000)

    // After 5 seconds, duration should increase by 5000ms
    act(() => {
      vi.advanceTimersByTime(5000)
    })

    expect(result.current).toBe(65_000)
  })

  it('stops ticking when status transitions from running to complete', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2025-01-01T12:01:00Z'))

    const startedAt = '2025-01-01T12:00:00Z'
    const completedAt = '2025-01-01T12:01:05Z' // completed 5s after fake now

    let status = 'running'
    let completed: string | null = null

    const { result, rerender } = renderHook(() =>
      useLiveDuration(startedAt, completed, status),
    )

    expect(result.current).toBe(60_000)

    // Advance 3 seconds while still running
    act(() => {
      vi.advanceTimersByTime(3000)
    })
    expect(result.current).toBe(63_000)

    // Transition to terminal state
    status = 'complete'
    completed = completedAt
    rerender()

    // Duration should now be fixed at the completedAt difference (65s)
    expect(result.current).toBe(65_000)

    // Advancing further should not change the value
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(result.current).toBe(65_000)
  })

  it.each(['complete', 'failed', 'interrupted'])(
    'treats status=%s as terminal (no ticking)',
    (terminalStatus) => {
      vi.useFakeTimers()
      vi.setSystemTime(new Date('2025-01-01T12:01:00Z'))

      const startedAt = '2025-01-01T12:00:00Z'
      const completedAt = '2025-01-01T12:01:00Z'

      const { result } = renderHook(() =>
        useLiveDuration(startedAt, completedAt, terminalStatus),
      )

      const initial = result.current

      act(() => {
        vi.advanceTimersByTime(3000)
      })

      expect(result.current).toBe(initial)
    },
  )

  describe('beforeEach fake timer setup', () => {
    beforeEach(() => {
      vi.useFakeTimers()
    })

    it('uses Date.now() as fallback when terminal but completedAt is null', () => {
      vi.setSystemTime(new Date('2025-01-01T12:01:00Z'))

      const startedAt = '2025-01-01T12:00:00Z'

      const { result } = renderHook(() =>
        useLiveDuration(startedAt, null, 'complete'),
      )

      // completedAt is null, so it falls back to Date.now()
      expect(result.current).toBe(60_000)

      // Should not tick
      act(() => {
        vi.advanceTimersByTime(5000)
      })
      expect(result.current).toBe(60_000)
    })
  })
})
