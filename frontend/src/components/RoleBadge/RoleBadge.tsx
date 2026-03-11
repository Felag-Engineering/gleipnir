import styles from './RoleBadge.module.css';

export type CapabilityRole = 'sensor' | 'actuator' | 'feedback';

interface RoleBadgeProps {
  role: CapabilityRole;
}

const VARIANT: Record<CapabilityRole, string> = {
  sensor:   styles.sensor,
  actuator: styles.actuator,
  feedback: styles.feedback,
};

export function RoleBadge({ role }: RoleBadgeProps) {
  return (
    <span className={`${styles.badge} ${VARIANT[role]}`}>{role}</span>
  );
}
