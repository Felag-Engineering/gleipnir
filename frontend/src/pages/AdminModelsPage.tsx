import { useState, useCallback } from 'react'
import { KeyRound, Bot, Layers } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { usePageTitle } from '@/hooks/usePageTitle'
import { useProviders, useAllAdminModels, useAdminSettings } from '@/hooks/queries/admin'
import { useModels } from '@/hooks/queries/users'
import type { ApiProviderStatus, ApiAllModelEntry } from '@/api/types'
import { useSetProviderKey } from '@/hooks/mutations/admin'
import { useUpdateAdminSettings, useSetModelEnabled } from '@/hooks/mutations/admin'
import { OpenAICompatProvidersSection } from '@/components/admin/OpenAICompatProvidersSection'
import { EncryptionKeyNotice } from '@/components/admin/EncryptionKeyNotice'
import { useOpenAICompatProviders } from '@/hooks/queries/openaiCompatProviders'
import { formatProviderName } from '@/utils/format'
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

  const capitalizedName = formatProviderName(provider.name)

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

function groupAllModels(allModels: ApiAllModelEntry[] | undefined): Map<string, MergedModel[]> {
  const grouped = new Map<string, MergedModel[]>()
  if (!allModels) return grouped

  for (const m of allModels) {
    const list = grouped.get(m.provider) ?? []
    list.push({
      provider: m.provider,
      modelName: m.model_name,
      displayName: m.display_name,
      enabled: m.enabled,
    })
    grouped.set(m.provider, list)
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
  const { data: allModels } = useAllAdminModels()
  const { data: providers } = useProviders()
  const { data: compatProviders } = useOpenAICompatProviders()
  const { data: settings } = useAdminSettings()
  const toggleModel = useSetModelEnabled()

  const currentDefault = settings?.default_model ?? ''

  // Hardcoded providers (Anthropic, Google) carry a has_key flag from
  // /admin/providers. Admin-managed OpenAI-compat providers always have a key
  // by construction — the row cannot exist in the DB without one — so they
  // are treated as has_key=true here.
  const providerKeyMap = new Map<string, boolean>(
    providers?.map((p) => [p.name, p.has_key]) ?? [],
  )
  for (const p of compatProviders ?? []) {
    providerKeyMap.set(p.name, true)
  }

  const grouped = groupAllModels(allModels)

  // Render the three primary providers (Anthropic, Google, OpenAI) first in a
  // fixed order so the UI doesn't depend on API response ordering, then append
  // any admin-managed openai-compat providers. The Set guards against the
  // (unlikely) case where a compat provider was named identically to a primary.
  const PRIMARY_PROVIDERS = ["anthropic", "google", "openai"] as const
  const primary = PRIMARY_PROVIDERS.filter((name) =>
    providers?.some((p) => p.name === name),
  )
  const allProviders = Array.from(
    new Set([...primary, ...(compatProviders?.map((p) => p.name) ?? [])]),
  )

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
            const capitalizedName = formatProviderName(providerName)

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
      <EncryptionKeyNotice />
      <ApiKeysSection />
      <OpenAICompatProvidersSection />
      <SystemDefaultSection />
      <AvailableModelsSection />
    </div>
  )
}
