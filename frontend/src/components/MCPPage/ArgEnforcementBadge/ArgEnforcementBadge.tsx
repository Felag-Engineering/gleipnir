import type { ApiArgEnforcement } from '@/api/types'
import styles from './ArgEnforcementBadge.module.css'

export interface ArgEnforcementExplanation {
  reason: string
  detail: string
}

const FALLBACK = 'Only the parameter names the agent policy allows are checked; argument values are not validated.'

// The reduced causes have different remedies, so the wording keeps them apart:
// no schema at all, a schema Gleipnir could not process, and a schema that was
// read but cannot be used for checking. Nothing here names the transport
// (ADR-030).
const EXPLANATIONS: Record<Exclude<ApiArgEnforcement, 'exact'>, ArgEnforcementExplanation> = {
  no_schema: {
    reason: 'the tool does not declare an argument schema',
    detail: FALLBACK,
  },
  no_canonical_schema: {
    reason: "the tool's schema could not be processed",
    detail: `${FALLBACK} The server needs to publish a schema Gleipnir can process, then the tool needs to be rediscovered.`,
  },
  schema_uncompilable: {
    reason: "the tool's schema could not be used for validation",
    detail: `${FALLBACK} The schema was read but is invalid or uses features Gleipnir does not support, so it needs fixing on the server.`,
  },
}

/** Explanation for a reduced state; undefined for exact, missing or unknown states. */
export function explainArgEnforcement(state: ApiArgEnforcement | undefined): ArgEnforcementExplanation | undefined {
  if (!state || state === 'exact') return undefined
  return EXPLANATIONS[state]
}

// ArgEnforcementBadge flags a tool whose call arguments are not checked
// against its full schema. Renders nothing for exact enforcement, so a present
// badge stands out.
export function ArgEnforcementBadge({ state }: { state: ApiArgEnforcement | undefined }) {
  const explanation = explainArgEnforcement(state)
  if (!explanation) return null

  return (
    <span className={styles.badge} title={`Argument checking: reduced — ${explanation.reason}. ${explanation.detail}`}>
      Reduced argument checking
    </span>
  )
}
