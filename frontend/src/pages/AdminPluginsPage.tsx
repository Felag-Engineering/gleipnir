import { useState, useEffect, useRef } from 'react'
import { Link } from 'react-router-dom'
import { PageHeader } from '@/components/PageHeader'
import { QueryBoundary, SkeletonList } from '@/components/QueryBoundary'
import { PluginHealthChip } from '@/components/admin/PluginHealthChip/PluginHealthChip'
import { InstallPluginButton } from '@/components/admin/InstallPluginButton'
import { AddInstanceModal } from '@/components/admin/AddInstanceModal'
import { UninstallPluginModal } from '@/components/admin/UninstallPluginModal'
import { Button } from '@/components/Button'
import { usePluginInstancesForAudience } from '@/hooks/queries/admin'
import { useCurrentUser } from '@/hooks/queries/users'
import { useUninstallPlugin } from '@/hooks/mutations/plugins'
import { usePageTitle } from '@/hooks/usePageTitle'
import { extractErrorMessage } from '@/api/fetch'
import type { ApiInstalledPlugin, ApiPluginInstanceForAudience } from '@/api/types'
import styles from './AdminPluginsPage.module.css'

export default function AdminPluginsPage() {
  usePageTitle('Plugins')

  const { data: instances, status, refetch } = usePluginInstancesForAudience()
  const { data: currentUser } = useCurrentUser()

  // Admin-only: the install and create-instance API endpoints require admin role.
  const canManage = currentUser?.roles.includes('admin') ?? false

  const allInstances = instances ?? []

  const needsReauth = allInstances.filter((inst) => inst.state === 'pending_reauthorize')

  // Group the full list by plugin_id for the "All instances" section so we can
  // render a per-plugin "Add instance" button in each group header.
  const pluginGroups = groupByPluginId(allInstances)

  // Tracks which plugin's "Add instance" modal is currently open.
  const [openAddInstance, setOpenAddInstance] = useState<{
    pluginId: string
    pluginName: string
  } | null>(null)

  // Tracks which plugin's "Uninstall" modal is currently open.
  const [openUninstall, setOpenUninstall] = useState<{
    pluginId: string
    pluginName: string
    instanceNames: string[]
  } | null>(null)
  const [uninstallError, setUninstallError] = useState<string | null>(null)

  // One ref per plugin group's <details> kebab element, keyed by pluginId.
  // Used to close the disclosure when a menu item is activated.
  const kebabRefs = useRef<Map<string, HTMLDetailsElement>>(new Map())

  function closeKebab(pluginId: string) {
    kebabRefs.current.get(pluginId)?.removeAttribute('open')
  }

  const uninstallMutation = useUninstallPlugin()

  function handleUninstall() {
    if (!openUninstall) return
    setUninstallError(null)
    uninstallMutation.mutate(
      { pluginId: openUninstall.pluginId },
      {
        onSuccess: () => {
          setOpenUninstall(null)
        },
        onError: (err: unknown) => {
          setUninstallError(extractErrorMessage(err))
        },
      },
    )
  }

  // lastInstalledPluginId drives the scroll-into-view effect after an install.
  const [lastInstalledPluginId, setLastInstalledPluginId] = useState<string | null>(null)

  // Scroll the newly installed plugin's section into view once it appears in
  // the instance list (it only appears after the first instance is created).
  useEffect(() => {
    if (!lastInstalledPluginId) return
    const el = document.getElementById(`plugin-group-${lastInstalledPluginId}`)
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'start' })
      setLastInstalledPluginId(null)
    }
  }, [lastInstalledPluginId, allInstances])

  function handleInstalled(plugin: ApiInstalledPlugin) {
    setLastInstalledPluginId(plugin.id)
  }

  // Passed to InstallPluginButton so it can auto-clear the success card once
  // the newly installed plugin appears in the instance list.
  function hasInstancesForPlugin(pluginId: string): boolean {
    return allInstances.some((inst) => inst.plugin_id === pluginId)
  }

  return (
    <div className={styles.page}>
      {/* InstallPluginButton lives outside the QueryBoundary so it stays
          visible during loading, error, and empty states. */}
      <PageHeader title="Plugins">
        {canManage && (
          <InstallPluginButton
            onInstalled={handleInstalled}
            hasInstancesForPlugin={hasInstancesForPlugin}
          />
        )}
      </PageHeader>

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
              {canManage
                ? 'Use the Install plugin button above to add one.'
                : 'Install a plugin to see instances here.'}
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
          <div className={styles.pluginGroupList}>
            {pluginGroups.map(({ pluginId, pluginName, instances: groupInstances }) => (
              <div
                key={pluginId}
                id={`plugin-group-${pluginId}`}
                className={styles.pluginGroup}
              >
                <div className={styles.pluginGroupHeader}>
                  <div>
                    <span className={styles.pluginGroupTitle}>{pluginName}</span>
                    <span className={styles.pluginGroupId}>{pluginId}</span>
                  </div>
                  {canManage && (
                    <div className={styles.pluginGroupActions}>
                      <Button
                        variant="secondary"
                        size="small"
                        onClick={() =>
                          setOpenAddInstance({ pluginId, pluginName })
                        }
                      >
                        Add instance
                      </Button>
                      <details
                        className={styles.kebab}
                        ref={(el) => {
                          if (el) kebabRefs.current.set(pluginId, el)
                          else kebabRefs.current.delete(pluginId)
                        }}
                      >
                        <summary className={styles.kebabToggle} aria-label="Plugin actions">
                          &#8942;
                        </summary>
                        <div className={styles.kebabMenu}>
                          <button
                            type="button"
                            className={styles.menuItemDanger}
                            onClick={() => {
                              closeKebab(pluginId)
                              setUninstallError(null)
                              setOpenUninstall({
                                pluginId,
                                pluginName,
                                instanceNames: groupInstances.map((i) => i.instance_name),
                              })
                            }}
                          >
                            Uninstall plugin
                          </button>
                        </div>
                      </details>
                    </div>
                  )}
                </div>
                <div className={styles.tableWrapper}>
                  <table className={styles.table}>
                    <thead>
                      <tr>
                        <th>Instance</th>
                        <th>Status</th>
                      </tr>
                    </thead>
                    <tbody>
                      {groupInstances.map((inst) => (
                        <tr key={inst.id}>
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
              </div>
            ))}
          </div>
        </section>
      </QueryBoundary>

      {openAddInstance && (
        <AddInstanceModal
          pluginId={openAddInstance.pluginId}
          pluginName={openAddInstance.pluginName}
          existingNames={
            allInstances
              .filter((inst) => inst.plugin_id === openAddInstance.pluginId)
              .map((inst) => inst.instance_name)
          }
          onClose={() => setOpenAddInstance(null)}
        />
      )}

      {openUninstall && (
        <UninstallPluginModal
          pluginName={openUninstall.pluginName}
          instanceNames={openUninstall.instanceNames}
          onClose={() => {
            setOpenUninstall(null)
            setUninstallError(null)
          }}
          onConfirm={handleUninstall}
          isPending={uninstallMutation.isPending}
          error={uninstallError}
        />
      )}
    </div>
  )
}

interface PluginGroup {
  pluginId: string
  pluginName: string
  instances: ApiPluginInstanceForAudience[]
}

// groupByPluginId preserves insertion order (first-seen plugin id wins) so the
// list order is stable across refetches.
function groupByPluginId(instances: ApiPluginInstanceForAudience[]): PluginGroup[] {
  const order: string[] = []
  const map = new Map<string, PluginGroup>()

  for (const inst of instances) {
    if (!map.has(inst.plugin_id)) {
      order.push(inst.plugin_id)
      map.set(inst.plugin_id, {
        pluginId: inst.plugin_id,
        pluginName: inst.plugin_name ?? inst.plugin_id,
        instances: [],
      })
    }
    map.get(inst.plugin_id)!.instances.push(inst)
  }

  return order.map((id) => map.get(id)!)
}
