import type { ReasoningStep } from './types';
import { FONT } from './styles';

interface ReasoningTraceProps {
  steps: ReasoningStep[];
}

export function ReasoningTrace({ steps }: ReasoningTraceProps) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
      {steps.map((step, i) => {
        const isCall = step.type === 'tool_call';
        const isResult = step.type === 'tool_result';
        return (
          <div key={i} style={{ display: 'flex', gap: 10 }}>
            {/* Vertical connector */}
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', flexShrink: 0, width: 22 }}>
              <div style={{
                width: 20, height: 20,
                borderRadius: isCall || isResult ? 4 : '50%',
                background: isCall ? 'rgba(96,165,250,0.12)' : isResult ? 'rgba(74,222,128,0.09)' : 'rgba(100,116,139,0.09)',
                border: `1px solid ${isCall ? 'rgba(96,165,250,0.25)' : isResult ? 'rgba(74,222,128,0.2)' : 'rgba(100,116,139,0.14)'}`,
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                fontSize: 10,
                color: isCall ? '#60a5fa' : isResult ? '#4ade80' : '#475569',
                flexShrink: 0, marginTop: 2,
              }}>
                {isCall ? '→' : isResult ? '←' : '·'}
              </div>
              {i < steps.length - 1 && (
                <div style={{ width: 1, flex: 1, minHeight: 6, background: 'rgba(255,255,255,0.05)', margin: '2px 0' }} />
              )}
            </div>

            {/* Step content */}
            <div style={{ flex: 1, paddingBottom: i < steps.length - 1 ? 10 : 0, paddingTop: 2, minWidth: 0 }}>
              {isCall && (
                <div>
                  <span style={{ fontFamily: FONT.mono, fontSize: 11, color: '#60a5fa' }}>{step.text}</span>
                  {step.detail && (
                    <div style={{
                      fontFamily: FONT.mono, fontSize: 10, color: '#334155', marginTop: 3,
                      padding: '4px 8px', background: 'rgba(96,165,250,0.05)',
                      borderRadius: 4, border: '1px solid rgba(96,165,250,0.1)',
                      wordBreak: 'break-all',
                    }}>
                      {step.detail}
                    </div>
                  )}
                </div>
              )}
              {isResult && (
                <div style={{
                  fontFamily: FONT.mono, fontSize: 10, color: '#334155',
                  padding: '4px 8px', background: 'rgba(74,222,128,0.04)',
                  borderRadius: 4, border: '1px solid rgba(74,222,128,0.1)',
                  wordBreak: 'break-all',
                }}>
                  {step.text}
                </div>
              )}
              {step.type === 'thought' && (
                <p style={{ fontSize: 12, color: '#64748b', lineHeight: 1.6, margin: 0 }}>{step.text}</p>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}
