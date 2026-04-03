import styles from './RoleChips.module.css'

const ALL_ROLES = ['admin', 'operator', 'approver', 'auditor'] as const
type Role = (typeof ALL_ROLES)[number]

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
