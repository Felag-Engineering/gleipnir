import { useState, useEffect } from 'react'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/Button'
import { ThemeToggle } from '@/components/ThemeToggle'
import { useModels } from '@/hooks/useModels'
import { usePageTitle } from '@/hooks/usePageTitle'
import {
  usePreferences,
  useUpdatePreferences,
  useSessions,
  useRevokeSession,
  useChangePassword,
} from '@/hooks/useSettings'
import {
  formatTimestamp,
  getPreferredTimezone,
  getPreferredDateFormat,
  saveTimezonePreference,
  saveDateFormatPreference,
  type DateFormat,
} from '@/utils/format'
import type { ApiError } from '@/api/fetch'
import styles from './SettingsPage.module.css'

const DATE_FORMAT_OPTIONS: { value: DateFormat; label: string }[] = [
  { value: 'relative', label: 'Relative (5m ago)' },
  { value: 'absolute', label: 'Absolute (Apr 1, 14:30)' },
  { value: 'iso', label: 'ISO (2026-04-02T14:30:00)' },
]

// Best-effort user agent parser for display purposes
function parseUserAgent(ua: string): string {
  if (!ua) return 'Unknown client'
  if (/Chrome\/[\d.]+/.test(ua) && !/Edg\//.test(ua) && !/OPR\//.test(ua)) {
    const m = ua.match(/Chrome\/([\d.]+)/)
    return `Chrome ${m?.[1]?.split('.')[0] ?? ''}`
  }
  if (/Firefox\/[\d.]+/.test(ua)) {
    const m = ua.match(/Firefox\/([\d.]+)/)
    return `Firefox ${m?.[1]?.split('.')[0] ?? ''}`
  }
  if (/Safari\/[\d.]+/.test(ua) && !/Chrome/.test(ua)) {
    const m = ua.match(/Version\/([\d.]+)/)
    return `Safari ${m?.[1]?.split('.')[0] ?? ''}`
  }
  if (/Edg\/[\d.]+/.test(ua)) {
    const m = ua.match(/Edg\/([\d.]+)/)
    return `Edge ${m?.[1]?.split('.')[0] ?? ''}`
  }
  return ua.slice(0, 60)
}

function AppearanceSection() {
  return (
    <section className={styles.card}>
      <div className={styles.cardHeader}>
        <h2 className={styles.cardTitle}>Appearance</h2>
      </div>
      <div className={styles.cardBody}>
        <div className={styles.appearanceRow}>
          <span className={styles.appearanceLabel}>Theme</span>
          <ThemeToggle compact={false} />
        </div>
      </div>
    </section>
  )
}

function ChangePasswordSection() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [clientError, setClientError] = useState<string | null>(null)
  const [success, setSuccess] = useState(false)

  const mutation = useChangePassword()

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setClientError(null)
    setSuccess(false)

    if (next.length < 8) {
      setClientError('New password must be at least 8 characters.')
      return
    }
    if (next !== confirm) {
      setClientError('New passwords do not match.')
      return
    }

    mutation.mutate(
      { current_password: current, new_password: next },
      {
        onSuccess: () => {
          setCurrent('')
          setNext('')
          setConfirm('')
          setSuccess(true)
          mutation.reset()
        },
      },
    )
  }

  const serverError = mutation.error as ApiError | null

  return (
    <section className={styles.card}>
      <div className={styles.cardHeader}>
        <h2 className={styles.cardTitle}>Change Password</h2>
      </div>
      <div className={styles.cardBody}>
        <form onSubmit={handleSubmit}>
          <div className={styles.formInner}>
            <div className={styles.fieldGroup}>
              <label htmlFor="current-password" className={styles.label}>
                Current password
              </label>
              <input
                id="current-password"
                type="password"
                className={styles.input}
                value={current}
                onChange={(e) => { setCurrent(e.target.value); setSuccess(false) }}
                autoComplete="current-password"
                required
              />
            </div>

            <div className={styles.fieldGroup}>
              <label htmlFor="new-password" className={styles.label}>
                New password
              </label>
              <input
                id="new-password"
                type="password"
                className={styles.input}
                value={next}
                onChange={(e) => { setNext(e.target.value); setClientError(null); setSuccess(false) }}
                autoComplete="new-password"
                required
              />
            </div>

            <div className={styles.fieldGroup}>
              <label htmlFor="confirm-password" className={styles.label}>
                Confirm new password
              </label>
              <input
                id="confirm-password"
                type="password"
                className={styles.input}
                value={confirm}
                onChange={(e) => { setConfirm(e.target.value); setClientError(null); setSuccess(false) }}
                autoComplete="new-password"
                required
              />
            </div>

            {(clientError ?? serverError) && (
              <div className={styles.errorMsg}>
                {clientError ?? serverError?.message}
              </div>
            )}

            {success && (
              <div className={styles.successMsg}>
                Password changed successfully.
              </div>
            )}

            <div className={styles.formActions}>
              <Button type="submit" variant="primary" disabled={mutation.isPending}>
                {mutation.isPending ? 'Saving…' : 'Change password'}
              </Button>
            </div>
          </div>
        </form>
      </div>
    </section>
  )
}

function DefaultModelSection() {
  const { data: preferences } = usePreferences()
  const { data: providerModels, status: modelsStatus } = useModels()
  const updatePreferences = useUpdatePreferences()

  const [selected, setSelected] = useState<string>('')

  useEffect(() => {
    if (preferences?.default_model !== undefined) {
      setSelected(preferences.default_model ?? '')
    }
  }, [preferences?.default_model])

  function handleChange(e: React.ChangeEvent<HTMLSelectElement>) {
    const value = e.target.value
    setSelected(value)
    updatePreferences.mutate({ default_model: value || undefined })
  }

  return (
    <section className={styles.card}>
      <div className={styles.cardHeader}>
        <h2 className={styles.cardTitle}>Default Model</h2>
        <p className={styles.cardDesc}>Used when a policy does not specify a model.</p>
      </div>
      <div className={styles.cardBody}>
        <div className={styles.fieldGroup}>
          <label htmlFor="default-model" className={styles.label}>
            Model
          </label>
          <select
            id="default-model"
            className={styles.select}
            value={selected}
            onChange={handleChange}
            disabled={modelsStatus === 'pending' || updatePreferences.isPending}
          >
            <option value="">— no default —</option>
            {(providerModels ?? []).map((group) => (
              <optgroup key={group.provider} label={group.provider}>
                {group.models.map((m) => (
                  <option key={m.name} value={m.name}>
                    {m.display_name}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        </div>
      </div>
    </section>
  )
}

function DateTimeSection() {
  const { data: preferences } = usePreferences()
  const updatePreferences = useUpdatePreferences()

  // Intl.supportedValuesOf is available in modern environments but not yet in lib.dom.d.ts
  const allTimezones = (Intl as unknown as { supportedValuesOf: (key: string) => string[] }).supportedValuesOf('timeZone')

  const [timezone, setTimezone] = useState<string>(() => getPreferredTimezone())
  const [dateFormat, setDateFormat] = useState<DateFormat>(() => getPreferredDateFormat())

  useEffect(() => {
    if (preferences?.timezone) setTimezone(preferences.timezone)
    if (preferences?.date_format) {
      const fmt = preferences.date_format
      if (fmt === 'relative' || fmt === 'absolute' || fmt === 'iso') {
        setDateFormat(fmt)
      }
    }
  }, [preferences?.timezone, preferences?.date_format])

  function handleTimezoneChange(e: React.ChangeEvent<HTMLSelectElement>) {
    const tz = e.target.value
    setTimezone(tz)
    saveTimezonePreference(tz)
    updatePreferences.mutate({ timezone: tz, date_format: dateFormat })
  }

  function handleDateFormatChange(e: React.ChangeEvent<HTMLSelectElement>) {
    const fmt = e.target.value as DateFormat
    setDateFormat(fmt)
    saveDateFormatPreference(fmt)
    updatePreferences.mutate({ timezone: timezone, date_format: fmt })
  }

  return (
    <section className={styles.card}>
      <div className={styles.cardHeader}>
        <h2 className={styles.cardTitle}>Date &amp; Time</h2>
      </div>
      <div className={styles.cardBody}>
        <div className={styles.fieldGroup}>
          <label htmlFor="timezone" className={styles.label}>
            Timezone
          </label>
          <select
            id="timezone"
            className={styles.select}
            value={timezone}
            onChange={handleTimezoneChange}
            disabled={updatePreferences.isPending}
          >
            {allTimezones.map((tz: string) => (
              <option key={tz} value={tz}>
                {tz}
              </option>
            ))}
          </select>
        </div>

        <div className={styles.fieldGroup}>
          <label htmlFor="date-format" className={styles.label}>
            Date format
          </label>
          <select
            id="date-format"
            className={styles.select}
            value={dateFormat}
            onChange={handleDateFormatChange}
            disabled={updatePreferences.isPending}
          >
            {DATE_FORMAT_OPTIONS.map(({ value, label }) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
          <span className={styles.fieldHint}>
            Preview: {formatTimestamp(new Date().toISOString())}
          </span>
        </div>
      </div>
    </section>
  )
}

function SessionsSection() {
  const { data: sessions, status } = useSessions()
  const revokeMutation = useRevokeSession()

  if (status === 'pending') {
    return (
      <section className={styles.card}>
        <div className={styles.cardHeader}>
          <h2 className={styles.cardTitle}>Active Sessions</h2>
        </div>
        <div className={styles.cardBody}>
          <span className={styles.appearanceLabel}>Loading…</span>
        </div>
      </section>
    )
  }

  return (
    <section className={styles.card}>
      <div className={styles.cardHeader}>
        <h2 className={styles.cardTitle}>Active Sessions</h2>
      </div>
      <div className={styles.cardBody}>
        {(sessions ?? []).length === 0 ? (
          <span className={styles.appearanceLabel}>No active sessions found.</span>
        ) : (
          <div className={styles.sessionList}>
            {(sessions ?? []).map((session) => (
              <div key={session.id} className={styles.sessionRow}>
                <div className={styles.sessionMeta}>
                  <span className={styles.sessionAgent}>{parseUserAgent(session.user_agent)}</span>
                  <span className={styles.sessionDetails}>
                    {session.ip_address} &middot; Created {formatTimestamp(session.created_at)} &middot; Expires{' '}
                    {formatTimestamp(session.expires_at)}
                  </span>
                </div>
                <div className={styles.sessionRight}>
                  {session.is_current ? (
                    <span className={styles.currentBadge}>Current</span>
                  ) : (
                    <button
                      type="button"
                      className={styles.revokeBtn}
                      onClick={() => revokeMutation.mutate(session.id)}
                      disabled={revokeMutation.isPending}
                    >
                      Revoke
                    </button>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}

export default function SettingsPage() {
  usePageTitle('Settings')

  return (
    <div className={styles.page}>
      <PageHeader title="Settings" />
      <AppearanceSection />
      <ChangePasswordSection />
      <DefaultModelSection />
      <DateTimeSection />
      <SessionsSection />
    </div>
  )
}
