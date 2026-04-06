import { useState, useCallback } from 'react'
import { KeyRound, Bot, Layers } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { usePageTitle } from '@/hooks/usePageTitle'
import { useProviders } from '@/hooks/queries/admin'
import { useAdminModels, useAdminSettings } from '@/hooks/queries/admin'
import { useModels, type ProviderModels } from '@/hooks/queries/users'
import { useSetProviderKey } from '@/hooks/mutations/admin'
import { useUpdateAdminSettings, useSetModelEnabled } from '@/hooks/mutations/admin'
import type { ApiProviderStatus, ApiModelSetting } from '@/api/types'
import cardStyles from '@/components/Settings/Settings.module.css'
import styles from './AdminModelsPage.module.css'

/* ------------------------------------------------------------------ */
/*  ApiKeysSection                                                     */
/* ------------------------------------------------------------------ */

function ProviderKeyRow({ provider }: { provider: ApiProviderStatus }) {
  const [editing, setEditing] = useState(false)
  const [keyValue, setKeyValue] = useState('')
  const setKey = useSetProviderKey()

  const handleSave = useCallback(() => {
    if (!keyValue.trim()) return
    setKey.mutate(
      { provider: provider.name, key: keyValue.trim() },
      {
        onSuccess: () => {
          setEditing(false)
          setKeyValue('')
        },
      },
    )
  }, [keyValue, provider.name, setKey])

  const handleCancel = useCallback(() => {
    setEditing(false)
    setKeyValue('')
  }, [])

  const capitalizedName = provider.name.charAt(0).toUpperCase() + provider.name.slice(1)

  return (
    <div>
      <div
        className={`${styles.providerRow} ${editing ? styles.providerRowEditing : ''}`}
      >
        <div className={styles.providerInfo}>
          <span className={styles.providerName}>{capitalizedName}</span>
          <span className={styles.providerKeyHint}>
            {provider.has_key ? provider.masked_key : 'Not configured'}
          </span>
        </div>
        <div className={styles.providerActions}>
          {provider.has_key && <span className={styles.activeBadge}>Active</span>}
          {!editing && (
            <button
              className={provider.has_key ? styles.btnSecondary : styles.btnPrimary}
              onClick={() => setEditing(true)}
            >
              {provider.has_key ? 'Update' : 'Set Key'}
            </button>
          )}
        </div>
      </div>
      {editing && (
        <div className={styles.keyInputRow}>
          <input
            type="password"
            placeholder="Paste API key"
            value={keyValue}
            onChange={(e) => setKeyValue(e.target.value)}
            autoFocus
          />
          <div className={styles.keyInputActions}>
            <button
              className={styles.btnPrimary}
              onClick={handleSave}
              disabled={!keyValue.trim() || setKey.isPending}
            >
              Save
            </button>
            <button className={styles.btnSecondary} onClick={handleCancel}>
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

function ApiKeysSection() {
  const { data: providers } = useProviders()

  return (
    <section className={cardStyles.card}>
      <div className={cardStyles.cardHeader}>
        <h2 className={cardStyles.cardTitle}>
          <KeyRound size={16} strokeWidth={1.5} className={cardStyles.cardTitleIcon} />
          API Keys
        </h2>
        <p className={cardStyles.cardDesc}>
          Configure provider API keys to enable model access.
        </p>
      </div>
      <div className={cardStyles.cardBody}>
        {providers?.map((p) => <ProviderKeyRow key={p.name} provider={p} />)}
      </div>
    </section>
  )
}

/* ------------------------------------------------------------------ */
/*  SystemDefaultSection                                               */
/* ------------------------------------------------------------------ */

function SystemDefaultSection() {
  const { data: providerModels } = useModels()
  const { data: settings } = useAdminSettings()
  const updateSettings = useUpdateAdminSettings()

  const currentDefault = settings?.default_model ?? ''

  const handleChange = useCallback(
    (e: React.ChangeEvent<HTMLSelectElement>) => {
      const value = e.target.value
      if (value) {
        updateSettings.mutate({ default_model: value })
      }
    },
    [updateSettings],
  )

  return (
    <section className={cardStyles.card}>
      <div className={cardStyles.cardHeader}>
        <h2 className={cardStyles.cardTitle}>
          <Bot size={16} strokeWidth={1.5} className={cardStyles.cardTitleIcon} />
          System Default Model
        </h2>
        <p className={cardStyles.cardDesc}>
          Default model used for new agent runs when no model is specified.
        </p>
      </div>
      <div className={cardStyles.cardBody}>
        <div className={cardStyles.fieldGroup}>
          <label className={cardStyles.label} htmlFor="default-model">
            Default model
          </label>
          <select
            id="default-model"
            className={cardStyles.select}
            value={currentDefault}
            onChange={handleChange}
          >
            <option value="">Select a model</option>
            {providerModels?.map((pg) =>
              pg.models.map((m) => (
                <option key={`${pg.provider}:${m.name}`} value={`${pg.provider}:${m.name}`}>
                  {m.display_name}
                </option>
              )),
            )}
          </select>
          <span className={cardStyles.fieldHint}>
            Only enabled models appear in this list.
          </span>
        </div>
      </div>
    </section>
  )
}

/* ------------------------------------------------------------------ */
/*  AvailableModelsSection                                             */
/* ------------------------------------------------------------------ */

interface MergedModel {
  provider: string
  modelName: string
  displayName: string
  enabled: boolean
}

function mergeModels(
  providerModels: ProviderModels[] | undefined,
  adminModels: ApiModelSetting[] | undefined,
): Map<string, MergedModel[]> {
  const grouped = new Map<string, MergedModel[]>()

  // Enabled models from the regular endpoint
  if (providerModels) {
    for (const pg of providerModels) {
      const models: MergedModel[] = pg.models.map((m) => ({
        provider: pg.provider,
        modelName: m.name,
        displayName: m.display_name,
        enabled: true,
      }))
      grouped.set(pg.provider, models)
    }
  }

  // Disabled models from the admin endpoint
  if (adminModels) {
    for (const am of adminModels) {
      if (!am.enabled) {
        const existing = grouped.get(am.provider) ?? []
        const alreadyListed = existing.some((m) => m.modelName === am.model_name)
        if (!alreadyListed) {
          existing.push({
            provider: am.provider,
            modelName: am.model_name,
            displayName: am.model_name,
            enabled: false,
          })
          grouped.set(am.provider, existing)
        }
      }
    }
  }

  return grouped
}

function ToggleSwitch({
  on,
  onToggle,
  disabled,
}: {
  on: boolean
  onToggle: () => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      className={`${styles.toggle} ${on ? styles.toggleOn : ''}`}
      onClick={onToggle}
      disabled={disabled}
      aria-pressed={on}
    >
      <span className={styles.toggleThumb} />
    </button>
  )
}

function AvailableModelsSection() {
  const { data: providerModels } = useModels()
  const { data: adminModels } = useAdminModels()
  const { data: providers } = useProviders()
  const { data: settings } = useAdminSettings()
  const toggleModel = useSetModelEnabled()

  const currentDefault = settings?.default_model ?? ''
  const providerKeyMap = new Map(providers?.map((p) => [p.name, p.has_key]) ?? [])
  const grouped = mergeModels(providerModels, adminModels)

  // Get all provider names (from providers endpoint, which lists all even without keys)
  const allProviders = providers?.map((p) => p.name) ?? []

  return (
    <section className={cardStyles.card}>
      <div className={cardStyles.cardHeader}>
        <h2 className={cardStyles.cardTitle}>
          <Layers size={16} strokeWidth={1.5} className={cardStyles.cardTitleIcon} />
          Available Models
        </h2>
        <p className={cardStyles.cardDesc}>
          Enable or disable models. Disabled models cannot be selected for runs.
        </p>
      </div>
      <div className={cardStyles.cardBody}>
        <div className={styles.providerGroups}>
          {allProviders.map((providerName) => {
            const hasKey = providerKeyMap.get(providerName) ?? false
            const models = grouped.get(providerName) ?? []
            const capitalizedName =
              providerName.charAt(0).toUpperCase() + providerName.slice(1)

            return (
              <div key={providerName} className={styles.providerGroup}>
                <div className={styles.providerGroupLabel}>{capitalizedName}</div>
                {!hasKey ? (
                  <div className={styles.noKeyPlaceholder}>
                    No API key configured for {capitalizedName}
                  </div>
                ) : models.length === 0 ? (
                  <div className={styles.noKeyPlaceholder}>No models available</div>
                ) : (
                  <div className={styles.modelList}>
                    {models.map((m) => {
                      const modelKey = `${m.provider}:${m.modelName}`
                      const isDefault = currentDefault === modelKey

                      return (
                        <div
                          key={modelKey}
                          className={`${styles.modelRow} ${!m.enabled ? styles.modelRowDisabled : ''}`}
                        >
                          <div className={styles.modelInfo}>
                            <span className={styles.modelName}>{m.displayName}</span>
                            {m.displayName !== m.modelName && (
                              <span className={styles.modelId}>{m.modelName}</span>
                            )}
                          </div>
                          <div className={styles.modelActions}>
                            {isDefault && (
                              <span className={styles.defaultBadge}>Default</span>
                            )}
                            <ToggleSwitch
                              on={m.enabled}
                              onToggle={() => {
                                // Don't allow disabling the current default
                                if (m.enabled && isDefault) return
                                toggleModel.mutate({
                                  modelId: m.modelName,
                                  provider: m.provider,
                                  enabled: !m.enabled,
                                })
                              }}
                            />
                          </div>
                        </div>
                      )
                    })}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}

/* ------------------------------------------------------------------ */
/*  Page                                                               */
/* ------------------------------------------------------------------ */

export default function AdminModelsPage() {
  usePageTitle('Models')

  return (
    <div className={styles.page}>
      <PageHeader title="Models" />
      <ApiKeysSection />
      <SystemDefaultSection />
      <AvailableModelsSection />
    </div>
  )
}
