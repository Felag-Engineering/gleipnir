import { useNavigate, useParams } from 'react-router'
import { Link } from 'react-router'
import { ArrowLeft } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { QueryBoundary } from '@/components/QueryBoundary'
import { PluginReviewCard } from '@/components/admin/PluginReviewCard'
import { RejectPluginModal } from '@/components/admin/RejectPluginModal'
import { usePluginDetail } from '@/hooks/queries/plugins'
import { useApprovePlugin, useRejectPlugin } from '@/hooks/mutations/plugins'
import { usePageTitle } from '@/hooks/usePageTitle'
import { extractErrorMessage } from '@/api/fetch'
import { useState } from 'react'
import styles from './PluginReviewPage.module.css'

export default function PluginReviewPage() {
  usePageTitle('Review Plugin')

  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()

  const { data: plugin, status, refetch } = usePluginDetail(id)
  const approveMutation = useApprovePlugin()
  const rejectMutation = useRejectPlugin()

  const [actionError, setActionError] = useState<string | null>(null)
  const [showRejectModal, setShowRejectModal] = useState(false)
  const [rejectError, setRejectError] = useState<string | null>(null)

  function handleApprove() {
    if (!id) return
    setActionError(null)
    approveMutation.mutate(
      { pluginId: id },
      {
        onSuccess: () => {
          // Navigate back to the plugins page once approved so the admin can
          // create the first instance. The plugin will now appear in the active
          // list with an "Add instance" button.
          navigate('/admin/plugins')
        },
        onError: (err: unknown) => {
          setActionError(extractErrorMessage(err))
        },
      },
    )
  }

  function openRejectModal() {
    setRejectError(null)
    setShowRejectModal(true)
  }

  function handleRejectConfirm() {
    if (!id) return
    setRejectError(null)
    rejectMutation.mutate(
      { pluginId: id },
      {
        onSuccess: () => {
          setShowRejectModal(false)
          navigate('/admin/plugins')
        },
        onError: (err: unknown) => {
          setRejectError(extractErrorMessage(err))
        },
      },
    )
  }

  return (
    <div className={styles.page}>
      <PageHeader title="Review Plugin">
        <Link to="/admin/plugins" className={styles.backLink}>
          <ArrowLeft size={16} />
          Back to plugins
        </Link>
      </PageHeader>

      <p className={styles.intro}>
        Review the plugin manifest before allowing it to run on this instance.
      </p>

      <QueryBoundary
        status={status}
        isEmpty={false}
        errorMessage="Failed to load plugin details."
        onRetry={() => { void refetch() }}
      >
        {plugin && (
          <div className={styles.content}>
            {plugin.status !== 'pending_review' && (
              <div className={styles.notPendingBanner}>
                This plugin is no longer pending review (status: {plugin.status}).
                {plugin.status === 'active' && (
                  <> It has already been approved and is active.</>
                )}
              </div>
            )}

            {actionError && (
              <div className={styles.errorBanner}>{actionError}</div>
            )}

            <PluginReviewCard
              plugin={plugin}
              onApprove={handleApprove}
              onReject={openRejectModal}
              isApproving={approveMutation.isPending}
              isRejecting={rejectMutation.isPending}
            />
          </div>
        )}
      </QueryBoundary>

      {showRejectModal && plugin && (
        <RejectPluginModal
          pluginName={plugin.name}
          onClose={() => {
            setShowRejectModal(false)
            setRejectError(null)
          }}
          onConfirm={handleRejectConfirm}
          isPending={rejectMutation.isPending}
          error={rejectError}
        />
      )}
    </div>
  )
}
