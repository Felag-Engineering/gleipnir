import { CapabilitySnapshotCard } from './CapabilitySnapshotCard'
import { StepCard } from './StepCard'
import { ToolBlock } from './ToolBlock'
import { pairToolBlocks, isToolBlock } from './types'
import type { ParsedStep, GrantedToolEntry } from './types'
import styles from './StepTimeline.module.css'

interface Props {
  steps: ParsedStep[]
  toolRoleMap: Map<string, GrantedToolEntry['Role']>
  systemPrompt?: string | null
  runId: string
  runStatus: string
}

export function StepTimeline({ steps, toolRoleMap, systemPrompt, runId, runStatus }: Props) {
  if (steps.length === 0) {
    return (
      <p className={styles.empty}>No steps to display.</p>
    )
  }

  const pairedSteps = pairToolBlocks(steps)

  return (
    <ol className={styles.timeline} aria-label="Run steps">
      {pairedSteps.map((item, idx) => {
        const key = isToolBlock(item)
          ? (item.approval?.raw.id ?? item.call?.raw.id ?? String(idx))
          : item.raw.id

        return (
          <li key={key} className={styles.item}>
            <div className={styles.connector} aria-hidden="true" />
            {isToolBlock(item) ? (
              <ToolBlock block={item} runId={runId} runStatus={runStatus} />
            ) : item.type === 'capability_snapshot' ? (
              <CapabilitySnapshotCard content={item.content} systemPrompt={systemPrompt} />
            ) : (
              <StepCard step={item} toolRoleMap={toolRoleMap} runId={runId} runStatus={runStatus} />
            )}
          </li>
        )
      })}
    </ol>
  )
}
