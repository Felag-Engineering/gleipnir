import { useState, type FormEvent } from 'react'
import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import type { ApiError } from '@/api/fetch'
import { ErrorBanner, type BannerIssue } from '@/components/form/ErrorBanner'
import { FieldError } from '@/components/form/FieldError'
import {
  ROLE_HIERARCHY,
  ROLE_TOOLTIP,
  rolesForHighest,
  type Role,
} from '@/components/UsersPage/roles'
import { PermissionsPanel } from '@/components/UsersPage/PermissionsPanel'
import styles from './CreateUserModal.module.css'

type BaseProps = {
  onClose: () => void
  isPending: boolean
  error: ApiError | null
}

type CreateProps = BaseProps & {
  mode: 'create'
  onSubmit: (username: string, password: string, roles: string[]) => void
}

type EditProps = BaseProps & {
  mode: 'edit'
  initialRole: Role | null
  onSubmit: (roles: string[]) => void
}

type Props = CreateProps | EditProps

export function CreateUserModal(props: Props) {
  const { onClose, isPending, error } = props

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [selectedRole, setSelectedRole] = useState<Role | null>(
    props.mode === 'edit' ? props.initialRole : null,
  )
  const [clientIssues, setClientIssues] = useState<BannerIssue[]>([])
  const [fieldErrors, setFieldErrors] = useState<{ username?: string; password?: string }>({})

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setClientIssues([])
    setFieldErrors({})

    if (props.mode === 'create') {
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

      props.onSubmit(username, password, selectedRole ? rolesForHighest(selectedRole) : [])
    } else {
      props.onSubmit(selectedRole ? rolesForHighest(selectedRole) : [])
    }
  }

  const bannerIssues: BannerIssue[] =
    clientIssues.length > 0
      ? clientIssues
      : error
        ? (error.issues ?? (error.detail ? [{ message: error.detail }] : [{ message: error.message }]))
        : []

  const isCreate = props.mode === 'create'

  const footer = (
    <ModalFooter
      onCancel={onClose}
      formId="user-modal-form"
      isLoading={isPending}
      submitLabel={isCreate ? 'Create user' : 'Save changes'}
      loadingLabel={isCreate ? 'Creating…' : 'Saving…'}
    />
  )

  return (
    <Modal title={isCreate ? 'Create user' : 'Edit user'} onClose={onClose} footer={footer}>
      <form
        id="user-modal-form"
        className={styles.form}
        onSubmit={handleSubmit}
        onInvalid={(e) => e.preventDefault()}
      >
        {isCreate && (
          <>
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
          </>
        )}

        <div className={styles.fieldGroup}>
          <span className={styles.label}>Role</span>
          <div className={styles.rolesLayout}>
            <div className={styles.radioGroup}>
              {ROLE_HIERARCHY.map((role) => (
                <label key={role} className={styles.radioLabel} title={ROLE_TOOLTIP[role]}>
                  <input
                    type="radio"
                    name="role"
                    checked={selectedRole === role}
                    onChange={() => setSelectedRole(role)}
                  />
                  {role}
                </label>
              ))}
            </div>
            <PermissionsPanel key={selectedRole} role={selectedRole} />
          </div>
        </div>

        <ErrorBanner issues={bannerIssues} />
      </form>
    </Modal>
  )
}
