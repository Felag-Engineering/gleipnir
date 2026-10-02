import { Fragment, useId, useState } from 'react'
import { Check, ChevronDown, ChevronRight, HelpCircle, MinusCircle, ShieldCheck, X } from 'lucide-react'
import { formatDurationMs } from '@/utils/format'
import { groupByOutcome, outcomeLabel, outcomeTone, OUTCOME_ORDER } from './fanOutResult'
import type { FanOutAnswerRefusal, FanOutDecision, FanOutResult, FanOutRow, OutcomeTone } from './fanOutResult'
import styles from './FanOutResultView.module.css'

interface Props {
  result: FanOutResult
  // node_id → hostname, learned from a list_nodes result on the same server
  // earlier in the run (nodeHostnames.ts). Optional: without it, or for an id
  // it does not hold, a row shows its node_id exactly as Relay sent it.
  hostnames?: ReadonlyMap<string, string>
}

const TONE_ICON: Record<OutcomeTone, typeof Check> = {
  ok: Check,
  policy: ShieldCheck,
  failed: X,
  notRun: MinusCircle,
  unknown: HelpCircle,
}

const TONE_CLASS: Record<OutcomeTone, string> = {
  ok: styles.toneOk,
  policy: styles.tonePolicy,
  failed: styles.toneFailed,
  notRun: styles.toneNotRun,
  unknown: styles.toneUnknown,
}

interface OutcomeCount {
  outcome: string
  count: number
}

// Chips run success-first, then the remaining groups in OUTCOME_ORDER, so the
// summary line reads "21 success · 2 denied by policy · 1 unreachable".
function summaryOrder(counts: OutcomeCount[]): OutcomeCount[] {
  const byOutcome = new Map(counts.map((c) => [c.outcome, c]))
  const order = ['success', ...OUTCOME_ORDER.filter((o) => o !== 'success')]
  const known = order.filter((o) => byOutcome.has(o)).map((o) => byOutcome.get(o)!)
  const unknown = counts.filter((c) => !(order as string[]).includes(c.outcome))
  return [...known, ...unknown]
}

// DECISION_TONE colours Relay's request state the way the outcome chips colour
// a Node: approved reads as the go-ahead, anything else as "did not proceed".
// An unrecognized state is shown verbatim in the neutral tone.
function decisionTone(state: string): OutcomeTone {
  switch (state) {
    case 'approved':
      return 'ok'
    case 'denied':
      return 'failed'
    case 'pending':
    case 'expired':
      return 'notRun'
    default:
      return 'unknown'
  }
}

// DecisionLine renders the decision block Relay returns beside the Job an
// in-band approval released. Every field is shown: on_behalf_of is the name
// Gleipnir asserted to Relay, and Relay records it as an assertion, never as a
// verified identity, so the label says so. next_step is Relay's instruction to
// the AI-agent, labelled as such rather than presented as advice to the reader.
function DecisionLine({ decision }: { decision: FanOutDecision }) {
  const tone = decisionTone(decision.state)
  // A denial is the human gate working, but nothing ran: the line is marked
  // as a refusal so it cannot be read as the go-ahead at a glance.
  const refusal = tone === 'failed'
  return (
    <div className={`${styles.decision} ${refusal ? styles.decisionRefusal : ''}`}>
      <div className={styles.decisionRow}>
        <span className={styles.decisionLabel}>Relay decision</span>
        <span className={`${styles.chip} ${TONE_CLASS[tone]}`} data-tone={tone}>
          {decision.state}
        </span>
        {decision.progress && <span className={styles.decisionFact}>{decision.progress}</span>}
        {decision.channel && <span className={styles.decisionFact}>{decision.channel}</span>}
        {decision.on_behalf_of && (
          <span className={styles.decisionFact}>
            on behalf of <strong>{decision.on_behalf_of}</strong> (asserted)
          </span>
        )}
        <span className={styles.jobId}>request {decision.request_id}</span>
      </div>
      {decision.next_step && (
        <p className={styles.decisionNote}>Relay to the agent: {decision.next_step}</p>
      )}
    </div>
  )
}

// AnswerRefusedLine renders Relay's answer_refused block: the operator's
// answer reached Relay but Relay did not take it as a decision. reason and
// next_step are Relay's own words and are shown as plain text.
function AnswerRefusedLine({ refusal }: { refusal: FanOutAnswerRefusal }) {
  return (
    <div className={`${styles.decision} ${styles.decisionRefusal}`}>
      <div className={styles.decisionRow}>
        <span className={styles.decisionLabel}>Relay decision</span>
        <span className={`${styles.chip} ${TONE_CLASS.failed}`} data-tone="failed">
          answer refused
        </span>
        {refusal.state && <span className={styles.decisionFact}>request {refusal.state}</span>}
        {refusal.job_id && <span className={styles.decisionFact}>released job {refusal.job_id} earlier</span>}
        {refusal.request_id && <span className={styles.jobId}>request {refusal.request_id}</span>}
      </div>
      <p className={styles.decisionReason}>{refusal.reason}</p>
      {refusal.next_step && (
        <p className={styles.decisionNote}>Relay to the agent: {refusal.next_step}</p>
      )}
    </div>
  )
}

// NodeCell labels a row by hostname when one is known, keeping the node_id
// in full beneath it (and as the cell's tooltip): the id is what Relay's audit
// trail and every other Relay surface use, so it must stay readable and
// copyable. The hostname is a plain text node, never markup.
function NodeCell({ nodeId, hostname }: { nodeId: string; hostname: string | undefined }) {
  if (!hostname) {
    return <td className={styles.node}>{nodeId}</td>
  }
  return (
    <td className={styles.node} title={nodeId}>
      <span className={styles.hostname}>{hostname}</span>
      <span className={styles.nodeIdSecondary}>{nodeId}</span>
    </td>
  )
}

// previewText picks the first non-empty of stdout/stderr/refusal_explanation and
// returns only its first line — the rest is reachable only in the expanded
// detail. A long first line is still cut visually by CSS text-overflow ellipsis.
function previewText(row: FanOutRow): string | null {
  const text = row.stdout || row.stderr || row.refusal_explanation
  return text ? text.split('\n')[0] : null
}

export function FanOutResultView({ result, hostnames }: Props) {
  const [expandedRows, setExpandedRows] = useState<Set<number>>(new Set())
  const idPrefix = useId()

  const rows = result.results
  const n = rows.length
  const groups = groupByOutcome(rows)
  const counts = summaryOrder(groups.map((g) => ({ outcome: g.outcome, count: g.rows.length })))

  function toggleRow(index: number) {
    setExpandedRows((prev) => {
      const next = new Set(prev)
      if (next.has(index)) {
        next.delete(index)
      } else {
        next.add(index)
      }
      return next
    })
  }

  const decision = result.decision ? <DecisionLine decision={result.decision} /> : null

  // No job_id means Relay dispatched nothing (its own contract), so there is
  // no Job to summarize and no table to draw — only what Relay decided.
  if (result.job_id === undefined) {
    return (
      <div>
        {decision}
        {result.answer_refused && <AnswerRefusedLine refusal={result.answer_refused} />}
        <p className={styles.nothingDispatched}>Nothing was dispatched.</p>
      </div>
    )
  }

  const summary = (
    <div className={styles.summary}>
      <span className={styles.total}>
        {n} Node{n === 1 ? '' : 's'}
      </span>
      {counts.map(({ outcome, count }) => {
        const tone = outcomeTone(outcome)
        return (
          <span key={outcome} className={`${styles.chip} ${TONE_CLASS[tone]}`} data-tone={tone}>
            {count} {outcomeLabel(outcome).toLowerCase()}
          </span>
        )
      })}
      <span className={styles.jobId}>job {result.job_id}</span>
    </div>
  )

  if (n === 0) {
    return (
      <div>
        {decision}
        {summary}
        <p className={styles.empty}>No per-Node results were returned for this job.</p>
      </div>
    )
  }

  const flatRows = groups.flatMap((group) => group.rows)

  return (
    <div>
      {decision}
      {summary}
      <table className={styles.table}>
        <thead>
          <tr>
            <th>Node</th>
            <th>Outcome</th>
            <th>Exit</th>
            <th>Duration</th>
            <th>Output</th>
          </tr>
        </thead>
        <tbody>
          {flatRows.map((row, index) => {
            const tone = outcomeTone(row.outcome)
            const Icon = TONE_ICON[tone]
            const open = expandedRows.has(index)
            const detailId = `${idPrefix}-detail-${index}`
            const preview = previewText(row)
            // 0ms only means "never dispatched" for outcomes that didn't run
            // a process; for ok/failed it's a real (if implausibly fast) duration.
            const showDuration = row.duration_ms !== 0 || tone === 'ok' || tone === 'failed'

            return (
              <Fragment key={index}>
                <tr data-outcome={row.outcome} className={tone === 'policy' ? styles.rowPolicy : ''}>
                  <NodeCell nodeId={row.node_id} hostname={hostnames?.get(row.node_id)} />
                  <td>
                    <span className={`${styles.badge} ${TONE_CLASS[tone]}`} data-tone={tone}>
                      <Icon size={14} aria-hidden />
                      {outcomeLabel(row.outcome)}
                    </span>
                  </td>
                  <td>
                    {row.exit_code !== undefined ? (
                      row.exit_code
                    ) : (
                      <span title="No exit status: the process did not run or was killed">—</span>
                    )}
                  </td>
                  <td>{showDuration ? formatDurationMs(row.duration_ms) : '—'}</td>
                  <td>
                    <button
                      type="button"
                      aria-expanded={open}
                      aria-controls={detailId}
                      className={styles.expandBtn}
                      onClick={() => toggleRow(index)}
                    >
                      {open ? <ChevronDown size={14} aria-hidden /> : <ChevronRight size={14} aria-hidden />}
                      <span className={styles.preview}>{preview ?? '(no output)'}</span>
                    </button>
                  </td>
                </tr>
                {open && (
                  <tr className={styles.detailRow}>
                    <td colSpan={5} id={detailId}>
                      <RowDetail row={row} tone={tone} labelledByHostname={hostnames?.has(row.node_id) ?? false} />
                    </td>
                  </tr>
                )}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function RowDetail({ row, tone, labelledByHostname }: { row: FanOutRow; tone: OutcomeTone; labelledByHostname: boolean }) {
  const hasStdout = row.stdout !== ''
  const hasStderr = row.stderr !== ''

  // When the row is labelled by hostname, the expanded detail repeats the
  // node_id on its own line, where it can be selected without the hostname.
  const nodeIdLine = labelledByHostname && (
    <div className={styles.streamLabel}>
      node_id <span className={styles.detailNodeId}>{row.node_id}</span>
    </div>
  )

  if (!hasStdout && !hasStderr && !row.refusal_explanation) {
    return (
      <>
        {nodeIdLine}
        <p className={styles.streamLabel}>(empty)</p>
      </>
    )
  }

  return (
    <>
      {nodeIdLine}
      {hasStdout && (
        <div>
          <div className={styles.streamLabel}>
            stdout{row.stdout_truncated && <span className={styles.truncNote}> — Truncated by Relay at 4 KiB</span>}
          </div>
          <pre className={styles.stream}>{row.stdout}</pre>
        </div>
      )}
      {hasStderr && (
        <div>
          <div className={styles.streamLabel}>
            stderr{row.stderr_truncated && <span className={styles.truncNote}> — Truncated by Relay at 4 KiB</span>}
          </div>
          <pre className={styles.stream}>{row.stderr}</pre>
        </div>
      )}
      {row.refusal_explanation && (
        <div>
          <div className={styles.streamLabel}>
            Relay: refusal explanation
            {tone === 'policy' && <span className={styles.truncNote}> — Policy boundary: nothing ran</span>}
          </div>
          <pre className={styles.stream}>{row.refusal_explanation}</pre>
        </div>
      )}
    </>
  )
}
