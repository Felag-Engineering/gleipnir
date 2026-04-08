import { useState } from 'react'
import type { ApiOpenAICompatProvider } from '@/api/types'
import type { ApiError } from '@/api/fetch'
import { useOpenAICompatProviders } from '@/hooks/queries/openaiCompatProviders'
import {
  useDeleteOpenAICompatProvider,
  useTestOpenAICompatProvider,
} from '@/hooks/mutations/openaiCompatProviders'
import { formatTimestamp } from '@/utils/format'
import { OpenAICompatProviderModal } from './OpenAICompatProviderModal'
import { OpenAICompatProviderDeleteDialog } from './OpenAICompatProviderDeleteDialog'
import styles from './OpenAICompatProvidersSection.module.css'

type ModalState =
  | { mode: 'create' }
  | { mode: 'edit'; provider: ApiOpenAICompatProvider }
  | null

export function OpenAICompatProvidersSection() {
  const { data: rows = [], isLoading } = useOpenAICompatProviders()
  const deleteMut = useDeleteOpenAICompatProvider()
  const testMut = useTestOpenAICompatProvider()

  const [modalState, setModalState] = useState<ModalState>(null)
  const [deleteTarget, setDeleteTarget] = useState<ApiOpenAICompatProvider | null>(null)

  return (
    <section className={styles.section} aria-labelledby="openai-compat-heading">
      <header className={styles.header}>
        <div className={styles.headerRow}>
          <h2 id="openai-compat-heading" className={styles.heading}>
            OpenAI-compatible providers
          </h2>
          <button
            type="button"
            className={styles.addButton}
            onClick={() => setModalState({ mode: 'create' })}
          >
            Add provider
          </button>
        </div>
        <p className={styles.description}>
          Admin-managed instances backed by the OpenAI Chat Completions API. Add one per backend
          (OpenAI itself, Ollama, vLLM, OpenRouter, etc.).
        </p>
      </header>

      {isLoading ? (
        <div className={styles.loading}>Loading...</div>
      ) : rows.length === 0 ? (
        <div className={styles.empty}>
          <p>No OpenAI-compatible providers configured.</p>
          <p>Add one to use OpenAI, Ollama, vLLM, or any compatible backend.</p>
        </div>
      ) : (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Name</th>
              <th>Base URL</th>
              <th>API Key</th>
              <th>Models</th>
              <th>Updated</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((p) => (
              <tr key={p.id}>
                <td className={styles.name}>{p.name}</td>
                <td className={styles.url}>{p.base_url}</td>
                <td className={styles.key}>{p.masked_key}</td>
                <td>
                  {p.models_endpoint_available ? (
                    <span className={styles.badgeOk}>Available</span>
                  ) : (
                    <span className={styles.badgeWarn}>models endpoint unavailable</span>
                  )}
                </td>
                <td>{formatTimestamp(p.updated_at)}</td>
                <td>
                  <div className={styles.actions}>
                    <button
                      type="button"
                      onClick={() => testMut.mutate(p.id)}
                      disabled={testMut.isPending}
                    >
                      Test
                    </button>
                    <button
                      type="button"
                      onClick={() => setModalState({ mode: 'edit', provider: p })}
                    >
                      Edit
                    </button>
                    <button
                      type="button"
                      className={styles.danger}
                      onClick={() => setDeleteTarget(p)}
                    >
                      Delete
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {modalState !== null && (
        <OpenAICompatProviderModal
          mode={modalState.mode}
          provider={modalState.mode === 'edit' ? modalState.provider : undefined}
          onClose={() => setModalState(null)}
        />
      )}

      {deleteTarget !== null && (
        <OpenAICompatProviderDeleteDialog
          provider={deleteTarget}
          isPending={deleteMut.isPending}
          error={deleteMut.error as ApiError | null}
          onClose={() => {
            setDeleteTarget(null)
            deleteMut.reset()
          }}
          onConfirm={() => {
            deleteMut.mutate(deleteTarget.id, {
              onSuccess: () => setDeleteTarget(null),
            })
          }}
        />
      )}
    </section>
  )
}
