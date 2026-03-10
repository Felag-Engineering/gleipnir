import type { TriggerType } from './types';
import { FONT, TRIGGER_COLORS } from './styles';

interface TriggerChipProps {
  type: TriggerType;
}

export function TriggerChip({ type }: TriggerChipProps) {
  const col = TRIGGER_COLORS[type] || '#94a3b8';
  return (
    <span style={{
      fontSize: 10, fontFamily: FONT.mono, color: col,
      background: 'rgba(255,255,255,0.04)',
      border: '1px solid rgba(255,255,255,0.08)',
      padding: '1px 6px', borderRadius: 3, flexShrink: 0,
    }}>
      {type}
    </span>
  );
}
