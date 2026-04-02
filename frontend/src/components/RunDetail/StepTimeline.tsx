import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import { CapabilitySnapshotCard } from './CapabilitySnapshotCard'
import { CompleteBlock } from './CompleteBlock'
import { ErrorBlock } from './ErrorBlock'
import { FeedbackBlock } from './FeedbackBlock'
import { ThinkingBlock } from './ThinkingBlock'
import { ThoughtBlock } from './ThoughtBlock'
import { ToolBlock } from './ToolBlock'
import { isToolBlock } from './types'
import type { ParsedStep, ToolBlockData } from './types'
import styles from './StepTimeline.module.css'

interface Props {
  items: (ParsedStep | ToolBlockData)[]
  systemPrompt?: string | null
  runId: string
  runStatus: string
  // durationSeconds is optional — Storybook stories and test contexts may omit it.
  // CompleteBlock renders without a duration when this is undefined or null.
  durationSeconds?: number | null
}

export function StepTimeline({ items, systemPrompt, runId, runStatus, durationSeconds }: Props) {
  if (items.length === 0) {
    return (
      <p className={styles.empty}>No steps to display.</p>
    )
  }

  return (
    <ol className={styles.timeline} aria-label="Run steps">
      {items.map((item, idx) => {
        const key = isToolBlock(item)
          ? (item.approval?.raw.id ?? item.call?.raw.id ?? String(idx))
          : item.raw.id

        return (
          <li key={key} className={styles.item}>
            {renderBlock(item, { runId, runStatus, systemPrompt, durationSeconds })}
          </li>
        )
      })}
    </ol>
  )
}

interface RenderContext {
  runId: string
  runStatus: string
  systemPrompt?: string | null
  durationSeconds?: number | null
}

// renderBlock selects the appropriate block component for each item type.
// Orphan tool_result and unknown steps fall back to a plain CollapsibleJSON display
// rather than crashing — these are rare edge cases (out-of-order delivery, unknown
// future step types) that should degrade gracefully.
function renderBlock(item: ParsedStep | ToolBlockData, ctx: RenderContext) {
  if (isToolBlock(item)) {
    return <ToolBlock block={item} runId={ctx.runId} runStatus={ctx.runStatus} />
  }

  switch (item.type) {
    case 'capability_snapshot':
      return <CapabilitySnapshotCard content={item.content} systemPrompt={ctx.systemPrompt} />
    case 'thinking':
      return <ThinkingBlock step={item} />
    case 'thought':
      return <ThoughtBlock step={item} />
    case 'error':
      return <ErrorBlock step={item} />
    case 'complete':
      return <CompleteBlock step={item} durationSeconds={ctx.durationSeconds ?? null} />
    case 'feedback_request':
      return <FeedbackBlock step={item} runId={ctx.runId} runStatus={ctx.runStatus} />
    case 'feedback_response':
      return <FeedbackBlock step={item} runId={ctx.runId} runStatus={ctx.runStatus} />
    default:
      // Orphan tool_result and unknown step types are only visible under the 'all' filter.
      // Wrap in .fallback for visual consistency with other block components.
      return (
        <div className={styles.fallback}>
          <CollapsibleJSON value={item.content} />
        </div>
      )
  }
}
