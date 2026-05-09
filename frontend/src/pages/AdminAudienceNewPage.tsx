import { useEffect } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { QueryBoundary, SkeletonList } from '@/components/QueryBoundary'
import { AudienceEditor } from '@/components/admin/AudienceEditor'
import { usePluginInstancesForAudience } from '@/hooks/queries/admin'
import { useCreateAudience } from '@/hooks/mutations/admin'
import { useCurrentUser } from '@/hooks/queries/users'
import { usePageTitle } from '@/hooks/usePageTitle'
import type { AudienceCreateRequest } from '@/api/types'
import type { ApiError } from '@/api/fetch'
import styles from './AdminAudienceNewPage.module.css'

export default function AdminAudienceNewPage() {
  usePageTitle('New Audience')
  const navigate = useNavigate()

  const { data: currentUser } = useCurrentUser()
  const canManage = currentUser?.roles?.some((r) => r === 'admin' || r === 'operator') ?? false

  // Guard: redirect non-managers immediately (server enforces, but client guards early).
  useEffect(() => {
    if (currentUser && !canManage) {
      navigate('/admin/audiences', { replace: true })
    }
  }, [currentUser, canManage, navigate])

  const pluginInstancesQuery = usePluginInstancesForAudience()
  const createMutation = useCreateAudience()

  async function handleSave(req: AudienceCreateRequest) {
    const audience = await createMutation.mutateAsync(req)
    navigate(`/admin/audiences/${audience.id}`)
    return audience
  }

  return (
    <div className={styles.page}>
      <Link to="/admin/audiences" className={styles.backLink}>
        <ArrowLeft size={14} strokeWidth={1.5} aria-hidden />
        All audiences
      </Link>

      <PageHeader title="New Audience" />

      <QueryBoundary
        status={pluginInstancesQuery.status}
        errorMessage="Failed to load plugin instances."
        onRetry={() => { void pluginInstancesQuery.refetch() }}
        skeleton={<SkeletonList count={3} height={48} gap={12} borderRadius={8} />}
      >
        <AudienceEditor
          initial={null}
          pluginInstances={pluginInstancesQuery.data ?? []}
          references={null}
          canManage={canManage}
          onSave={handleSave as Parameters<typeof AudienceEditor>[0]['onSave']}
          saveError={createMutation.error as ApiError | null}
          deleteError={null}
        />
      </QueryBoundary>
    </div>
  )
}
