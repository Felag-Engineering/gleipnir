import { Button } from '@/components/Button'
import type { ApiPluginDetail } from '@/api/types'
import styles from './PluginReviewCard.module.css'

interface PluginReviewCardProps {
  plugin: ApiPluginDetail
  onApprove: () => void
  onReject: () => void
  isApproving: boolean
  isRejecting: boolean
}

// SERVICE_LABEL maps a manifest service key to the display label for the badge.
const SERVICE_LABEL: Record<string, string> = {
  tool: 'Tool',
  trigger: 'Trigger',
  channel: 'Channel',
}

// SERVICE_BADGE_CLASS maps a service key to the appropriate color class.
const SERVICE_BADGE_CLASS: Record<string, string> = {
  tool: styles.serviceTool,
  trigger: styles.serviceTrigger,
  channel: styles.serviceChannel,
}

// AUTH_STRATEGY_LABEL gives human-readable names for the strategy strings
// from the manifest (spec §9.1).
const AUTH_STRATEGY_LABEL: Record<string, string> = {
  none: 'None',
  static_api_key: 'Static API key',
  header_set: 'Custom headers',
  basic_auth: 'Basic auth',
  oauth2_authcode: 'OAuth 2.0 (authorization code)',
  oauth2_clientcred: 'OAuth 2.0 (client credentials)',
}

export function PluginReviewCard({
  plugin,
  onApprove,
  onReject,
  isApproving,
  isRejecting,
}: PluginReviewCardProps) {
  const busy = isApproving || isRejecting

  return (
    <div className={styles.card}>
      {/* Header: name + version */}
      <div className={styles.header}>
        <div className={styles.nameRow}>
          <span className={styles.name}>{plugin.name}</span>
          <span className={styles.version}>{plugin.version}</span>
        </div>
        {plugin.description && (
          <p className={styles.description}>{plugin.description}</p>
        )}
      </div>

      {/* Consent surface: metadata the admin needs to make a trust decision */}
      <dl className={styles.meta}>
        {plugin.services.length > 0 && (
          <div className={styles.metaRow}>
            <dt className={styles.metaLabel}>Services</dt>
            <dd className={styles.metaValue}>
              <div className={styles.badges}>
                {plugin.services.map((svc) => (
                  <span
                    key={svc}
                    className={SERVICE_BADGE_CLASS[svc] ?? styles.serviceTool}
                  >
                    {SERVICE_LABEL[svc] ?? svc}
                  </span>
                ))}
              </div>
            </dd>
          </div>
        )}

        {plugin.tier2_capabilities && plugin.tier2_capabilities.length > 0 && (
          <div className={styles.metaRow}>
            <dt className={styles.metaLabel}>Tier-2 capabilities</dt>
            <dd className={styles.metaValue}>
              <div className={styles.badges}>
                {plugin.tier2_capabilities.map((cap) => (
                  <span key={cap} className={styles.capBadge}>{cap}</span>
                ))}
              </div>
            </dd>
          </div>
        )}

        <div className={styles.metaRow}>
          <dt className={styles.metaLabel}>Auth strategy</dt>
          <dd className={styles.metaValue}>
            {AUTH_STRATEGY_LABEL[plugin.auth_strategy] ?? plugin.auth_strategy}
            {plugin.has_oauth_defaults && (
              <span className={styles.hint}> (OAuth defaults declared)</span>
            )}
          </dd>
        </div>

        {plugin.pubkey_fingerprint && (
          <div className={styles.metaRow}>
            <dt className={styles.metaLabel}>Signing key</dt>
            <dd className={styles.metaValueMono}>{plugin.pubkey_fingerprint}</dd>
          </div>
        )}

        <div className={styles.metaRow}>
          <dt className={styles.metaLabel}>SBOM</dt>
          <dd className={styles.metaValue}>
            {plugin.has_sbom ? (
              <a
                href={`/api/v1/admin/plugins/${encodeURIComponent(plugin.id)}/sbom`}
                target="_blank"
                rel="noopener noreferrer"
                className={styles.sbomLink}
              >
                Included
              </a>
            ) : (
              'Not included'
            )}
          </dd>
        </div>

        {plugin.author && (
          <div className={styles.metaRow}>
            <dt className={styles.metaLabel}>Author</dt>
            <dd className={styles.metaValue}>{plugin.author}</dd>
          </div>
        )}

        {plugin.license && (
          <div className={styles.metaRow}>
            <dt className={styles.metaLabel}>License</dt>
            <dd className={styles.metaValue}>{plugin.license}</dd>
          </div>
        )}
      </dl>

      {/* Action footer */}
      <div className={styles.actions}>
        <Button
          variant="primary"
          size="small"
          onClick={onApprove}
          disabled={busy}
        >
          {isApproving ? 'Approving…' : 'Approve'}
        </Button>
        <Button
          variant="danger"
          size="small"
          onClick={onReject}
          disabled={busy}
        >
          {isRejecting ? 'Rejecting…' : 'Reject'}
        </Button>
      </div>
    </div>
  )
}
