import { useState, useMemo } from 'react'
import { useUsers } from '@/hooks/queries/users'
import { useCreateUser } from '@/hooks/mutations/users'
import { useUpdateUser } from '@/hooks/mutations/users'
import type { ApiUser } from '@/api/types'
import type { ApiError } from '@/api/fetch'
import { QueryBoundary, SkeletonList } from '@/components/QueryBoundary'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/Button'
import { RoleChips } from '@/components/UsersPage/RoleChips'
import { CreateUserModal } from '@/components/UsersPage/CreateUserModal'
import { formatDate } from '@/utils/format'
import { usePageTitle } from '@/hooks/usePageTitle'
import styles from './UsersPage.module.css'

export default function UsersPage() {
  usePageTitle('Users')
  const [showCreateModal, setShowCreateModal] = useState(false)

  const { data: users, status } = useUsers()
  const createMutation = useCreateUser()
  const updateMutation = useUpdateUser()

  const usersById = useMemo(
    () => new Map(users?.map((u) => [u.id, u]) ?? []),
    [users],
  )

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
    const user = usersById.get(userId)
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
                <th>Roles</th>
                <th>Status</th>
                <th>Created</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(users ?? []).map((user) => (
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
      </QueryBoundary>

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
