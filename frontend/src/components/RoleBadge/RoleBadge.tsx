import styles from './RoleBadge.module.css';

export type CapabilityRole = 'tool' | 'feedback';

interface RoleBadgeProps {
  role: CapabilityRole;
}

const VARIANT: Record<CapabilityRole, string> = {
  tool:     styles.tool,
  feedback: styles.feedback,
};

export function RoleBadge({ role }: RoleBadgeProps) {
  return (
    <span className={`${styles.badge} ${VARIANT[role]}`}>{role}</span>
  );
}
