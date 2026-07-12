import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { ToastProvider, useToast } from './ToastProvider'
import { ToastRegion } from './ToastRegion'

function Harness() {
  const toast = useToast()
  return (
    <div>
      <button onClick={() => toast.success('Saved')}>fire-success</button>
      <button onClick={() => toast.error('Failed')}>fire-error</button>
      <button onClick={() => toast.info('Heads up')}>fire-info</button>
      <button onClick={() => toast.success('Sticky', { duration: 0 })}>fire-sticky</button>
      <button onClick={() => toast.error('Dup')}>fire-dup</button>
    </div>
  )
}

function renderHarness() {
  return render(
    <ToastProvider>
      <Harness />
      <ToastRegion />
    </ToastProvider>,
  )
}

afterEach(() => {
  vi.useRealTimers()
})

describe('ToastProvider / ToastRegion', () => {
  it('renders a success toast inside the role="status" live region', () => {
    renderHarness()
    fireEvent.click(screen.getByText('fire-success'))
    expect(screen.getByRole('status')).toHaveTextContent('Saved')
  })

  it('auto-dismisses a success toast after 4000ms', () => {
    vi.useFakeTimers()
    renderHarness()
    fireEvent.click(screen.getByText('fire-success'))
    expect(screen.getByText('Saved')).toBeInTheDocument()

    act(() => {
      vi.advanceTimersByTime(4000)
    })

    expect(screen.queryByText('Saved')).not.toBeInTheDocument()
  })

  it('keeps an error toast alive at 4000ms and dismisses it by 6000ms', () => {
    vi.useFakeTimers()
    renderHarness()
    fireEvent.click(screen.getByText('fire-error'))

    act(() => {
      vi.advanceTimersByTime(4000)
    })
    expect(screen.getByText('Failed')).toBeInTheDocument()

    act(() => {
      vi.advanceTimersByTime(2000)
    })
    expect(screen.queryByText('Failed')).not.toBeInTheDocument()
  })

  it('dismisses a toast when its Dismiss button is clicked', () => {
    renderHarness()
    fireEvent.click(screen.getByText('fire-info'))
    expect(screen.getByText('Heads up')).toBeInTheDocument()

    fireEvent.click(screen.getByLabelText('Dismiss'))
    expect(screen.queryByText('Heads up')).not.toBeInTheDocument()
  })

  it('dismisses only the newest toast on Escape', () => {
    renderHarness()
    fireEvent.click(screen.getByText('fire-success'))
    fireEvent.click(screen.getByText('fire-info'))
    expect(screen.getByText('Saved')).toBeInTheDocument()
    expect(screen.getByText('Heads up')).toBeInTheDocument()

    fireEvent.keyDown(document, { key: 'Escape' })

    expect(screen.queryByText('Heads up')).not.toBeInTheDocument()
    expect(screen.getByText('Saved')).toBeInTheDocument()
  })

  it('stacks three toasts simultaneously', () => {
    renderHarness()
    fireEvent.click(screen.getByText('fire-success'))
    fireEvent.click(screen.getByText('fire-error'))
    fireEvent.click(screen.getByText('fire-info'))

    expect(screen.getAllByLabelText('Dismiss')).toHaveLength(3)
  })

  it('caps the visible stack at 3, evicting the oldest toast', () => {
    renderHarness()
    // Fire four distinct toasts; the first ('Saved') should be evicted.
    fireEvent.click(screen.getByText('fire-success'))
    fireEvent.click(screen.getByText('fire-error'))
    fireEvent.click(screen.getByText('fire-info'))
    fireEvent.click(screen.getByText('fire-sticky'))

    expect(screen.getAllByLabelText('Dismiss')).toHaveLength(3)
    expect(screen.queryByText('Saved')).not.toBeInTheDocument()
    expect(screen.getByText('Sticky')).toBeInTheDocument()
  })

  it('dedups an identical toast instead of stacking a duplicate', () => {
    renderHarness()
    fireEvent.click(screen.getByText('fire-dup'))
    fireEvent.click(screen.getByText('fire-dup'))

    expect(screen.getAllByText('Dup')).toHaveLength(1)
    expect(screen.getAllByLabelText('Dismiss')).toHaveLength(1)
  })

  it('never auto-dismisses a toast fired with duration: 0 (sticky)', () => {
    vi.useFakeTimers()
    renderHarness()
    fireEvent.click(screen.getByText('fire-sticky'))

    act(() => {
      vi.advanceTimersByTime(60_000)
    })

    expect(screen.getByText('Sticky')).toBeInTheDocument()
  })

  it('clears pending auto-dismiss timers on unmount', () => {
    vi.useFakeTimers()
    const { unmount } = renderHarness()
    fireEvent.click(screen.getByText('fire-success'))

    unmount()

    // Advancing timers after unmount must not throw or warn about updates
    // on an unmounted component — the effect cleanup should have cleared
    // the pending setTimeout.
    expect(() => {
      act(() => {
        vi.advanceTimersByTime(10_000)
      })
    }).not.toThrow()
  })

  it('useToast() never throws when rendered without a ToastProvider ancestor', () => {
    function Standalone() {
      const toast = useToast()
      return <button onClick={() => toast.success('x')}>fire</button>
    }
    render(<Standalone />)
    expect(() => fireEvent.click(screen.getByText('fire'))).not.toThrow()
  })
})
