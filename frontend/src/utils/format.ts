// Canonical formatting helpers for the Gleipnir frontend.
// All duration, token, timestamp, and relative-time formatting lives here.

// Known provider display labels. The wire format uses lowercase enum values
// (e.g. "openai"), but naive title-casing produces "Openai" — wrong for brands
// like OpenAI. Add entries here whenever a new first-party provider is added.
const PROVIDER_LABELS: Record<string, string> = {
  anthropic: 'Anthropic',
  google: 'Google',
  openai: 'OpenAI',
}

// formatProviderName returns a human-readable display label for a provider
// identifier. Known providers (anthropic, google, openai) use their canonical
// brand capitalisation. Unknown providers (e.g. admin-managed OpenAI-compat
// backends with arbitrary names) fall back to simple first-letter capitalisation
// so something reasonable always renders.
export function formatProviderName(provider: string): string {
  if (!provider) return provider
  return PROVIDER_LABELS[provider] ?? provider.charAt(0).toUpperCase() + provider.slice(1)
}

const TZ_KEY = 'gleipnir-timezone'
const DATE_FORMAT_KEY = 'gleipnir-date-format'

export type DateFormat = 'relative' | 'absolute' | 'iso'

function readStorageKey(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch (err) {
    console.warn('localStorage read failed for key', key, err)
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
  } catch (err) {
    console.warn('localStorage write failed for key', TZ_KEY, err)
  }
}

export function saveDateFormatPreference(fmt: DateFormat): void {
  try {
    localStorage.setItem(DATE_FORMAT_KEY, fmt)
  } catch (err) {
    console.warn('localStorage write failed for key', DATE_FORMAT_KEY, err)
  }
}

// formatDuration converts a duration in seconds to a human-readable string.
// The format adapts to the magnitude so short runs don't show misleading "0s":
//   under 1s  → "841ms"
//   1s–60s    → "2.5s"  (one decimal place)
//   60s+      → "3m 12s"
export function formatDuration(s: number | null): string {
  if (s == null) return '—'
  if (s < 1) return `${Math.round(s * 1000)}ms`
  if (s < 60) return `${s.toFixed(1)}s`
  return `${Math.floor(s / 60)}m ${Math.floor(s % 60)}s`
}

// formatDurationMs accepts milliseconds (e.g. from Date arithmetic or useLiveDuration)
// and delegates to formatDuration.
export function formatDurationMs(ms: number): string {
  return formatDuration(ms / 1000)
}

// formatTokens handles millions in addition to thousands.
export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

export const formatTimestamp = (iso: string): string => {
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

export const formatTimeAgo = (iso: string): string => {
  const m = Math.floor((Date.now() - new Date(iso).getTime()) / 60000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  return h < 24 ? `${h}h ago` : new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}

export const formatCountdown = (expiresAt: string): { str: string; urgent: boolean } => {
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
  return (new Date(run.completed_at).getTime() - new Date(run.started_at).getTime()) / 1000
}
