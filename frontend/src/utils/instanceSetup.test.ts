import { describe, it, expect } from 'vitest'
import {
  authStrategyNeedsCredentials,
  credentialsConfigured,
  missingRequiredConfigFields,
  scopeConfigured,
  deriveSetupSteps,
  firstIncompleteBlockingStep,
  humanizeHealthDetail,
} from './instanceSetup'
import type { SchemaShape } from '@/components/form/SchemaForm'
import type { ApiRedactedCredentials } from '@/api/types'

describe('authStrategyNeedsCredentials', () => {
  it.each([
    [undefined, false],
    ['', false],
    ['none', false],
    ['static_api_key', true],
    ['oauth2_authcode', true],
  ])('strategy %s → %s', (strategy, expected) => {
    expect(authStrategyNeedsCredentials(strategy)).toBe(expected)
  })
})

describe('credentialsConfigured', () => {
  it('returns true for strategies that need no credentials', () => {
    expect(credentialsConfigured('none', undefined)).toBe(true)
    expect(credentialsConfigured('', undefined)).toBe(true)
  })

  it('returns false when creds are still loading / inaccessible', () => {
    expect(credentialsConfigured('static_api_key', undefined)).toBe(false)
  })

  it('reads the right presence flag per strategy', () => {
    expect(credentialsConfigured('static_api_key', { strategy: 'static_api_key', has_api_key: true })).toBe(true)
    expect(credentialsConfigured('static_api_key', { strategy: 'static_api_key', has_api_key: false })).toBe(false)

    expect(credentialsConfigured('header_set', { strategy: 'header_set', header_names: ['X-Api'] })).toBe(true)
    expect(credentialsConfigured('header_set', { strategy: 'header_set', header_names: [] })).toBe(false)

    expect(credentialsConfigured('basic_auth', { strategy: 'basic_auth', has_password: true })).toBe(true)
    // username alone is not enough
    expect(credentialsConfigured('basic_auth', { strategy: 'basic_auth', username: 'bob' })).toBe(false)

    expect(credentialsConfigured('oauth2_authcode', { strategy: 'oauth2_authcode', has_token: true })).toBe(true)
    // client creds present but no token yet → not usable
    expect(credentialsConfigured('oauth2_authcode', { strategy: 'oauth2_authcode', has_client_secret: true })).toBe(false)
  })

  it('is forward-compatible: unknown strategy treats any presence flag as set', () => {
    const creds: ApiRedactedCredentials = { strategy: 'future', has_token: true }
    expect(credentialsConfigured('future_strategy', creds)).toBe(true)
    expect(credentialsConfigured('future_strategy', { strategy: 'future' })).toBe(false)
  })
})

describe('missingRequiredConfigFields', () => {
  const schema: SchemaShape = {
    properties: { app_token: {}, channel: {}, enabled: {} },
    required: ['app_token', 'channel'],
  }

  it('returns [] for a null schema', () => {
    expect(missingRequiredConfigFields(null, {})).toEqual([])
  })

  it('returns [] when nothing is required', () => {
    expect(missingRequiredConfigFields({ properties: { a: {} } }, {})).toEqual([])
  })

  it('flags missing and empty values', () => {
    expect(missingRequiredConfigFields(schema, {})).toEqual(['app_token', 'channel'])
    expect(missingRequiredConfigFields(schema, { app_token: '', channel: null })).toEqual([
      'app_token',
      'channel',
    ])
  })

  it('treats a redacted secret ("***") as SET, not missing', () => {
    expect(missingRequiredConfigFields(schema, { app_token: '***', channel: 'C123' })).toEqual([])
  })

  it('treats falsey-but-valid values (false, 0) as set', () => {
    const s: SchemaShape = { required: ['flag', 'count'] }
    expect(missingRequiredConfigFields(s, { flag: false, count: 0 })).toEqual([])
  })

  it('treats an empty array as missing', () => {
    const s: SchemaShape = { required: ['channels'] }
    expect(missingRequiredConfigFields(s, { channels: [] })).toEqual(['channels'])
  })
})

describe('scopeConfigured', () => {
  it('false for undefined / empty', () => {
    expect(scopeConfigured(undefined)).toBe(false)
    expect(scopeConfigured({})).toBe(false)
    expect(scopeConfigured({ channels: [] })).toBe(false)
  })
  it('true when any field is filled', () => {
    expect(scopeConfigured({ channels: ['C1'] })).toBe(true)
    expect(scopeConfigured({ all: true })).toBe(true)
  })
})

describe('deriveSetupSteps', () => {
  it('omits the credentials step for a "none" plugin', () => {
    const steps = deriveSetupSteps({
      authStrategy: 'none',
      configSchema: null,
      config: {},
      hasSubscriptionSchema: false,
    })
    expect(steps).toEqual([])
  })

  it('builds the full ordered list and marks done correctly (all incomplete)', () => {
    const steps = deriveSetupSteps({
      authStrategy: 'oauth2_authcode',
      credentials: undefined,
      configSchema: { required: ['app_token'] },
      config: {},
      hasSubscriptionSchema: true,
      subscriptionScope: {},
    })
    expect(steps.map((s) => s.key)).toEqual(['credentials', 'config', 'subscriptions'])
    expect(steps.every((s) => !s.done)).toBe(true)
    expect(steps.find((s) => s.key === 'config')?.label).toContain('1 left')
  })

  it('marks steps done when data is present', () => {
    const steps = deriveSetupSteps({
      authStrategy: 'oauth2_authcode',
      credentials: { strategy: 'oauth2_authcode', has_token: true },
      configSchema: { required: ['app_token'] },
      config: { app_token: '***' },
      hasSubscriptionSchema: true,
      subscriptionScope: { channels: ['C1'] },
    })
    expect(steps.every((s) => s.done)).toBe(true)
  })

  it('subscription step is non-blocking; credentials + config are blocking', () => {
    const steps = deriveSetupSteps({
      authStrategy: 'static_api_key',
      configSchema: { required: ['x'] },
      config: {},
      hasSubscriptionSchema: true,
      subscriptionScope: {},
    })
    expect(steps.find((s) => s.key === 'credentials')?.blocking).toBe(true)
    expect(steps.find((s) => s.key === 'config')?.blocking).toBe(true)
    expect(steps.find((s) => s.key === 'subscriptions')?.blocking).toBe(false)
  })
})

describe('firstIncompleteBlockingStep', () => {
  it('returns the first incomplete blocking step', () => {
    const steps = deriveSetupSteps({
      authStrategy: 'static_api_key',
      credentials: { strategy: 'static_api_key', has_api_key: true },
      configSchema: { required: ['x'] },
      config: {},
      hasSubscriptionSchema: false,
    })
    // credentials done, config not → config is the blocker
    expect(firstIncompleteBlockingStep(steps)?.key).toBe('config')
  })

  it('returns null when all blocking steps are done', () => {
    const steps = deriveSetupSteps({
      authStrategy: 'none',
      configSchema: { required: ['x'] },
      config: { x: 'set' },
      hasSubscriptionSchema: true,
      subscriptionScope: {},
    })
    expect(firstIncompleteBlockingStep(steps)).toBeNull()
  })
})

describe('humanizeHealthDetail', () => {
  it('returns null for empty / oauth-refresh details', () => {
    expect(humanizeHealthDetail(undefined)).toBeNull()
    expect(humanizeHealthDetail('')).toBeNull()
    expect(humanizeHealthDetail('oauth refresh failed: token expired')).toBeNull()
  })

  it('maps config_missing and credentials_missing to actionable copy + tab', () => {
    expect(humanizeHealthDetail('config_missing')).toMatchObject({ tab: 'config', cta: expect.any(String) })
    expect(humanizeHealthDetail('credentials_missing')).toMatchObject({ tab: 'credentials' })
  })

  it('maps config_schema_unparseable without a tab link', () => {
    const c = humanizeHealthDetail('config_schema_unparseable')
    expect(c?.tab).toBeUndefined()
    expect(c?.message).toContain('config schema')
  })

  it('falls back to the raw detail for unknown strings', () => {
    expect(humanizeHealthDetail('something_new')).toEqual({ message: 'something_new' })
  })
})
