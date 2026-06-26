import { useState, useEffect, useMemo, useRef } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowLeft } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/Button'
import { SchemaForm, REDACTION_SENTINEL } from '@/components/form/SchemaForm'
import type { SchemaShape, SchemaProperty } from '@/components/form/SchemaForm'
import { useOptionsContext } from '@/hooks/useOptionsContext'
import { ReauthorizeButton } from '@/components/admin/ReauthorizeButton/ReauthorizeButton'
import { CredentialsTab } from '@/components/admin/CredentialsTab/CredentialsTab'
import { InstanceSetupSteps } from '@/components/admin/InstanceSetupSteps/InstanceSetupSteps'
import { DeletePluginInstanceModal } from '@/components/admin/DeletePluginInstanceModal'
import { AcceptManifestModal } from '@/components/admin/AcceptManifestModal'
import { usePluginInstancesForAudience, usePluginInstanceDetail } from '@/hooks/queries/admin'
import { usePluginInstanceCredentials } from '@/hooks/queries/plugins'
import { useCurrentUser } from '@/hooks/queries/users'
import { deriveSetupSteps, firstIncompleteBlockingStep, humanizeHealthDetail } from '@/utils/instanceSetup'
import {
  useSetInstanceSubscriptionScope,
  useDeletePluginInstance,
  useDeactivatePluginInstance,
  useActivatePluginInstance,
  useSetInstanceConfig,
  useAcceptPluginManifest,
} from '@/hooks/mutations/plugins'
import { isOAuthRefreshFailure } from '@/utils/pluginHealth'
import { queryKeys } from '@/hooks/queryKeys'
import { ApiError, extractErrorMessage } from '@/api/fetch'
import type { PluginAuthStrategy } from '@/api/types'
import alertStyles from '@/styles/alerts.module.css'
import styles from './AdminPluginInstancePage.module.css'

// ── tab type ─────────────────────────────────────────────────────────────────

type Tab = 'subscriptions' | 'config' | 'credentials'

// SETUP_RELEVANT_STATES are the health states where the "Steps to healthy"
// onboarding checklist is meaningful — i.e. the instance still needs operator
// setup. Healthy/inactive/terminal-error states get their own treatment and are
// not nagged with a setup checklist (#658). pending_reauthorize already has a
// dedicated Re-authorize banner.
const SETUP_RELEVANT_STATES = new Set(['unhealthy', 'pending_config_migration'])

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
  const optionsCtx = useOptionsContext(pluginId, instanceId)
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

      <SchemaForm schema={schema} value={scope} onChange={setScope} fieldErrors={fieldErrors} optionsContext={optionsCtx} />

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
}

function ConfigTab({ pluginId, instanceId }: ConfigTabProps) {
  const queryClient = useQueryClient()
  const optionsCtx = useOptionsContext(pluginId, instanceId)

  // Fetch the per-instance detail to get both the current (redacted) config values
  // AND the instance-level config_schema. Previously the schema came from the
  // listing endpoint (usePluginInstancesForAudience.config_schema), which only
  // carries the CHANNEL config schema — wrong for instance-level fields like
  // Slack's app_level_token. The detail endpoint now returns the correct
  // instance-level schema verbatim (ADR-049; schema is metadata, not a secret).
  const { data: detail, status: detailStatus } = usePluginInstanceDetail(pluginId, instanceId)

  // Derive the instance-level schema from the detail response rather than from
  // the listing's channel schema (which the audience editor depends on — leave
  // that unchanged at usePluginInstancesForAudience).
  const configSchema = (detail?.config_schema ?? null) as Record<string, unknown> | null

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
          optionsContext={optionsCtx}
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

  // Deactivate / Activate state
  const [deactivateError, setDeactivateError] = useState<string | null>(null)
  const [activateError, setActivateError] = useState<string | null>(null)
  const deactivateMutation = useDeactivatePluginInstance()
  const activateMutation = useActivatePluginInstance()

  // Accept-manifest state — committing the candidate manifest affects ALL
  // instances of this plugin, so the count is computed across the full list.
  const [showAcceptManifestModal, setShowAcceptManifestModal] = useState(false)
  const [acceptManifestError, setAcceptManifestError] = useState<string | null>(null)
  const acceptManifestMutation = useAcceptPluginManifest()
  const pendingManifestInstanceCount = (allInstances ?? []).filter(
    (i) => i.plugin_id === pluginId && i.state === 'pending_manifest_approval',
  ).length

  function handleAcceptManifestConfirm() {
    if (!pluginId) return
    setAcceptManifestError(null)
    acceptManifestMutation.mutate(
      { pluginId },
      {
        onSuccess: () => {
          setShowAcceptManifestModal(false)
        },
        onError: (err: unknown) => {
          setAcceptManifestError(extractErrorMessage(err))
        },
      },
    )
  }

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
          setDeleteError(extractErrorMessage(err))
        },
      },
    )
  }

  function handleDeactivate() {
    if (!pluginId || !instanceId) return
    setDeactivateError(null)
    deactivateMutation.mutate(
      { pluginId, instanceId },
      {
        onError: (err: unknown) => {
          setDeactivateError(extractErrorMessage(err))
        },
      },
    )
  }

  function handleActivate() {
    if (!pluginId || !instanceId) return
    setActivateError(null)
    activateMutation.mutate(
      { pluginId, instanceId },
      {
        onError: (err: unknown) => {
          setActivateError(extractErrorMessage(err))
        },
      },
    )
  }

  const hasSubscriptionSchema = !!instance?.subscription_schema

  // ── Onboarding-step derivation (#658) ────────────────────────────────────────
  //
  // The instance detail (config schema + redacted config values) and the
  // redacted credentials power the "Steps to healthy" checklist, the tab badges,
  // and the blocking-tab default. All three endpoints are admin-only, so the
  // onboarding surface is gated on canManage; the underlying queries share their
  // cache keys with the Config/Credentials tabs (no extra network round-trips).
  const { data: detail, status: detailStatus } = usePluginInstanceDetail(
    canManage ? pluginId : undefined,
    canManage ? instanceId : undefined,
  )
  const { data: creds, status: credsStatus } = usePluginInstanceCredentials(
    canManage ? pluginId : undefined,
    canManage ? instanceId : undefined,
  )

  const configSchema = (detail?.config_schema ?? null) as SchemaShape | null
  const instanceConfig = useMemo<Record<string, unknown>>(() => {
    if (!detail?.config_json) return {}
    try {
      return JSON.parse(detail.config_json) as Record<string, unknown>
    } catch {
      return {}
    }
  }, [detail?.config_json])

  const setupSteps = useMemo(() => {
    if (!instance) return []
    return deriveSetupSteps({
      authStrategy: instance.auth_strategy,
      credentials: creds,
      configSchema,
      config: instanceConfig,
      hasSubscriptionSchema,
      subscriptionScope: instance.subscription_scope,
    })
  }, [instance, creds, configSchema, instanceConfig, hasSubscriptionSchema])

  // Tabs that still have an incomplete BLOCKING step get an information-scent dot.
  const incompleteTabs = useMemo(
    () => new Set(setupSteps.filter((s) => s.blocking && !s.done).map((s) => s.tab)),
    [setupSteps],
  )

  const healthDetailCopy = humanizeHealthDetail(instance?.health_detail)

  const showSetupSteps =
    canManage &&
    !!instance &&
    SETUP_RELEVANT_STATES.has(instance.state) &&
    setupSteps.length > 0

  // One-time default tab. When the instance is mid-setup, open the first
  // incomplete blocking step's tab (so the operator lands where the work is)
  // instead of always defaulting to Subscriptions. Falls back to the old rule:
  // Config when there is no subscription schema, else Subscriptions. Runs once,
  // after the data needed to compute the blocker has settled, and never fights a
  // manual tab click thereafter.
  const didDefaultTab = useRef(false)
  useEffect(() => {
    if (didDefaultTab.current) return
    if (status !== 'success' || !instance) return
    // Wait for the admin-only detail/credentials queries to settle so the
    // derived blocker is accurate before we commit to a default tab.
    if (canManage && (detailStatus === 'pending' || credsStatus === 'pending')) return
    didDefaultTab.current = true

    const blocker = canManage ? firstIncompleteBlockingStep(setupSteps) : null
    if (blocker) {
      setActiveTab(blocker.tab)
    } else if (!hasSubscriptionSchema) {
      setActiveTab('config')
    }
  }, [status, instance, canManage, detailStatus, credsStatus, setupSteps, hasSubscriptionSchema])

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
        {canManage && instance?.state === 'inactive' && (
          <Button
            variant="secondary"
            size="small"
            disabled={activateMutation.isPending}
            onClick={handleActivate}
          >
            {activateMutation.isPending ? 'Activating…' : 'Activate'}
          </Button>
        )}
        {canManage &&
          instance?.state !== 'inactive' &&
          instance?.state !== 'signature_invalid' &&
          instance?.state !== 'verification_error' && (
            <Button
              variant="secondary"
              size="small"
              disabled={deactivateMutation.isPending}
              onClick={handleDeactivate}
            >
              {deactivateMutation.isPending ? 'Deactivating…' : 'Deactivate'}
            </Button>
          )}
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

      {instance?.state === 'pending_manifest_approval' && canManage && (
        <div className={styles.reauthBanner}>
          <span>
            A new manifest version is waiting for approval. This instance is
            blocked until you accept it.
          </span>
          <Button
            type="button"
            variant="primary"
            size="small"
            onClick={() => {
              setAcceptManifestError(null)
              setShowAcceptManifestModal(true)
            }}
          >
            Review change
          </Button>
        </div>
      )}

      {instance?.state === 'inactive' && (
        <div className={styles.inactiveBanner}>
          This instance has been deactivated by an admin. The subprocess is stopped and tool
          calls are refused. Click <strong>Activate</strong> to re-enable it.
        </div>
      )}

      {deactivateError && (
        <div className={alertStyles.alertError} role="alert">{deactivateError}</div>
      )}

      {activateError && (
        <div className={alertStyles.alertError} role="alert">{activateError}</div>
      )}

      {showSetupSteps && (
        <InstanceSetupSteps
          steps={setupSteps}
          healthDetail={healthDetailCopy}
          onNavigate={setActiveTab}
        />
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
          {incompleteTabs.has('config') && (
            <span
              className={styles.tabBadge}
              aria-label="required setup incomplete"
              title="Required configuration is missing"
            />
          )}
        </button>
        <button
          type="button"
          className={[styles.tab, activeTab === 'credentials' ? styles.tabActive : ''].join(' ')}
          onClick={() => setActiveTab('credentials')}
        >
          Credentials
          {incompleteTabs.has('credentials') && (
            <span
              className={styles.tabBadge}
              aria-label="required setup incomplete"
              title="Credentials are missing"
            />
          )}
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

      {showAcceptManifestModal && (
        <AcceptManifestModal
          pluginName={pluginName ?? ''}
          pendingInstanceCount={pendingManifestInstanceCount}
          onClose={() => {
            setShowAcceptManifestModal(false)
            setAcceptManifestError(null)
          }}
          onConfirm={handleAcceptManifestConfirm}
          isPending={acceptManifestMutation.isPending}
          error={acceptManifestError}
        />
      )}
    </div>
  )
}
