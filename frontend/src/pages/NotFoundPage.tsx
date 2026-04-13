import { Link } from 'react-router-dom'
import styles from './NotFoundPage.module.css'

interface NotFoundPageProps {
  title?: string
  message?: string
  primary?: { label: string; to: string }
  secondary?: { label: string; to: string }
  /** When true, renders only the card without the outer container div.
   * Use this when embedding inside another page's layout to avoid double-wrapping. */
  embedded?: boolean
}

export function NotFoundPage({
  title = 'Page not found',
  message = 'The page you are looking for does not exist or has been moved.',
  primary = { label: 'Go to Dashboard', to: '/dashboard' },
  secondary,
  embedded = false,
}: NotFoundPageProps) {
  const card = (
    <div className={styles.card}>
      <p className={styles.errorCode}>404</p>
      <h1 className={styles.heading}>{title}</h1>
      <p className={styles.message}>{message}</p>
      <div className={styles.links}>
        <Link to={primary.to} className={styles.link}>
          {primary.label}
        </Link>
        {secondary && (
          <Link to={secondary.to} className={styles.secondaryLink}>
            {secondary.label}
          </Link>
        )}
      </div>
    </div>
  )

  if (embedded) {
    return card
  }

  return (
    <div className={styles.container} data-testid="not-found-container">
      {card}
    </div>
  )
}

export default NotFoundPage
