import { useState } from 'react'
import { useUsers } from '@/hooks/queries/users'
import { useCreateUser } from '@/hooks/mutations/users'
import { useUpdateUser } from '@/hooks/mutations/users'
import type { ApiUser } from '@/api/types'
import type { ApiError } from '@/api/fetch'
import { QueryBoundary, SkeletonList } from '@/components/QueryBoundary'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/Button'
import { CreateUserModal } from '@/components/UsersPage/CreateUserModal'
import { highestRoleFromArray } from '@/components/UsersPage/roles'
import { formatDate } from '@/utils/format'
import { usePageTitle } from '@/hooks/usePageTitle'
import styles from './UsersPage.module.css'

const ROLE_BADGE_CLASS: Record<string, string> = {
  admin: styles.roleBadgeAdmin,
  operator: styles.roleBadgeOperator,
  approver: styles.roleBadgeApprover,
  auditor: styles.roleBadgeAuditor,
}

export default function UsersPage() {
  usePageTitle('Users')
  const [showCreateModal, setShowCreateModal] = useState(false)
  const [editingUser, setEditingUser] = useState<ApiUser | null>(null)

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

  function handleEditSubmit(roles: string[]) {
    if (!editingUser) return
    updateMutation.mutate(
      { id: editingUser.id, roles },
      {
        onSuccess: () => {
          setEditingUser(null)
          updateMutation.reset()
        },
      },
    )
  }

  function handleEditClose() {
    setEditingUser(null)
    updateMutation.reset()
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

      <QueryBoundary
        status={status}
        isEmpty={(users ?? []).length === 0}
        errorMessage="Failed to load users."
        skeleton={<SkeletonList count={3} height={48} gap={12} borderRadius={8} />}
        emptyState={
          <div className={styles.emptyState}>
            <p className={styles.emptyHeadline}>No users</p>
            <p className={styles.emptySubtext}>Create a user to get started.</p>
          </div>
        }
      >
        <div className={styles.tableWrapper}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>Username</th>
                <th>Role</th>
                <th>Status</th>
                <th>Created</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(users ?? []).map((user) => {
                const topRole = highestRoleFromArray(user.roles)
                return (
                  <tr key={user.id}>
                    <td>{user.username}</td>
                    <td>
                      {topRole ? (
                        <span className={`${styles.roleBadge} ${ROLE_BADGE_CLASS[topRole] ?? ''}`}>
                          {topRole}
                        </span>
                      ) : (
                        <span className={`${styles.roleBadge} ${styles.roleBadgeNone}`}>—</span>
                      )}
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
                      <div className={styles.actionsCell}>
                        <button
                          type="button"
                          className={styles.actionBtn}
                          onClick={() => {
                            updateMutation.reset()
                            setEditingUser(user)
                          }}
                          disabled={updateMutation.isPending}
                        >
                          Edit
                        </button>
                        <button
                          type="button"
                          className={`${styles.actionBtn} ${user.deactivated_at ? '' : styles.actionBtnDestructive}`}
                          onClick={() => handleStatusToggle(user)}
                          disabled={updateMutation.isPending}
                        >
                          {user.deactivated_at ? 'Reactivate' : 'Deactivate'}
                        </button>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </QueryBoundary>

      {showCreateModal && (
        <CreateUserModal
          mode="create"
          onClose={handleCreateClose}
          onSubmit={handleCreateSubmit}
          isPending={createMutation.isPending}
          error={createMutation.error as ApiError | null}
        />
      )}

      {editingUser && (
        <CreateUserModal
          mode="edit"
          initialRole={highestRoleFromArray(editingUser.roles)}
          onClose={handleEditClose}
          onSubmit={handleEditSubmit}
          isPending={updateMutation.isPending}
          error={updateMutation.error as ApiError | null}
        />
      )}
    </div>
  )
}
