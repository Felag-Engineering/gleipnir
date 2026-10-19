import type { ApiPluginV2Consent } from '@/api/types'
import { CopyBlock } from '@/components/CopyBlock'
import {
  AUTH_STRATEGY_LABEL,
  elicitationLabel,
  formatCpu,
  formatMemory,
  limitSourceLabel,
} from './consentFormat'
import styles from './PluginV2Consent.module.css'

interface PluginV2ConsentProps {
  consent: ApiPluginV2Consent
  // Empty for an unsigned bundle; the review screen must say so rather than
  // leave the row out.
  pubkeyFingerprint?: string
  tier2Capabilities?: string[]
}

const PROFILE_LABEL: Record<string, string> = {
  tool_provider: 'Tool provider',
  event_source: 'Event source',
  human_channel: 'Human channel',
  identity_provider: 'Identity provider',
}

// Every string rendered here comes from the plugin's manifest, so it is placed
// as a plain text node and never interpreted.
export function PluginV2Consent({
  consent,
  pubkeyFingerprint,
  tier2Capabilities = [],
}: PluginV2ConsentProps) {
  const { image, egress, effective_resources: limits, profiles, tools, event_kinds, auth } = consent

  return (
    <div className={styles.root} aria-label="Container plugin permissions" role="group">
      <section className={styles.section} aria-labelledby="consent-network">
        <h3 id="consent-network" className={styles.heading}>
          {egress.length === 0 ? 'No internet access' : 'Network access'}
        </h3>
        {egress.length === 0 ? (
          <p className={styles.note}>This plugin cannot reach any external host.</p>
        ) : (
          <ul className={styles.items}>
            {egress.map((grant) => (
              <li key={grant.domain}>
                <span className={styles.mono}>{grant.domain}</span>
                {grant.reason && <span className={styles.hint}> {grant.reason}</span>}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className={styles.section} aria-labelledby="consent-limits">
        <h3 id="consent-limits" className={styles.heading}>Resource limits</h3>
        <dl className={styles.list}>
          <div className={styles.row}>
            <dt className={styles.label}>Memory</dt>
            <dd className={styles.value}>
              {formatMemory(limits.memory_mb)}
              <span className={styles.hint}> ({limitSourceLabel(limits.memory_source)})</span>
            </dd>
          </div>
          <div className={styles.row}>
            <dt className={styles.label}>CPU</dt>
            <dd className={styles.value}>
              {formatCpu(limits.cpu_millicores)}
              <span className={styles.hint}> ({limitSourceLabel(limits.cpu_source)})</span>
            </dd>
          </div>
        </dl>
      </section>

      <section className={styles.section} aria-labelledby="consent-capabilities">
        <h3 id="consent-capabilities" className={styles.heading}>Capabilities</h3>
        <dl className={styles.list}>
          <div className={styles.row}>
            <dt className={styles.label}>Roles</dt>
            <dd className={styles.value}>
              <div className={styles.badges}>
                {profiles.declared.map((name) => (
                  <span key={name} className={styles.badge}>{PROFILE_LABEL[name] ?? name}</span>
                ))}
              </div>
              {profiles.human_channel_assurance && (
                <div className={styles.hint}>
                  Channel assurance: {profiles.human_channel_assurance}
                </div>
              )}
              {profiles.identity_link_methods && profiles.identity_link_methods.length > 0 && (
                <div className={styles.hint}>
                  Identity link methods: {profiles.identity_link_methods.join(', ')}
                </div>
              )}
            </dd>
          </div>

          {tools.length > 0 && (
            <div className={styles.row}>
              <dt className={styles.label}>Tools</dt>
              <dd className={styles.value}>
                <ul className={styles.items}>
                  {tools.map((tool) => {
                    const ask = elicitationLabel(tool.elicitation_kind)
                    return (
                      <li key={tool.name}>
                        <span className={styles.mono}>{tool.name}</span>
                        {ask && <span className={styles.hint}> {ask}</span>}
                      </li>
                    )
                  })}
                </ul>
              </dd>
            </div>
          )}

          {event_kinds.length > 0 && (
            <div className={styles.row}>
              <dt className={styles.label}>Events</dt>
              <dd className={styles.value}>
                <ul className={styles.items}>
                  {event_kinds.map((kind) => (
                    <li key={kind.kind}>
                      <span className={styles.mono}>{kind.kind}</span>
                      {kind.description && <span className={styles.hint}> {kind.description}</span>}
                    </li>
                  ))}
                </ul>
              </dd>
            </div>
          )}

          <div className={styles.row}>
            <dt className={styles.label}>Credentials</dt>
            <dd className={styles.value}>
              <span>{AUTH_STRATEGY_LABEL[auth.strategy] ?? (auth.strategy || 'None')}</span>
              {auth.header_name && <div className={styles.hint}>Header: {auth.header_name}</div>}
              {auth.header_names && auth.header_names.length > 0 && (
                <div className={styles.hint}>Headers: {auth.header_names.join(', ')}</div>
              )}
              {auth.oauth_defaults && (
                <div className={styles.hint}>
                  Token URL: {auth.oauth_defaults.token_url}
                  {auth.oauth_defaults.authorization_url &&
                    `; authorization URL: ${auth.oauth_defaults.authorization_url}`}
                  {auth.oauth_defaults.scopes.length > 0 &&
                    `; scopes: ${auth.oauth_defaults.scopes.join(', ')}`}
                </div>
              )}
            </dd>
          </div>

          {tier2Capabilities.length > 0 && (
            <div className={styles.row}>
              <dt className={styles.label}>Host access</dt>
              <dd className={styles.value}>
                <div className={styles.badges}>
                  {tier2Capabilities.map((cap) => (
                    <span key={cap} className={styles.badge}>{cap}</span>
                  ))}
                </div>
              </dd>
            </div>
          )}

          <div className={styles.row}>
            <dt className={styles.label}>Config schema</dt>
            <dd className={styles.value}>
              {consent.config_schema == null ? (
                'None declared'
              ) : (
                <details>
                  <summary className={styles.summary}>Show schema</summary>
                  <pre className={styles.schema}>
                    {JSON.stringify(consent.config_schema, null, 2)}
                  </pre>
                </details>
              )}
            </dd>
          </div>
        </dl>
      </section>

      <section className={styles.section} aria-labelledby="consent-image">
        <h3 id="consent-image" className={styles.heading}>Image</h3>
        <dl className={styles.list}>
          <div className={styles.row}>
            <dt className={styles.label}>Reference</dt>
            <dd className={styles.valueMono}>{image.repository}</dd>
          </div>
          <div className={styles.row}>
            <dt className={styles.label}>Digest</dt>
            <dd className={styles.valueMono}>
              <CopyBlock text={image.digest}>
                <span className={styles.digest}>{image.digest}</span>
              </CopyBlock>
            </dd>
          </div>
          <div className={styles.row}>
            <dt className={styles.label}>Signing key</dt>
            <dd className={pubkeyFingerprint ? styles.valueMono : styles.valueWarn}>
              {pubkeyFingerprint || 'Unsigned bundle: no signing key'}
            </dd>
          </div>
        </dl>
      </section>
    </div>
  )
}
