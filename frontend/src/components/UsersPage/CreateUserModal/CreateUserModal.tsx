import { useState, type FormEvent } from 'react'
import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import type { ApiError } from '@/api/fetch'
import { ErrorBanner, type BannerIssue } from '@/components/form/ErrorBanner'
import { FieldError } from '@/components/form/FieldError'
import {
  ROLE_HIERARCHY,
  ROLE_TOOLTIP,
  highestSelectedRole,
  rolesWhenChecked,
  rolesWhenUnchecked,
  type Role,
} from '@/components/UsersPage/roles'
import { PermissionsPanel } from '@/components/UsersPage/PermissionsPanel'
import styles from './CreateUserModal.module.css'

interface Props {
  onClose: () => void
  onSubmit: (username: string, password: string, roles: string[]) => void
  isPending: boolean
  error: ApiError | null
}

export function CreateUserModal({ onClose, onSubmit, isPending, error }: Props) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [selectedRoles, setSelectedRoles] = useState<Set<Role>>(new Set())
  const [clientIssues, setClientIssues] = useState<BannerIssue[]>([])
  const [fieldErrors, setFieldErrors] = useState<{ username?: string; password?: string }>({})

  function handleRoleToggle(role: Role, checked: boolean) {
    setSelectedRoles((prev) =>
      checked ? rolesWhenChecked(role, prev) : rolesWhenUnchecked(role, prev),
    )
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()

    // Reset stale client errors before re-validating.
    setClientIssues([])
    setFieldErrors({})

    const issues: BannerIssue[] = []
    const errors: { username?: string; password?: string } = {}

    if (username.trim() === '') {
      issues.push({ field: 'username', message: 'Username is required' })
      errors.username = 'Username is required'
    }
    if (password === '') {
      issues.push({ field: 'password', message: 'Password is required' })
      errors.password = 'Password is required'
    }

    if (issues.length > 0) {
      setClientIssues(issues)
      setFieldErrors(errors)
      return
    }

    onSubmit(username, password, Array.from(selectedRoles))
  }

  const bannerIssues: BannerIssue[] =
    clientIssues.length > 0
      ? clientIssues
      : error
        ? (error.issues ?? (error.detail ? [{ message: error.detail }] : [{ message: error.message }]))
        : []

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
      <form
        id="create-user-form"
        className={styles.form}
        onSubmit={handleSubmit}
        onInvalid={(e) => e.preventDefault()}
      >
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
            aria-invalid={Boolean(fieldErrors.username)}
            aria-describedby="new-username-error"
          />
          <FieldError id="new-username-error" messages={fieldErrors.username} />
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
            aria-invalid={Boolean(fieldErrors.password)}
            aria-describedby="new-password-error"
          />
          <FieldError id="new-password-error" messages={fieldErrors.password} />
        </div>

        <div className={styles.fieldGroup}>
          <span className={styles.label}>Roles</span>
          <div className={styles.rolesLayout}>
            <div className={styles.checkboxGroup}>
              {ROLE_HIERARCHY.map((role) => (
                <label key={role} className={styles.checkboxLabel} title={ROLE_TOOLTIP[role]}>
                  <input
                    type="checkbox"
                    checked={selectedRoles.has(role)}
                    onChange={(e) => handleRoleToggle(role, e.target.checked)}
                  />
                  {role}
                </label>
              ))}
            </div>
            <PermissionsPanel key={highestSelectedRole(selectedRoles)} role={highestSelectedRole(selectedRoles)} />
          </div>
        </div>

        <ErrorBanner issues={bannerIssues} />
      </form>
    </Modal>
  )
}
