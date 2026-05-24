import { useState, useEffect } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowLeft } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/Button'
import { FieldError } from '@/components/form/FieldError/FieldError'
import { ReauthorizeButton } from '@/components/admin/ReauthorizeButton/ReauthorizeButton'
import { CredentialsTab } from '@/components/admin/CredentialsTab/CredentialsTab'
import { DeletePluginInstanceModal } from '@/components/admin/DeletePluginInstanceModal'
import { usePluginInstancesForAudience, usePluginInstanceDetail } from '@/hooks/queries/admin'
import { useCurrentUser } from '@/hooks/queries/users'
import { useSetInstanceSubscriptionScope, useDeletePluginInstance, useSetInstanceConfig } from '@/hooks/mutations/plugins'
import { isOAuthRefreshFailure } from '@/utils/pluginHealth'
import { queryKeys } from '@/hooks/queryKeys'
import { ApiError } from '@/api/fetch'
import type { PluginAuthStrategy } from '@/api/types'
import styles from './AdminPluginInstancePage.module.css'

// ── tab type ─────────────────────────────────────────────────────────────────

type Tab = 'subscriptions' | 'config' | 'credentials'

// ── SchemaForm ────────────────────────────────────────────────────────────────

// SchemaForm renders a dynamic form driven by a JSON Schema's `properties` map.
// Only string, string[] (array of strings), boolean, and number leaf types are
// supported — the common shape for Slack-style subscription scopes.

interface SchemaProperty {
  type?: string
  items?: { type?: string }
  description?: string
  // ADR-049: fields annotated with x-gleipnir-secret: true are redacted on read
  // and must be submitted via a separate write (never round-tripped as "***").
  'x-gleipnir-secret'?: boolean
}

interface SchemaShape {
  properties?: Record<string, SchemaProperty>
}

function isStringArrayProp(prop: SchemaProperty): boolean {
  return prop.type === 'array' && prop.items?.type === 'string'
}

// REDACTION_SENTINEL is the value the backend substitutes for secret fields on
// GET. The bulk PUT rejects it — we strip such fields before sending (ADR-049).
const REDACTION_SENTINEL = '***'

interface SchemaFormProps {
  schema: SchemaShape
  value: Record<string, unknown>
  onChange: (next: Record<string, unknown>) => void
  fieldErrors: Record<string, string>
  // idPrefix scopes generated DOM ids so subscription and config forms don't collide.
  idPrefix?: string
}

function SchemaForm({ schema, value, onChange, fieldErrors, idPrefix = 'field' }: SchemaFormProps) {
  const properties = schema.properties ?? {}
  const fields = Object.keys(properties)

  if (fields.length === 0) {
    return <p className={styles.noFields}>No fields declared in subscription_schema.</p>
  }

  function handleChange(name: string, newVal: unknown) {
    onChange({ ...value, [name]: newVal })
  }

  return (
    <div className={styles.schemaFields}>
      {fields.map((name) => {
        const prop = properties[name]
        const fieldId = `${idPrefix}-${name}`
        const fieldErrId = `${idPrefix}-err-${name}`
        const err = fieldErrors[name]
        const isSecret = !!prop['x-gleipnir-secret']

        if (prop.type === 'boolean') {
          return (
            <div key={name} className={styles.fieldRow}>
              <label className={styles.checkboxLabel}>
                <input
                  type="checkbox"
                  checked={!!value[name]}
                  onChange={(e) => handleChange(name, e.target.checked)}
                />
                <span className={styles.fieldName}>{name}</span>
              </label>
              {prop.description && <p className={styles.fieldDesc}>{prop.description}</p>}
              <FieldError id={fieldErrId} messages={err} />
            </div>
          )
        }

        if (prop.type === 'number' || prop.type === 'integer') {
          return (
            <div key={name} className={styles.fieldRow}>
              <label htmlFor={fieldId} className={styles.fieldLabel}>
                {name}
              </label>
              {prop.description && <p className={styles.fieldDesc}>{prop.description}</p>}
              <input
                id={fieldId}
                type="number"
                className={styles.input}
                value={typeof value[name] === 'number' ? (value[name] as number) : ''}
                onChange={(e) => handleChange(name, e.target.valueAsNumber)}
                aria-describedby={err ? fieldErrId : undefined}
              />
              <FieldError id={fieldErrId} messages={err} />
            </div>
          )
        }

        if (isStringArrayProp(prop)) {
          // Render string[] as a textarea with one entry per line.
          const rawArr = value[name]
          const displayVal = Array.isArray(rawArr) ? (rawArr as string[]).join('\n') : ''
          return (
            <div key={name} className={styles.fieldRow}>
              <label htmlFor={fieldId} className={styles.fieldLabel}>
                {name}
                <span className={styles.fieldHint}> (one per line)</span>
              </label>
              {prop.description && <p className={styles.fieldDesc}>{prop.description}</p>}
              <textarea
                id={fieldId}
                className={styles.textarea}
                rows={4}
                value={displayVal}
                onChange={(e) =>
                  handleChange(
                    name,
                    e.target.value
                      .split('\n')
                      .map((s) => s.trim())
                      .filter((s) => s.length > 0),
                  )
                }
                aria-describedby={err ? fieldErrId : undefined}
              />
              <FieldError id={fieldErrId} messages={err} />
            </div>
          )
        }

        // Secret string: password input. The server returns "***" for existing
        // values — show as placeholder rather than pre-filling so the operator
        // must explicitly type to change it (prevents sentinel round-trip).
        if (isSecret) {
          const currentVal = typeof value[name] === 'string' ? (value[name] as string) : ''
          const isSentinel = currentVal === REDACTION_SENTINEL
          return (
            <div key={name} className={styles.fieldRow}>
              <label htmlFor={fieldId} className={styles.fieldLabel}>
                {name}
                <span className={styles.fieldHint}> (secret)</span>
              </label>
              {prop.description && <p className={styles.fieldDesc}>{prop.description}</p>}
              <input
                id={fieldId}
                type="password"
                className={styles.input}
                // Show empty when the sentinel arrives — the placeholder communicates
                // that a value is already set without echoing "***" into the field.
                value={isSentinel ? '' : currentVal}
                placeholder={isSentinel ? '(already set — leave blank to keep)' : ''}
                autoComplete="new-password"
                onChange={(e) => handleChange(name, e.target.value)}
                aria-describedby={err ? fieldErrId : undefined}
              />
              <FieldError id={fieldErrId} messages={err} />
            </div>
          )
        }

        // Default: plain string input.
        return (
          <div key={name} className={styles.fieldRow}>
            <label htmlFor={fieldId} className={styles.fieldLabel}>
              {name}
            </label>
            {prop.description && <p className={styles.fieldDesc}>{prop.description}</p>}
            <input
              id={fieldId}
              type="text"
              className={styles.input}
              value={typeof value[name] === 'string' ? (value[name] as string) : ''}
              onChange={(e) => handleChange(name, e.target.value)}
              aria-describedby={err ? fieldErrId : undefined}
            />
            <FieldError id={fieldErrId} messages={err} />
          </div>
        )
      })}
    </div>
  )
}

// ── SubscriptionsTab ──────────────────────────────────────────────────────────

interface SubscriptionsTabProps {
  pluginId: string
  instanceId: string
  subscriptionSchema: unknown
  initialScope: Record<string, unknown>
  currentVersion: number
}

function SubscriptionsTab({
  pluginId,
  instanceId,
  subscriptionSchema,
  initialScope,
  currentVersion,
}: SubscriptionsTabProps) {
  const [scope, setScope] = useState<Record<string, unknown>>(initialScope)
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [globalError, setGlobalError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const mutation = useSetInstanceSubscriptionScope()

  // Reset local state when the instance refreshes from the server.
  useEffect(() => {
    setScope(initialScope)
  }, [instanceId])

  const schema = subscriptionSchema as SchemaShape

  function handleSave() {
    setSaved(false)
    setFieldErrors({})
    setGlobalError(null)

    mutation.mutate(
      { pluginId, instanceId, scope, expectedVersion: currentVersion },
      {
        onSuccess: () => {
          setSaved(true)
        },
        onError: (err) => {
          const apiErr = err as ApiError
          if (apiErr.status === 422 && apiErr.issues) {
            const errs: Record<string, string> = {}
            for (const issue of apiErr.issues) {
              const key = issue.field ?? ''
              errs[key] = issue.message
            }
            setFieldErrors(errs)
          } else {
            setGlobalError(apiErr.message ?? 'Save failed')
          }
        },
      },
    )
  }

  return (
    <div className={styles.tabContent}>
      <p className={styles.tabIntro}>
        Configure the coarse subscription scope for this instance. The scope is sent to the
        plugin when the event stream opens so it can limit which substrate connections it
        establishes (e.g. which Slack channels to watch).
      </p>

      <SchemaForm schema={schema} value={scope} onChange={setScope} fieldErrors={fieldErrors} />

      {globalError && <p className={styles.globalError}>{globalError}</p>}

      <div className={styles.saveRow}>
        <Button
          type="button"
          variant="primary"
          size="small"
          onClick={handleSave}
          disabled={mutation.isPending}
        >
          {mutation.isPending ? 'Saving…' : 'Save scope'}
        </Button>
        {saved && !mutation.isPending && (
          <span className={styles.savedConfirm}>Saved — stream will restart.</span>
        )}
      </div>
    </div>
  )
}

// ── ConfigTab ─────────────────────────────────────────────────────────────────

interface ConfigTabProps {
  pluginId: string
  instanceId: string
  configSchema: Record<string, unknown> | null
}

function ConfigTab({ pluginId, instanceId, configSchema }: ConfigTabProps) {
  const queryClient = useQueryClient()

  // Fetch the per-instance detail to get the current (redacted) config values.
  // The listing endpoint (usePluginInstancesForAudience) includes config_schema
  // but omits config_json — we need GetInstance for actual values.
  const { data: detail, status: detailStatus } = usePluginInstanceDetail(pluginId, instanceId)

  // Parse the config_json string from the backend into a plain object.
  const serverConfig: Record<string, unknown> = (() => {
    if (!detail?.config_json) return {}
    try {
      return JSON.parse(detail.config_json) as Record<string, unknown>
    } catch {
      return {}
    }
  })()

  const [config, setConfig] = useState<Record<string, unknown>>(serverConfig)
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [globalError, setGlobalError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const [casConflict, setCasConflict] = useState(false)

  const mutation = useSetInstanceConfig()

  // Sync local config whenever the server data refreshes (e.g. after a save
  // that invalidates the query, or after navigating between instances).
  useEffect(() => {
    if (detail?.config_json) {
      try {
        setConfig(JSON.parse(detail.config_json) as Record<string, unknown>)
      } catch {
        setConfig({})
      }
    } else {
      setConfig({})
    }
    setCasConflict(false)
    setSaved(false)
    setFieldErrors({})
    setGlobalError(null)
  }, [detail?.config_json, instanceId])

  const schema = (configSchema ?? {}) as SchemaShape

  function buildPayloadConfig(): Record<string, unknown> {
    // Strip fields that still hold the redaction sentinel — the bulk PUT rejects
    // them (ADR-049 §5). An empty value on a secret field where the server
    // returned "***" means the operator left it blank (keep existing), so we
    // also omit those to avoid sending an empty string as the new secret value.
    const properties = (schema.properties ?? {}) as Record<string, SchemaProperty>
    const out: Record<string, unknown> = {}
    for (const [key, val] of Object.entries(config)) {
      const prop = properties[key]
      if (prop?.['x-gleipnir-secret']) {
        // Skip sentinel (backend would reject it anyway).
        if (val === REDACTION_SENTINEL) continue
        // Skip empty value when the original was a sentinel (operator left blank
        // = keep current secret).
        if (val === '' && serverConfig[key] === REDACTION_SENTINEL) continue
      }
      out[key] = val
    }
    return out
  }

  function handleSave() {
    setSaved(false)
    setFieldErrors({})
    setGlobalError(null)
    setCasConflict(false)

    if (!detail) return

    mutation.mutate(
      {
        pluginId,
        instanceId,
        config: buildPayloadConfig(),
        expectedVersion: detail.version,
      },
      {
        onSuccess: () => {
          setSaved(true)
          // Refresh to pick up the updated version number and any redacted values.
          void queryClient.invalidateQueries({
            queryKey: queryKeys.plugins.instance(pluginId, instanceId),
          })
        },
        onError: (err) => {
          const apiErr = err as ApiError
          if (apiErr.status === 409) {
            setCasConflict(true)
          } else if ((apiErr.status === 422 || apiErr.status === 400) && apiErr.issues) {
            const errs: Record<string, string> = {}
            for (const issue of apiErr.issues) {
              const key = issue.field ?? ''
              errs[key] = issue.message
            }
            setFieldErrors(errs)
          } else {
            setGlobalError(apiErr.message ?? 'Save failed')
          }
        },
      },
    )
  }

  function handleRefresh() {
    void queryClient.invalidateQueries({
      queryKey: queryKeys.plugins.instance(pluginId, instanceId),
    })
    setCasConflict(false)
  }

  if (detailStatus === 'pending') {
    return (
      <div className={styles.tabContent}>
        <p className={styles.loading}>Loading…</p>
      </div>
    )
  }

  if (detailStatus === 'error') {
    return (
      <div className={styles.tabContent}>
        <p className={styles.errorMsg}>Could not load instance config.</p>
      </div>
    )
  }

  const hasSchema =
    configSchema !== null &&
    typeof configSchema === 'object' &&
    Object.keys(configSchema).length > 0

  return (
    <div className={styles.tabContent}>
      <p className={styles.tabIntro}>
        Configure this plugin instance. Fields marked as secrets are write-only — a
        placeholder is shown when a value is already set. Leave a secret field blank to
        keep the current value.
      </p>

      {hasSchema ? (
        <SchemaForm
          schema={schema}
          value={config}
          onChange={setConfig}
          fieldErrors={fieldErrors}
          idPrefix="config-field"
        />
      ) : (
        <p className={styles.noFields}>No config fields declared in manifest.</p>
      )}

      {casConflict && (
        <div className={styles.casConflict}>
          <span>Configuration was modified elsewhere — refresh to see latest.</span>
          <Button type="button" variant="secondary" size="small" onClick={handleRefresh}>
            Refresh
          </Button>
        </div>
      )}

      {globalError && <p className={styles.globalError}>{globalError}</p>}

      <div className={styles.saveRow}>
        <Button
          type="button"
          variant="primary"
          size="small"
          onClick={handleSave}
          disabled={mutation.isPending || !detail}
        >
          {mutation.isPending ? 'Saving…' : 'Save config'}
        </Button>
        {saved && !mutation.isPending && (
          <span className={styles.savedConfirm}>Saved.</span>
        )}
      </div>
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

export default function AdminPluginInstancePage() {
  const { id: pluginId, iid: instanceId } = useParams<{ id: string; iid: string }>()
  const navigate = useNavigate()
  const [activeTab, setActiveTab] = useState<Tab>('subscriptions')
  const queryClient = useQueryClient()

  const { data: allInstances, status } = usePluginInstancesForAudience()
  const { data: currentUser } = useCurrentUser()

  // The backend gates all credentials endpoints with RequireRole(admin).
  // canManage matches that exactly so write controls are only shown to admins.
  const canManage = currentUser?.roles.includes('admin') ?? false

  const instance = allInstances?.find((inst) => inst.id === instanceId && inst.plugin_id === pluginId)

  // Delete instance state
  const [showDeleteModal, setShowDeleteModal] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const deleteMutation = useDeletePluginInstance()

  function handleDeleteConfirm() {
    if (!pluginId || !instanceId) return
    setDeleteError(null)
    deleteMutation.mutate(
      { pluginId, instanceId },
      {
        onSuccess: () => {
          navigate('/admin/plugins')
        },
        onError: (err: unknown) => {
          if (err instanceof ApiError) {
            setDeleteError(err.detail ?? err.message)
          } else if (err instanceof Error) {
            setDeleteError(err.message)
          } else {
            setDeleteError('Unexpected error — please try again.')
          }
        },
      },
    )
  }

  const hasSubscriptionSchema = !!instance?.subscription_schema

  // If the plugin has no subscription_schema, default to the config tab.
  // The Subscriptions tab is only rendered when subscription_schema is present.
  useEffect(() => {
    if (status === 'success' && !hasSubscriptionSchema && activeTab === 'subscriptions') {
      setActiveTab('config')
    }
  }, [status, hasSubscriptionSchema, activeTab])

  // After a successful OAuth authcode round-trip the callback handler redirects
  // the browser back here with ?oauth_ok=1. Invalidate the instance list and
  // credentials cache so the Re-authorize banner clears and has_token flips
  // without requiring a manual refresh.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    if (params.get('oauth_ok') === '1') {
      void queryClient.invalidateQueries({ queryKey: queryKeys.admin.pluginInstances })
      if (pluginId && instanceId) {
        void queryClient.invalidateQueries({
          queryKey: queryKeys.plugins.credentials(pluginId, instanceId),
        })
      }
    }
  // Run once on mount; queryClient reference is stable.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function renderTabContent() {
    if (status === 'pending') {
      return <p className={styles.loading}>Loading…</p>
    }
    if (status === 'error' || !instance) {
      return <p className={styles.errorMsg}>Instance not found or could not be loaded.</p>
    }

    if (activeTab === 'subscriptions' && hasSubscriptionSchema) {
      return (
        <SubscriptionsTab
          pluginId={pluginId!}
          instanceId={instanceId!}
          subscriptionSchema={instance.subscription_schema}
          initialScope={instance.subscription_scope ?? {}}
          currentVersion={instance.version}
        />
      )
    }

    if (activeTab === 'config') {
      return (
        <ConfigTab
          pluginId={pluginId!}
          instanceId={instanceId!}
          configSchema={instance.config_schema}
        />
      )
    }

    if (activeTab === 'credentials') {
      return (
        <div className={styles.tabContent}>
          <CredentialsTab
            pluginId={pluginId!}
            instanceId={instanceId!}
            strategy={(instance.auth_strategy ?? 'none') as PluginAuthStrategy}
            canManage={canManage}
            healthState={instance.state}
            healthDetail={instance.health_detail}
          />
        </div>
      )
    }

    return null
  }

  const instanceName = instance?.instance_name ?? instanceId
  const pluginName = instance?.plugin_name ?? pluginId

  return (
    <div className={styles.page}>
      <Link to="/admin/plugins" className={styles.backLink}>
        <ArrowLeft size={14} strokeWidth={1.5} aria-hidden />
        Plugins
      </Link>

      <PageHeader title={`${pluginName} / ${instanceName}`}>
        {canManage && (
          <Button
            variant="danger"
            size="small"
            onClick={() => {
              setDeleteError(null)
              setShowDeleteModal(true)
            }}
          >
            Delete instance
          </Button>
        )}
      </PageHeader>

      {instance && isOAuthRefreshFailure(instance.state, instance.health_detail) && (
        <div className={styles.reauthBanner}>
          <span>OAuth credentials need re-authorization.</span>
          <ReauthorizeButton
            pluginId={pluginId!}
            instanceId={instanceId!}
            strategy={instance.auth_strategy ?? ''}
          />
        </div>
      )}

      <nav className={styles.tabs} aria-label="Instance settings">
        {hasSubscriptionSchema && (
          <button
            type="button"
            className={[styles.tab, activeTab === 'subscriptions' ? styles.tabActive : ''].join(' ')}
            onClick={() => setActiveTab('subscriptions')}
          >
            Subscriptions
          </button>
        )}
        <button
          type="button"
          className={[styles.tab, activeTab === 'config' ? styles.tabActive : ''].join(' ')}
          onClick={() => setActiveTab('config')}
        >
          Config
        </button>
        <button
          type="button"
          className={[styles.tab, activeTab === 'credentials' ? styles.tabActive : ''].join(' ')}
          onClick={() => setActiveTab('credentials')}
        >
          Credentials
        </button>
      </nav>

      {renderTabContent()}

      {showDeleteModal && (
        <DeletePluginInstanceModal
          pluginName={pluginName ?? ''}
          instanceName={instanceName ?? ''}
          onClose={() => {
            setShowDeleteModal(false)
            setDeleteError(null)
          }}
          onConfirm={handleDeleteConfirm}
          isPending={deleteMutation.isPending}
          error={deleteError}
        />
      )}
    </div>
  )
}
