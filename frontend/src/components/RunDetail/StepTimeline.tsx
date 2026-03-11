import { CapabilitySnapshotCard } from './CapabilitySnapshotCard'
import { StepCard } from './StepCard'
import type { ParsedStep, GrantedToolEntry } from './types'
import styles from './StepTimeline.module.css'

interface Props {
  steps: ParsedStep[]
  toolRoleMap: Map<string, GrantedToolEntry['Role']>
}

export function StepTimeline({ steps, toolRoleMap }: Props) {
  if (steps.length === 0) {
    return (
      <p className={styles.empty}>No steps to display.</p>
    )
  }

  return (
    <ol className={styles.timeline} aria-label="Run steps">
      {steps.map((step) => (
        <li key={step.raw.id} className={styles.item}>
          <div className={styles.connector} aria-hidden="true" />
          {step.type === 'capability_snapshot' ? (
            <CapabilitySnapshotCard content={step.content} />
          ) : (
            <StepCard step={step} toolRoleMap={toolRoleMap} />
          )}
        </li>
      ))}
    </ol>
  )
}
