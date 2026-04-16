import { useState, useEffect, useCallback } from 'react'
import { Gauge, Globe, Info } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { usePageTitle } from '@/hooks/usePageTitle'
import { useAdminSettings, useSystemInfo } from '@/hooks/queries/admin'
import { useUpdateAdminSettings } from '@/hooks/mutations/admin'
import cardStyles from '@/components/Settings/Settings.module.css'
import styles from './AdminSystemPage.module.css'

/* ------------------------------------------------------------------ */
/*  PublicURLSection                                                   */
/* ------------------------------------------------------------------ */

function PublicURLSection() {
  const { data: settings } = useAdminSettings()
  const updateSettings = useUpdateAdminSettings()

  const [publicUrl, setPublicUrl] = useState('')
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    if (settings) {
      setPublicUrl(settings.public_url ?? '')
    }
  }, [settings])

  const handleSave = useCallback(() => {
    updateSettings.mutate(
      { public_url: publicUrl },
      {
        onSuccess: () => {
          setSaved(true)
          setTimeout(() => setSaved(false), 3000)
        },
      },
    )
  }, [publicUrl, updateSettings])

  return (
    <section className={cardStyles.card}>
      <div className={cardStyles.cardHeader}>
        <h2 className={cardStyles.cardTitle}>
          <Globe size={16} strokeWidth={1.5} className={cardStyles.cardTitleIcon} />
          Public URL
        </h2>
        <p className={cardStyles.cardDesc}>
          The external URL where Gleipnir is accessible. Used to display full webhook URLs.
        </p>
      </div>
      <div className={cardStyles.cardBody}>
        <div className={cardStyles.fieldGroup}>
          <label className={cardStyles.label} htmlFor="public-url">
            Public URL
          </label>
          <input
            id="public-url"
            type="url"
            className={cardStyles.input}
            placeholder="https://gleipnir.example.com"
            value={publicUrl}
            onChange={(e) => setPublicUrl(e.target.value)}
          />
          <span className={cardStyles.fieldHint}>
            Must be an absolute URL with scheme and host. Leave empty to show path-only webhook URLs.
          </span>
        </div>
        <div className={cardStyles.formActions}>
          <button
            className={styles.saveButton}
            onClick={handleSave}
            disabled={updateSettings.isPending}
          >
            Save
          </button>
          {saved && <span className={cardStyles.successMsg}>Saved</span>}
          {updateSettings.isError && (
            <span className={cardStyles.errorMsg}>Failed to save settings</span>
          )}
        </div>
      </div>
    </section>
  )
}

/* ------------------------------------------------------------------ */
/*  RunLimitsSection                                                   */
/* ------------------------------------------------------------------ */

function RunLimitsSection() {
  const { data: settings } = useAdminSettings()
  const updateSettings = useUpdateAdminSettings()

  const [maxTokens, setMaxTokens] = useState('')
  const [maxToolCalls, setMaxToolCalls] = useState('')
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    if (settings) {
      setMaxTokens(settings.max_tokens_per_run ?? '')
      setMaxToolCalls(settings.max_tool_calls_per_run ?? '')
    }
  }, [settings])

  const handleSave = useCallback(() => {
    updateSettings.mutate(
      {
        max_tokens_per_run: maxTokens,
        max_tool_calls_per_run: maxToolCalls,
      },
      {
        onSuccess: () => {
          setSaved(true)
          setTimeout(() => setSaved(false), 3000)
        },
      },
    )
  }, [maxTokens, maxToolCalls, updateSettings])

  return (
    <section className={cardStyles.card}>
      <div className={cardStyles.cardHeader}>
        <h2 className={cardStyles.cardTitle}>
          <Gauge size={16} strokeWidth={1.5} className={cardStyles.cardTitleIcon} />
          Run Limits
        </h2>
        <p className={cardStyles.cardDesc}>
          System-wide defaults for agent run resource limits.
        </p>
      </div>
      <div className={cardStyles.cardBody}>
        <div className={cardStyles.fieldGroup}>
          <label className={cardStyles.label} htmlFor="max-tokens">
            Max Tokens per Run
          </label>
          <input
            id="max-tokens"
            type="number"
            className={cardStyles.input}
            placeholder="0 (unlimited)"
            value={maxTokens}
            onChange={(e) => setMaxTokens(e.target.value)}
            min={0}
          />
          <span className={cardStyles.fieldHint}>
            Maximum token budget for a single agent run. 0 means unlimited.
          </span>
        </div>
        <div className={cardStyles.fieldGroup}>
          <label className={cardStyles.label} htmlFor="max-tool-calls">
            Max Tool Calls per Run
          </label>
          <input
            id="max-tool-calls"
            type="number"
            className={cardStyles.input}
            placeholder="0 (unlimited)"
            value={maxToolCalls}
            onChange={(e) => setMaxToolCalls(e.target.value)}
            min={0}
          />
          <span className={cardStyles.fieldHint}>
            Maximum number of tool invocations per run. 0 means unlimited.
          </span>
        </div>
        <div className={cardStyles.formActions}>
          <button
            className={styles.saveButton}
            onClick={handleSave}
            disabled={updateSettings.isPending}
          >
            Save
          </button>
          {saved && <span className={cardStyles.successMsg}>Saved</span>}
          {updateSettings.isError && (
            <span className={cardStyles.errorMsg}>Failed to save settings</span>
          )}
        </div>
      </div>
    </section>
  )
}

/* ------------------------------------------------------------------ */
/*  SystemInfoSection                                                  */
/* ------------------------------------------------------------------ */

function SystemInfoSection() {
  const { data: info } = useSystemInfo()

  const tiles = info
    ? [
        { label: 'Version', value: info.version },
        { label: 'Uptime', value: info.uptime },
        { label: 'Database Size', value: info.db_size },
        { label: 'MCP Servers', value: String(info.mcp_servers) },
        { label: 'Agents', value: String(info.policies) },
        { label: 'Users', value: String(info.users) },
      ]
    : []

  return (
    <section className={cardStyles.card}>
      <div className={cardStyles.cardHeader}>
        <h2 className={cardStyles.cardTitle}>
          <Info size={16} strokeWidth={1.5} className={cardStyles.cardTitleIcon} />
          System Information
        </h2>
        <p className={cardStyles.cardDesc}>
          Runtime statistics refreshed every 30 seconds.
        </p>
      </div>
      <div className={cardStyles.cardBody}>
        <div className={styles.statsGrid}>
          {tiles.map((t) => (
            <div key={t.label} className={styles.statTile}>
              <span className={styles.statLabel}>{t.label}</span>
              <span className={styles.statValue}>{t.value}</span>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}

/* ------------------------------------------------------------------ */
/*  Page                                                               */
/* ------------------------------------------------------------------ */

export default function AdminSystemPage() {
  usePageTitle('System')

  return (
    <div className={styles.page}>
      <PageHeader title="System" />
      <PublicURLSection />
      <RunLimitsSection />
      <SystemInfoSection />
    </div>
  )
}
