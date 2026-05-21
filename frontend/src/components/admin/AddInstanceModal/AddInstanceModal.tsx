import { useState, useId } from 'react'
import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import { ApiError } from '@/api/fetch'
import { useCreatePluginInstance } from '@/hooks/mutations/plugins'
import type { ApiCreatedPluginInstance } from '@/api/types'
import styles from './AddInstanceModal.module.css'

interface AddInstanceModalProps {
  pluginId: string
  // pluginName is shown in the modal title for operator context.
  pluginName: string
  // existingNames is used for a client-side courtesy duplicate check.
  // The server's 409 remains the source of truth for concurrent creates.
  existingNames: string[]
  onClose: () => void
  onCreated?: (inst: ApiCreatedPluginInstance) => void
}

function mapCreateError(err: unknown): string {
  if (!(err instanceof ApiError)) {
    const msg = err instanceof Error ? err.message : 'Unexpected error — please try again.'
    return `Failed to create instance: ${msg}`
  }

  if (err.status === 404) {
    // Handled separately in the component with a special action (close + refresh).
    return 'This plugin no longer exists. The list has been refreshed.'
  }

  return `Failed to create instance: ${err.message}`
}

// AddInstanceModal opens a small form to create a named plugin instance.
// It validates the name client-side before hitting the API, but the server
// 409 path is authoritative for concurrent duplicate-name creates.
export function AddInstanceModal({
  pluginId,
  pluginName,
  existingNames,
  onClose,
  onCreated,
}: AddInstanceModalProps) {
  const inputId = useId()
  const [instanceName, setInstanceName] = useState('')
  const [validationError, setValidationError] = useState<string | null>(null)
  const [apiError, setApiError] = useState<{ message: string; detail?: string } | null>(null)
  const mutation = useCreatePluginInstance()

  function validate(name: string): string | null {
    if (!name.trim()) return 'Instance name is required.'
    if (existingNames.includes(name)) {
      return `An instance named '${name}' already exists for this plugin.`
    }
    return null
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setApiError(null)

    const err = validate(instanceName)
    if (err) {
      setValidationError(err)
      return
    }
    setValidationError(null)

    mutation.mutate(
      { pluginId, instanceName },
      {
        onSuccess: (inst) => {
          onCreated?.(inst)
          onClose()
        },
        onError: (rawErr) => {
          const message = mapCreateError(rawErr)
          const detail = rawErr instanceof ApiError ? rawErr.detail : undefined
          setApiError({ message, detail })

          if (rawErr instanceof ApiError && rawErr.status === 404) {
            // Close after a short pause so the operator can read the message.
            setTimeout(onClose, 1500)
          }
        },
      },
    )
  }

  const footer = (
    <ModalFooter
      onCancel={onClose}
      formId={inputId + '-form'}
      isLoading={mutation.isPending}
      submitLabel="Create instance"
      loadingLabel="Creating…"
      submitDisabled={mutation.isPending}
    />
  )

  return (
    <Modal title={`Add instance to ${pluginName}`} onClose={onClose} footer={footer}>
      <form id={inputId + '-form'} onSubmit={handleSubmit} className={styles.form} noValidate>
        <div className={styles.field}>
          <label htmlFor={inputId} className={styles.label}>
            Instance name
          </label>
          <input
            id={inputId}
            type="text"
            className={styles.input}
            value={instanceName}
            onChange={(e) => {
              setInstanceName(e.target.value)
              if (validationError) setValidationError(null)
            }}
            placeholder="e.g. production"
            required
            disabled={mutation.isPending}
            autoFocus
          />
          {validationError && (
            <p className={styles.fieldError} role="alert">
              {validationError}
            </p>
          )}
        </div>

        {apiError && (
          <div className={styles.apiError} role="alert">
            <p className={styles.apiErrorMessage}>{apiError.message}</p>
            {apiError.detail && (
              <details className={styles.apiErrorDetail}>
                <summary>Details</summary>
                <pre>{apiError.detail}</pre>
              </details>
            )}
          </div>
        )}
      </form>
    </Modal>
  )
}
