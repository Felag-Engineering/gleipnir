import type { ApiPluginInstanceStartupFault } from '@/api/types'
import { formatTimestamp } from '@/utils/format'
import styles from './StartupFaultBanner.module.css'

interface StartupFaultBannerProps {
  fault: ApiPluginInstanceStartupFault
  // now is injectable so a test can pin whether the next attempt is still
  // ahead; defaults to the wall clock.
  now?: Date
}

function nextAttemptText(fault: ApiPluginInstanceStartupFault, now: Date): string {
  if (fault.attempt_in_progress) return 'A new attempt is in progress.'
  if (new Date(fault.next_attempt_at).getTime() <= now.getTime()) {
    return 'The next attempt is due now.'
  }
  return `Next attempt at ${formatTimestamp(fault.next_attempt_at)}.`
}

// StartupFaultBanner explains why an instance's plugin container keeps failing
// to start. It is shown next to the health chip, not in place of it: a failed
// upgrade leaves the previous generation serving, and that instance is not down.
export function StartupFaultBanner({ fault, now = new Date() }: StartupFaultBannerProps) {
  const serving = fault.serving_generation !== null
  const attempts = fault.failures === 1 ? '1 attempt' : `${fault.failures} consecutive attempts`

  return (
    <section
      className={serving ? styles.bannerServing : styles.bannerDown}
      role="status"
      aria-label="Container startup"
    >
      <p className={styles.title}>
        {serving
          ? `Upgrade failing to start — generation ${fault.serving_generation} is still serving`
          : 'Plugin container failed to start'}
      </p>
      {/* last_error may quote container output: rendered as text, never markup. */}
      <code className={styles.error}>{fault.last_error}</code>
      <p className={styles.meta}>
        {attempts} failed, most recently generation {fault.last_generation} at{' '}
        {formatTimestamp(fault.last_failed_at)}. {nextAttemptText(fault, now)}
      </p>
    </section>
  )
}
