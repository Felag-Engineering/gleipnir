import styles from './StatsBar.module.css';
import { fmtTok } from '../styles';

export interface Stat {
  label: string;
  value: string | number;
  variant: 'blue' | 'amber' | 'muted';
  sub: string;
  pulse?: boolean;
}

interface StatsBarProps {
  stats: Stat[];
}

const CARD_CLASS: Record<Stat['variant'], string> = {
  blue: styles.blue,
  amber: styles.amber,
  muted: '',
};

export function StatsBar({ stats }: StatsBarProps) {
  return (
    <div className={styles.grid}>
      {stats.map(s => {
        // Only apply amber highlight when pulse is active (count > 0)
        const variantClass = s.variant === 'amber' && !s.pulse ? '' : CARD_CLASS[s.variant];
        return (
          <div key={s.label} className={`${styles.card} ${variantClass}`.trim()}>
            <div className={styles.label}>{s.label}</div>
            <div className={`${styles.value}${s.pulse ? ` ${styles.pulse}` : ''}`}>
              {s.value}
            </div>
            <div className={styles.sub}>{s.sub}</div>
          </div>
        );
      })}
    </div>
  );
}

export function makeDashboardStats(
  activeRuns: number,
  pendingApprovals: number,
  policyCount: number,
  totalTokens: number,
): Stat[] {
  return [
    { label: 'Active runs',       value: activeRuns,          variant: 'blue',  sub: 'right now',             pulse: activeRuns > 0 },
    { label: 'Pending approvals', value: pendingApprovals,    variant: 'amber', sub: 'agents waiting on you', pulse: pendingApprovals > 0 },
    { label: 'Policies',          value: policyCount,         variant: 'muted', sub: 'configured' },
    { label: 'Tokens today',      value: fmtTok(totalTokens), variant: 'muted', sub: 'latest run per policy' },
  ];
}
