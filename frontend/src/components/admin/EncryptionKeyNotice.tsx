import { AlertTriangle } from 'lucide-react'
import styles from './EncryptionKeyNotice.module.css'

/**
 * Persistent warning banner reminding operators that GLEIPNIR_ENCRYPTION_KEY
 * protects every credential stored in the database. Placed above the API Keys
 * section on the admin Models page so it's in view while handling credentials.
 *
 * Non-dismissible by design (acceptance criterion: persistent notice).
 */
export function EncryptionKeyNotice() {
  return (
    <div className={styles.banner} role="alert">
      <AlertTriangle size={20} strokeWidth={1.5} className={styles.icon} aria-hidden="true" />
      <div className={styles.content}>
        <p className={styles.primary}>
          Back up <code>GLEIPNIR_ENCRYPTION_KEY</code> in a password manager or secrets vault.
        </p>
        <p className={styles.body}>
          Provider API keys and webhook secrets are encrypted with this key. If it is lost, every
          credential stored in the database becomes permanently unrecoverable.
        </p>
        <p className={styles.footnote}>Key rotation is not supported in v1.0 — see Operations docs.</p>
      </div>
    </div>
  )
}
