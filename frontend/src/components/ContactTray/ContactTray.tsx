import { ExternalLink, Mail } from 'lucide-react'
import { Modal } from '@/components/Modal'
import styles from './ContactTray.module.css'

const SUPPORT_EMAIL = 'support@gleipnir.dev'
const GITHUB_REPO_URL = 'https://github.com/Felag-Engineering/gleipnir'

interface Props {
  open: boolean
  onClose: () => void
}

/**
 * ContactTray surfaces the maintainer's contact info in a tray that pops out
 * over the app shell. It reuses the shared Modal primitive, which already
 * provides focus-trap, Escape-to-close, outside-click dismissal, and focus
 * restoration to the trigger — no need to reimplement that behavior here.
 */
export function ContactTray({ open, onClose }: Props) {
  if (!open) return null

  return (
    <Modal title="Get in touch" onClose={onClose}>
      <p className={styles.intro}>
        Questions, feedback, or a bug to report? Reach the Gleipnir maintainers
        through either channel below.
      </p>
      <ul className={styles.channelList}>
        <li>
          <a className={styles.channel} href={`mailto:${SUPPORT_EMAIL}`}>
            <span className={styles.channelIcon}>
              <Mail size={18} aria-hidden strokeWidth={1.5} />
            </span>
            <span className={styles.channelText}>
              <span className={styles.channelLabel}>Email</span>
              <span className={styles.channelValue}>{SUPPORT_EMAIL}</span>
            </span>
          </a>
        </li>
        <li>
          <a
            className={styles.channel}
            href={GITHUB_REPO_URL}
            target="_blank"
            rel="noopener noreferrer"
          >
            <span className={styles.channelIcon}>
              <ExternalLink size={18} aria-hidden strokeWidth={1.5} />
            </span>
            <span className={styles.channelText}>
              <span className={styles.channelLabel}>GitHub</span>
              <span className={styles.channelValue}>Felag-Engineering/gleipnir</span>
            </span>
          </a>
        </li>
      </ul>
    </Modal>
  )
}
