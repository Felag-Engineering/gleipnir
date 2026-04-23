import { ROLE_CAPABILITIES, ROLE_TOOLTIP, type Role } from '@/components/UsersPage/roles'
import styles from './PermissionsPanel.module.css'

interface Props {
  role: Role | null
}

// key={role} on this component causes React to unmount/remount on role change,
// giving a clean visual refresh. This means CSS transitions won't fire between
// role switches — acceptable for v1 (see plan for trade-off note).
export function PermissionsPanel({ role }: Props) {
  if (role === null) {
    return (
      <div className={styles.placeholder}>Select a role to see its permissions.</div>
    )
  }

  return (
    <div className={styles.panel}>
      <p className={styles.heading}>{role}</p>
      <p className={styles.lead}>{ROLE_TOOLTIP[role]}</p>
      <ul className={styles.list}>
        {ROLE_CAPABILITIES[role].map((capability) => (
          <li key={capability} className={styles.item}>
            {capability}
          </li>
        ))}
      </ul>
    </div>
  )
}
