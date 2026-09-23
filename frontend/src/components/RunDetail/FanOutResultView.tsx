import { Fragment, useId, useState } from 'react'
import { Check, ChevronDown, ChevronRight, HelpCircle, MinusCircle, ShieldCheck, X } from 'lucide-react'
import { formatDurationMs } from '@/utils/format'
import { groupByOutcome, outcomeLabel, outcomeTone, OUTCOME_ORDER } from './fanOutResult'
import type { FanOutResult, FanOutRow, OutcomeTone } from './fanOutResult'
import styles from './FanOutResultView.module.css'

interface Props {
  result: FanOutResult
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

// previewText picks the first non-empty of stdout/stderr/refusal_explanation and
// returns only its first line — the rest is reachable only in the expanded
// detail. A long first line is still cut visually by CSS text-overflow ellipsis.
function previewText(row: FanOutRow): string | null {
  const text = row.stdout || row.stderr || row.refusal_explanation
  return text ? text.split('\n')[0] : null
}

export function FanOutResultView({ result }: Props) {
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
        {summary}
        <p className={styles.empty}>No per-Node results were returned for this job.</p>
      </div>
    )
  }

  const flatRows = groups.flatMap((group) => group.rows)

  return (
    <div>
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
                  <td className={styles.node}>{row.node_id}</td>
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
                      <RowDetail row={row} tone={tone} />
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

function RowDetail({ row, tone }: { row: FanOutRow; tone: OutcomeTone }) {
  const hasStdout = row.stdout !== ''
  const hasStderr = row.stderr !== ''

  if (!hasStdout && !hasStderr && !row.refusal_explanation) {
    return <p className={styles.streamLabel}>(empty)</p>
  }

  return (
    <>
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
