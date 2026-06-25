import { describe, it, expect } from 'vitest'
import { triggerPillLabel, BUILTIN_TRIGGER_LABELS } from './triggerLabel'

describe('BUILTIN_TRIGGER_LABELS', () => {
  it('maps all five built-in trigger types to title-case labels', () => {
    expect(BUILTIN_TRIGGER_LABELS['webhook']).toBe('Webhook')
    expect(BUILTIN_TRIGGER_LABELS['manual']).toBe('Manual')
    expect(BUILTIN_TRIGGER_LABELS['scheduled']).toBe('Scheduled')
    expect(BUILTIN_TRIGGER_LABELS['poll']).toBe('Poll')
    expect(BUILTIN_TRIGGER_LABELS['cron']).toBe('Cron')
  })
})

describe('triggerPillLabel', () => {
  const cases: Array<{
    name: string
    input: { trigger_type: string; trigger_source?: string; trigger_event_kind?: string }
    expected: string
  }> = [
    { name: 'webhook → Webhook', input: { trigger_type: 'webhook' }, expected: 'Webhook' },
    { name: 'manual → Manual', input: { trigger_type: 'manual' }, expected: 'Manual' },
    { name: 'scheduled → Scheduled', input: { trigger_type: 'scheduled' }, expected: 'Scheduled' },
    { name: 'poll → Poll', input: { trigger_type: 'poll' }, expected: 'Poll' },
    { name: 'cron → Cron', input: { trigger_type: 'cron' }, expected: 'Cron' },
    {
      name: 'unknown type → returned verbatim',
      input: { trigger_type: 'some_future_type' },
      expected: 'some_future_type',
    },
    {
      name: 'subscribed with both source and event_kind',
      input: { trigger_type: 'subscribed', trigger_source: 'slack-e2e', trigger_event_kind: 'channel_message' },
      expected: 'channel_message (slack-e2e)',
    },
    {
      name: 'subscribed with event_kind only (no source)',
      input: { trigger_type: 'subscribed', trigger_event_kind: 'channel_message' },
      expected: 'channel_message',
    },
    {
      name: 'subscribed with neither source nor event_kind → Plugin event fallback',
      input: { trigger_type: 'subscribed' },
      expected: 'Plugin event',
    },
  ]

  for (const { name, input, expected } of cases) {
    it(name, () => {
      expect(triggerPillLabel(input)).toBe(expected)
    })
  }

  it('never returns "subscribed" for a subscribed trigger with both fields', () => {
    const label = triggerPillLabel({
      trigger_type: 'subscribed',
      trigger_source: 'slack-e2e',
      trigger_event_kind: 'channel_message',
    })
    expect(label).not.toBe('subscribed')
    expect(label).not.toContain('subscribed')
  })

  it('never returns "subscribed" for a subscribed trigger with event_kind only', () => {
    const label = triggerPillLabel({
      trigger_type: 'subscribed',
      trigger_event_kind: 'channel_message',
    })
    expect(label).not.toBe('subscribed')
  })

  it('never returns "subscribed" for a subscribed trigger with no fields', () => {
    const label = triggerPillLabel({ trigger_type: 'subscribed' })
    expect(label).not.toBe('subscribed')
  })
})
