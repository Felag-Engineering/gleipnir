import { useState, useEffect } from 'react'
import { Clock } from 'lucide-react'
import { usePreferences, useUpdatePreferences } from '@/hooks/useSettings'
import {
  formatTimestamp,
  getPreferredTimezone,
  getPreferredDateFormat,
  saveTimezonePreference,
  saveDateFormatPreference,
  type DateFormat,
} from '@/utils/format'
import styles from './Settings.module.css'

const DATE_FORMAT_OPTIONS: { value: DateFormat; label: string }[] = [
  { value: 'relative', label: 'Relative (5m ago)' },
  { value: 'absolute', label: 'Absolute (Apr 1, 14:30)' },
  { value: 'iso', label: 'ISO (2026-04-02T14:30:00)' },
]

export function DateTimeSection() {
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
        <h2 className={styles.cardTitle}>
          <Clock size={16} strokeWidth={1.5} className={styles.cardTitleIcon} />
          Date &amp; Time
        </h2>
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
