import { useSessions, useRevokeSession } from '@/hooks/useSettings'
import { formatTimestamp } from '@/utils/format'
import { parseUserAgent } from '@/utils/userAgent'
import styles from './Settings.module.css'

export function SessionsSection() {
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
