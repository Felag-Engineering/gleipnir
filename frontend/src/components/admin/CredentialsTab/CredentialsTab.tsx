import { useState } from 'react'
import { Button } from '@/components/Button'
import { ClearCredentialsModal } from '@/components/admin/ClearCredentialsModal/ClearCredentialsModal'
import { ReauthorizeButton } from '@/components/admin/ReauthorizeButton/ReauthorizeButton'
import { usePluginInstanceCredentials } from '@/hooks/queries/plugins'
import {
  useSetPluginStaticAPIKey,
  useSetPluginHeader,
  useDeletePluginHeader,
  useSetPluginBasicAuth,
  useSetPluginOAuthClient,
  useClearPluginCredentials,
} from '@/hooks/mutations/plugins'
import { isOAuthRefreshFailure } from '@/utils/pluginHealth'
import { formatTimestamp } from '@/utils/format'
import { errMessage } from '@/api/fetch'
import type { PluginAuthStrategy } from '@/api/types'
import styles from './CredentialsTab.module.css'


// Reserved header names mirroring internal/infra/headervalidate.go.
// Rejecting these client-side gives immediate feedback; the server also rejects them.
const RESERVED_HEADERS = new Set([
  'Mcp-Session-Id',
  'Mcp-Method',
  'Mcp-Name',
  'Mcp-Protocol-Version',
  'Content-Type',
  'Accept',
  'Content-Length',
  'Host',
])

function isReservedHeader(name: string): boolean {
  // Case-insensitive check to match RFC 7230 header name comparison.
  const normalised = name.toLowerCase()
  for (const reserved of RESERVED_HEADERS) {
    if (reserved.toLowerCase() === normalised) return true
  }
  return false
}

// ── StaticAPIKeyForm ──────────────────────────────────────────────────────────

interface StaticAPIKeyFormProps {
  pluginId: string
  instanceId: string
  initialHeaderName: string
  initialScheme: string
  canManage: boolean
}

function StaticAPIKeyForm({
  pluginId,
  instanceId,
  initialHeaderName,
  initialScheme,
  canManage,
}: StaticAPIKeyFormProps) {
  const [headerName, setHeaderName] = useState(initialHeaderName)
  const [scheme, setScheme] = useState(initialScheme)
  const [apiKey, setApiKey] = useState('')
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const mutation = useSetPluginStaticAPIKey()

  function handleSave() {
    setSaved(false)
    setError(null)
    if (!headerName.trim()) {
      setError('Header name is required.')
      return
    }
    if (!apiKey.trim()) {
      setError('API key is required.')
      return
    }
    mutation.mutate(
      { pluginId, instanceId, header_name: headerName.trim(), scheme: scheme.trim() || undefined, api_key: apiKey },
      {
        onSuccess: () => {
          setSaved(true)
          setApiKey('')
        },
        onError: (err) => {
          setError(errMessage(err, 'Save failed.'))
        },
      },
    )
  }

  return (
    <div className={styles.form}>
      <div className={styles.fieldRow}>
        <label htmlFor="static-header-name" className={styles.fieldLabel}>
          Header name
        </label>
        <input
          id="static-header-name"
          type="text"
          className={styles.input}
          value={headerName}
          onChange={(e) => setHeaderName(e.target.value)}
          placeholder="e.g. X-API-Key"
          disabled={!canManage}
        />
      </div>

      <div className={styles.fieldRow}>
        <label htmlFor="static-scheme" className={styles.fieldLabel}>
          Scheme <span className={styles.optional}>(optional)</span>
        </label>
        <input
          id="static-scheme"
          type="text"
          className={styles.input}
          value={scheme}
          onChange={(e) => setScheme(e.target.value)}
          placeholder="e.g. Bearer"
          disabled={!canManage}
        />
      </div>

      <div className={styles.fieldRow}>
        <label htmlFor="static-api-key" className={styles.fieldLabel}>
          API key
        </label>
        <p className={styles.writeOnlyNote}>Write-only — enter a new value to replace.</p>
        <input
          id="static-api-key"
          type="password"
          className={styles.input}
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          autoComplete="new-password"
          disabled={!canManage}
        />
      </div>

      {error && <p className={styles.errorMsg} role="alert">{error}</p>}

      {canManage && (
        <div className={styles.actionRow}>
          <Button
            type="button"
            variant="primary"
            size="small"
            onClick={handleSave}
            disabled={mutation.isPending}
          >
            {mutation.isPending ? 'Saving…' : 'Save'}
          </Button>
          {saved && !mutation.isPending && (
            <span className={styles.savedConfirm}>Saved.</span>
          )}
        </div>
      )}
    </div>
  )
}

// ── HeaderSetForm ─────────────────────────────────────────────────────────────

interface HeaderSetFormProps {
  pluginId: string
  instanceId: string
  headerNames: string[]
  canManage: boolean
}

function HeaderSetForm({ pluginId, instanceId, headerNames, canManage }: HeaderSetFormProps) {
  const [newName, setNewName] = useState('')
  const [newValue, setNewValue] = useState('')
  const [addError, setAddError] = useState<string | null>(null)
  const [addSaved, setAddSaved] = useState(false)

  const setMutation = useSetPluginHeader()
  const deleteMutation = useDeletePluginHeader()

  function handleAdd() {
    setAddError(null)
    setAddSaved(false)
    const trimmedName = newName.trim()
    if (!trimmedName) {
      setAddError('Header name is required.')
      return
    }
    if (isReservedHeader(trimmedName)) {
      // Client-side guard matching internal/infra/headervalidate reserved-header blocklist.
      setAddError(`"${trimmedName}" is a reserved header name and cannot be used.`)
      return
    }
    if (!newValue.trim()) {
      setAddError('Header value is required.')
      return
    }
    setMutation.mutate(
      { pluginId, instanceId, name: trimmedName, value: newValue },
      {
        onSuccess: () => {
          setNewName('')
          setNewValue('')
          setAddSaved(true)
        },
        onError: (err) => {
          setAddError(errMessage(err, 'Save failed.'))
        },
      },
    )
  }

  function handleDelete(name: string) {
    deleteMutation.mutate(
      { pluginId, instanceId, name },
      {
        onError: (err) => {
          setAddError(errMessage(err, 'Delete failed.'))
        },
      },
    )
  }

  return (
    <div className={styles.form}>
      {headerNames.length === 0 ? (
        <p className={styles.emptyMsg}>No headers set.</p>
      ) : (
        <ul className={styles.headerList}>
          {headerNames.map((name) => (
            <li key={name} className={styles.headerItem}>
              <span className={styles.headerName}>{name}</span>
              {canManage && (
                <Button
                  type="button"
                  variant="ghost"
                  size="small"
                  onClick={() => handleDelete(name)}
                  disabled={deleteMutation.isPending}
                >
                  Delete
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}

      {canManage && (
        <>
          <p className={styles.sectionLabel}>Add header</p>
          <div className={styles.addHeaderRow}>
            <input
              type="text"
              className={styles.input}
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              placeholder="Header name"
              aria-label="New header name"
            />
            <input
              type="password"
              className={styles.input}
              value={newValue}
              onChange={(e) => setNewValue(e.target.value)}
              placeholder="Value"
              aria-label="New header value"
              autoComplete="new-password"
            />
            <Button
              type="button"
              variant="primary"
              size="small"
              onClick={handleAdd}
              disabled={setMutation.isPending}
            >
              {setMutation.isPending ? 'Adding…' : 'Add'}
            </Button>
          </div>

          {addError && <p className={styles.errorMsg} role="alert">{addError}</p>}
          {addSaved && !setMutation.isPending && (
            <p className={styles.savedConfirm}>Header added.</p>
          )}
        </>
      )}
    </div>
  )
}

// ── BasicAuthForm ─────────────────────────────────────────────────────────────

interface BasicAuthFormProps {
  pluginId: string
  instanceId: string
  initialUsername: string
  canManage: boolean
}

function BasicAuthForm({ pluginId, instanceId, initialUsername, canManage }: BasicAuthFormProps) {
  const [username, setUsername] = useState(initialUsername)
  const [password, setPassword] = useState('')
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const mutation = useSetPluginBasicAuth()

  function handleSave() {
    setSaved(false)
    setError(null)
    if (!username.trim()) {
      setError('Username is required.')
      return
    }
    if (!password.trim()) {
      setError('Password is required.')
      return
    }
    mutation.mutate(
      { pluginId, instanceId, username: username.trim(), password },
      {
        onSuccess: () => {
          setSaved(true)
          setPassword('')
        },
        onError: (err) => {
          setError(errMessage(err, 'Save failed.'))
        },
      },
    )
  }

  return (
    <div className={styles.form}>
      <div className={styles.fieldRow}>
        <label htmlFor="basic-username" className={styles.fieldLabel}>
          Username
        </label>
        <input
          id="basic-username"
          type="text"
          className={styles.input}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          disabled={!canManage}
        />
      </div>

      <div className={styles.fieldRow}>
        <label htmlFor="basic-password" className={styles.fieldLabel}>
          Password
        </label>
        <p className={styles.writeOnlyNote}>Write-only — enter a new value to replace.</p>
        <input
          id="basic-password"
          type="password"
          className={styles.input}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="new-password"
          disabled={!canManage}
        />
      </div>

      {error && <p className={styles.errorMsg} role="alert">{error}</p>}

      {canManage && (
        <div className={styles.actionRow}>
          <Button
            type="button"
            variant="primary"
            size="small"
            onClick={handleSave}
            disabled={mutation.isPending}
          >
            {mutation.isPending ? 'Saving…' : 'Save'}
          </Button>
          {saved && !mutation.isPending && (
            <span className={styles.savedConfirm}>Saved.</span>
          )}
        </div>
      )}
    </div>
  )
}

// ── OAuthSection ──────────────────────────────────────────────────────────────

interface OAuthSectionProps {
  pluginId: string
  instanceId: string
  strategy: string
  clientId?: string
  hasClientSecret: boolean
  authorizationUrl?: string
  tokenUrl?: string
  scopes?: string[]
  hasToken: boolean
  tokenExpiresAt?: string
  healthState?: string
  healthDetail?: string
  canManage: boolean
}

function OAuthClientForm({
  pluginId,
  instanceId,
  initialClientId,
  canManage,
}: {
  pluginId: string
  instanceId: string
  initialClientId: string
  canManage: boolean
}) {
  const [clientId, setClientId] = useState(initialClientId)
  const [clientSecret, setClientSecret] = useState('')
  const [saveError, setSaveError] = useState<string | null>(null)
  const mutation = useSetPluginOAuthClient()

  function handleSave(e: React.FormEvent) {
    e.preventDefault()
    setSaveError(null)
    mutation.mutate(
      { pluginId, instanceId, client_id: clientId.trim(), client_secret: clientSecret },
      {
        onSuccess: () => setClientSecret(''),
        onError: (err) => setSaveError(errMessage(err, 'Save failed.')),
      },
    )
  }

  const canSubmit =
    canManage && !mutation.isPending && clientId.trim() !== '' && clientSecret !== ''

  return (
    <form className={styles.form} onSubmit={handleSave}>
      <p className={styles.emptyMsg}>
        Paste the Client ID and Client Secret from the provider (e.g. Slack app
        Basic Information). Both values are required before you can authorize.
      </p>
      <label className={styles.fieldRow}>
        <span className={styles.fieldLabel}>Client ID</span>
        <input
          type="text"
          className={styles.input}
          value={clientId}
          onChange={(e) => setClientId(e.target.value)}
          disabled={!canManage}
          autoComplete="off"
        />
      </label>
      <label className={styles.fieldRow}>
        <span className={styles.fieldLabel}>Client Secret</span>
        <input
          type="password"
          className={styles.input}
          value={clientSecret}
          onChange={(e) => setClientSecret(e.target.value)}
          disabled={!canManage}
          autoComplete="off"
          placeholder={initialClientId ? 'enter to rotate' : ''}
        />
      </label>
      <div className={styles.actionRow}>
        <Button type="submit" disabled={!canSubmit}>
          {mutation.isPending ? 'Saving…' : 'Save client credentials'}
        </Button>
      </div>
      {saveError && (
        <p className={styles.errorMsg} role="alert">
          {saveError}
        </p>
      )}
    </form>
  )
}

function OAuthSection({
  pluginId,
  instanceId,
  strategy,
  clientId,
  hasClientSecret,
  authorizationUrl,
  tokenUrl,
  scopes,
  hasToken,
  tokenExpiresAt,
  healthState,
  healthDetail,
  canManage,
}: OAuthSectionProps) {
  const isRefreshFailed = isOAuthRefreshFailure(healthState ?? '', healthDetail)
  // BeginAuthcode requires both client_id and client_secret in StoredCredentials.
  // Until both are present the Authorize button would 4xx; surface a form first.
  const clientReady = !!clientId && hasClientSecret
  const [oauthError, setOauthError] = useState<string | null>(null)

  return (
    <div className={styles.form}>
      {!clientReady && (
        <OAuthClientForm
          pluginId={pluginId}
          instanceId={instanceId}
          initialClientId={clientId ?? ''}
          canManage={canManage}
        />
      )}
      <dl className={styles.metaGrid}>
        {clientId && (
          <>
            <dt>Client ID</dt>
            <dd>{clientId}</dd>
          </>
        )}
        {authorizationUrl && (
          <>
            <dt>Authorization URL</dt>
            <dd className={styles.metaUrl}>{authorizationUrl}</dd>
          </>
        )}
        {tokenUrl && (
          <>
            <dt>Token URL</dt>
            <dd className={styles.metaUrl}>{tokenUrl}</dd>
          </>
        )}
        {scopes && scopes.length > 0 && (
          <>
            <dt>Scopes</dt>
            <dd>{scopes.join(', ')}</dd>
          </>
        )}
        <dt>Token present</dt>
        <dd>{hasToken ? 'Yes' : 'No'}</dd>
        {hasToken && tokenExpiresAt && (
          <>
            <dt>Token expires</dt>
            <dd>{formatTimestamp(tokenExpiresAt)}</dd>
          </>
        )}
      </dl>

      {canManage && (
        <>
          <div className={styles.actionRow}>
            {!hasToken && clientReady && (
              // No token yet, but client credentials are stored — show the initial
              // "Authorize" CTA. We hide it before client_id/client_secret are
              // saved because BeginAuthcode would 4xx without them.
              <ReauthorizeButton
                pluginId={pluginId}
                instanceId={instanceId}
                strategy={strategy}
                label="Authorize"
                pendingLabel="Authorizing…"
                onError={setOauthError}
              />
            )}
            {hasToken && isRefreshFailed && (
              // Token present but refresh failed — Re-authorize is the primary CTA.
              // The page-level banner already shows this; we surface it here too so
              // the operator can act from the Credentials tab without scrolling up.
              <ReauthorizeButton
                pluginId={pluginId}
                instanceId={instanceId}
                strategy={strategy}
                label="Re-authorize"
                pendingLabel="Starting…"
                onError={setOauthError}
              />
            )}
            {/* When has_token && !isRefreshFailed: no button — avoid duplicating
                the page-level banner CTA. Operator can clear + re-authorize if needed. */}
          </div>
          {oauthError && (
            <p className={styles.errorMsg} role="alert">{oauthError}</p>
          )}
        </>
      )}
    </div>
  )
}

// ── CredentialsTab ────────────────────────────────────────────────────────────

export interface CredentialsTabProps {
  pluginId: string
  instanceId: string
  // strategy is derived from instance.auth_strategy (manifest snapshot). The
  // tab fetches the server-side redacted view and warns when they disagree.
  strategy: PluginAuthStrategy
  canManage: boolean
  healthState?: string
  healthDetail?: string
}

export function CredentialsTab({
  pluginId,
  instanceId,
  strategy,
  canManage,
  healthState,
  healthDetail,
}: CredentialsTabProps) {
  const { data: creds, status } = usePluginInstanceCredentials(pluginId, instanceId)
  const clearMutation = useClearPluginCredentials()
  const [clearError, setClearError] = useState<string | null>(null)
  const [showClearModal, setShowClearModal] = useState(false)

  function openClearModal() {
    setClearError(null)
    setShowClearModal(true)
  }

  function closeClearModal() {
    setShowClearModal(false)
  }

  // Clearing credentials is destructive with no undo (for OAuth it forces a full
  // re-auth), so it is gated behind a confirmation modal — mirroring the
  // already-confirmed "Delete instance" action (issue #659). The modal stays open
  // on error so the operator sees the failure in context; it closes on success.
  function handleConfirmClear() {
    setClearError(null)
    clearMutation.mutate(
      { pluginId, instanceId },
      {
        onSuccess: () => {
          setShowClearModal(false)
        },
        onError: (err) => {
          setClearError(errMessage(err, 'Clear failed.'))
        },
      },
    )
  }

  if (status === 'pending') {
    return <p className={styles.loading}>Loading credentials…</p>
  }
  if (status === 'error') {
    return <p className={styles.errorMsg}>Could not load credentials.</p>
  }

  // Warn when the manifest strategy (prop) disagrees with the server-side
  // strategy stored in the credential blob. This can happen briefly after a
  // manifest hot-reload if the backend has not yet migrated the credential row.
  const serverStrategy = creds?.strategy
  const strategyMismatch = serverStrategy && serverStrategy !== strategy

  const isSet = (
    creds?.has_api_key ||
    (creds?.header_names && creds.header_names.length > 0) ||
    creds?.has_password ||
    creds?.has_token
  )

  return (
    <div className={styles.tab}>
      {strategyMismatch && (
        <div className={styles.mismatchWarning} role="alert">
          Strategy mismatch: manifest declares <strong>{strategy}</strong>, but stored
          credentials use <strong>{serverStrategy}</strong>. Contact your plugin administrator.
        </div>
      )}

      {strategy === 'none' && (
        <p className={styles.emptyMsg}>
          This plugin instance uses no authentication credentials.
        </p>
      )}

      {strategy === 'static_api_key' && (
        <StaticAPIKeyForm
          pluginId={pluginId}
          instanceId={instanceId}
          initialHeaderName={creds?.header_name ?? ''}
          initialScheme={creds?.scheme ?? ''}
          canManage={canManage}
        />
      )}

      {strategy === 'header_set' && (
        <HeaderSetForm
          pluginId={pluginId}
          instanceId={instanceId}
          headerNames={creds?.header_names ?? []}
          canManage={canManage}
        />
      )}

      {strategy === 'basic_auth' && (
        <BasicAuthForm
          pluginId={pluginId}
          instanceId={instanceId}
          initialUsername={creds?.username ?? ''}
          canManage={canManage}
        />
      )}

      {(strategy === 'oauth2_authcode' || strategy === 'oauth2_clientcred') && (
        <OAuthSection
          pluginId={pluginId}
          instanceId={instanceId}
          strategy={strategy}
          clientId={creds?.client_id}
          hasClientSecret={creds?.has_client_secret ?? false}
          authorizationUrl={creds?.authorization_url}
          tokenUrl={creds?.token_url}
          scopes={creds?.scopes}
          hasToken={creds?.has_token ?? false}
          tokenExpiresAt={creds?.token_expires_at}
          healthState={healthState}
          healthDetail={healthDetail}
          canManage={canManage}
        />
      )}

      {canManage && strategy !== 'none' && isSet && (
        <div className={styles.clearSection}>
          <Button
            type="button"
            variant="ghost"
            size="small"
            onClick={openClearModal}
            disabled={clearMutation.isPending}
          >
            Clear credentials
          </Button>
          {/* Error from a failed clear is shown inside the modal while it is open;
              surface it here too once the modal closes so it is not lost. */}
          {clearError && !showClearModal && (
            <p className={styles.errorMsg} role="alert">{clearError}</p>
          )}
        </div>
      )}

      {showClearModal && (
        <ClearCredentialsModal
          onClose={closeClearModal}
          onConfirm={handleConfirmClear}
          isPending={clearMutation.isPending}
          error={clearError}
        />
      )}
    </div>
  )
}
