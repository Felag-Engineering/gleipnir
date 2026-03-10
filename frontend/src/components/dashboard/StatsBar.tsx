import { FONT, fmtTok } from './styles';

interface Stat {
  label: string;
  value: string | number;
  color: string;
  sub: string;
  pulse?: boolean;
}

interface StatsBarProps {
  stats: Stat[];
}

export function StatsBar({ stats }: StatsBarProps) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: `repeat(${stats.length}, 1fr)`, gap: 10 }}>
      {stats.map(s => (
        <div key={s.label} style={{
          background: '#131720', border: '1px solid #1e2330',
          borderRadius: 8, padding: '13px 16px',
        }}>
          <div style={{ fontSize: 10, color: '#2d3748', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 7 }}>
            {s.label}
          </div>
          <div style={{
            fontSize: 25, fontFamily: FONT.mono, fontWeight: 500,
            color: s.color, lineHeight: 1,
            animation: s.pulse ? 'gPulse 1.6s ease-in-out infinite' : 'none',
          }}>
            {s.value}
          </div>
          <div style={{ fontSize: 10, color: '#1e2a3a', marginTop: 5 }}>{s.sub}</div>
        </div>
      ))}
    </div>
  );
}

// Convenience factory for the default dashboard stats
export function makeDashboardStats(activeRuns: number, pendingApprovals: number, folderCount: number, totalTokens: number): Stat[] {
  return [
    { label: 'Active runs',       value: activeRuns,        color: '#60a5fa', sub: 'right now',             pulse: activeRuns > 0 },
    { label: 'Pending approvals', value: pendingApprovals,  color: '#f59e0b', sub: 'agents waiting on you', pulse: pendingApprovals > 0 },
    { label: 'Folders',           value: folderCount,       color: '#94a3b8', sub: 'configured' },
    { label: 'Tokens today',      value: fmtTok(totalTokens), color: '#94a3b8', sub: 'latest run per policy' },
  ];
}
