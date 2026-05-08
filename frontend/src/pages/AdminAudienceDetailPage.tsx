import { Link, useParams } from 'react-router-dom'
import { ArrowLeft, Bell, MessageSquare } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { QueryBoundary, SkeletonList } from '@/components/QueryBoundary'
import { CollapsibleJSON } from '@/components/CollapsibleJSON/CollapsibleJSON'
import { useAudience, useAudienceReferences } from '@/hooks/queries/admin'
import { useCurrentUser } from '@/hooks/queries/users'
import { usePageTitle } from '@/hooks/usePageTitle'
import { formatDate, formatTimeAgo } from '@/utils/format'
import type { ApiAudienceEntry } from '@/api/types'
import styles from './AdminAudienceDetailPage.module.css'

const SYNTHETIC_IN_APP_LABEL = 'gleipnir.in-app (built-in fallback)'

function entryDisplayName(entry: ApiAudienceEntry): string {
  if (entry.auto) return SYNTHETIC_IN_APP_LABEL
  return entry.plugin_instance_id || '(unset)'
}

function routingPreview(entries: ApiAudienceEntry[]): { notify: string[]; request: string | null } {
  const notify = entries.filter((e) => e.notify).map(entryDisplayName)
  const requestEntry = entries.find((e) => e.request)
  return {
    notify,
    request: requestEntry ? entryDisplayName(requestEntry) : null,
  }
}

export default function AdminAudienceDetailPage() {
  const { id } = useParams<{ id: string }>()
  usePageTitle('Audience')

  const audienceQuery = useAudience(id)
  const referencesQuery = useAudienceReferences(id)
  const { data: currentUser } = useCurrentUser()
  const canManage = currentUser?.roles?.some((r) => r === 'admin' || r === 'operator') ?? false

  return (
    <div className={styles.page}>
      <Link to="/admin/audiences" className={styles.backLink}>
        <ArrowLeft size={14} strokeWidth={1.5} aria-hidden />
        All audiences
      </Link>

      <PageHeader title={audienceQuery.data?.name ?? 'Audience'} />

      <QueryBoundary
        status={audienceQuery.status}
        errorMessage="Failed to load audience."
        onRetry={() => { void audienceQuery.refetch() }}
        skeleton={<SkeletonList count={4} height={56} gap={12} borderRadius={8} />}
      >
        {audienceQuery.data && (
          <>
            <RoutingPreview entries={audienceQuery.data.entries} />

            <section className={styles.section}>
              <h2 className={styles.sectionTitle}>Entries</h2>
              <p className={styles.sectionDesc}>
                Channels are tried in order. The synthetic <code>gleipnir.in-app</code> fallback
                is appended automatically unless explicitly disabled.
              </p>
              <ol className={styles.entryList}>
                {audienceQuery.data.entries.map((entry) => (
                  <EntryCard key={`${entry.id}:${entry.position}`} entry={entry} />
                ))}
              </ol>
            </section>

            <section className={styles.section}>
              <h2 className={styles.sectionTitle}>Metadata</h2>
              <dl className={styles.meta}>
                <div className={styles.metaRow}>
                  <dt>ID</dt>
                  <dd className={styles.mono}>{audienceQuery.data.id}</dd>
                </div>
                <div className={styles.metaRow}>
                  <dt>Version</dt>
                  <dd>{audienceQuery.data.version}</dd>
                </div>
                <div className={styles.metaRow}>
                  <dt>Disable in-app fallback</dt>
                  <dd>{audienceQuery.data.disable_in_app_fallback ? 'Yes' : 'No'}</dd>
                </div>
                <div className={styles.metaRow}>
                  <dt>Created</dt>
                  <dd>{formatDate(audienceQuery.data.created_at)}</dd>
                </div>
                <div className={styles.metaRow}>
                  <dt>Updated</dt>
                  <dd>{formatTimeAgo(audienceQuery.data.updated_at)}</dd>
                </div>
              </dl>
            </section>

            <ReferencesSection
              status={referencesQuery.status}
              data={referencesQuery.data}
              onRetry={() => { void referencesQuery.refetch() }}
            />

            {!canManage && (
              <p className={styles.readOnlyNotice}>
                You have read-only access. Editing requires an admin or operator role.
              </p>
            )}
          </>
        )}
      </QueryBoundary>
    </div>
  )
}

function RoutingPreview({ entries }: { entries: ApiAudienceEntry[] }) {
  const { notify, request } = routingPreview(entries)
  return (
    <section className={styles.routingPreview} aria-label="Routing preview">
      <div className={styles.routingRow}>
        <Bell size={14} strokeWidth={1.5} aria-hidden className={styles.routingIcon} />
        <span className={styles.routingLabel}>Notifications fan out to:</span>
        <span className={styles.routingValue}>
          {notify.length > 0 ? notify.join(', ') : <em className={styles.muted}>no entries</em>}
        </span>
      </div>
      <div className={styles.routingRow}>
        <MessageSquare size={14} strokeWidth={1.5} aria-hidden className={styles.routingIcon} />
        <span className={styles.routingLabel}>Requests routed to:</span>
        <span className={styles.routingValue}>
          {request ?? <em className={styles.muted}>no Request-capable entry</em>}
        </span>
      </div>
    </section>
  )
}

function EntryCard({ entry }: { entry: ApiAudienceEntry }) {
  return (
    <li className={`${styles.entryCard} ${entry.auto ? styles.entryCardAuto : ''}`}>
      <div className={styles.entryHeader}>
        <span className={styles.entryPosition}>{entry.position + 1}</span>
        <span className={styles.entryName}>{entryDisplayName(entry)}</span>
        <div className={styles.entryFlags}>
          {entry.notify && (
            <span className={`${styles.flag} ${styles.flagNotify}`}>Notify</span>
          )}
          {entry.request && (
            <span className={`${styles.flag} ${styles.flagRequest}`}>Request</span>
          )}
          {entry.auto && (
            <span className={`${styles.flag} ${styles.flagAuto}`}>Auto-appended</span>
          )}
        </div>
      </div>
      {!entry.auto && Object.keys(entry.config ?? {}).length > 0 && (
        <div className={styles.entryConfig}>
          <CollapsibleJSON value={entry.config} />
        </div>
      )}
    </li>
  )
}

interface ReferencesSectionProps {
  status: 'pending' | 'error' | 'success'
  data: ReturnType<typeof useAudienceReferences>['data']
  onRetry: () => void
}

function ReferencesSection({ status, data, onRetry }: ReferencesSectionProps) {
  return (
    <section className={styles.section}>
      <h2 className={styles.sectionTitle}>References</h2>
      <p className={styles.sectionDesc}>
        Policies that route to this audience and any in-flight runs that would be affected by edits.
      </p>
      <QueryBoundary
        status={status}
        errorMessage="Failed to load references."
        onRetry={onRetry}
        skeleton={<SkeletonList count={2} height={32} gap={8} borderRadius={6} />}
      >
        {data && (
          <div className={styles.refsGrid}>
            <div>
              <h3 className={styles.refsHeading}>
                Referencing policies ({data.policies.length})
              </h3>
              {data.policies.length === 0 ? (
                <p className={styles.muted}>No policies reference this audience.</p>
              ) : (
                <ul className={styles.refList}>
                  {data.policies.map((p) => (
                    <li key={p.id}>
                      <Link to={`/agents/${p.id}`} className={styles.refLink}>
                        {p.name}
                      </Link>
                    </li>
                  ))}
                </ul>
              )}
            </div>
            <div>
              <h3 className={styles.refsHeading}>
                In-flight runs ({data.in_flight_runs.length})
              </h3>
              {data.in_flight_runs.length === 0 ? (
                <p className={styles.muted}>No in-flight runs reference this audience.</p>
              ) : (
                <ul className={styles.refList}>
                  {data.in_flight_runs.map((r) => (
                    <li key={r.id}>
                      <Link to={`/runs/${r.id}`} className={styles.refLink}>
                        {r.id.slice(0, 8)}…
                      </Link>
                      <span className={styles.refSubtle}> ({r.status})</span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        )}
      </QueryBoundary>
    </section>
  )
}
