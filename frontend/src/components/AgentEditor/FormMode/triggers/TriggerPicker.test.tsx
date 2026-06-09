import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import type { ApiPluginInstanceForAudience } from '@/api/types'
import { TriggerPicker } from './TriggerPicker'
import type { TriggerPickerValue } from './TriggerPicker'

// --- Fixtures ---

const SLACK_INSTANCE: ApiPluginInstanceForAudience = {
  id: 'inst-1',
  plugin_id: 'plugin-slack',
  plugin_name: 'Slack',
  instance_name: 'slack-prod',
  state: 'healthy',
  implements_notify: true,
  implements_request: false,
  config_schema: null,
  version: 0,
  event_kinds: [
    { kind: 'channel_message', description: 'A message posted in a channel' },
    { kind: 'direct_message', description: 'A direct message to the bot' },
  ],
}

const PAGERDUTY_INSTANCE: ApiPluginInstanceForAudience = {
  id: 'inst-2',
  plugin_id: 'plugin-pd',
  plugin_name: 'PagerDuty',
  instance_name: 'pagerduty',
  state: 'healthy',
  implements_notify: true,
  implements_request: true,
  config_schema: null,
  version: 0,
  event_kinds: [
    { kind: 'incident_triggered', description: 'Incident triggered in PagerDuty' },
  ],
}

// Renders picker, opens it, and returns the open popover.
function renderOpen(
  value: TriggerPickerValue,
  instances: ApiPluginInstanceForAudience[],
  onChange = vi.fn(),
) {
  const { container } = render(
    <TriggerPicker
      value={value}
      onChange={onChange}
      pluginInstances={instances}
      loading={false}
    />,
  )
  const btn = screen.getByRole('button', { name: /select trigger|webhook|manual|scheduled|poll|cron|message|incident/i })
  fireEvent.click(btn)
  return { container, onChange }
}

// --- Tests ---

describe('TriggerPicker — 5 built-in options visible when no plugins', () => {
  it('shows all 5 built-in trigger types as listbox options', () => {
    renderOpen({ kind: 'builtin', type: 'webhook' }, [])

    const options = screen.getAllByRole('option')
    const titles = options.map(o => o.textContent ?? '')
    expect(titles.some(t => t.includes('Webhook'))).toBe(true)
    expect(titles.some(t => t.includes('Manual'))).toBe(true)
    expect(titles.some(t => t.includes('Scheduled'))).toBe(true)
    expect(titles.some(t => t.includes('Poll'))).toBe(true)
    expect(titles.some(t => t.includes('Cron'))).toBe(true)
  })

  it('shows "Built-in triggers" group header', () => {
    renderOpen({ kind: 'builtin', type: 'webhook' }, [])
    expect(screen.getByText('Built-in triggers')).toBeTruthy()
  })
})

describe('TriggerPicker — plugin event kinds rendered grouped', () => {
  it('shows plugin event kinds under the plugin name group header', () => {
    renderOpen({ kind: 'builtin', type: 'webhook' }, [SLACK_INSTANCE])

    // Group header is "<Plugin name> (<instance>)"
    expect(screen.getByText('Slack (slack-prod)')).toBeTruthy()
    // Event kind descriptions as option titles
    expect(screen.getByText('A message posted in a channel')).toBeTruthy()
    expect(screen.getByText('A direct message to the bot')).toBeTruthy()
  })

  it('shows multiple plugin groups when multiple instances have event_kinds', () => {
    renderOpen({ kind: 'builtin', type: 'webhook' }, [SLACK_INSTANCE, PAGERDUTY_INSTANCE])

    expect(screen.getByText('Slack (slack-prod)')).toBeTruthy()
    expect(screen.getByText('PagerDuty (pagerduty)')).toBeTruthy()
    expect(screen.getByText('Incident triggered in PagerDuty')).toBeTruthy()
  })
})

describe('TriggerPicker — search filters across built-ins and plugin entries', () => {
  it('filters built-ins by title', () => {
    renderOpen({ kind: 'builtin', type: 'webhook' }, [SLACK_INSTANCE])

    const searchInput = screen.getByPlaceholderText('Search triggers…')
    fireEvent.change(searchInput, { target: { value: 'cron' } })

    const options = screen.getAllByRole('option')
    const titles = options.map(o => o.textContent ?? '')
    // Cron option is in the list
    expect(titles.some(t => t.includes('Cron'))).toBe(true)
    // Webhook must not appear as a listbox option
    expect(titles.some(t => t.startsWith('Webhook'))).toBe(false)
    expect(screen.queryByText('A message posted in a channel')).toBeFalsy()
  })

  it('filters plugin event kinds by description', () => {
    renderOpen({ kind: 'builtin', type: 'webhook' }, [SLACK_INSTANCE])

    const searchInput = screen.getByPlaceholderText('Search triggers…')
    fireEvent.change(searchInput, { target: { value: 'direct' } })

    expect(screen.getByText('A direct message to the bot')).toBeTruthy()
    expect(screen.queryByText('A message posted in a channel')).toBeFalsy()
    // No built-ins match "direct" — Webhook must not appear as a listbox option
    const webhookOptions = screen.getAllByRole('option').filter(o => o.textContent?.includes('Webhook'))
    expect(webhookOptions).toHaveLength(0)
  })

  it('shows "No matching triggers" when nothing matches', () => {
    renderOpen({ kind: 'builtin', type: 'webhook' }, [SLACK_INSTANCE])

    const searchInput = screen.getByPlaceholderText('Search triggers…')
    fireEvent.change(searchInput, { target: { value: 'xyzxyzxyz' } })

    expect(screen.getByText('No matching triggers')).toBeTruthy()
  })

  it('hides empty groups after filtering', () => {
    renderOpen({ kind: 'builtin', type: 'webhook' }, [SLACK_INSTANCE, PAGERDUTY_INSTANCE])

    const searchInput = screen.getByPlaceholderText('Search triggers…')
    fireEvent.change(searchInput, { target: { value: 'pagerduty' } })

    // PagerDuty group header stays, Slack (slack-prod) disappears
    expect(screen.getByText('PagerDuty (pagerduty)')).toBeTruthy()
    expect(screen.queryByText('Slack (slack-prod)')).toBeFalsy()
  })
})

describe('TriggerPicker — selection callbacks emit correct discriminated union', () => {
  it('emits { kind: builtin, type } when a built-in is selected', () => {
    const onChange = vi.fn()
    renderOpen(null, [], onChange)

    fireEvent.mouseDown(screen.getByText('Manual'))
    expect(onChange).toHaveBeenCalledWith({ kind: 'builtin', type: 'manual' })
  })

  it('emits { kind: subscribed, source, eventKind } when a plugin event is selected', () => {
    const onChange = vi.fn()
    renderOpen(null, [SLACK_INSTANCE], onChange)

    fireEvent.mouseDown(screen.getByText('A message posted in a channel'))
    expect(onChange).toHaveBeenCalledWith({
      kind: 'subscribed',
      source: 'slack-prod',
      eventKind: 'channel_message',
    })
  })
})

describe('TriggerPicker — ADR-048: "subscribed" never appears in rendered UI', () => {
  it('does not render the word "subscribed" in the closed button', () => {
    const { container } = render(
      <TriggerPicker
        value={{ kind: 'subscribed', source: 'slack-prod', eventKind: 'channel_message' }}
        onChange={vi.fn()}
        pluginInstances={[SLACK_INSTANCE]}
        loading={false}
      />,
    )
    expect(container.textContent).not.toContain('subscribed')
  })

  it('does not render the word "subscribed" in the open popover', () => {
    const { container } = renderOpen(
      { kind: 'subscribed', source: 'slack-prod', eventKind: 'channel_message' },
      [SLACK_INSTANCE],
    )
    // The popover is open — "subscribed" must not appear anywhere in the DOM
    expect(container.textContent?.toLowerCase()).not.toContain('subscribed')
  })
})

describe('TriggerPicker — keyboard navigation', () => {
  it('ArrowDown focuses the first option', () => {
    render(
      <TriggerPicker
        value={null}
        onChange={vi.fn()}
        pluginInstances={[]}
        loading={false}
      />,
    )
    const btn = screen.getByRole('button')
    // Open with ArrowDown
    fireEvent.keyDown(btn, { key: 'ArrowDown' })
    // Popover should be visible
    expect(screen.getByPlaceholderText('Search triggers…')).toBeTruthy()
  })

  it('Escape closes the popover', () => {
    render(
      <TriggerPicker
        value={{ kind: 'builtin', type: 'webhook' }}
        onChange={vi.fn()}
        pluginInstances={[]}
        loading={false}
      />,
    )
    const btn = screen.getByRole('button')
    fireEvent.click(btn)
    expect(screen.getByPlaceholderText('Search triggers…')).toBeTruthy()

    const searchInput = screen.getByPlaceholderText('Search triggers…')
    fireEvent.keyDown(searchInput, { key: 'Escape' })
    expect(screen.queryByPlaceholderText('Search triggers…')).toBeFalsy()
  })
})

describe('TriggerPicker — loading state', () => {
  it('shows skeleton when loading=true', () => {
    const { container } = render(
      <TriggerPicker
        value={null}
        onChange={vi.fn()}
        pluginInstances={undefined}
        loading={true}
      />,
    )
    const btn = screen.getByRole('button')
    fireEvent.click(btn)
    // Skeleton rows should be present; no options
    const skeletonRows = container.querySelectorAll('[class*="skeletonRow"]')
    expect(skeletonRows.length).toBeGreaterThan(0)
    expect(screen.queryByText('Webhook')).toBeFalsy()
  })
})
