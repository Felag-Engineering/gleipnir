import { useState, useEffect, useRef } from 'react'
import { Link } from 'react-router-dom'
import { PageHeader } from '@/components/PageHeader'
import { QueryBoundary, SkeletonList } from '@/components/QueryBoundary'
import { PluginHealthChip } from '@/components/admin/PluginHealthChip/PluginHealthChip'
import { PluginCard } from '@/components/admin/PluginCard'
import { InstallPluginButton } from '@/components/admin/InstallPluginButton'
import { AddInstanceModal } from '@/components/admin/AddInstanceModal'
import { UninstallPluginModal } from '@/components/admin/UninstallPluginModal'
import { Button } from '@/components/Button'
import { usePluginInstancesForAudience } from '@/hooks/queries/admin'
import { usePlugins } from '@/hooks/queries/plugins'
import { useCurrentUser } from '@/hooks/queries/users'
import { useUninstallPlugin } from '@/hooks/mutations/plugins'
import { usePageTitle } from '@/hooks/usePageTitle'
import { extractErrorMessage } from '@/api/fetch'
import { worstHealth } from '@/utils/pluginHealth'
import type {
  ApiInstalledPlugin,
  ApiPluginInstanceForAudience,
  ApiPluginListItem,
  PluginHealthState,
} from '@/api/types'
import styles from './AdminPluginsPage.module.css'

export default function AdminPluginsPage() {
  usePageTitle('Plugins')

  const { data: instances, status, refetch } = usePluginInstancesForAudience()
  const { data: pluginList } = usePlugins()
  const { data: currentUser } = useCurrentUser()

  // Admin-only: the install and create-instance API endpoints require admin role.
  const canManage = currentUser?.roles.includes('admin') ?? false

  const allInstances = instances ?? []

  // Pending-review plugins have no instances yet, so they don't appear in
  // the instance list. Surface them as a separate action-required section.
  const pendingReview = (pluginList ?? []).filter((p) => p.status === 'pending_review')

  const needsReauth = allInstances.filter((inst) => inst.state === 'pending_reauthorize')

  // Derive one card per unique plugin_id. Insertion order is stable across
  // refetches (same ordering as the old groupByPluginId function).
  const pluginCards = derivePluginCards(allInstances)

  // Which plugin's detail pane is currently shown on the right.
  const [selectedPluginId, setSelectedPluginId] = useState<string | null>(null)

  // Auto-select the first plugin when data arrives and nothing is selected yet.
  useEffect(() => {
    if (selectedPluginId === null && pluginCards.length > 0) {
      setSelectedPluginId(pluginCards[0].pluginId)
    }
  }, [selectedPluginId, pluginCards])

  const selectedCard = pluginCards.find((c) => c.pluginId === selectedPluginId) ?? null

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

  // One ref per plugin's <details> kebab element, keyed by pluginId.
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
          // Select the first remaining plugin after this one is uninstalled.
          const remaining = pluginCards
            .map((c) => c.pluginId)
            .filter((id) => id !== openUninstall.pluginId)
          setSelectedPluginId(remaining[0] ?? null)
        },
        onError: (err: unknown) => {
          setUninstallError(extractErrorMessage(err))
        },
      },
    )
  }

  function handleInstalled(plugin: ApiInstalledPlugin) {
    // Select the newly installed plugin directly; the card list will include it
    // once the query refetches (driven by InstallPluginButton's own invalidation).
    setSelectedPluginId(plugin.id)
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
        {/* Aggregate plugin RSS — separate issue */}
        {canManage && (
          <InstallPluginButton
            onInstalled={handleInstalled}
            hasInstancesForPlugin={hasInstancesForPlugin}
          />
        )}
      </PageHeader>

      <p className={styles.intro}>
        All installed plugins and their instances.
      </p>

      {/* Pending-review plugins have no instances yet and must appear regardless
          of the active-instance list state (outside QueryBoundary). */}
      {pendingReview.length > 0 && (
        <section className={styles.section}>
          <h2 className={styles.sectionTitle}>Pending review</h2>
          <p className={styles.sectionDesc}>
            These plugins were installed but require admin approval before any
            instances can be created. Review each plugin&apos;s manifest to
            decide whether to allow it to run on this instance.
          </p>
          <div className={styles.pendingGrid}>
            {pendingReview.map((plugin) => (
              <PendingReviewRow key={plugin.id} plugin={plugin} />
            ))}
          </div>
        </section>
      )}

      <QueryBoundary
        status={status}
        isEmpty={allInstances.length === 0}
        errorMessage="Failed to load plugin instances."
        onRetry={() => { void refetch() }}
        skeleton={<SkeletonList count={3} height={48} gap={12} borderRadius={8} />}
        emptyState={
          <div className={styles.emptyState}>
            <p className={styles.emptyHeadline}>No plugins installed yet</p>
            <p className={styles.emptySubtext}>
              {canManage
                ? <>Drop a signed bundle into <code className={styles.emptyCode}>/plugins</code> to begin.</>
                : 'No plugins are installed.'}
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

        <div className={styles.twoPaneLayout}>
          {/* Left pane: one PluginCard per plugin */}
          <div className={styles.leftPane}>
            {pluginCards.map((card) => (
              <PluginCard
                key={card.pluginId}
                pluginName={card.pluginName}
                pluginVersion={card.pluginVersion}
                services={card.services}
                instanceCount={card.instances.length}
                aggregateHealth={card.aggregateHealth}
                isSelected={card.pluginId === selectedPluginId}
                onClick={() => setSelectedPluginId(card.pluginId)}
              />
            ))}
          </div>

          {/* Right pane: instance list for the selected plugin */}
          {selectedCard && (
            <div className={styles.rightPane}>
              <div className={styles.detailHeader}>
                <h2 className={styles.detailTitle}>{selectedCard.pluginName}</h2>
                {canManage && (
                  <div className={styles.detailActions}>
                    <Button
                      variant="secondary"
                      size="small"
                      onClick={() =>
                        setOpenAddInstance({
                          pluginId: selectedCard.pluginId,
                          pluginName: selectedCard.pluginName,
                        })
                      }
                    >
                      Add instance
                    </Button>
                    <details
                      className={styles.kebab}
                      ref={(el) => {
                        if (el) kebabRefs.current.set(selectedCard.pluginId, el)
                        else kebabRefs.current.delete(selectedCard.pluginId)
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
                            closeKebab(selectedCard.pluginId)
                            setUninstallError(null)
                            setOpenUninstall({
                              pluginId: selectedCard.pluginId,
                              pluginName: selectedCard.pluginName,
                              instanceNames: selectedCard.instances.map((i) => i.instance_name),
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
                    {selectedCard.instances.map((inst) => (
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
          )}
        </div>
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

// PendingReviewRow renders a single row in the pending-review section.
// It shows the plugin name + version and a "Review" link to the review page.
function PendingReviewRow({ plugin }: { plugin: ApiPluginListItem }) {
  return (
    <div className={styles.pendingRow}>
      <div className={styles.pendingMeta}>
        <span className={styles.pendingName}>{plugin.name}</span>
        <span className={styles.pendingVersion}>{plugin.version}</span>
      </div>
      <Link
        to={`/admin/plugins/${encodeURIComponent(plugin.id)}/review`}
        className={styles.reviewLink}
      >
        Review &amp; approve
      </Link>
    </div>
  )
}

interface PluginCardData {
  pluginId: string
  pluginName: string
  pluginVersion: string
  services: string[]
  instances: ApiPluginInstanceForAudience[]
  aggregateHealth: PluginHealthState
}

// derivePluginCards groups instances by plugin_id (preserving insertion order
// so the list is stable across refetches) and computes the aggregate health
// state (worst across all instances) for each plugin card.
function derivePluginCards(instances: ApiPluginInstanceForAudience[]): PluginCardData[] {
  const order: string[] = []
  const map = new Map<string, PluginCardData>()

  for (const inst of instances) {
    if (!map.has(inst.plugin_id)) {
      order.push(inst.plugin_id)
      map.set(inst.plugin_id, {
        pluginId: inst.plugin_id,
        // plugin_name and services/version come from the manifest and are
        // identical across all instances of the same plugin, so taking from
        // the first instance is correct.
        pluginName: inst.plugin_name ?? inst.plugin_id,
        pluginVersion: inst.plugin_version ?? '',
        services: inst.services ?? [],
        instances: [],
        aggregateHealth: 'healthy',
      })
    }
    map.get(inst.plugin_id)!.instances.push(inst)
  }

  // Compute aggregate health after all instances are collected.
  for (const card of map.values()) {
    card.aggregateHealth = worstHealth(card.instances.map((i) => i.state))
  }

  return order.map((id) => map.get(id)!)
}
