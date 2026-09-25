import { useState } from 'react'
import type { ApiMcpServer, RunAttributionMode } from '@/api/types'
import { useUpdateMcpServer } from '@/hooks/mutations/servers'
import {
  MODE_LABELS,
  buildRunAttributionRequest,
  formatRunAttribution,
  validateCustomHeaderNames,
  type CustomHeaderNames,
} from '../runAttribution'
import styles from './RunAttributionSection.module.css'

interface Props {
  server: ApiMcpServer
}

const MODE_OPTIONS = Object.keys(MODE_LABELS) as RunAttributionMode[]

function customNamesFor(server: ApiMcpServer): CustomHeaderNames {
  const attribution = server.run_attribution
  if (attribution?.mode !== 'custom') {
    return { onBehalfOfHeader: '', sessionRefHeader: '', traceparentHeader: '' }
  }
  return {
    onBehalfOfHeader: attribution.on_behalf_of_header,
    sessionRefHeader: attribution.session_ref_header,
    traceparentHeader: attribution.traceparent_header,
  }
}

// RunAttributionSection shows a server's effective run attribution setting
// (issue #943) and, for non-managed servers, lets an operator choose Off,
// the Relay preset, or custom header names. It owns its own
// useUpdateMcpServer mutation, mirroring CallTimeoutSection — name/url and
// run attribution are edited independently.
export function RunAttributionSection({ server }: Props) {
  // A managed entry's endpoint belongs to the plugin lifecycle, the same
  // reason CallTimeoutSection and CaCertificateSection hide their editors
  // for it.
  const isManaged = server.trust_tier === 'managed'
  const currentMode: RunAttributionMode = server.run_attribution?.mode ?? 'off'

  const [editing, setEditing] = useState(false)
  const [mode, setMode] = useState<RunAttributionMode>(currentMode)
  const [names, setNames] = useState<CustomHeaderNames>(() => customNamesFor(server))
  const [error, setError] = useState<string | null>(null)

  const updateMutation = useUpdateMcpServer()

  function openEditor() {
    setMode(currentMode)
    setNames(customNamesFor(server))
    setError(null)
    setEditing(true)
  }

  function save(request: ReturnType<typeof buildRunAttributionRequest>) {
    setError(null)
    updateMutation.mutate(
      { id: server.id, name: server.name, url: server.url, run_attribution: request },
      {
        onSuccess: () => setEditing(false),
        onError: (err) => setError(err.detail ?? err.message),
      },
    )
  }

  function handleSave() {
    const validationError = validateCustomHeaderNames(mode, names)
    if (validationError) {
      setError(validationError)
      return
    }
    save(buildRunAttributionRequest(mode, names))
  }

  function handleTurnOff() {
    save({ mode: 'off' })
  }

  return (
    <div className={styles.section}>
      <div className={styles.title}>Run attribution</div>
      <div className={styles.value}>{formatRunAttribution(server)}</div>

      {!isManaged && !editing && (
        <div className={styles.actions}>
          <button type="button" className={styles.editBtn} onClick={openEditor}>
            Edit
          </button>
          {currentMode !== 'off' && (
            <button
              type="button"
              className={styles.removeBtn}
              onClick={handleTurnOff}
              disabled={updateMutation.isPending}
            >
              Turn off
            </button>
          )}
        </div>
      )}

      {!isManaged && editing && (
        <div className={styles.editor}>
          <select
            id="server-run-attribution"
            className={styles.select}
            value={mode}
            onChange={(e) => setMode(e.target.value as RunAttributionMode)}
            aria-label="Run attribution mode"
          >
            {MODE_OPTIONS.map((m) => (
              <option key={m} value={m}>
                {MODE_LABELS[m]}
              </option>
            ))}
          </select>

          {mode === 'custom' && (
            <div className={styles.customFields}>
              <label className={styles.fieldLabel}>
                On-behalf-of header
                <input
                  type="text"
                  className={styles.input}
                  value={names.onBehalfOfHeader}
                  onChange={(e) => setNames((n) => ({ ...n, onBehalfOfHeader: e.target.value }))}
                  placeholder="e.g. X-Actor"
                />
              </label>
              <label className={styles.fieldLabel}>
                Session-ref header
                <input
                  type="text"
                  className={styles.input}
                  value={names.sessionRefHeader}
                  onChange={(e) => setNames((n) => ({ ...n, sessionRefHeader: e.target.value }))}
                  placeholder="e.g. X-Session"
                />
              </label>
              <label className={styles.fieldLabel}>
                Traceparent header
                <input
                  type="text"
                  className={styles.input}
                  value={names.traceparentHeader}
                  onChange={(e) => setNames((n) => ({ ...n, traceparentHeader: e.target.value }))}
                  placeholder="e.g. traceparent"
                />
              </label>
            </div>
          )}

          <p className={styles.hint}>
            Sends the agent name, run ID and a trace ID on each tool call. The server records them
            as claims; they are not credentials.
          </p>
          {error && <div className={styles.error}>{error}</div>}
          <div className={styles.editorFooter}>
            <button
              type="button"
              className={styles.cancelBtn}
              onClick={() => {
                setEditing(false)
                setError(null)
              }}
            >
              Cancel
            </button>
            <button
              type="button"
              className={styles.saveBtn}
              onClick={handleSave}
              disabled={updateMutation.isPending}
            >
              {updateMutation.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
