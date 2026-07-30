import { Link } from 'react-router'
import { Modal } from '@/components/Modal/Modal'
import { ModalFooter } from '@/components/ModalFooter/ModalFooter'
import type { ApiAudienceReferences } from '@/api/types'
import alertStyles from '@/styles/alerts.module.css'
import styles from './SaveGuardDialog.module.css'

interface Props {
  references: ApiAudienceReferences
  onConfirm: () => void
  onCancel: () => void
  isPending: boolean
}

export function SaveGuardDialog({ references, onConfirm, onCancel, isPending }: Props) {
  const footer = (
    <ModalFooter
      onCancel={onCancel}
      onSubmit={onConfirm}
      isLoading={isPending}
      submitLabel="Save anyway"
      loadingLabel="Saving…"
      variant="primary"
    />
  )

  return (
    <Modal title="Confirm audience change" onClose={onCancel} footer={footer}>
      <div className={styles.body}>
        {references.policies.length > 0 && (
          <div className={styles.section}>
            <p className={styles.message}>
              <strong>{references.policies.length}</strong>{' '}
              {references.policies.length === 1 ? 'policy' : 'policies'} will receive new routing:
            </p>
            <ul className={styles.policyList}>
              {references.policies.map((p) => (
                <li key={p.id}>
                  <Link to={`/agents/${p.id}`} className={styles.policyLink} target="_blank" rel="noreferrer">
                    {p.name}
                  </Link>
                </li>
              ))}
            </ul>
          </div>
        )}

        {references.in_flight_runs.length > 0 && (
          <div className={`${alertStyles.alertWarning} ${styles.inFlightWarning}`} role="alert">
            <strong>{references.in_flight_runs.length}</strong> in-flight{' '}
            {references.in_flight_runs.length === 1 ? 'run' : 'runs'} affected — change applies to
            subsequent steps only; in-flight Channel Requests already issued continue to resolve
            against the previous routing.
          </div>
        )}
      </div>
    </Modal>
  )
}
