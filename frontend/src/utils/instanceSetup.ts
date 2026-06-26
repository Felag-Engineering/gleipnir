// Onboarding-step derivation for a plugin instance (#658).
//
// Getting a plugin instance to `healthy` is a multi-step setup: credentials →
// required config → subscription scope. This module derives an ordered list of
// those steps from the data the instance page already has (manifest auth
// strategy, redacted credentials, config schema + redacted config values, and
// the subscription scope), plus a humanizer that turns internal `health_detail`
// strings into actionable copy with a deep link to the relevant tab.
//
// Everything here is PURE so it can be unit-tested without rendering. The page
// owns data fetching and passes plain values in.

import { REDACTION_SENTINEL } from '@/components/form/SchemaForm'
import type { SchemaShape } from '@/components/form/SchemaForm'
import type { ApiRedactedCredentials } from '@/api/types'

// The three settings tabs an onboarding step can point at. Matches the page's
// internal Tab union (kept local so this module stays page-agnostic).
export type SetupTab = 'subscriptions' | 'config' | 'credentials'

export interface SetupStep {
  // Stable identity for keys / tab badges.
  key: 'credentials' | 'config' | 'subscriptions'
  label: string
  done: boolean
  // Which tab the step's CTA navigates to.
  tab: SetupTab
  // blocking: an incomplete blocking step prevents the instance reaching
  // `healthy`. Credentials and required config are blocking; subscription scope
  // is a recommended (non-blocking) step.
  blocking: boolean
}

// authStrategyNeedsCredentials reports whether a manifest auth strategy actually
// consumes operator-supplied credentials. "none" (and the unset/empty default)
// are config-only, mirroring the backend's computeInstanceReadinessDetail.
export function authStrategyNeedsCredentials(strategy: string | undefined): boolean {
  const s = strategy ?? ''
  return s !== '' && s !== 'none'
}

// credentialsConfigured reports whether the redacted credential view shows a
// usable credential for the given strategy. Secret values are never present in
// the redacted view — only presence flags — so we read those flags per strategy.
//
// Conservative on missing data: when creds is undefined (still loading or the
// caller lacks access) we return false rather than claiming "done".
export function credentialsConfigured(
  strategy: string | undefined,
  creds: ApiRedactedCredentials | undefined,
): boolean {
  if (!authStrategyNeedsCredentials(strategy)) return true
  if (!creds) return false
  switch (strategy) {
    case 'static_api_key':
      return !!creds.has_api_key
    case 'header_set':
      return (creds.header_names?.length ?? 0) > 0
    case 'basic_auth':
      // A password is the secret half; username alone is not enough.
      return !!creds.has_password
    case 'oauth2_authcode':
    case 'oauth2_clientcred':
      // A stored token is what makes the instance able to authenticate; client
      // id/secret without a token is not yet usable.
      return !!creds.has_token
    default:
      // Unknown/forward-compatible strategy: treat any presence flag as set so
      // we neither crash nor nag indefinitely.
      return !!(
        creds.has_api_key ||
        creds.has_password ||
        creds.has_token ||
        (creds.header_names?.length ?? 0) > 0
      )
  }
}

// isValueSet reports whether a config/scope value counts as filled. A redacted
// secret ("***") means the value IS set. Empty string, null, undefined, and an
// empty array all count as missing. `false` and `0` are valid set values.
function isValueSet(val: unknown): boolean {
  if (val === undefined || val === null || val === '') return false
  if (Array.isArray(val) && val.length === 0) return false
  if (val === REDACTION_SENTINEL) return true
  return true
}

// missingRequiredConfigFields returns the names of required config_schema fields
// that are not yet filled in the (redacted) config values. Robust to a null
// schema or an absent `required` array — returns [] when nothing is required.
export function missingRequiredConfigFields(
  schema: SchemaShape | null,
  config: Record<string, unknown>,
): string[] {
  if (!schema) return []
  const required = schema.required ?? []
  return required.filter((name) => !isValueSet(config[name]))
}

// scopeConfigured reports whether the subscription scope has at least one filled
// field. Empty object / undefined → false.
export function scopeConfigured(scope: Record<string, unknown> | undefined): boolean {
  if (!scope) return false
  return Object.values(scope).some(isValueSet)
}

export interface DeriveStepsInput {
  // Manifest auth strategy (instance.auth_strategy). Empty/undefined = "none".
  authStrategy?: string
  // Redacted credential view; undefined while loading or when inaccessible.
  credentials?: ApiRedactedCredentials
  // Instance-level config schema (from the detail endpoint). null when the
  // manifest declares none.
  configSchema: SchemaShape | null
  // Parsed (redacted) config values.
  config: Record<string, unknown>
  // Whether the plugin declares a subscription_schema (the Subscriptions tab is
  // only rendered when true).
  hasSubscriptionSchema: boolean
  // Current instance subscription scope.
  subscriptionScope?: Record<string, unknown>
}

// deriveSetupSteps builds the ordered onboarding step list. Order is
// credentials → required config → subscription scope. Steps that do not apply to
// the instance (e.g. credentials for a "none" plugin, config when nothing is
// required, subscriptions when no schema) are omitted.
export function deriveSetupSteps(input: DeriveStepsInput): SetupStep[] {
  const steps: SetupStep[] = []

  if (authStrategyNeedsCredentials(input.authStrategy)) {
    const done = credentialsConfigured(input.authStrategy, input.credentials)
    steps.push({
      key: 'credentials',
      label: done ? 'Credentials set' : 'Add credentials',
      done,
      tab: 'credentials',
      blocking: true,
    })
  }

  const requiredCount = input.configSchema?.required?.length ?? 0
  if (requiredCount > 0) {
    const missing = missingRequiredConfigFields(input.configSchema, input.config)
    const done = missing.length === 0
    steps.push({
      key: 'config',
      label: done
        ? 'Required config set'
        : `Fill required config (${missing.length} left)`,
      done,
      tab: 'config',
      blocking: true,
    })
  }

  if (input.hasSubscriptionSchema) {
    const done = scopeConfigured(input.subscriptionScope)
    steps.push({
      key: 'subscriptions',
      label: done ? 'Subscription scope set' : 'Set subscription scope',
      done,
      tab: 'subscriptions',
      blocking: false,
    })
  }

  return steps
}

// firstIncompleteBlockingStep returns the first blocking step that is not done,
// or null when none — used to pick the default tab and the "Next" emphasis.
export function firstIncompleteBlockingStep(steps: SetupStep[]): SetupStep | null {
  return steps.find((s) => s.blocking && !s.done) ?? null
}

export interface HealthDetailCopy {
  // Human-readable, actionable sentence.
  message: string
  // Tab to deep-link to, when the detail maps to a specific setup step.
  tab?: SetupTab
  // CTA label for the deep link.
  cta?: string
}

// humanizeHealthDetail maps an internal `health_detail` string to actionable
// operator copy with an optional deep link. Returns null when there is nothing
// actionable to say (empty detail), or when the detail is handled by a dedicated
// banner elsewhere (OAuth refresh failure → ReauthorizeButton banner).
//
// Unknown details fall back to surfacing the raw string so we never hide signal.
export function humanizeHealthDetail(detail: string | undefined): HealthDetailCopy | null {
  if (!detail) return null
  // OAuth refresh failures get their own dedicated "Re-authorize" banner.
  if (detail.startsWith('oauth refresh failed')) return null

  switch (detail) {
    case 'config_missing':
      return {
        message: 'Add the required configuration to finish setting up this instance.',
        tab: 'config',
        cta: 'Go to Config',
      }
    case 'credentials_missing':
      return {
        message: 'Add credentials so this instance can authenticate.',
        tab: 'credentials',
        cta: 'Add credentials',
      }
    case 'config_schema_unparseable':
      return {
        message:
          "The plugin's config schema could not be parsed. Contact your plugin administrator.",
      }
    default:
      // Fall back to the raw detail rather than hiding it.
      return { message: detail }
  }
}
