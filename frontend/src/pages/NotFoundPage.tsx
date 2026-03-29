import { Link } from 'react-router-dom'
import styles from './NotFoundPage.module.css'

export default function NotFoundPage() {
  return (
    <div className={styles.container}>
      <div className={styles.card}>
        <p className={styles.errorCode}>404</p>
        <h1 className={styles.heading}>Page not found</h1>
        <p className={styles.message}>
          The page you are looking for does not exist or has been moved.
        </p>
        <Link to="/dashboard" className={styles.link}>
          Go to Dashboard
        </Link>
      </div>
    </div>
  )
}
