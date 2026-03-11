import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import type { ParsedStep, GrantedToolEntry } from './types'
import styles from './StepCard.module.css'

interface Props {
  step: ParsedStep
  toolRoleMap: Map<string, GrantedToolEntry['Role']>
}

function StepIcon({ type, role }: { type: string; role?: GrantedToolEntry['Role'] }) {
  const cls = [styles.icon]
  if (type === 'thought') cls.push(styles.iconThought)
  else if (type === 'tool_call' && role === 'sensor') cls.push(styles.iconSensor)
  else if (type === 'tool_call' && role === 'actuator') cls.push(styles.iconActuator)
  else if (type === 'tool_result') cls.push(styles.iconResult)
  else if (type === 'error') cls.push(styles.iconError)
  else if (type === 'complete') cls.push(styles.iconComplete)
  else if (type === 'approval_request') cls.push(styles.iconApproval)
  else cls.push(styles.iconDefault)
  return <span className={cls.join(' ')} aria-hidden="true" />
}

export function StepCard({ step, toolRoleMap }: Props) {
  if (step.type === 'thought') {
    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="thought" />
        </div>
        <div className={styles.body}>
          <span className={styles.typeLabel}>Thought</span>
          <p className={styles.thoughtText}>{step.content.text}</p>
        </div>
      </div>
    )
  }

  if (step.type === 'tool_call') {
    const role = toolRoleMap.get(step.content.tool_name) ?? 'sensor'
    const isActuator = role === 'actuator'
    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="tool_call" role={role} />
        </div>
        <div className={styles.body}>
          <div className={styles.row}>
            <span className={`${styles.typeLabel} ${isActuator ? styles.actuatorLabel : styles.sensorLabel}`}>
              {isActuator ? 'actuator call' : 'sensor call'}
            </span>
            <code className={styles.toolName}>{step.content.tool_name}</code>
          </div>
          <CollapsibleJSON value={step.content.input} />
        </div>
      </div>
    )
  }

  if (step.type === 'tool_result') {
    const isError = step.content.is_error
    let parsed: unknown
    try {
      parsed = JSON.parse(step.content.output)
    } catch {
      parsed = step.content.output
    }
    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="tool_result" />
        </div>
        <div className={styles.body}>
          <div className={styles.row}>
            <span className={`${styles.typeLabel} ${isError ? styles.errorLabel : styles.resultLabel}`}>
              {isError ? 'result (error)' : 'result'}
            </span>
            <code className={styles.toolName}>{step.content.tool_name}</code>
          </div>
          <CollapsibleJSON value={parsed} />
        </div>
      </div>
    )
  }

  if (step.type === 'error') {
    return (
      <div className={`${styles.card} ${styles.cardError}`}>
        <div className={styles.iconCol}>
          <StepIcon type="error" />
        </div>
        <div className={styles.body}>
          <span className={`${styles.typeLabel} ${styles.errorLabel}`}>Error</span>
          <pre className={styles.errorText}>{step.content.message}</pre>
        </div>
      </div>
    )
  }

  if (step.type === 'complete') {
    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="complete" />
        </div>
        <div className={styles.body}>
          <span className={`${styles.typeLabel} ${styles.completeLabel}`}>Complete</span>
          {step.content.message && (
            <p className={styles.bodyText}>{step.content.message}</p>
          )}
        </div>
      </div>
    )
  }

  if (step.type === 'approval_request') {
    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="approval_request" />
        </div>
        <div className={styles.body}>
          <span className={`${styles.typeLabel} ${styles.approvalLabel}`}>Approval requested</span>
          <code className={styles.toolName}>{step.content.tool}</code>
        </div>
      </div>
    )
  }

  // unknown
  return (
    <div className={styles.card}>
      <div className={styles.iconCol}>
        <StepIcon type="unknown" />
      </div>
      <div className={styles.body}>
        <span className={styles.typeLabel}>{step.raw.type}</span>
        <CollapsibleJSON value={step.content} />
      </div>
    </div>
  )
}
