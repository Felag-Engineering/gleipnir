import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import type { ApiOpenAICompatProvider } from '@/api/types'
import type { ApiError } from '@/api/fetch'
import alertStyles from '@/styles/alerts.module.css'

interface Props {
  provider: ApiOpenAICompatProvider
  isPending: boolean
  error?: ApiError | null
  onClose: () => void
  onConfirm: () => void
}

export function OpenAICompatProviderDeleteDialog({ provider, isPending, error, onClose, onConfirm }: Props) {
  const footer = (
    <ModalFooter
      onCancel={onClose}
      onSubmit={onConfirm}
      submitLabel="Delete provider"
      loadingLabel="Deleting…"
      variant="danger"
      isLoading={isPending}
    />
  )

  return (
    <Modal title="Delete provider" onClose={onClose} footer={footer}>
      <p>
        Deleting <strong>{provider.name}</strong> will not stop any runs currently in progress,
        but new runs that reference &ldquo;{provider.name}&rdquo; will fail. Policies referencing
        this provider will need to be updated manually.
      </p>
      {error && (
        <div className={alertStyles.alertError} role="alert">
          {error.detail ?? error.message}
        </div>
      )}
    </Modal>
  )
}
