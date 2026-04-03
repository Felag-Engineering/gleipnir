import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import React from 'react'
import { renderHook, act } from '@testing-library/react'
import { useScrollSentinel } from './useScrollSentinel'

afterEach(() => {
  vi.unstubAllGlobals()
})

// HookHarness mounts the hook into a real DOM tree so that sentinelRef.current
// points to an actual element, which is required for the IntersectionObserver
// path to execute.
function HookHarness({ count, onResult }: {
  count: number
  onResult: (r: ReturnType<typeof useScrollSentinel>) => void
}) {
  const result = useScrollSentinel(count)
  onResult(result)
  return <div ref={result.sentinelRef} data-testid="sentinel" />
}

describe('useScrollSentinel — default state', () => {
  it('starts with showNewPill false', () => {
    const { result } = renderHook(() => useScrollSentinel(0))
    expect(result.current.showNewPill).toBe(false)
  })

  it('returns a sentinelRef', () => {
    const { result } = renderHook(() => useScrollSentinel(0))
    expect(result.current.sentinelRef).toBeDefined()
  })

  it('returns a scrollToBottom callback', () => {
    const { result } = renderHook(() => useScrollSentinel(0))
    expect(typeof result.current.scrollToBottom).toBe('function')
  })
})

describe('useScrollSentinel — new steps pill (via DOM harness)', () => {
  it('does not show pill when user is near bottom (isIntersecting = true)', async () => {
    function MockObserver(this: IntersectionObserver, cb: IntersectionObserverCallback) {
      this.observe = (el: Element) => {
        cb([{ isIntersecting: true, target: el } as IntersectionObserverEntry], this)
      }
      this.unobserve = vi.fn()
      this.disconnect = vi.fn()
    }
    vi.stubGlobal('IntersectionObserver', MockObserver)

    let latest: ReturnType<typeof useScrollSentinel> | null = null
    const { rerender } = render(<HookHarness count={1} onResult={(r) => { latest = r }} />)
    rerender(<HookHarness count={2} onResult={(r) => { latest = r }} />)

    await waitFor(() => {
      expect(latest!.showNewPill).toBe(false)
    })
  })

  it('shows pill when items grow and user is not near bottom', async () => {
    function MockObserver(this: IntersectionObserver, cb: IntersectionObserverCallback) {
      this.observe = (el: Element) => {
        // Fires immediately as NOT intersecting (user scrolled away)
        cb([{ isIntersecting: false, target: el } as IntersectionObserverEntry], this)
      }
      this.unobserve = vi.fn()
      this.disconnect = vi.fn()
    }
    vi.stubGlobal('IntersectionObserver', MockObserver)

    let latest: ReturnType<typeof useScrollSentinel> | null = null
    const { rerender } = render(<HookHarness count={1} onResult={(r) => { latest = r }} />)
    rerender(<HookHarness count={2} onResult={(r) => { latest = r }} />)

    await waitFor(() => {
      expect(latest!.showNewPill).toBe(true)
    })
  })

  it('scrollToBottom hides the pill', async () => {
    window.HTMLElement.prototype.scrollIntoView = vi.fn()

    function MockObserver(this: IntersectionObserver, cb: IntersectionObserverCallback) {
      this.observe = (el: Element) => {
        cb([{ isIntersecting: false, target: el } as IntersectionObserverEntry], this)
      }
      this.unobserve = vi.fn()
      this.disconnect = vi.fn()
    }
    vi.stubGlobal('IntersectionObserver', MockObserver)

    let latest: ReturnType<typeof useScrollSentinel> | null = null
    const { rerender } = render(<HookHarness count={1} onResult={(r) => { latest = r }} />)
    rerender(<HookHarness count={2} onResult={(r) => { latest = r }} />)

    await waitFor(() => { expect(latest!.showNewPill).toBe(true) })

    act(() => { latest!.scrollToBottom() })

    await waitFor(() => { expect(latest!.showNewPill).toBe(false) })
  })

  it('does not show pill when itemCount does not grow', async () => {
    function MockObserver(this: IntersectionObserver, cb: IntersectionObserverCallback) {
      this.observe = (el: Element) => {
        cb([{ isIntersecting: false, target: el } as IntersectionObserverEntry], this)
      }
      this.unobserve = vi.fn()
      this.disconnect = vi.fn()
    }
    vi.stubGlobal('IntersectionObserver', MockObserver)

    let latest: ReturnType<typeof useScrollSentinel> | null = null
    const { rerender } = render(<HookHarness count={5} onResult={(r) => { latest = r }} />)

    // Same count — no growth
    rerender(<HookHarness count={5} onResult={(r) => { latest = r }} />)

    // Allow effects to flush
    await waitFor(() => {
      expect(latest!.showNewPill).toBe(false)
    })
  })
})

describe('useScrollSentinel — IntersectionObserver unavailable', () => {
  it('does not throw when IntersectionObserver is undefined', () => {
    vi.stubGlobal('IntersectionObserver', undefined)

    // Should not throw
    expect(() => renderHook(() => useScrollSentinel(0))).not.toThrow()
  })
})
