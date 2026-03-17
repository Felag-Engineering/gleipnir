import type { ReasoningStep } from './types';
import styles from './ReasoningTrace.module.css';

interface ReasoningTraceProps {
  steps: ReasoningStep[];
}

export function ReasoningTrace({ steps }: ReasoningTraceProps) {
  return (
    <div className={styles.container}>
      {steps.map((step, i) => {
        const isCall = step.type === 'tool_call';
        const isResult = step.type === 'tool_result';
        const iconVariant = isCall ? styles.iconCall : isResult ? styles.iconResult : styles.iconThought;
        return (
          <div key={i} className={styles.step}>
            {/* Vertical connector */}
            <div className={styles.connector}>
              <div className={`${styles.icon} ${iconVariant}`} aria-hidden="true">
                {isCall ? '→' : isResult ? '←' : '·'}
              </div>
              {i < steps.length - 1 && (
                <div className={styles.line} />
              )}
            </div>

            {/* Step content */}
            <div className={`${styles.content} ${i < steps.length - 1 ? styles.contentSpaced : ''}`}>
              <span className={styles.srOnly}>
                {isCall ? 'Tool call:' : isResult ? 'Tool result:' : 'Thought:'}
              </span>
              {isCall && (
                <div>
                  <span className={styles.callText}>{step.text}</span>
                  {step.detail && (
                    <div className={styles.callDetail}>
                      {step.detail}
                    </div>
                  )}
                </div>
              )}
              {isResult && (
                <div className={styles.resultBlock}>
                  {step.text}
                </div>
              )}
              {step.type === 'thought' && (
                <p className={styles.thought}>{step.text}</p>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}
