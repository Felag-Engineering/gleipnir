// Canonical formatting helpers for the Gleipnir frontend.
// All duration, token, timestamp, and relative-time formatting lives here.

const TZ_KEY = 'gleipnir-timezone'
const DATE_FORMAT_KEY = 'gleipnir-date-format'

export type DateFormat = 'relative' | 'absolute' | 'iso'

function readStorageKey(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

export function getPreferredTimezone(): string {
  return readStorageKey(TZ_KEY) ?? Intl.DateTimeFormat().resolvedOptions().timeZone
}

export function getPreferredDateFormat(): DateFormat {
  const raw = readStorageKey(DATE_FORMAT_KEY)
  if (raw === 'relative' || raw === 'absolute' || raw === 'iso') return raw
  return 'absolute'
}

export function saveTimezonePreference(tz: string): void {
  try {
    localStorage.setItem(TZ_KEY, tz)
  } catch {
    // localStorage unavailable
  }
}

export function saveDateFormatPreference(fmt: DateFormat): void {
  try {
    localStorage.setItem(DATE_FORMAT_KEY, fmt)
  } catch {
    // localStorage unavailable
  }
}

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
    const fmt = getPreferredDateFormat()
    const tz = getPreferredTimezone()

    if (fmt === 'iso') {
      // Re-render in the preferred timezone as ISO-like string
      const d = new Date(iso)
      const parts = new Intl.DateTimeFormat('en-US', {
        timeZone: tz,
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        hour12: false,
      }).formatToParts(d)
      const p: Record<string, string> = {}
      for (const { type, value } of parts) p[type] = value
      return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}:${p.second}`
    }

    if (fmt === 'relative') {
      return formatTimeAgo(iso)
    }

    // absolute (default)
    return new Date(iso).toLocaleString('en-US', {
      timeZone: tz,
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

// formatDate formats an ISO timestamp as a short date (e.g. "Jan 1, 2026").
// Unlike formatTimestamp, it omits the time portion — suitable for "Created" columns.
export function formatDate(iso: string): string {
  try {
    const tz = getPreferredTimezone()
    return new Date(iso).toLocaleDateString(undefined, {
      timeZone: tz,
      year: 'numeric',
      month: 'short',
      day: 'numeric',
    })
  } catch {
    return iso
  }
}

// computeRunDuration returns the run duration in seconds, or null if the run has not completed.
// started_at is required on ApiRun (non-nullable).
export function computeRunDuration(run: { completed_at: string | null; started_at: string }): number | null {
  if (!run.completed_at) return null
  return Math.floor(
    (new Date(run.completed_at).getTime() - new Date(run.started_at).getTime()) / 1000,
  )
}
