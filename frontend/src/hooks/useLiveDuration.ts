import { useState, useEffect } from 'react'

// Terminal statuses indicate a run has stopped. Once a run reaches one of
// these states its duration is fixed and does not need a live interval.
const TERMINAL_STATUSES = new Set(['complete', 'failed', 'interrupted'])

// useLiveDuration returns the elapsed duration of a run in milliseconds.
//
// For non-terminal runs it ticks every second via setInterval so the caller
// automatically re-renders with an up-to-date value. For terminal runs it
// returns the static difference between startedAt and completedAt (or
// Date.now() if completedAt is missing, as a safety fallback).
//
// Returns null when startedAt is null (run has not started yet).
export function useLiveDuration(
  startedAt: string | null,
  completedAt: string | null,
  status: string,
): number | null {
  const isTerminal = TERMINAL_STATUSES.has(status)

  const [duration, setDuration] = useState<number | null>(() => {
    if (!startedAt) return null
    const started = new Date(startedAt).getTime()
    const ended = completedAt ? new Date(completedAt).getTime() : Date.now()
    return ended - started
  })

  useEffect(() => {
    if (!startedAt) {
      setDuration(null)
      return
    }

    const started = new Date(startedAt).getTime()

    if (isTerminal) {
      // Run is done — compute once and do not set up a polling interval.
      const ended = completedAt ? new Date(completedAt).getTime() : Date.now()
      setDuration(ended - started)
      return
    }

    // Run is still in progress — update every second so the displayed
    // duration ticks live rather than staying frozen at the initial value.
    setDuration(Date.now() - started)

    const id = setInterval(() => {
      setDuration(Date.now() - started)
    }, 1000)

    return () => clearInterval(id)
  }, [startedAt, completedAt, isTerminal])

  return duration
}
