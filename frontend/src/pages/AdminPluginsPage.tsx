import { useState, useEffect, useRef } from 'react'
import { Link } from 'react-router-dom'
import { PageHeader } from '@/components/PageHeader'
import { QueryBoundary, SkeletonList } from '@/components/QueryBoundary'
import { PluginHealthChip } from '@/components/admin/PluginHealthChip/PluginHealthChip'
import { PluginCard } from '@/components/admin/PluginCard'
import { InstallPluginButton } from '@/components/admin/InstallPluginButton'
import { PluginMemoryBar } from '@/components/admin/PluginMemoryBar/PluginMemoryBar'
import { AddInstanceModal } from '@/components/admin/AddInstanceModal'
import { UninstallPluginModal } from '@/components/admin/UninstallPluginModal'
import { Button } from '@/components/Button'
import { usePluginInstancesForAudience } from '@/hooks/queries/admin'
import { usePlugins, usePluginDetail } from '@/hooks/queries/plugins'
import { useCurrentUser } from '@/hooks/queries/users'
import { useUninstallPlugin } from '@/hooks/mutations/plugins'
import { usePageTitle } from '@/hooks/usePageTitle'
import { extractErrorMessage } from '@/api/fetch'
import { worstHealth } from '@/utils/pluginHealth'
import type {
  ApiInstalledPlugin,
  ApiPluginDetail,
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
  const pluginCards = derivePluginCards(allInstances, pluginList ?? [])

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
        <PluginMemoryBar />
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
                description={card.description || undefined}
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
              <PluginDetailPane
                card={selectedCard}
                canManage={canManage}
                onAddInstance={() =>
                  setOpenAddInstance({
                    pluginId: selectedCard.pluginId,
                    pluginName: selectedCard.pluginName,
                  })
                }
                onUninstall={() => {
                  closeKebab(selectedCard.pluginId)
                  setUninstallError(null)
                  setOpenUninstall({
                    pluginId: selectedCard.pluginId,
                    pluginName: selectedCard.pluginName,
                    instanceNames: selectedCard.instances.map((i) => i.instance_name),
                  })
                }}
                kebabRef={(el) => {
                  if (el) kebabRefs.current.set(selectedCard.pluginId, el)
                  else kebabRefs.current.delete(selectedCard.pluginId)
                }}
              />

              <div className={styles.tableWrapper}>
                <table className={styles.table}>
                  <thead>
                    <tr>
                      <th>Instance</th>
                      <th>Status</th>
                      <th>Detail</th>
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
                        <td className={styles.instanceDetail}>
                          {inst.state !== 'healthy' && inst.health_detail
                            ? inst.health_detail
                            : <span className={styles.muted}>—</span>}
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
  description: string
  services: string[]
  instances: ApiPluginInstanceForAudience[]
  aggregateHealth: PluginHealthState
  hasSbom: boolean
}

const AUTH_STRATEGY_LABEL: Record<string, string> = {
  none: 'None',
  static_api_key: 'Static API key',
  header_set: 'Custom headers',
  basic_auth: 'Basic auth',
  oauth2_authcode: 'OAuth 2.0 (authorization code)',
  oauth2_clientcred: 'OAuth 2.0 (client credentials)',
}

interface PluginDetailPaneProps {
  card: PluginCardData
  canManage: boolean
  onAddInstance: () => void
  onUninstall: () => void
  kebabRef: (el: HTMLDetailsElement | null) => void
}

// PluginDetailPane renders the detail header + metadata summary for the
// selected plugin. It fetches the full plugin detail (description, author,
// license, tier-2 caps, auth strategy) from the detail endpoint while also
// reading version, services, and has_sbom from the already-loaded card data
// and instance event_kinds.
function PluginDetailPane({
  card,
  canManage,
  onAddInstance,
  onUninstall,
  kebabRef,
}: PluginDetailPaneProps) {
  const { data: detail } = usePluginDetail(card.pluginId)

  // Deduplicate event kinds across instances — same plugin = same manifest,
  // but keep full objects (with description) by deduplicating on kind name.
  const eventKindMap = new Map<string, { kind: string; description: string }>()
  for (const inst of card.instances) {
    for (const ek of inst.event_kinds ?? []) {
      if (!eventKindMap.has(ek.kind)) {
        eventKindMap.set(ek.kind, { kind: ek.kind, description: ek.description })
      }
    }
  }
  const eventKinds = Array.from(eventKindMap.values())

  // Deduplicate tools across instances — same plugin = same manifest.
  const toolMap = new Map<string, { name: string; description: string }>()
  for (const inst of card.instances) {
    for (const tool of inst.tools ?? []) {
      if (!toolMap.has(tool.name)) {
        toolMap.set(tool.name, { name: tool.name, description: tool.description })
      }
    }
  }
  const tools = Array.from(toolMap.values())

  // Channel capabilities from any instance (all instances share the same manifest).
  const firstInst = card.instances[0]
  const implementsNotify = firstInst?.implements_notify ?? false
  const implementsRequest = firstInst?.implements_request ?? false

  return (
    <>
      <div className={styles.detailHeader}>
        <div className={styles.detailTitleRow}>
          <h2 className={styles.detailTitle}>{card.pluginName}</h2>
          {card.pluginVersion && (
            <span className={styles.detailVersion}>{card.pluginVersion}</span>
          )}
        </div>
        {canManage && (
          <div className={styles.detailActions}>
            <Button
              variant="secondary"
              size="small"
              onClick={onAddInstance}
            >
              Add instance
            </Button>
            <details
              className={styles.kebab}
              ref={kebabRef}
            >
              <summary className={styles.kebabToggle} aria-label="Plugin actions">
                &#8942;
              </summary>
              <div className={styles.kebabMenu}>
                <button
                  type="button"
                  className={styles.menuItemDanger}
                  onClick={onUninstall}
                >
                  Uninstall plugin
                </button>
              </div>
            </details>
          </div>
        )}
      </div>

      <PluginDetailSummary
        card={card}
        detail={detail}
        eventKinds={eventKinds}
        tools={tools}
        implementsNotify={implementsNotify}
        implementsRequest={implementsRequest}
      />
    </>
  )
}

interface PluginDetailSummaryProps {
  card: PluginCardData
  detail: ApiPluginDetail | undefined
  eventKinds: { kind: string; description: string }[]
  tools: { name: string; description: string }[]
  implementsNotify: boolean
  implementsRequest: boolean
}

// PluginDetailSummary renders a compact metadata block grouped into two
// visual sections: Capabilities (what it does) and About (identity/reference).
function PluginDetailSummary({
  card,
  detail,
  eventKinds,
  tools,
  implementsNotify,
  implementsRequest,
}: PluginDetailSummaryProps) {
  const hasChannel = card.services.includes('channel')
  const hasCapabilities =
    tools.length > 0 ||
    eventKinds.length > 0 ||
    (hasChannel && (implementsNotify || implementsRequest))

  // Compact identity line: author · license, skipping missing parts.
  // Version is now shown in the detail pane header, not here.
  const identityParts: { text: string; mono: boolean }[] = []
  if (detail?.author) identityParts.push({ text: detail.author, mono: false })
  if (detail?.license) identityParts.push({ text: detail.license, mono: false })

  return (
    <div className={styles.detailSummary}>
      {hasCapabilities && (
        <div className={styles.summarySection}>
          <div className={styles.summarySectionLabel}>Capabilities</div>

          <dl className={styles.summaryDl}>
            {tools.length > 0 && (
              <div className={styles.summaryRow}>
                <dt className={styles.summaryLabel}>Tools</dt>
                <dd className={styles.summaryValue}>
                  <div className={styles.summaryItemList}>
                    {tools.map((tool) => (
                      <div key={tool.name} className={styles.summaryItem}>
                        <span className={styles.summaryItemMono}>{tool.name}</span>
                        {tool.description && (
                          <span className={styles.summaryItemDesc}>{tool.description}</span>
                        )}
                      </div>
                    ))}
                  </div>
                </dd>
              </div>
            )}

            {eventKinds.length > 0 && (
              <div className={styles.summaryRow}>
                <dt className={styles.summaryLabel}>Trigger events</dt>
                <dd className={styles.summaryValue}>
                  <div className={styles.summaryItemList}>
                    {eventKinds.map((ek) => (
                      <div key={ek.kind} className={styles.summaryItem}>
                        <span className={styles.summaryItemMono}>{ek.kind}</span>
                        {ek.description && (
                          <span className={styles.summaryItemDesc}>{ek.description}</span>
                        )}
                      </div>
                    ))}
                  </div>
                </dd>
              </div>
            )}

            {hasChannel && (implementsNotify || implementsRequest) && (
              <div className={styles.summaryRow}>
                <dt className={styles.summaryLabel}>Channel support</dt>
                <dd className={styles.summaryValue}>
                  <div className={styles.summaryItemList}>
                    {implementsNotify && (
                      <div className={styles.summaryItem}>
                        <span className={styles.summaryItemMono}>Notify</span>
                        <span className={styles.summaryItemDesc}>Can send messages</span>
                      </div>
                    )}
                    {implementsRequest && (
                      <div className={styles.summaryItem}>
                        <span className={styles.summaryItemMono}>Request</span>
                        <span className={styles.summaryItemDesc}>Can route approval/feedback requests</span>
                      </div>
                    )}
                  </div>
                </dd>
              </div>
            )}
          </dl>
        </div>
      )}

      <div className={`${styles.summarySection} ${hasCapabilities ? styles.summarySectionAbout : ''}`}>
        <div className={styles.summarySectionLabel}>About</div>

        {identityParts.length > 0 && (
          <div className={styles.summaryIdentityLine}>
            {identityParts.map((part, i) => (
              <span key={i}>
                {i > 0 && <span className={styles.summaryIdentitySep}> · </span>}
                <span className={part.mono ? styles.summaryIdentityMono : styles.summaryIdentityText}>
                  {part.text}
                </span>
              </span>
            ))}
          </div>
        )}

        <dl className={styles.summaryDl}>
          {detail?.auth_strategy && (
            <div className={styles.summaryRow}>
              <dt className={styles.summaryLabel}>Auth strategy</dt>
              <dd className={styles.summaryValue}>
                {AUTH_STRATEGY_LABEL[detail.auth_strategy] ?? detail.auth_strategy}
                {detail.has_oauth_defaults && (
                  <span className={styles.summaryHint}> (OAuth defaults declared)</span>
                )}
              </dd>
            </div>
          )}

          {detail?.tier2_capabilities && detail.tier2_capabilities.length > 0 && (
            <div className={styles.summaryRow}>
              <dt className={styles.summaryLabel}>Extended permissions</dt>
              <dd className={styles.summaryValue}>
                <div className={styles.summaryBadges}>
                  {detail.tier2_capabilities.map((cap) => (
                    <span key={cap} className={styles.capBadge}>{cap}</span>
                  ))}
                </div>
              </dd>
            </div>
          )}

          {card.hasSbom && (
            <div className={styles.summaryRow}>
              <dt className={styles.summaryLabel}>SBOM</dt>
              <dd className={styles.summaryValue}>
                <a
                  href={`/api/v1/admin/plugins/${encodeURIComponent(card.pluginId)}/sbom`}
                  target="_blank"
                  rel="noopener noreferrer"
                  className={styles.sbomLink}
                >
                  Download
                </a>
              </dd>
            </div>
          )}
        </dl>
      </div>
    </div>
  )
}

// derivePluginCards groups instances by plugin_id (preserving insertion order
// so the list is stable across refetches) and computes the aggregate health
// state (worst across all instances) for each plugin card.
//
// pluginList is used to look up has_sbom by plugin UUID (inst.plugin_id). The
// map is keyed by p.id because that is what inst.plugin_id carries.
function derivePluginCards(
  instances: ApiPluginInstanceForAudience[],
  pluginList: ApiPluginListItem[],
): PluginCardData[] {
  const pluginMap = new Map<string, ApiPluginListItem>()
  for (const p of pluginList) {
    pluginMap.set(p.id, p)
  }

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
        description: pluginMap.get(inst.plugin_id)?.description ?? '',
        services: inst.services ?? [],
        instances: [],
        aggregateHealth: 'healthy',
        hasSbom: pluginMap.get(inst.plugin_id)?.has_sbom ?? false,
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
