import { useState } from 'react'
import { Check, X } from 'lucide-react'
import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import { formatDurationMs } from '@/utils/format'
import { ApprovalActions } from './ApprovalActions'
import { FanOutResultView } from './FanOutResultView'
import { parseFanOutResult } from './fanOutResult'
import type { FanOutResult } from './fanOutResult'
import { parseToolOutput } from './toolOutput'
import type { ToolBlockData } from './types'
import styles from './ToolBlock.module.css'

interface Props {
  block: ToolBlockData
  runId: string
  runStatus: string
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

function renderRawOutput(value: unknown) {
  return typeof value === 'string'
    ? <pre className={styles.outputText}>{value}</pre>
    : <CollapsibleJSON value={value} />
}

// renderOutputBody is shared by the success and error output panes: the label
// row (with a raw/table toggle when a fan-out result was recognized) plus the
// body itself. Both panes decide their own wrapping div and any pane-specific
// classes/comments; only this label+body pairing is common between them.
function renderOutputBody(
  fanOut: FanOutResult | null,
  outputValue: unknown,
  showRaw: boolean,
  onToggleRaw: () => void,
) {
  return (
    <>
      {fanOut ? (
        <div className={styles.paneLabelRow}>
          <span className={styles.paneLabel}>Output</span>
          <button type="button" className={styles.rawToggle} onClick={onToggleRaw}>
            {showRaw ? 'Show table' : 'Show raw output'}
          </button>
        </div>
      ) : (
        <div className={styles.paneLabel}>Output</div>
      )}
      {fanOut && !showRaw ? <FanOutResultView result={fanOut} /> : renderRawOutput(outputValue)}
    </>
  )
}

export function ToolBlock({ block, runId, runStatus }: Props) {
  const status = deriveStatus(block, runStatus)
  const [showRaw, setShowRaw] = useState(false)

  const toolName = block.call?.content.tool_name ?? block.approval?.content.tool ?? 'unknown'
  const serverId = block.call?.content.server_id

  // Duration: diff between call created_at and result created_at.
  let duration: string | null = null
  if (block.call && block.result) {
    const callTime = new Date(block.call.raw.created_at).getTime()
    const resultTime = new Date(block.result.raw.created_at).getTime()
    const ms = resultTime - callTime
    if (!isNaN(ms) && ms >= 0) {
      duration = formatDurationMs(ms)
    }
  }

  // Input to display: use call.content.input if available, fall back to approval.content.input.
  const inputValue: Record<string, unknown> =
    block.call?.content.input ?? block.approval?.content.input ?? {}
  const inputEmpty = Object.keys(inputValue).length === 0

  const outputValue: unknown = block.result
    ? parseToolOutput(block.result.content.output)
    : null
  const fanOut = outputValue === null ? null : parseFanOutResult(outputValue)

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

  const panesClass = [
    styles.panes,
    hasOutputPane ? '' : styles.panesSingle,
    fanOut ? styles.panesStacked : '',
  ]
    .filter(Boolean)
    .join(' ')

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

      <div className={panesClass}>
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
          <div className={`${styles.pane} ${styles.paneOutput} ${fanOut ? styles.paneOutputStacked : ''}`}>
            {renderOutputBody(fanOut, outputValue, showRaw, () => setShowRaw((v) => !v))}
          </div>
        )}

        {status === 'error' && (
          <div className={`${styles.pane} ${styles.paneOutput} ${styles.paneError} ${fanOut ? styles.paneOutputStacked : ''}`}>
            {/* Defensive: Relay renders fan-out results as successful tool_result
                steps, so this branch should be unreachable in practice. */}
            {renderOutputBody(fanOut, outputValue, showRaw, () => setShowRaw((v) => !v))}
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
