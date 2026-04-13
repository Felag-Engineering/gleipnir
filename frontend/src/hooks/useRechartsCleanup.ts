import { useEffect } from 'react'

// Recharts appends a singleton <span id="recharts_measurement_span"> to
// document.body to measure text widths for axis labels. This span is never
// removed by Recharts itself, so it persists after the chart unmounts and
// leaks into the accessibility tree on every subsequent page. This hook
// removes that span when the component that renders Recharts charts unmounts.
// Recharts recreates the span on next use, so removing it here is safe.
const RECHARTS_MEASUREMENT_SPAN_ID = 'recharts_measurement_span'

export function useRechartsCleanup(): void {
  useEffect(() => {
    return () => {
      const span = document.getElementById(RECHARTS_MEASUREMENT_SPAN_ID)
      if (span) {
        span.remove()
      }
    }
  }, [])
}
