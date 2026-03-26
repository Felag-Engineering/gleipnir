// Canonical formatting helpers for the Gleipnir frontend.
// All duration, token, timestamp, and relative-time formatting lives here.

export const formatDuration = (s: number | null) =>
  s == null ? '—' : s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`

// formatDurationMs accepts milliseconds (e.g. from Date arithmetic) and delegates to formatDuration.
export function formatDurationMs(ms: number): string {
  return formatDuration(Math.floor(ms / 1000))
}

// formatTokens handles millions in addition to thousands.
export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

export const formatTimestamp = (iso: string) => {
  try {
    return new Date(iso).toLocaleString('en-US', {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    })
  } catch {
    return iso
  }
}

export const formatTimeAgo = (iso: string) => {
  const m = Math.floor((Date.now() - new Date(iso).getTime()) / 60000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  return h < 24 ? `${h}h ago` : new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}

export const formatCountdown = (expiresAt: string) => {
  const secs = Math.max(0, Math.floor((new Date(expiresAt).getTime() - Date.now()) / 1000))
  const m = Math.floor(secs / 60), s = secs % 60
  return { str: `${m}:${String(s).padStart(2, '0')}`, urgent: secs < 300 }
}

// computeRunDuration returns the run duration in seconds, or null if the run has not completed.
// started_at is required on ApiRun (non-nullable).
export function computeRunDuration(run: { completed_at: string | null; started_at: string }): number | null {
  if (!run.completed_at) return null
  return Math.floor(
    (new Date(run.completed_at).getTime() - new Date(run.started_at).getTime()) / 1000,
  )
}
