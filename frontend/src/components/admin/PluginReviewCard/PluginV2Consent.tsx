import type { ApiPluginV2Consent } from '@/api/types'
import styles from './PluginV2Consent.module.css'

interface PluginV2ConsentProps {
  consent: ApiPluginV2Consent
  // Empty for an unsigned bundle; the review screen must say so rather than
  // leave the row out.
  pubkeyFingerprint?: string
}

const PROFILE_LABEL: Record<string, string> = {
  tool_provider: 'Tool provider',
  event_source: 'Event source',
  human_channel: 'Human channel',
  identity_provider: 'Identity provider',
}

// Every string rendered here comes from the plugin's manifest, so it is placed
// as a plain text node and never interpreted.
export function PluginV2Consent({ consent, pubkeyFingerprint }: PluginV2ConsentProps) {
  const { image, egress, resources, profiles, tools, event_kinds, auth } = consent

  return (
    <section className={styles.section} aria-label="Container plugin permissions">
      <h3 className={styles.heading}>What this plugin will run and reach</h3>
      <dl className={styles.list}>
        <div className={styles.row}>
          <dt className={styles.label}>Image</dt>
          <dd className={styles.valueMono}>{image.repository}</dd>
        </div>
        <div className={styles.row}>
          <dt className={styles.label}>Image digest</dt>
          <dd className={styles.valueMono}>{image.digest}</dd>
        </div>
        <div className={styles.row}>
          <dt className={styles.label}>Transport</dt>
          <dd className={styles.value}>
            {image.transport_type}
            {image.transport_port ? `, port ${image.transport_port}` : ''}
          </dd>
        </div>

        <div className={styles.row}>
          <dt className={styles.label}>Signing key</dt>
          <dd className={pubkeyFingerprint ? styles.valueMono : styles.valueWarn}>
            {pubkeyFingerprint || 'Unsigned bundle: no signing key'}
          </dd>
        </div>

        <div className={styles.row}>
          <dt className={styles.label}>Network egress</dt>
          <dd className={styles.value}>
            {egress.length === 0 ? (
              'None: the plugin cannot reach any external host'
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
          </dd>
        </div>

        <div className={styles.row}>
          <dt className={styles.label}>Resource limits</dt>
          <dd className={styles.value}>
            {resources && (resources.memory_mb || resources.cpu_millicores) ? (
              <>
                {resources.memory_mb ? `${resources.memory_mb} MiB memory` : 'Default memory'}
                {', '}
                {resources.cpu_millicores ? `${resources.cpu_millicores} millicores CPU` : 'default CPU'}
              </>
            ) : (
              'Not declared: the host default applies'
            )}
          </dd>
        </div>

        <div className={styles.row}>
          <dt className={styles.label}>Profiles</dt>
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
            <dt className={styles.label}>Declared tools</dt>
            <dd className={styles.value}>
              <ul className={styles.items}>
                {tools.map((tool) => (
                  <li key={tool.name}>
                    <span className={styles.mono}>{tool.name}</span>
                    {tool.elicitation_kind && (
                      <span className={styles.hint}> asks for {tool.elicitation_kind}</span>
                    )}
                  </li>
                ))}
              </ul>
            </dd>
          </div>
        )}

        {event_kinds.length > 0 && (
          <div className={styles.row}>
            <dt className={styles.label}>Event kinds</dt>
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
            <span>{auth.strategy || 'none'}</span>
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
  )
}
