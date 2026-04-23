import { ROLE_HIERARCHY, ROLE_TOOLTIP } from '@/components/UsersPage/roles'
import styles from './RoleChips.module.css'

type Role = (typeof ROLE_HIERARCHY)[number]

const ROLE_CHIP_CLASS: Record<Role, string> = {
  admin: styles.roleChipAdmin,
  operator: styles.roleChipOperator,
  approver: styles.roleChipApprover,
  auditor: styles.roleChipAuditor,
}

interface Props {
  userId: string
  roles: string[]
  onToggle: (userId: string, role: string, add: boolean) => void
  disabled: boolean
}

export function RoleChips({ userId, roles, onToggle, disabled }: Props) {
  return (
    <div className={styles.roleChips}>
      {ROLE_HIERARCHY.map((role) => {
        const active = roles.includes(role)
        return (
          <button
            key={role}
            type="button"
            className={`${styles.roleChip} ${active ? ROLE_CHIP_CLASS[role] : styles.roleChipInactive}`}
            onClick={() => onToggle(userId, role, !active)}
            disabled={disabled}
            title={`${active ? 'Remove' : 'Add'} ${role} role — ${ROLE_TOOLTIP[role]}`}
          >
            {role}
          </button>
        )
      })}
    </div>
  )
}
