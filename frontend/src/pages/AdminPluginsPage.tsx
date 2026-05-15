import { Link } from 'react-router-dom'
import { PageHeader } from '@/components/PageHeader'
import { QueryBoundary, SkeletonList } from '@/components/QueryBoundary'
import { PluginHealthChip } from '@/components/admin/PluginHealthChip/PluginHealthChip'
import { usePluginInstancesForAudience } from '@/hooks/queries/admin'
import { usePageTitle } from '@/hooks/usePageTitle'
import styles from './AdminPluginsPage.module.css'

export default function AdminPluginsPage() {
  usePageTitle('Plugins')

  const { data: instances, status, refetch } = usePluginInstancesForAudience()

  const needsReauth = (instances ?? []).filter(
    (inst) => inst.state === 'pending_reauthorize',
  )
  const allInstances = instances ?? []

  return (
    <div className={styles.page}>
      <PageHeader title="Plugins" />

      <p className={styles.intro}>
        All installed plugin instances and their current health state.
      </p>

      <QueryBoundary
        status={status}
        isEmpty={allInstances.length === 0}
        errorMessage="Failed to load plugin instances."
        onRetry={() => { void refetch() }}
        skeleton={<SkeletonList count={3} height={48} gap={12} borderRadius={8} />}
        emptyState={
          <div className={styles.emptyState}>
            <p className={styles.emptyHeadline}>No plugin instances</p>
            <p className={styles.emptySubtext}>
              Install a plugin to see instances here.
            </p>
          </div>
        }
      >
        {needsReauth.length > 0 && (
          <section className={styles.section}>
            <h2 className={styles.sectionTitle}>Needs re-authorization</h2>
            <p className={styles.sectionDesc}>
              The <code>public_url</code> setting changed and the OAuth callback
              URL for these instances no longer matches. Re-authorize each
              instance to update the redirect URI registered with the provider.
            </p>
            <div className={styles.tableWrapper}>
              <table className={styles.table}>
                <thead>
                  <tr>
                    <th>Plugin</th>
                    <th>Instance</th>
                    <th>Recorded callback URL</th>
                    <th>Status</th>
                  </tr>
                </thead>
                <tbody>
                  {needsReauth.map((inst) => (
                    <tr key={inst.id}>
                      <td className={styles.pluginName}>{inst.plugin_name ?? inst.plugin_id}</td>
                      <td>
                        <Link
                          to={`/admin/plugins/${encodeURIComponent(inst.plugin_id)}/instances/${encodeURIComponent(inst.id)}`}
                          className={styles.nameLink}
                        >
                          {inst.instance_name}
                        </Link>
                      </td>
                      <td className={styles.callbackUrl}>
                        {inst.last_oauth_callback_url ?? <span className={styles.muted}>—</span>}
                      </td>
                      <td>
                        <PluginHealthChip state={inst.state} detail={inst.health_detail} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        )}

        <section className={styles.section}>
          <h2 className={styles.sectionTitle}>All instances</h2>
          <div className={styles.tableWrapper}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>Plugin</th>
                  <th>Instance</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {allInstances.map((inst) => (
                  <tr key={inst.id}>
                    <td className={styles.pluginName}>{inst.plugin_name ?? inst.plugin_id}</td>
                    <td>
                      <Link
                        to={`/admin/plugins/${encodeURIComponent(inst.plugin_id)}/instances/${encodeURIComponent(inst.id)}`}
                        className={styles.nameLink}
                      >
                        {inst.instance_name}
                      </Link>
                    </td>
                    <td>
                      <PluginHealthChip state={inst.state} detail={inst.health_detail} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      </QueryBoundary>
    </div>
  )
}
