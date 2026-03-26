import { useState } from 'react'
import { useUsers } from '@/hooks/useUsers'
import { useCreateUser } from '@/hooks/useCreateUser'
import { useUpdateUser } from '@/hooks/useUpdateUser'
import type { ApiUser } from '@/api/types'
import type { ApiError } from '@/api/fetch'
import { SkeletonBlock } from '@/components/SkeletonBlock'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/Button'
import styles from './UsersPage.module.css'

const ALL_ROLES = ['admin', 'operator', 'approver', 'auditor'] as const
type Role = (typeof ALL_ROLES)[number]

const ROLE_CHIP_CLASS: Record<Role, string> = {
  admin: styles.roleChipAdmin,
  operator: styles.roleChipOperator,
  approver: styles.roleChipApprover,
  auditor: styles.roleChipAuditor,
}

function RoleChips({
  userId,
  roles,
  onToggle,
  disabled,
}: {
  userId: string
  roles: string[]
  onToggle: (userId: string, role: string, add: boolean) => void
  disabled: boolean
}) {
  return (
    <div className={styles.roleChips}>
      {ALL_ROLES.map((role) => {
        const active = roles.includes(role)
        return (
          <button
            key={role}
            type="button"
            className={`${styles.roleChip} ${active ? ROLE_CHIP_CLASS[role] : styles.roleChipInactive}`}
            onClick={() => onToggle(userId, role, !active)}
            disabled={disabled}
            title={active ? `Remove ${role} role` : `Add ${role} role`}
          >
            {role}
          </button>
        )
      })}
    </div>
  )
}

interface CreateUserModalProps {
  onClose: () => void
  onSubmit: (username: string, password: string, roles: string[]) => void
  isPending: boolean
  error: ApiError | null
}

function CreateUserModal({ onClose, onSubmit, isPending, error }: CreateUserModalProps) {
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

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    onSubmit(username, password, Array.from(selectedRoles))
  }

  return (
    <div className={styles.modalOverlay} role="dialog" aria-modal="true" aria-labelledby="create-user-title">
      <div className={styles.modal}>
        <h2 id="create-user-title" className={styles.modalTitle}>
          Create user
        </h2>

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

          {error && <div className={styles.modalError}>{error.message}</div>}
        </form>

        <div className={styles.modalActions}>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="create-user-form"
            variant="primary"
            disabled={isPending}
          >
            {isPending ? 'Creating…' : 'Create user'}
          </Button>
        </div>
      </div>
    </div>
  )
}

function formatDate(iso: string) {
  return new Date(iso).toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })
}

export default function UsersPage() {
  const [showCreateModal, setShowCreateModal] = useState(false)

  const { data: users, status } = useUsers()
  const createMutation = useCreateUser()
  const updateMutation = useUpdateUser()

  function handleCreateSubmit(username: string, password: string, roles: string[]) {
    createMutation.mutate(
      { username, password, roles },
      {
        onSuccess: () => {
          setShowCreateModal(false)
          createMutation.reset()
        },
      },
    )
  }

  function handleCreateClose() {
    setShowCreateModal(false)
    createMutation.reset()
  }

  function handleRoleToggle(userId: string, role: string, add: boolean) {
    const user = users?.find((u) => u.id === userId)
    if (!user) return

    const newRoles = add
      ? [...user.roles, role]
      : user.roles.filter((r) => r !== role)

    updateMutation.mutate({ id: userId, roles: newRoles })
  }

  function handleStatusToggle(user: ApiUser) {
    updateMutation.mutate({ id: user.id, deactivated: !user.deactivated_at })
  }

  return (
    <div className={styles.page}>
      <PageHeader title="Users">
        <Button
          variant="primary"
          onClick={() => {
            createMutation.reset()
            setShowCreateModal(true)
          }}
        >
          Create user
        </Button>
      </PageHeader>

      {status === 'pending' && (
        <div className={styles.skeletonList}>
          <SkeletonBlock height={48} borderRadius={8} />
          <SkeletonBlock height={48} borderRadius={8} />
          <SkeletonBlock height={48} borderRadius={8} />
        </div>
      )}

      {status === 'error' && (
        <div className={styles.errorState}>Failed to load users.</div>
      )}

      {status === 'success' && users.length === 0 && (
        <div className={styles.emptyState}>
          <p className={styles.emptyHeadline}>No users</p>
          <p className={styles.emptySubtext}>Create a user to get started.</p>
        </div>
      )}

      {status === 'success' && users.length > 0 && (
        <div className={styles.tableWrapper}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>Username</th>
                <th>Roles</th>
                <th>Status</th>
                <th>Created</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {users.map((user) => (
                <tr key={user.id}>
                  <td>{user.username}</td>
                  <td>
                    <RoleChips
                      userId={user.id}
                      roles={user.roles}
                      onToggle={handleRoleToggle}
                      disabled={updateMutation.isPending}
                    />
                  </td>
                  <td>
                    <span
                      className={`${styles.statusBadge} ${user.deactivated_at ? styles.statusDeactivated : styles.statusActive}`}
                    >
                      {user.deactivated_at ? 'Deactivated' : 'Active'}
                    </span>
                  </td>
                  <td>{formatDate(user.created_at)}</td>
                  <td>
                    <button
                      type="button"
                      className={`${styles.actionBtn} ${user.deactivated_at ? '' : styles.actionBtnDestructive}`}
                      onClick={() => handleStatusToggle(user)}
                      disabled={updateMutation.isPending}
                    >
                      {user.deactivated_at ? 'Reactivate' : 'Deactivate'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {showCreateModal && (
        <CreateUserModal
          onClose={handleCreateClose}
          onSubmit={handleCreateSubmit}
          isPending={createMutation.isPending}
          error={createMutation.error as ApiError | null}
        />
      )}
    </div>
  )
}
