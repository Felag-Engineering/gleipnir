import { useRef, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Button } from '@/components/Button'
import { AddInstanceModal } from '@/components/admin/AddInstanceModal'
import { ApiError } from '@/api/fetch'
import { useInstallPlugin } from '@/hooks/mutations/plugins'
import type { ApiInstalledPlugin } from '@/api/types'
import spinnerStyles from '@/styles/spinner.module.css'
import styles from './InstallPluginButton.module.css'

const MAX_SIZE_BYTES = 100 * 1024 * 1024 // 100 MiB

interface InstallPluginButtonProps {
  // Called when a plugin is successfully installed; the parent can use this
  // to scroll the new plugin row into view or clear success state once it
  // appears in the instance list.
  onInstalled?: (plugin: ApiInstalledPlugin) => void
  // Callback that returns true when the freshly installed plugin already has
  // at least one instance in the list query. When it flips to true the
  // success card auto-clears (the operator no longer needs the CTA).
  hasInstancesForPlugin?: (pluginId: string) => boolean
}

type InstallState =
  | { type: 'idle' }
  | { type: 'uploading' }
  | { type: 'success'; plugin: ApiInstalledPlugin }
  | { type: 'error'; message: string; detail?: string; is503: boolean }

function mapInstallError(err: unknown): { message: string; detail?: string; is503: boolean } {
  if (!(err instanceof ApiError)) {
    const msg = err instanceof Error ? err.message : 'Unexpected error — please try again.'
    return { message: `Install failed: ${msg}`, is503: false }
  }

  switch (err.status) {
    case 413:
      return { message: 'Tarball too large (max 100 MiB).', is503: false }
    case 503:
      return {
        message:
          'Plugin install is disabled on this server. Set GLEIPNIR_PLUGINS_ENABLED=true and restart to enable.',
        is503: true,
      }
    default:
      return {
        message: `Install failed: ${err.message}`,
        detail: err.detail,
        is503: false,
      }
  }
}

export function InstallPluginButton({ onInstalled, hasInstancesForPlugin }: InstallPluginButtonProps) {
  const fileInputRef = useRef<HTMLInputElement>(null)
  const mutation = useInstallPlugin()
  const [state, setState] = useState<InstallState>({ type: 'idle' })
  // When the modal is open for post-install "Add instance" CTA, store the plugin.
  const [addInstancePlugin, setAddInstancePlugin] = useState<ApiInstalledPlugin | null>(null)

  // Auto-clear the success card once the installed plugin appears in the
  // instances list. This avoids stranding the operator in the case where
  // they created the first instance via the inline CTA and the list refreshed.
  useEffect(() => {
    if (state.type !== 'success') return
    if (!hasInstancesForPlugin) return
    if (hasInstancesForPlugin(state.plugin.id)) {
      setState({ type: 'idle' })
    }
  }, [state, hasInstancesForPlugin])

  function handleButtonClick() {
    fileInputRef.current?.click()
  }

  function handleFileChange(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    if (!file) return
    // Reset the input so selecting the same file again triggers onChange.
    e.target.value = ''

    if (file.size > MAX_SIZE_BYTES) {
      setState({
        type: 'error',
        message: 'Tarball too large (max 100 MiB).',
        is503: false,
      })
      return
    }

    setState({ type: 'uploading' })
    mutation.mutate(
      { file },
      {
        onSuccess: (plugin) => {
          setState({ type: 'success', plugin })
          onInstalled?.(plugin)
        },
        onError: (err) => {
          const mapped = mapInstallError(err)
          setState({ type: 'error', message: mapped.message, detail: mapped.detail, is503: mapped.is503 })
        },
      },
    )
  }

  function dismissState() {
    setState({ type: 'idle' })
  }

  const isUploading = state.type === 'uploading'

  return (
    <div className={styles.root}>
      {/* Hidden file input — triggered by the styled button below */}
      <input
        ref={fileInputRef}
        type="file"
        accept=".tar.gz,.tgz,application/gzip"
        className={styles.hiddenInput}
        onChange={handleFileChange}
        aria-hidden="true"
        tabIndex={-1}
        data-testid="install-plugin-input"
      />

      <Button
        variant="primary"
        size="small"
        onClick={handleButtonClick}
        disabled={isUploading}
      >
        {isUploading ? (
          <>
            <span className={spinnerStyles.spinner} aria-hidden="true" />
            Installing…
          </>
        ) : (
          'Install plugin'
        )}
      </Button>

      {state.type === 'success' && (
        <div className={styles.successCard} role="status">
          <p className={styles.successMessage}>
            Installed <strong>{state.plugin.name}</strong> v{state.plugin.version}.
          </p>
          <div className={styles.successActions}>
            {state.plugin.status === 'pending_review' ? (
              <Link
                to={`/admin/plugins/${encodeURIComponent(state.plugin.id)}/review`}
                className={styles.reviewLink}
              >
                Review &amp; approve
              </Link>
            ) : (
              <Button
                variant="secondary"
                size="small"
                onClick={() => setAddInstancePlugin(state.plugin)}
              >
                Add instance
              </Button>
            )}
            <Button variant="ghost" size="small" onClick={dismissState}>
              Dismiss
            </Button>
          </div>
        </div>
      )}

      {state.type === 'error' && state.is503 && (
        <div className={styles.disabledNotice} role="alert">
          {state.message}
        </div>
      )}

      {state.type === 'error' && !state.is503 && (
        <div className={styles.errorCard} role="alert">
          <p className={styles.errorMessage}>{state.message}</p>
          {state.detail && (
            <details className={styles.errorDetail}>
              <summary>Details</summary>
              <pre>{state.detail}</pre>
            </details>
          )}
          <Button variant="ghost" size="small" onClick={dismissState}>
            Dismiss
          </Button>
        </div>
      )}

      {addInstancePlugin && (
        <AddInstanceModal
          pluginId={addInstancePlugin.id}
          pluginName={addInstancePlugin.name}
          existingNames={[]}
          onClose={() => setAddInstancePlugin(null)}
        />
      )}
    </div>
  )
}
