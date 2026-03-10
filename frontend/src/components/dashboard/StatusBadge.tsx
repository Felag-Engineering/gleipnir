import type { RunStatus } from './types';
import { STATUS_CONFIG } from './types';
import { FONT } from './styles';

interface StatusBadgeProps {
  status: RunStatus;
}

export function StatusBadge({ status }: StatusBadgeProps) {
  const c = STATUS_CONFIG[status];
  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center', gap: 5,
      padding: '2px 8px', borderRadius: 4,
      background: c.bg, border: `1px solid ${c.border}`,
      fontSize: 11, fontFamily: FONT.mono, color: c.color,
      whiteSpace: 'nowrap',
    }}>
      <span style={{
        width: 6, height: 6, borderRadius: '50%',
        background: c.color, flexShrink: 0,
        animation: c.pulse ? 'gPulse 1.6s ease-in-out infinite' : 'none',
      }} />
      {c.label}
    </span>
  );
}
