import { useEffect } from 'react'
import { useToastState } from './ToastProvider'
import type { ToastVariant } from './ToastProvider'
import styles from './ToastRegion.module.css'

const VARIANT_CLASS: Record<ToastVariant, string> = {
  success: styles.success,
  error: styles.error,
  info: styles.info,
}

/**
 * Fixed bottom-right toast stack. Mounted once in Layout; toasts are pushed
 * via the useToast() API from anywhere in the app.
 */
export function ToastRegion() {
  const { toasts, dismiss } = useToastState()

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (e.key !== 'Escape') return
      const newest = toasts[toasts.length - 1]
      if (newest) dismiss(newest.id)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [toasts, dismiss])

  // Only mount the live region while there's something to announce — an
  // always-present empty status region collides with other role="status"
  // elements (e.g. Layout's connection banner) for assistive tech and tests.
  if (toasts.length === 0) return null

  return (
    <div className={styles.region} role="status" aria-live="polite" aria-atomic="false">
      {toasts.map(toast => (
        <div key={toast.id} className={`${styles.toast} ${VARIANT_CLASS[toast.variant]}`}>
          <span className={styles.message}>{toast.message}</span>
          <button
            type="button"
            className={styles.dismissBtn}
            onClick={() => dismiss(toast.id)}
            aria-label="Dismiss"
          >
            &times;
          </button>
        </div>
      ))}
    </div>
  )
}
