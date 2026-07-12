import { Link, useNavigate } from 'react-router-dom'
import { PageHeader } from '@/components/PageHeader'
import { QueryBoundary, SkeletonList } from '@/components/QueryBoundary'
import { EmptyState } from '@/components/EmptyState'
import { Button } from '@/components/Button'
import { useAudiences } from '@/hooks/queries/admin'
import { useCurrentUser } from '@/hooks/queries/users'
import { usePageTitle } from '@/hooks/usePageTitle'
import { formatTimeAgo } from '@/utils/format'
import styles from './AdminAudiencesPage.module.css'

export default function AdminAudiencesPage() {
  usePageTitle('Audiences')
  const navigate = useNavigate()

  const { data: audiences, status, refetch } = useAudiences()
  const { data: currentUser } = useCurrentUser()
  const canManage = currentUser?.roles?.some((r) => r === 'admin' || r === 'operator') ?? false

  return (
    <div className={styles.page}>
      <PageHeader title="Audiences">
        {canManage && (
          <Button
            variant="primary"
            size="small"
            type="button"
            onClick={() => navigate('/admin/audiences/new')}
          >
            + New audience
          </Button>
        )}
      </PageHeader>

      <p className={styles.intro}>
        Audiences are shared notification routing groups referenced from policies by name.
        {!canManage && ' You have read-only access.'}
      </p>

      <QueryBoundary
        status={status}
        isEmpty={(audiences ?? []).length === 0}
        errorMessage="Failed to load audiences."
        onRetry={() => { void refetch() }}
        skeleton={<SkeletonList count={3} height={48} gap={12} borderRadius={8} />}
        emptyState={
          canManage ? (
            <EmptyState
              headline="No audiences"
              ctaLabel="Create your first audience"
              onCtaClick={() => navigate('/admin/audiences/new')}
            />
          ) : (
            <EmptyState
              headline="No audiences"
              subtext="No audiences have been created yet."
            />
          )
        }
      >
        <div className={styles.tableWrapper}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>Name</th>
                <th className={styles.numCol}>Entries</th>
                <th className={styles.numCol}>Referenced by</th>
                <th>Status</th>
                <th>Updated</th>
              </tr>
            </thead>
            <tbody>
              {(audiences ?? []).map((a) => (
                <tr key={a.id}>
                  <td>
                    <Link to={`/admin/audiences/${a.id}`} className={styles.nameLink}>
                      {a.name}
                    </Link>
                  </td>
                  <td className={styles.numCol}>{a.entry_count}</td>
                  <td className={styles.numCol}>
                    {a.referenced_by_policy_count > 0 ? (
                      <span className={styles.refCount}>{a.referenced_by_policy_count}</span>
                    ) : (
                      <span className={styles.refNone}>0</span>
                    )}
                  </td>
                  <td>
                    {a.has_in_flight_runs ? (
                      <span className={`${styles.statusBadge} ${styles.statusInFlight}`}>
                        In-flight runs
                      </span>
                    ) : (
                      <span className={`${styles.statusBadge} ${styles.statusIdle}`}>Idle</span>
                    )}
                  </td>
                  <td className={styles.muted}>{formatTimeAgo(a.updated_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </QueryBoundary>
    </div>
  )
}
