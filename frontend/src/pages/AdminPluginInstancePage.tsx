import { useState, useEffect } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowLeft } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/Button'
import { FieldError } from '@/components/form/FieldError/FieldError'
import { ReauthorizeButton } from '@/components/admin/ReauthorizeButton/ReauthorizeButton'
import { CredentialsTab } from '@/components/admin/CredentialsTab/CredentialsTab'
import { usePluginInstancesForAudience } from '@/hooks/queries/admin'
import { useCurrentUser } from '@/hooks/queries/users'
import { useSetInstanceSubscriptionScope } from '@/hooks/mutations/plugins'
import { isOAuthRefreshFailure } from '@/utils/pluginHealth'
import { queryKeys } from '@/hooks/queryKeys'
import type { PluginAuthStrategy } from '@/api/types'
import type { ApiError } from '@/api/fetch'
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
}

interface SchemaShape {
  properties?: Record<string, SchemaProperty>
}

function isStringArrayProp(prop: SchemaProperty): boolean {
  return prop.type === 'array' && prop.items?.type === 'string'
}

interface SchemaFormProps {
  schema: SchemaShape
  value: Record<string, unknown>
  onChange: (next: Record<string, unknown>) => void
  fieldErrors: Record<string, string>
}

function SchemaForm({ schema, value, onChange, fieldErrors }: SchemaFormProps) {
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
        const fieldId = `scope-field-${name}`
        const fieldErrId = `scope-err-${name}`
        const err = fieldErrors[name]

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

// ── Page ──────────────────────────────────────────────────────────────────────

export default function AdminPluginInstancePage() {
  const { id: pluginId, iid: instanceId } = useParams<{ id: string; iid: string }>()
  const [activeTab, setActiveTab] = useState<Tab>('subscriptions')
  const queryClient = useQueryClient()

  const { data: allInstances, status } = usePluginInstancesForAudience()
  const { data: currentUser } = useCurrentUser()

  // The backend gates all credentials endpoints with RequireRole(admin).
  // canManage matches that exactly so write controls are only shown to admins.
  const canManage = currentUser?.roles.includes('admin') ?? false

  const instance = allInstances?.find((inst) => inst.id === instanceId && inst.plugin_id === pluginId)

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
        <div className={styles.tabContent}>
          {/* TODO #241: render instance config form */}
          <p className={styles.placeholder}>Instance configuration — coming in #241.</p>
        </div>
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
      <Link to="/admin/audiences" className={styles.backLink}>
        <ArrowLeft size={14} strokeWidth={1.5} aria-hidden />
        Audiences
      </Link>

      <PageHeader title={`${pluginName} / ${instanceName}`} />

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
    </div>
  )
}
