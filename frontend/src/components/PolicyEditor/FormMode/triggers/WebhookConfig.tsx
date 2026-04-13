import { useEffect, useState } from 'react';
import shared from '../FormSections.module.css';
import styles from '../TriggerSection.module.css';

export interface WebhookConfigProps {
  policyId?: string;
}

export function WebhookConfig({ policyId }: WebhookConfigProps) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  async function handleCopy() {
    if (!policyId) return;
    const url = `/api/v1/webhooks/${policyId}`;
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
    } catch {
      // clipboard API unavailable or permission denied — silently fail
    }
  }

  const url = policyId ? `POST /api/v1/webhooks/${policyId}` : undefined;
  const displayValue = url ?? 'POST /api/v1/webhooks/<agent-id>';

  return (
    <div className={shared.field}>
      <label className={shared.label}>Webhook URL</label>
      <div className={styles.webhookUrl}>
        <input
          className={policyId ? styles.webhookInput : `${styles.webhookInput} ${styles.webhookInputPlaceholder}`}
          type="text"
          value={displayValue}
          readOnly
        />
        <button
          className={copied ? `${styles.copyButton} ${styles.copyButtonDone}` : styles.copyButton}
          onClick={handleCopy}
          disabled={!policyId}
        >
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
    </div>
  );
}
