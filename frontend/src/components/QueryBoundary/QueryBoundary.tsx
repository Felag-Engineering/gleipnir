import type { ReactNode } from 'react'
import { Button } from '@/components/Button'
import SkeletonList from '@/components/QueryBoundary/SkeletonList'
import styles from './QueryBoundary.module.css'

export interface QueryBoundaryProps {
  status: 'pending' | 'error' | 'success'
  error?: Error | null
  isEmpty?: boolean
  onRetry?: () => void
  errorMessage?: string
  skeleton?: ReactNode
  emptyState?: ReactNode
  children: ReactNode
}

export default function QueryBoundary({
  status,
  isEmpty = false,
  onRetry,
  errorMessage = 'Something went wrong.',
  skeleton,
  emptyState,
  children,
}: QueryBoundaryProps) {
  if (status === 'pending') {
    return skeleton !== undefined ? <>{skeleton}</> : <SkeletonList />
  }

  if (status === 'error') {
    return (
      <div className={styles.errorState} role="alert">
        <span>{errorMessage}</span>
        {onRetry && (
          <Button variant="ghost" onClick={onRetry}>
            Retry
          </Button>
        )}
      </div>
    )
  }

  if (isEmpty) {
    return emptyState ? <>{emptyState}</> : null
  }

  return <>{children}</>
}
