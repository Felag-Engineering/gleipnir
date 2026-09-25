import { useState, type FormEvent } from 'react'
import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import { Button } from '@/components/Button/Button'
import { useTestMcpConnection } from '@/hooks/mutations/servers'
import type { ApiError } from '@/api/fetch'
import type { RunAttributionMode, RunAttributionRequest } from '@/api/types'
import { ErrorBanner } from '@/components/form/ErrorBanner'
import { parseCallTimeoutInput } from '../callTimeout'
import {
  MODE_LABELS,
  buildRunAttributionRequest,
  validateCustomHeaderNames,
  type CustomHeaderNames,
} from '../runAttribution'
import styles from './AddServerModal.module.css'
import formStyles from '@/styles/forms.module.css'
import alertStyles from '@/styles/alerts.module.css'

interface HeaderRow {
  key: string
  value: string
}

const RUN_ATTRIBUTION_MODE_OPTIONS = Object.keys(MODE_LABELS) as RunAttributionMode[]
const EMPTY_CUSTOM_NAMES: CustomHeaderNames = {
  onBehalfOfHeader: '',
  sessionRefHeader: '',
  traceparentHeader: '',
}

interface Props {
  onClose: () => void
  onSubmit: (
    name: string,
    url: string,
    headers: HeaderRow[],
    caCertPem: string,
    callTimeoutSeconds: number | null,
    // null means Off (issue #943).
    runAttribution: RunAttributionRequest | null,
  ) => void
  isPending: boolean
  error: ApiError | null
  discoveryWarning?: string | null
}

export function AddServerModal({ onClose, onSubmit, isPending, error, discoveryWarning }: Props) {
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [headers, setHeaders] = useState<HeaderRow[]>([])
  const [caCertPem, setCaCertPem] = useState('')
  const [callTimeout, setCallTimeout] = useState('')
  const [runAttributionMode, setRunAttributionMode] = useState<RunAttributionMode>('off')
  const [runAttributionNames, setRunAttributionNames] = useState<CustomHeaderNames>(EMPTY_CUSTOM_NAMES)
  const testMutation = useTestMcpConnection()

  const { value: parsedCallTimeout, error: callTimeoutError } = parseCallTimeoutInput(callTimeout)
  const runAttributionError = validateCustomHeaderNames(runAttributionMode, runAttributionNames)

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (name.trim() && url.trim() && !callTimeoutError && !runAttributionError) {
      // Filter out rows where both key and value are empty.
      const nonEmpty = headers.filter((h) => h.key.trim() || h.value.trim())
      const runAttribution =
        runAttributionMode === 'off'
          ? null
          : buildRunAttributionRequest(runAttributionMode, runAttributionNames)
      onSubmit(name.trim(), url.trim(), nonEmpty, caCertPem.trim(), parsedCallTimeout, runAttribution)
    }
  }

  function handleUrlChange(e: React.ChangeEvent<HTMLInputElement>) {
    setUrl(e.target.value)
    // Clear previous test result when the URL changes so stale results are not shown.
    if (testMutation.data || testMutation.isError) {
      testMutation.reset()
    }
  }

  function handleCaCertPemChange(e: React.ChangeEvent<HTMLTextAreaElement>) {
    setCaCertPem(e.target.value)
    if (testMutation.data || testMutation.isError) {
      testMutation.reset()
    }
  }

  function handleTestConnection() {
    if (url.trim()) {
      const nonEmpty = headers.filter((h) => h.key.trim() || h.value.trim())
      testMutation.mutate({
        url: url.trim(),
        auth_headers: nonEmpty.length > 0 ? nonEmpty : undefined,
        ca_cert_pem: caCertPem.trim() || undefined,
      })
    }
  }

  function addHeaderRow() {
    setHeaders((prev) => [...prev, { key: '', value: '' }])
  }

  function removeHeaderRow(index: number) {
    setHeaders((prev) => prev.filter((_, i) => i !== index))
  }

  function updateHeaderKey(index: number, key: string) {
    setHeaders((prev) => prev.map((h, i) => (i === index ? { ...h, key } : h)))
  }

  function updateHeaderValue(index: number, value: string) {
    setHeaders((prev) => prev.map((h, i) => (i === index ? { ...h, value } : h)))
  }

  const footer = (
    <ModalFooter
      onCancel={onClose}
      formId="add-server-form"
      isLoading={isPending}
      submitLabel="Add MCP server"
      loadingLabel="Adding…"
      submitDisabled={!name.trim() || !url.trim() || !!callTimeoutError || !!runAttributionError}
    />
  )

  return (
    <Modal title="Add MCP server" onClose={onClose} footer={footer}>
      <form id="add-server-form" onSubmit={handleSubmit} className={formStyles.form}>
        <div className={formStyles.field}>
          <label htmlFor="server-name" className={formStyles.labelMono}>Name</label>
          <input
            id="server-name"
            type="text"
            className={styles.input}
            placeholder="e.g. kubectl-mcp"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus
            required
          />
        </div>
        <div className={formStyles.field}>
          <label htmlFor="server-url" className={formStyles.labelMono}>URL</label>
          <div className={styles.testRow}>
            <input
              id="server-url"
              type="url"
              className={styles.input}
              placeholder="http://my-mcp-server:8080"
              value={url}
              onChange={handleUrlChange}
              required
            />
            <Button
              variant="secondary"
              size="small"
              className={styles.testButton}
              disabled={!url.trim() || testMutation.isPending}
              onClick={handleTestConnection}
            >
              {testMutation.isPending ? 'Testing...' : 'Test connection'}
            </Button>
          </div>
          {testMutation.data && (
            <div className={styles.testResult}>
              {testMutation.data.ok && testMutation.data.tool_count > 0 && (
                <div className={alertStyles.alertSuccess} role="status">
                  Connection successful — {testMutation.data.tool_count} tool(s) found
                  {testMutation.data.tools.length > 0 && (
                    <ul className={styles.toolList}>
                      {testMutation.data.tools.map((tool) => (
                        <li key={tool}>{tool}</li>
                      ))}
                    </ul>
                  )}
                </div>
              )}
              {testMutation.data.ok && testMutation.data.tool_count === 0 && (
                <div className={alertStyles.alertWarning} role="status">
                  Connection successful but no tools found
                </div>
              )}
              {!testMutation.data.ok && (
                <div className={alertStyles.alertError} role="alert">
                  {testMutation.data.error}
                </div>
              )}
            </div>
          )}
        </div>

        <div className={formStyles.field}>
          <label htmlFor="server-ca-cert" className={formStyles.labelMono}>
            CA certificate (PEM) <span className={styles.optionalLabel}>(optional)</span>
          </label>
          <textarea
            id="server-ca-cert"
            className={styles.caCertInput}
            placeholder="-----BEGIN CERTIFICATE-----"
            value={caCertPem}
            onChange={handleCaCertPemChange}
            spellCheck={false}
          />
          <p className={styles.fieldHint}>
            For HTTPS servers behind a private CA. Paste the CA certificate (not the server
            certificate); only this CA will be trusted for this server.
          </p>
        </div>

        <div className={formStyles.field}>
          <label htmlFor="server-call-timeout" className={formStyles.labelMono}>
            Call timeout (seconds) <span className={styles.optionalLabel}>(optional)</span>
          </label>
          <input
            id="server-call-timeout"
            type="number"
            inputMode="numeric"
            min={1}
            max={600}
            step={1}
            className={styles.callTimeoutInput}
            placeholder="Default"
            value={callTimeout}
            onChange={(e) => setCallTimeout(e.target.value)}
          />
          <p className={styles.fieldHint}>
            Leave blank to use the instance default. Raise it for servers whose tool calls
            legitimately run long (1–600 seconds).
          </p>
          {callTimeoutError && <div className={styles.fieldError}>{callTimeoutError}</div>}
        </div>

        <div className={formStyles.field}>
          <label htmlFor="server-run-attribution" className={formStyles.labelMono}>
            Run attribution <span className={styles.optionalLabel}>(optional)</span>
          </label>
          <select
            id="server-run-attribution"
            className={styles.input}
            value={runAttributionMode}
            onChange={(e) => setRunAttributionMode(e.target.value as RunAttributionMode)}
          >
            {RUN_ATTRIBUTION_MODE_OPTIONS.map((mode) => (
              <option key={mode} value={mode}>
                {MODE_LABELS[mode]}
              </option>
            ))}
          </select>
          {runAttributionMode === 'custom' && (
            <div className={styles.headerRow}>
              <input
                type="text"
                className={styles.headerKeyInput}
                placeholder="On-behalf-of header, e.g. X-Actor"
                value={runAttributionNames.onBehalfOfHeader}
                onChange={(e) =>
                  setRunAttributionNames((n) => ({ ...n, onBehalfOfHeader: e.target.value }))
                }
                aria-label="On-behalf-of header name"
              />
              <input
                type="text"
                className={styles.headerKeyInput}
                placeholder="Session-ref header, e.g. X-Session"
                value={runAttributionNames.sessionRefHeader}
                onChange={(e) =>
                  setRunAttributionNames((n) => ({ ...n, sessionRefHeader: e.target.value }))
                }
                aria-label="Session-ref header name"
              />
              <input
                type="text"
                className={styles.headerKeyInput}
                placeholder="Traceparent header, e.g. traceparent"
                value={runAttributionNames.traceparentHeader}
                onChange={(e) =>
                  setRunAttributionNames((n) => ({ ...n, traceparentHeader: e.target.value }))
                }
                aria-label="Traceparent header name"
              />
            </div>
          )}
          <p className={styles.fieldHint}>
            Sends the agent name, run ID and a trace ID on each tool call. The server records them
            as claims; they are not credentials.
          </p>
          {runAttributionError && <div className={styles.fieldError}>{runAttributionError}</div>}
        </div>

        <div className={formStyles.field}>
          <label className={formStyles.labelMono}>
            Authentication headers <span className={styles.optionalLabel}>(optional)</span>
          </label>
          {headers.map((header, index) => (
            <div key={index} className={styles.headerRow}>
              <input
                type="text"
                className={styles.headerKeyInput}
                placeholder="Header name"
                value={header.key}
                onChange={(e) => updateHeaderKey(index, e.target.value)}
                aria-label={`Auth header name ${index + 1}`}
              />
              <input
                type="text"
                className={styles.headerValueInput}
                placeholder="Value"
                value={header.value}
                onChange={(e) => updateHeaderValue(index, e.target.value)}
                aria-label={`Auth header value ${index + 1}`}
              />
              <button
                type="button"
                className={styles.headerRemoveButton}
                onClick={() => removeHeaderRow(index)}
                aria-label={`Remove header ${index + 1}`}
              >
                &times;
              </button>
            </div>
          ))}
          <button type="button" className={styles.addHeaderButton} onClick={addHeaderRow}>
            + Add header
          </button>
        </div>

        <ErrorBanner
          issues={
            error
              ? (error.issues ??
                  (error.detail
                    ? [{ message: error.detail }]
                    : [{ message: error.message }]))
              : []
          }
        />
        {discoveryWarning && (
          <div className={alertStyles.alertWarning} role="status">
            Server registered, but tool discovery failed: {discoveryWarning}. You can retry with the Discover button.
          </div>
        )}
      </form>
    </Modal>
  )
}
