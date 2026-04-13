import { describe, it, expect, afterEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useRechartsCleanup } from './useRechartsCleanup'

const SPAN_ID = 'recharts_measurement_span'

function appendMeasurementSpan(textContent: string): HTMLSpanElement {
  const span = document.createElement('span')
  span.id = SPAN_ID
  span.textContent = textContent
  document.body.appendChild(span)
  return span
}

afterEach(() => {
  // Guard: remove the span if any test left it behind
  document.getElementById(SPAN_ID)?.remove()
})

describe('useRechartsCleanup', () => {
  it('removes the Recharts measurement span from document.body on unmount', () => {
    appendMeasurementSpan('18:00')

    expect(document.getElementById(SPAN_ID)).not.toBeNull()

    const { unmount } = renderHook(() => useRechartsCleanup())
    unmount()

    expect(document.getElementById(SPAN_ID)).toBeNull()
  })

  it('does not throw when no measurement span exists on unmount', () => {
    // Confirm the span is absent before rendering
    expect(document.getElementById(SPAN_ID)).toBeNull()

    const { unmount } = renderHook(() => useRechartsCleanup())
    expect(() => unmount()).not.toThrow()
  })

  it('does not remove the span before the component unmounts', () => {
    appendMeasurementSpan('12:00')

    renderHook(() => useRechartsCleanup())

    // Still mounted — span should still be present
    expect(document.getElementById(SPAN_ID)).not.toBeNull()
  })
})
