import { useState } from 'react'
import type { ApiMcpServer } from '@/api/types'
import { useUpdateMcpServer } from '@/hooks/mutations/servers'
import { formatDate } from '@/utils/format'
import styles from './CaCertificateSection.module.css'

interface Props {
  server: ApiMcpServer
}

// CaCertificateSection shows the parsed summary of a server's pinned CA
// certificate (issue #928) and, for non-managed servers, lets an operator
// add, replace, or remove the pin. It owns its own useUpdateMcpServer
// mutation rather than sharing one with the rest of ServerDetailModal, since
// name/url and the CA pin are edited independently.
export function CaCertificateSection({ server }: Props) {
  // A managed entry's endpoint belongs to the plugin lifecycle, the same
  // reason ServerDetailModal hides the auth-header editor for it.
  const isManaged = server.trust_tier === 'managed'
  const certificates = server.ca_certificates ?? []

  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(server.ca_cert_pem ?? '')
  const [error, setError] = useState<string | null>(null)

  const updateMutation = useUpdateMcpServer()

  function openEditor() {
    setValue(server.ca_cert_pem ?? '')
    setError(null)
    setEditing(true)
  }

  function handleSave() {
    setError(null)
    updateMutation.mutate(
      { id: server.id, name: server.name, url: server.url, ca_cert_pem: value.trim() },
      {
        onSuccess: () => setEditing(false),
        onError: (err) => setError(err.detail ?? err.message),
      },
    )
  }

  function handleRemove() {
    setError(null)
    updateMutation.mutate(
      { id: server.id, name: server.name, url: server.url, ca_cert_pem: '' },
      {
        onSuccess: () => {
          setValue('')
          setEditing(false)
        },
        onError: (err) => setError(err.detail ?? err.message),
      },
    )
  }

  return (
    <div className={styles.section}>
      <div className={styles.title}>CA certificate</div>

      {certificates.length > 0 ? (
        <ul className={styles.list}>
          {certificates.map((cert) => {
            const expired = new Date(cert.not_after).getTime() < Date.now()
            return (
              <li key={cert.sha256_fingerprint} className={styles.item}>
                <div className={styles.subject}>{cert.subject}</div>
                <div className={styles.fingerprint}>{cert.sha256_fingerprint}</div>
                <div className={styles.expiry}>
                  Expires {formatDate(cert.not_after)}
                  {expired && <span className={styles.expiredBadge}>Expired</span>}
                </div>
              </li>
            )
          })}
        </ul>
      ) : (
        <p className={styles.empty}>
          No CA certificate pinned — this server is verified against the system trust store.
        </p>
      )}

      {!isManaged && !editing && (
        <div className={styles.actions}>
          <button type="button" className={styles.editBtn} onClick={openEditor}>
            {certificates.length > 0 ? 'Edit' : 'Add CA certificate'}
          </button>
          {certificates.length > 0 && (
            <button
              type="button"
              className={styles.removeBtn}
              onClick={handleRemove}
              disabled={updateMutation.isPending}
            >
              Remove CA
            </button>
          )}
        </div>
      )}

      {!isManaged && editing && (
        <div className={styles.editor}>
          <textarea
            className={styles.textarea}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="-----BEGIN CERTIFICATE-----"
            spellCheck={false}
            aria-label="CA certificate PEM"
          />
          <p className={styles.hint}>
            For HTTPS servers behind a private CA. Paste the CA certificate (not the server
            certificate); only this CA will be trusted for this server.
          </p>
          {error && <div className={styles.error}>{error}</div>}
          <div className={styles.editorFooter}>
            <button
              type="button"
              className={styles.cancelBtn}
              onClick={() => {
                setEditing(false)
                setError(null)
              }}
            >
              Cancel
            </button>
            <button
              type="button"
              className={styles.saveBtn}
              onClick={handleSave}
              disabled={updateMutation.isPending}
            >
              {updateMutation.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
