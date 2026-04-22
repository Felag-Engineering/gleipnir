import { useState, type FormEvent } from 'react'
import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import type { ApiError } from '@/api/fetch'
import { ErrorBanner } from '@/components/form/ErrorBanner'
import styles from './CreateUserModal.module.css'

const ALL_ROLES = ['admin', 'operator', 'approver', 'auditor'] as const

interface Props {
  onClose: () => void
  onSubmit: (username: string, password: string, roles: string[]) => void
  isPending: boolean
  error: ApiError | null
}

export function CreateUserModal({ onClose, onSubmit, isPending, error }: Props) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [selectedRoles, setSelectedRoles] = useState<Set<string>>(new Set())

  function toggleRole(role: string) {
    setSelectedRoles((prev) => {
      const next = new Set(prev)
      if (next.has(role)) {
        next.delete(role)
      } else {
        next.add(role)
      }
      return next
    })
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    onSubmit(username, password, Array.from(selectedRoles))
  }

  const footer = (
    <ModalFooter
      onCancel={onClose}
      formId="create-user-form"
      isLoading={isPending}
      submitLabel="Create user"
      loadingLabel="Creating…"
    />
  )

  return (
    <Modal title="Create user" onClose={onClose} footer={footer}>
      <form id="create-user-form" className={styles.form} onSubmit={handleSubmit}>
        <div className={styles.fieldGroup}>
          <label htmlFor="new-username" className={styles.label}>
            Username
          </label>
          <input
            id="new-username"
            type="text"
            className={styles.input}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="off"
            required
          />
        </div>

        <div className={styles.fieldGroup}>
          <label htmlFor="new-password" className={styles.label}>
            Password
          </label>
          <input
            id="new-password"
            type="password"
            className={styles.input}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            required
          />
        </div>

        <div className={styles.fieldGroup}>
          <span className={styles.label}>Roles</span>
          <div className={styles.checkboxGroup}>
            {ALL_ROLES.map((role) => (
              <label key={role} className={styles.checkboxLabel}>
                <input
                  type="checkbox"
                  checked={selectedRoles.has(role)}
                  onChange={() => toggleRole(role)}
                />
                {role}
              </label>
            ))}
          </div>
        </div>

        <ErrorBanner
          issues={
            error
              ? (error.issues ?? (error.detail ? [{ message: error.detail }] : [{ message: error.message }]))
              : []
          }
        />
      </form>
    </Modal>
  )
}
