import { useState } from 'react'
import { Button } from '@/components/Button'
import { useChangePassword } from '@/hooks/useSettings'
import type { ApiError } from '@/api/fetch'
import styles from './Settings.module.css'

export function ChangePasswordSection() {
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
