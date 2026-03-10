import styles from './ErrorFallback.module.css'

interface ErrorFallbackProps {
  error?: Error | unknown
  resetErrorBoundary: () => void
}

export default function ErrorFallback({ error, resetErrorBoundary }: ErrorFallbackProps) {
  const message =
    error instanceof Error ? error.message : 'An unexpected error occurred.'

  const stack = error instanceof Error ? error.stack : undefined

  return (
    <div className={styles.container}>
      <div className={styles.card}>
        <h2 className={styles.heading}>Something went wrong</h2>
        <p className={styles.message}>{message}</p>
        <button className={styles.retryButton} onClick={resetErrorBoundary}>
          Try again
        </button>
        {import.meta.env.DEV && stack && (
          <pre className={styles.stack}>{stack}</pre>
        )}
      </div>
    </div>
  )
}
