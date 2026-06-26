import { useNavigate } from 'react-router-dom'
import { useAudiences, useAudience, usePluginInstancesForAudience } from '@/hooks/queries/admin'
import { RoutingPreview } from '@/components/admin/AudienceEditor/RoutingPreview'
import { FieldError } from '@/components/form/FieldError'
import type { AudienceFormState, SectionIssues } from './types'
import shared from './FormSections.module.css'
import styles from './AudienceSection.module.css'
import alertStyles from '@/styles/alerts.module.css'

export interface AudienceSectionProps {
  value: AudienceFormState
  onChange: (next: AudienceFormState) => void
  onNewAudienceClick?: () => void
  errors?: SectionIssues
}

export function AudienceSection({ value, onChange, onNewAudienceClick, errors = [] }: AudienceSectionProps) {
  const navigate = useNavigate()
  const audiencesQuery = useAudiences()
  const audiences = audiencesQuery.data ?? []
  const pluginInstancesQuery = usePluginInstancesForAudience()

  // Find the audience record matching the selected name to get its id.
  const matchedAudience = value.name !== ''
    ? audiences.find(a => a.name === value.name) ?? null
    : null

  const audienceDetailQuery = useAudience(matchedAudience?.id)

  const audienceErrors = errors.filter(e => e.field === 'audience').map(e => e.message)

  function handleSelectChange(e: React.ChangeEvent<HTMLSelectElement>) {
    onChange({ name: e.target.value })
  }

  function handleNewAudienceClick() {
    if (onNewAudienceClick) {
      onNewAudienceClick()
    } else {
      navigate('/admin/audiences/new')
    }
  }

  // Three-state render machine for the preview area:
  // (a) list loading + name set — show neutral placeholder (don't know yet if it exists)
  // (b) list loaded + name set + no match — show "not available" warning
  // (c) list loaded + match found + detail resolved — show RoutingPreview
  function renderPreview() {
    if (value.name === '') {
      return <p className={styles.empty}>No audience selected.</p>
    }

    if (audiencesQuery.isLoading) {
      // State (a): list is still loading — cannot confirm existence yet
      return <p className={styles.placeholder}>Resolving routing…</p>
    }

    if (!matchedAudience) {
      // State (b): list loaded but no match — audience was deleted or renamed
      return (
        <span className={alertStyles.alertWarning} role="alert">
          {`Audience "${value.name}" is no longer available.`}
        </span>
      )
    }

    if (!audienceDetailQuery.data) {
      // Detail fetch in flight (list loaded, id known, detail pending)
      return <p className={styles.placeholder}>Resolving routing…</p>
    }

    // State (c): fully resolved
    const detail = audienceDetailQuery.data
    return (
      <div className={styles.preview}>
        <RoutingPreview
          entries={detail.entries}
          disableInAppFallback={detail.disable_in_app_fallback}
          pluginInstances={pluginInstancesQuery.data ?? []}
        />
      </div>
    )
  }

  return (
    <section className={shared.section} data-field="audience">
      <div>
        <h3 className={shared.heading}>Audience</h3>
        <p className={shared.label}>Optional — defines where notifications and feedback requests are routed.</p>
      </div>

      <div className={styles.row}>
        <select
          className={styles.select}
          value={value.name}
          onChange={handleSelectChange}
          aria-label="Audience"
        >
          <option value="">— None —</option>
          {audiences.map(a => (
            <option key={a.id} value={a.name}>{a.name}</option>
          ))}
        </select>

        <button
          type="button"
          className={styles.newLink}
          onClick={handleNewAudienceClick}
        >
          + New audience
        </button>
      </div>

      {renderPreview()}

      <FieldError messages={audienceErrors} />
    </section>
  )
}
