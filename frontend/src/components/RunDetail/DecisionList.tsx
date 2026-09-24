import { useRunDecisions } from '@/hooks/queries/runs'
import { formatTimestamp } from '@/utils/format'
import styles from './DecisionList.module.css'

interface Props {
  runId: string
}

// KIND_LABEL mirrors ToolInputCard's split: the same permission/information
// distinction, rendered the same way, so a reader recognizes the vocabulary
// across both views of the same request.
const KIND_LABEL: Record<string, string> = {
  permission: 'PERMISSION',
  information: 'INFORMATION',
}

// OUTCOME_LABEL turns the wire vocabulary into the short label an operator
// scans a table for; the full explanation lives in the run's own decision
// record fields (severity, link method), not in this list.
const OUTCOME_LABEL: Record<string, string> = {
  answered: 'Answered',
  rejected: 'Rejected',
  timeout: 'Timed out',
  cancelled: 'Cancelled',
  replayed_after_ttl: 'Replayed',
}

// DecisionList renders a run's tool-initiated HITL decision records
// (ADR-055 §6.6): who was asked, what kind of ask it was, how it ended, and
// who — if anyone — acted. It is oversight evidence the model never saw
// (ADR-046), which is why it rides its own endpoint rather than extra rows in
// the step trace, and a combined timeline would be a presentation choice made
// on top of both, not a wire concern.
//
// Renders nothing while loading or once loaded with no decisions: a run with
// no tool-initiated pauses has nothing to show here, and an empty section
// would only be noise on every ordinary run.
export function DecisionList({ runId }: Props) {
  const { decisions, status } = useRunDecisions(runId)

  if (status !== 'success' || decisions.length === 0) return null

  return (
    <section className={styles.card} aria-label="Decisions">
      <h3 className={styles.heading}>Decisions</h3>
      <div className={styles.tableWrapper}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Time</th>
              <th>Tool</th>
              <th>Kind</th>
              <th>Outcome</th>
              <th>By</th>
            </tr>
          </thead>
          <tbody>
            {decisions.map((d) => (
              <tr key={d.request_id}>
                <td className={styles.mono}>{formatTimestamp(d.decided_at)}</td>
                <td className={styles.mono}>{d.tool_name ?? '—'}</td>
                <td>{KIND_LABEL[d.kind] ?? d.kind}</td>
                <td>
                  {OUTCOME_LABEL[d.outcome] ?? d.outcome}
                  {d.replay_of_request_id && (
                    <span className={styles.replayHint}>replay of {d.replay_of_request_id}</span>
                  )}
                </td>
                <td>{d.actor_username ? `by ${d.actor_username}` : 'no responder'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}
