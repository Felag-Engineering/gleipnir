import { Check, X } from 'lucide-react'
import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import { ApprovalActions } from './ApprovalActions'
import { parseToolOutput } from './toolOutput'
import type { ToolBlockData } from './types'
import styles from './ToolBlock.module.css'

interface Props {
  block: ToolBlockData
  runId: string
  runStatus: string
}

function fmtDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60000)}m ${Math.round((ms % 60000) / 1000)}s`
}

type BlockStatus = 'success' | 'error' | 'approval_pending' | 'denied' | 'pending'

function deriveStatus(block: ToolBlockData, runStatus: string): BlockStatus {
  if (block.approval && !block.call) {
    // No tool_call follows the approval_request. This is either a pending
    // approval (run is still waiting) or a denied/timed-out approval (run
    // moved past this point).
    return runStatus === 'waiting_for_approval' ? 'approval_pending' : 'denied'
  }
  if (block.result?.content.is_error) return 'error'
  if (block.result && !block.result.content.is_error) return 'success'
  if (block.approval && block.call && !block.result) return 'approval_pending'
  return 'pending'
}

export function ToolBlock({ block, runId, runStatus }: Props) {
  const status = deriveStatus(block, runStatus)

  const toolName = block.call?.content.tool_name ?? block.approval?.content.tool ?? 'unknown'
  const serverId = block.call?.content.server_id

  // Duration: diff between call created_at and result created_at.
  let duration: string | null = null
  if (block.call && block.result) {
    const callTime = new Date(block.call.raw.created_at).getTime()
    const resultTime = new Date(block.result.raw.created_at).getTime()
    const ms = resultTime - callTime
    if (!isNaN(ms) && ms >= 0) {
      duration = fmtDuration(ms)
    }
  }

  // Input to display: use call.content.input if available, fall back to approval.content.input.
  const inputValue: Record<string, unknown> =
    block.call?.content.input ?? block.approval?.content.input ?? {}
  const inputEmpty = Object.keys(inputValue).length === 0

  const outputValue: unknown = block.result
    ? parseToolOutput(block.result.content.output)
    : null

  const dotClass = {
    success: styles.dotSuccess,
    error: styles.dotError,
    approval_pending: styles.dotApproval,
    denied: styles.dotDenied,
    pending: styles.dotPending,
  }[status]

  const blockClass = [
    styles.block,
    status === 'error' ? styles.blockError : '',
    status === 'approval_pending' ? styles.blockApproval : '',
    status === 'denied' ? styles.blockDenied : '',
  ]
    .filter(Boolean)
    .join(' ')

  const hasOutputPane = status !== 'pending'

  return (
    <div className={blockClass}>
      <div className={styles.header}>
        <span className={`${styles.dot} ${dotClass}`} aria-hidden="true" />
        <span className={styles.toolName}>{toolName}</span>
        {serverId && <span className={styles.serverPill}>{serverId}</span>}
        {status === 'approval_pending' && (
          <span className={styles.approvalPill}>Approval required</span>
        )}
        {status === 'denied' && (
          <span className={styles.deniedPill}>Denied</span>
        )}
        <div className={styles.headerRight}>
          {status === 'success' && (
            <Check size={14} strokeWidth={2} className={styles.statusIconSuccess} aria-label="Success" />
          )}
          {status === 'error' && (
            <X size={14} strokeWidth={2} className={styles.statusIconError} aria-label="Error" />
          )}
          {duration && <span className={styles.duration}>{duration}</span>}
        </div>
      </div>

      <div className={`${styles.panes} ${hasOutputPane ? '' : styles.panesSingle}`}>
        {/* Left pane: INPUT */}
        <div className={styles.pane}>
          <div className={styles.paneLabel}>Input</div>
          {inputEmpty
            ? <span className={styles.emptyInput}>No parameters</span>
            : <CollapsibleJSON value={inputValue} />
          }
        </div>

        {/* Right pane: OUTPUT (conditional on status) */}
        {status === 'success' && (
          <div className={`${styles.pane} ${styles.paneOutput}`}>
            <div className={styles.paneLabel}>Output</div>
            {typeof outputValue === 'string'
              ? <pre className={styles.outputText}>{outputValue}</pre>
              : <CollapsibleJSON value={outputValue} />}
          </div>
        )}

        {status === 'error' && (
          <div className={`${styles.pane} ${styles.paneOutput} ${styles.paneError}`}>
            <div className={styles.paneLabel}>Output</div>
            {typeof outputValue === 'string'
              ? <pre className={styles.outputText}>{outputValue}</pre>
              : <CollapsibleJSON value={outputValue} />}
          </div>
        )}

        {status === 'approval_pending' && (
          <div className={styles.paneApproval}>
            <span className={styles.awaitingText}>Awaiting Approval</span>
            <ApprovalActions runId={runId} runStatus={runStatus} />
          </div>
        )}

        {status === 'denied' && (
          <div className={styles.paneApproval}>
            <span className={styles.deniedText}>Denied</span>
          </div>
        )}
      </div>
    </div>
  )
}
