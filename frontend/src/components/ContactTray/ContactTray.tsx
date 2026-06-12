import { Bug, Mail, MessagesSquare } from 'lucide-react'
import { Modal } from '@/components/Modal'
import styles from './ContactTray.module.css'

const SUPPORT_EMAIL = 'support@gleipnir.dev'
const GITHUB_REPO_URL = 'https://github.com/Felag-Engineering/gleipnir'
const GITHUB_NEW_ISSUE_URL = `${GITHUB_REPO_URL}/issues/new`
const GITHUB_DISCUSSIONS_URL = `${GITHUB_REPO_URL}/discussions`

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
        through whichever channel fits.
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
            href={GITHUB_NEW_ISSUE_URL}
            target="_blank"
            rel="noopener noreferrer"
          >
            <span className={styles.channelIcon}>
              <Bug size={18} aria-hidden strokeWidth={1.5} />
            </span>
            <span className={styles.channelText}>
              <span className={styles.channelLabel}>Report a bug</span>
              <span className={styles.channelValue}>GitHub Issues</span>
            </span>
          </a>
        </li>
        <li>
          <a
            className={styles.channel}
            href={GITHUB_DISCUSSIONS_URL}
            target="_blank"
            rel="noopener noreferrer"
          >
            <span className={styles.channelIcon}>
              <MessagesSquare size={18} aria-hidden strokeWidth={1.5} />
            </span>
            <span className={styles.channelText}>
              <span className={styles.channelLabel}>Ask a question</span>
              <span className={styles.channelValue}>GitHub Discussions</span>
            </span>
          </a>
        </li>
      </ul>
    </Modal>
  )
}
