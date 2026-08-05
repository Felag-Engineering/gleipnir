import { useMemo, useState } from 'react'
import { Button } from '@/components/Button/Button'
import { SchemaForm } from '@/components/form/SchemaForm'
import { useSubmitToolInput } from '@/hooks/mutations/runs'
import { useCurrentUser } from '@/hooks/queries/users'
import { useCountdown } from '@/hooks/useCountdown'
import type {
  ApiToolInputQuestion,
  ApiToolInputRequest,
  ApiToolInputResponseItem,
} from '@/api/types'
import { extractElicitationLink } from './elicitationLink'
import styles from './ToolInputCard.module.css'

interface Props {
  request: ApiToolInputRequest
}

// deadlineNote explains which clock produced the countdown (spec §6.3). The
// displayed deadline is the MINIMUM of the policy timeout, the server's task
// TTL, and any requestState TTL, and which one won changes what running out
// means: Gleipnir giving up on the wait, or the server having discarded the
// state the answer was going to be spent against.
const DEADLINE_NOTES: Record<string, string> = {
  policy: "Gleipnir's own timeout for this policy.",
  server_ttl: "The tool server's task expiry — sooner than this policy's timeout.",
  request_state:
    'The tool server declared its saved state expires this soon. If it lapses first, Gleipnir re-asks the tool and replays your answer automatically.',
}

// KIND_LABEL keeps the two renderings distinguishable at a glance. The
// permission/information split (§6.1) is not cosmetic — it decides which role
// may answer and whether the operator is consenting or supplying data.
const KIND_LABEL: Record<string, string> = {
  permission: 'PERMISSION',
  information: 'INFORMATION',
}

// UntrustedText renders server-controlled elicitation text as content and
// nothing else (§6.1).
//
// React escapes by default, so this is a plain paragraph on purpose: the value
// of the component is that there is one named place where that decision is
// made, and any future temptation to run this text through a markdown renderer
// has to go through it.
function UntrustedText({ children }: { children: string }) {
  return <p className={styles.untrusted}>{children}</p>
}

// LinkStep renders a URL found in the elicitation as an explicit "open in
// browser" step with the host called out (§6.1 URL mode).
//
// Never auto-opened and never framed. The URL is chosen by the server, so the
// operator must be the one who decides to follow it, and must be able to see
// where it goes before they do — which is why the host is displayed separately
// from the rest of the link rather than left to be read out of a long string.
function LinkStep({ href, host }: { href: string; host: string }) {
  return (
    <div className={styles.linkStep}>
      <span className={styles.linkLabel}>This step continues in your browser at</span>
      <span className={styles.linkHost}>{host}</span>
      <a className={styles.linkAnchor} href={href} target="_blank" rel="noreferrer noopener">
        Open in browser
      </a>
    </div>
  )
}

function QuestionBody({
  question,
  index,
  value,
  onChange,
  disabled,
}: {
  question: ApiToolInputQuestion
  index: number
  value: Record<string, unknown>
  onChange: (next: Record<string, unknown>) => void
  disabled: boolean
}) {
  const link = extractElicitationLink(question.message)
  const schema = question.requested_schema as
    | { properties?: Record<string, unknown> }
    | undefined
  const hasFields = Boolean(schema?.properties && Object.keys(schema.properties).length > 0)

  return (
    <div className={styles.question}>
      <UntrustedText>{question.message}</UntrustedText>
      {link && <LinkStep href={link.href} host={link.host} />}
      {hasFields && (
        <fieldset className={styles.fields} disabled={disabled}>
          <SchemaForm
            schema={question.requested_schema as never}
            value={value}
            onChange={onChange}
            fieldErrors={{}}
            idPrefix={`tool-input-${index}`}
          />
        </fieldset>
      )}
    </div>
  )
}

// PriorAttempt shows what the operator was asked and answered immediately
// before this prompt (§6.5).
//
// It appears only when the server re-asked a DIFFERENT question after its MRTR
// state expired. A second prompt that looks like a duplicate but is not is
// exactly where a reflexive approval does the most damage, so the difference is
// put in front of the operator rather than left for them to notice.
function PriorAttempt({ request }: { request: ApiToolInputRequest }) {
  const prior = request.prior_attempt
  if (!prior) return null

  return (
    <div className={styles.prior}>
      <span className={styles.priorLabel}>You already answered a different question</span>
      {prior.reason && <span className={styles.priorReason}>{prior.reason}</span>}
      {(prior.prior_questions ?? []).map((q, i) => {
        const answer = prior.prior_answers?.[i]
        return (
          <div key={i} className={styles.priorRow}>
            <UntrustedText>{q.message}</UntrustedText>
            {answer && (
              <span className={styles.priorAnswer}>
                Your answer: <strong>{answer.action}</strong>
              </span>
            )}
          </div>
        )
      })}
    </div>
  )
}

// ToolInputCard renders a tool-initiated HITL request and, for an operator who
// may answer it, the controls to settle it (ADR-055 §6.1).
//
// Two renderings, decided by the elicitation kind:
//
//   - permission — a consent-only ask. Approve/Reject, resolvable by approvers.
//   - information — a request for values. A form built from the server's
//     `requestedSchema`, resolvable by operators.
//
// Whoever lacks the required role still sees the question. Reading what a run
// is blocked on is not the same authority as answering it, and hiding the
// question from an auditor would make the decision unauditable.
export function ToolInputCard({ request }: Props) {
  const { data: user } = useCurrentUser()
  const submit = useSubmitToolInput()
  const countdown = useCountdown(request.expires_at)

  const [values, setValues] = useState<Record<number, Record<string, unknown>>>({})

  const isPermission = request.elicitation_kind === 'permission'
  const canAnswer = useMemo(() => {
    const roles = user?.roles ?? []
    if (roles.includes('admin')) return true
    return roles.includes(request.required_role)
  }, [user?.roles, request.required_role])

  function answer(action: ApiToolInputResponseItem['action']) {
    const responses: ApiToolInputResponseItem[] = request.requests.map((q, i) => {
      if (action !== 'accept') return { action }
      // A consent-only ask still needs content on accept — the action carries
      // the decision, and the backend requires a body for it.
      const schema = q.requested_schema as { properties?: Record<string, unknown> } | undefined
      const hasFields = Boolean(schema?.properties && Object.keys(schema.properties).length > 0)
      return { action, content: hasFields ? (values[i] ?? {}) : { confirmed: true } }
    })
    submit.mutate({ runId: request.run_id, responses })
  }

  const deadlineNote = request.deadline_source
    ? DEADLINE_NOTES[request.deadline_source]
    : undefined

  return (
    <section className={styles.card} role="group" aria-label="Tool-initiated request">
      <header className={styles.header}>
        <span
          className={`${styles.badge} ${isPermission ? styles.badgePermission : styles.badgeInformation}`}
        >
          {KIND_LABEL[request.elicitation_kind] ?? request.elicitation_kind.toUpperCase()}
        </span>
        <code className={styles.tool}>{request.tool_name}</code>
        {countdown && (
          <span className={`${styles.countdown} ${countdown.urgent ? styles.countdownUrgent : ''}`}>
            {countdown.str}
          </span>
        )}
      </header>

      <p className={styles.provenance}>
        Asked by the tool server mid-call. The text below comes from the server, not from
        Gleipnir.
      </p>

      {deadlineNote && <p className={styles.deadlineNote}>{deadlineNote}</p>}

      <PriorAttempt request={request} />

      {request.requests.map((question, i) => (
        <QuestionBody
          key={i}
          question={question}
          index={i}
          value={values[i] ?? {}}
          onChange={(next) => setValues((prev) => ({ ...prev, [i]: next }))}
          disabled={!canAnswer || submit.isPending}
        />
      ))}

      {canAnswer ? (
        <div className={styles.actions}>
          <Button
            variant="primary"
            size="small"
            disabled={submit.isPending}
            onClick={() => answer('accept')}
          >
            {isPermission ? 'Approve' : 'Submit'}
          </Button>
          <Button
            variant="danger"
            size="small"
            disabled={submit.isPending}
            onClick={() => answer('decline')}
          >
            {isPermission ? 'Reject' : 'Decline'}
          </Button>
        </div>
      ) : (
        <p className={styles.roleNotice}>
          Answering this request needs the <strong>{request.required_role}</strong> role.
        </p>
      )}

      {submit.isError && (
        <p className={styles.error} role="alert">
          Could not submit your answer — please try again.
        </p>
      )}
    </section>
  )
}
