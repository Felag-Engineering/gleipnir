import { useEffect, useState } from 'react';
import shared from '../FormSections.module.css';
import triggerStyles from '../TriggerSection.module.css';
import styles from './WebhookConfig.module.css';
import alerts from '@/styles/alerts.module.css';
import { Modal } from '@/components/Modal/Modal';
import { ModalFooter } from '@/components/ModalFooter/ModalFooter';
import { useWebhookSecret } from '@/hooks/queries/policies';
import { useRotateWebhookSecret } from '@/hooks/mutations/policies';
import type { WebhookTriggerState, WebhookAuthMode } from '../types';

export interface WebhookConfigProps {
  policyId?: string;
  value: WebhookTriggerState;
  onChange: (next: WebhookTriggerState) => void;
}

export function WebhookConfig({ policyId, value, onChange }: WebhookConfigProps) {
  const [urlCopied, setUrlCopied] = useState(false);
  const [secretCopied, setSecretCopied] = useState(false);
  const [revealed, setRevealed] = useState(false);
  const [showRotateModal, setShowRotateModal] = useState(false);

  const { data: secret, isLoading: secretLoading } = useWebhookSecret(policyId ?? '', revealed && !!policyId);
  const rotateMutation = useRotateWebhookSecret();

  // Reset revealed state when policyId changes (navigating between policies).
  useEffect(() => {
    setRevealed(false);
    setSecretCopied(false);
  }, [policyId]);

  useEffect(() => {
    if (!urlCopied) return;
    const t = setTimeout(() => setUrlCopied(false), 1500);
    return () => clearTimeout(t);
  }, [urlCopied]);

  useEffect(() => {
    if (!secretCopied) return;
    const t = setTimeout(() => setSecretCopied(false), 1500);
    return () => clearTimeout(t);
  }, [secretCopied]);

  async function handleCopyUrl() {
    if (!policyId) return;
    try {
      await navigator.clipboard.writeText(`/api/v1/webhooks/${policyId}`);
      setUrlCopied(true);
    } catch {
      // clipboard API unavailable or permission denied — silently fail
    }
  }

  async function handleCopySecret() {
    if (!secret) return;
    try {
      await navigator.clipboard.writeText(secret);
      setSecretCopied(true);
    } catch {
      // clipboard API unavailable
    }
  }

  function handleHide() {
    setRevealed(false);
    setSecretCopied(false);
  }

  function handleConfirmRotate() {
    if (!policyId) return;
    rotateMutation.mutate(policyId, {
      onSuccess: () => {
        setRevealed(true);
        setShowRotateModal(false);
      },
    });
  }

  function handleGenerateInitial() {
    if (!policyId) return;
    // No confirm needed — there's nothing to invalidate yet.
    rotateMutation.mutate(policyId, {
      onSuccess: () => setRevealed(true),
    });
  }

  const url = policyId ? `/api/v1/webhooks/${policyId}` : undefined;
  const displayUrl = url ?? 'POST /api/v1/webhooks/<agent-id>';

  const needsSecret = value.auth !== 'none' && !!policyId;

  // Whether the secret has been fetched and is null (never rotated).
  const noSecretYet = needsSecret && revealed && !secretLoading && secret === null;
  const hasSecret = needsSecret && revealed && !secretLoading && secret !== null;

  function buildSnippet() {
    const endpoint = url ?? 'https://gleipnir.example.com/api/v1/webhooks/<id>';
    const secretVal = hasSecret && secret ? secret : '<secret>';

    if (value.auth === 'hmac') {
      return `SECRET="${secretVal}"
BODY='{"event":"deploy","ref":"main"}'
SIG=$(echo -n "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print "sha256="$2}')
curl -X POST "${endpoint}" \\
  -H "Content-Type: application/json" \\
  -H "X-Gleipnir-Signature: $SIG" \\
  -d "$BODY"`;
    }
    if (value.auth === 'bearer') {
      return `curl -X POST "${endpoint}" \\
  -H "Authorization: Bearer ${secretVal}" \\
  -H "Content-Type: application/json" \\
  -d '{"event":"deploy"}'`;
    }
    return '';
  }

  const snippet = buildSnippet();

  return (
    <>
      {/* Rotate confirmation modal */}
      {showRotateModal && (
        <Modal
          title="Rotate webhook secret"
          onClose={() => setShowRotateModal(false)}
          footer={
            <ModalFooter
              onCancel={() => setShowRotateModal(false)}
              onSubmit={handleConfirmRotate}
              isLoading={rotateMutation.isPending}
              submitLabel="Rotate"
              loadingLabel="Rotating…"
              variant="danger"
            />
          }
        >
          <p className={styles.modalBody}>
            Rotating generates a new secret and <strong>immediately invalidates the existing one</strong>.
            Any external system still sending the old signature or token will start receiving 401 errors.
            Continue?
          </p>
        </Modal>
      )}

      {/* Webhook URL row */}
      <div className={shared.field}>
        <label className={shared.label}>Webhook URL</label>
        <div className={triggerStyles.webhookUrl}>
          <input
            className={policyId
              ? triggerStyles.webhookInput
              : `${triggerStyles.webhookInput} ${triggerStyles.webhookInputPlaceholder}`}
            type="text"
            value={displayUrl}
            readOnly
          />
          <button
            className={urlCopied
              ? `${triggerStyles.copyButton} ${triggerStyles.copyButtonDone}`
              : triggerStyles.copyButton}
            onClick={handleCopyUrl}
            disabled={!policyId}
          >
            {urlCopied ? 'Copied' : 'Copy'}
          </button>
        </div>
      </div>

      {/* Auth mode selector */}
      <div className={shared.field}>
        <label className={shared.label}>Authentication mode</label>
        <div className={styles.authGroup} role="radiogroup" aria-label="Authentication mode">
          {(['hmac', 'bearer', 'none'] as WebhookAuthMode[]).map((mode) => (
            <label key={mode} className={styles.authOption}>
              <input
                type="radio"
                name="webhook-auth"
                value={mode}
                checked={value.auth === mode}
                onChange={() => onChange({ ...value, auth: mode })}
              />
              <span className={styles.authLabel}>
                {mode === 'hmac' ? 'HMAC (recommended)' : mode === 'bearer' ? 'Bearer token' : 'None (insecure)'}
              </span>
            </label>
          ))}
        </div>
      </div>

      {/* Secret management (only when auth is not none and policyId is known) */}
      {needsSecret && (
        <div className={shared.field}>
          <label className={shared.label}>Shared secret</label>

          {/* No secret yet — offer a generate CTA */}
          {!revealed && (
            <div className={styles.secretRow}>
              <input
                className={`${styles.secretInput} ${styles.secretInputPlaceholder}`}
                type="text"
                value="••••••••••••••••••••••••"
                readOnly
              />
              <div className={styles.secretActions}>
                <button
                  className={styles.secretButton}
                  onClick={() => setRevealed(true)}
                >
                  Show
                </button>
                <button
                  className={`${styles.secretButton} ${styles.secretButtonDanger}`}
                  onClick={() => setShowRotateModal(true)}
                  disabled={rotateMutation.isPending}
                >
                  Rotate
                </button>
              </div>
            </div>
          )}

          {/* Loading state */}
          {revealed && secretLoading && (
            <div className={styles.secretRow}>
              <input
                className={`${styles.secretInput} ${styles.secretInputPlaceholder}`}
                type="text"
                value="Loading…"
                readOnly
              />
            </div>
          )}

          {/* Never rotated — show generate CTA */}
          {noSecretYet && (
            <div className={styles.ctaRow}>
              <span className={styles.ctaHint}>No secret has been set yet.</span>
              <button
                className={styles.secretButton}
                onClick={handleGenerateInitial}
                disabled={rotateMutation.isPending}
              >
                {rotateMutation.isPending ? 'Generating…' : 'Generate initial secret'}
              </button>
            </div>
          )}

          {/* Secret revealed */}
          {hasSecret && secret && (
            <div className={styles.secretRow}>
              <input
                className={styles.secretInput}
                type="text"
                value={secret}
                readOnly
                aria-label="Webhook secret"
              />
              <div className={styles.secretActions}>
                <button
                  className={secretCopied
                    ? `${styles.secretButton} ${styles.secretButtonCopied}`
                    : styles.secretButton}
                  onClick={handleCopySecret}
                >
                  {secretCopied ? 'Copied' : 'Copy'}
                </button>
                <button
                  className={styles.secretButton}
                  onClick={handleHide}
                >
                  Hide
                </button>
                <button
                  className={`${styles.secretButton} ${styles.secretButtonDanger}`}
                  onClick={() => setShowRotateModal(true)}
                  disabled={rotateMutation.isPending}
                >
                  Rotate
                </button>
              </div>
            </div>
          )}
        </div>
      )}

      {/* Warning banner when auth mode is none */}
      {value.auth === 'none' && (
        <div className={alerts.alertWarning}>
          This webhook will accept unauthenticated requests. Anyone with the URL can
          trigger this agent. Enable HMAC or Bearer authentication before exposing the
          URL externally.
        </div>
      )}

      {/* Per-mode sample snippet */}
      {snippet && (
        <div className={shared.field}>
          <label className={shared.label}>Sample request</label>
          <pre className={styles.snippetWrapper}>{snippet}</pre>
        </div>
      )}
    </>
  );
}
