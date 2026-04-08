import { useState, type FormEvent } from 'react'
import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import {
  useCreateOpenAICompatProvider,
  useUpdateOpenAICompatProvider,
} from '@/hooks/mutations/openaiCompatProviders'
import type { ApiOpenAICompatProvider, ApiOpenAICompatProviderUpsert } from '@/api/types'
import styles from './OpenAICompatProviderModal.module.css'
import formStyles from '@/styles/forms.module.css'
import alertStyles from '@/styles/alerts.module.css'

interface Props {
  mode: 'create' | 'edit'
  /** Required when mode === 'edit'. */
  provider?: ApiOpenAICompatProvider
  onClose: () => void
}

export function OpenAICompatProviderModal({ mode, provider, onClose }: Props) {
  const [name, setName] = useState(mode === 'edit' ? (provider?.name ?? '') : '')
  const [baseUrl, setBaseUrl] = useState(mode === 'edit' ? (provider?.base_url ?? '') : '')
  // Never pre-fill the API key — the masked value from the server is not a valid key.
  const [apiKey, setApiKey] = useState('')
  const [error, setError] = useState<string | null>(null)

  const createMut = useCreateOpenAICompatProvider()
  const updateMut = useUpdateOpenAICompatProvider()

  const isPending = createMut.isPending || updateMut.isPending

  // In create mode the API key is required; in edit mode it is optional.
  const submitDisabled =
    !name.trim() ||
    !baseUrl.trim() ||
    (mode === 'create' && !apiKey.trim())

  function applyOpenAIPreset() {
    setBaseUrl('https://api.openai.com/v1')
    // Only fill the name if the user hasn't typed anything yet.
    if (!name.trim()) {
      setName('openai')
    }
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)

    const body: ApiOpenAICompatProviderUpsert = {
      name: name.trim(),
      base_url: baseUrl.trim(),
      // In edit mode with a blank API key the backend treats '' as "keep existing".
      api_key: apiKey,
    }

    if (mode === 'create') {
      createMut.mutate(body, {
        onSuccess: onClose,
        onError: (err) =>
          setError(err instanceof Error ? err.message : 'Save failed, please try again'),
      })
    } else {
      updateMut.mutate(
        { id: provider!.id, body },
        {
          onSuccess: onClose,
          onError: (err) =>
            setError(err instanceof Error ? err.message : 'Save failed, please try again'),
        },
      )
    }
  }

  const footer = (
    <ModalFooter
      onCancel={onClose}
      formId="openai-compat-provider-form"
      isLoading={isPending}
      submitLabel={mode === 'create' ? 'Add provider' : 'Save changes'}
      loadingLabel="Saving…"
      submitDisabled={submitDisabled}
    />
  )

  return (
    <Modal
      title={mode === 'create' ? 'Add OpenAI-compatible provider' : 'Edit provider'}
      onClose={onClose}
      footer={footer}
    >
      <div className={styles.presetRow}>
        <button type="button" className={styles.presetChip} onClick={applyOpenAIPreset}>
          OpenAI
        </button>
        <span className={styles.presetHelp}>
          Quick-fill the OpenAI defaults. Edit any field after applying.
        </span>
      </div>

      <form id="openai-compat-provider-form" onSubmit={handleSubmit} className={formStyles.form}>
        <div className={formStyles.field}>
          <label htmlFor="provider-name" className={formStyles.labelMono}>
            Name
          </label>
          <input
            id="provider-name"
            type="text"
            className={styles.input}
            placeholder="openai"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus={mode === 'create'}
            required
          />
        </div>

        <div className={formStyles.field}>
          <label htmlFor="provider-base-url" className={formStyles.labelMono}>
            Base URL
          </label>
          <input
            id="provider-base-url"
            type="url"
            className={styles.input}
            placeholder="https://api.openai.com/v1"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            required
          />
        </div>

        <div className={formStyles.field}>
          <label htmlFor="provider-api-key" className={formStyles.labelMono}>
            API Key{mode === 'edit' && <span className={formStyles.optional}> (optional)</span>}
          </label>
          <input
            id="provider-api-key"
            type="password"
            className={styles.input}
            placeholder={mode === 'edit' ? '••••••••' : 'sk-...'}
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            required={mode === 'create'}
          />
          <span className={styles.help}>
            {mode === 'edit'
              ? 'Leave blank to keep the current key. Paste a new key to replace it.'
              : 'Will be encrypted at rest.'}
          </span>
        </div>

        {error && (
          <div className={alertStyles.alertError} role="alert">
            {error}
          </div>
        )}
      </form>
    </Modal>
  )
}
