import { useState, useEffect } from 'react'
import { formatCountdown } from '@/utils/format'

// useCountdown returns a live countdown derived from an ISO 8601 expiry timestamp.
// The display string and urgency flag are updated every second via setInterval.
// Returns null when expiresAt is undefined (no timeout configured).
// When the countdown reaches zero, the interval is cleared and the hook returns
// { str: "0:00", urgent: true }.
export function useCountdown(expiresAt: string | undefined): { str: string; urgent: boolean } | null {
  const [countdown, setCountdown] = useState<{ str: string; urgent: boolean } | null>(() => {
    if (!expiresAt) return null
    return formatCountdown(expiresAt)
  })

  useEffect(() => {
    if (!expiresAt) {
      setCountdown(null)
      return
    }

    // Compute the initial state immediately so there is no stale display on mount.
    setCountdown(formatCountdown(expiresAt))

    const id = setInterval(() => {
      const next = formatCountdown(expiresAt)
      setCountdown(next)
      // Once the countdown reaches zero, stop polling to avoid redundant renders.
      if (next.str === '0:00') {
        clearInterval(id)
      }
    }, 1000)

    return () => clearInterval(id)
  }, [expiresAt])

  return countdown
}
