import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import { CapabilitySnapshotCard } from './CapabilitySnapshotCard'
import { CompleteBlock } from './CompleteBlock'
import { ErrorBlock } from './ErrorBlock'
import { FeedbackBlock } from './FeedbackBlock'
import { ThinkingBlock } from './ThinkingBlock'
import { ThoughtBlock } from './ThoughtBlock'
import { ToolBlock } from './ToolBlock'
import { TriggerBlock } from './TriggerBlock'
import { isToolBlock } from './types'
import type { ApiRunResponder } from '@/api/types'
import type { NodeHostnameIndex } from './nodeHostnames'
import type { ParsedStep, ToolBlockData } from './types'
import styles from './StepTimeline.module.css'

interface Props {
  items: (ParsedStep | ToolBlockData)[]
  // The run's capability snapshot step (ADR-018), rendered as the first entry
  // of the timeline whatever the active filter: it is the frame every other
  // step happened inside. It is passed separately from `items` because filter
  // counts and pagination exclude it.
  snapshot?: ParsedStep | null
  systemPrompt?: string | null
  runId: string
  runStatus: string
  triggerType?: string
  triggerPayload?: string | null
  // durationMs is optional — Storybook stories and test contexts may omit it.
  // CompleteBlock renders without a duration when this is undefined or null.
  durationMs?: number | null
  // Built once per step list by useRunTimeline from the WHOLE run, not just
  // the visible items, so a filter or page boundary cannot hide the list_nodes
  // result a fan-out table takes its hostnames from.
  nodeHostnames?: NodeHostnameIndex
  // request id -> who settled it, for the "Approved by" / "Answered by" labels.
  responders?: ReadonlyMap<string, ApiRunResponder>
}

export function StepTimeline({ items, snapshot, systemPrompt, runId, runStatus, triggerType, triggerPayload, durationMs, nodeHostnames, responders }: Props) {
  const snapshotContent = snapshot?.type === 'capability_snapshot' ? snapshot.content : null

  if (items.length === 0 && !triggerType && !snapshotContent) {
    return (
      <p className={styles.empty}>No steps to display.</p>
    )
  }

  const finalThoughtIndex = findFinalThoughtIndex(items)

  return (
    <ol className={styles.timeline} aria-label="Run steps">
      {snapshotContent && (
        <li className={styles.item}>
          <CapabilitySnapshotCard content={snapshotContent} systemPrompt={systemPrompt} />
        </li>
      )}
      {triggerType && (
        <li className={styles.item}>
          <TriggerBlock triggerType={triggerType} payload={triggerPayload ?? null} />
        </li>
      )}
      {items.map((item, idx) => {
        const key = isToolBlock(item)
          ? (item.approval?.raw.id ?? item.call?.raw.id ?? String(idx))
          : item.raw.id

        return (
          <li key={key} className={styles.item}>
            {renderBlock(item, {
              runId,
              runStatus,
              systemPrompt,
              durationMs,
              nodeHostnames,
              responders,
              isFinalThought: idx === finalThoughtIndex,
            })}
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
  durationMs?: number | null
  nodeHostnames?: NodeHostnameIndex
  responders?: ReadonlyMap<string, ApiRunResponder>
  isFinalThought: boolean
}

// findFinalThoughtIndex returns the index of the run's answer: the last
// thought, provided nothing but terminal steps follows it. A thought followed
// by another tool call is working-out, not an answer, so it stays collapsed.
// Returns -1 when there is no such thought (for example, mid-run).
function findFinalThoughtIndex(items: (ParsedStep | ToolBlockData)[]): number {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i]
    if (isToolBlock(item)) return -1
    if (item.type === 'thought') return i
    if (item.type !== 'complete' && item.type !== 'error') return -1
  }
  return -1
}

// renderBlock selects the appropriate block component for each item type.
// Orphan tool_result and unknown steps fall back to a plain CollapsibleJSON display
// rather than crashing — these are rare edge cases (out-of-order delivery, unknown
// future step types) that should degrade gracefully.
function renderBlock(item: ParsedStep | ToolBlockData, ctx: RenderContext) {
  if (isToolBlock(item)) {
    return <ToolBlock block={item} runId={ctx.runId} runStatus={ctx.runStatus} nodeHostnames={ctx.nodeHostnames} responders={ctx.responders} />
  }

  switch (item.type) {
    case 'capability_snapshot':
      return <CapabilitySnapshotCard content={item.content} systemPrompt={ctx.systemPrompt} />
    case 'thinking':
      return <ThinkingBlock step={item} />
    case 'thought':
      return <ThoughtBlock step={item} defaultExpanded={ctx.isFinalThought} />
    case 'error':
      return <ErrorBlock step={item} />
    case 'complete':
      return <CompleteBlock step={item} durationMs={ctx.durationMs ?? null} />
    case 'feedback_request':
      return <FeedbackBlock step={item} runId={ctx.runId} runStatus={ctx.runStatus} responders={ctx.responders} />
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
