import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, within } from '@testing-library/react'

import { CapabilityHealthPanel } from './CapabilityHealthPanel'
import type { ApiPluginCapabilityHealth } from '@/api/types'

vi.mock('@/hooks/queries/plugins')

import { usePluginInstanceCapabilities } from '@/hooks/queries/plugins'

function mockCapabilities(
  status: 'pending' | 'error' | 'success',
  data?: ApiPluginCapabilityHealth[],
) {
  vi.mocked(usePluginInstanceCapabilities).mockReturnValue({
    data,
    status,
  } as unknown as ReturnType<typeof usePluginInstanceCapabilities>)
}

describe('CapabilityHealthPanel', () => {
  beforeEach(() => {
    vi.resetAllMocks()
  })

  it('renders nothing while loading', () => {
    mockCapabilities('pending')
    const { container } = render(<CapabilityHealthPanel pluginId="p" instanceId="i" />)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows the empty state when no capabilities are reported', () => {
    mockCapabilities('success', [])
    render(<CapabilityHealthPanel pluginId="p" instanceId="i" />)
    expect(screen.getByRole('heading', { name: 'Capabilities' })).toBeInTheDocument()
    expect(screen.getByText(/No per-capability health/)).toBeInTheDocument()
    expect(screen.queryByRole('list')).not.toBeInTheDocument()
  })

  it('renders a chip per capability with its label and source', () => {
    mockCapabilities('success', [
      { profile: 'event_source', name: 'channel_message', state: 'unhealthy', detail: 'missing scope', source: 'self_report' },
      { profile: 'tool_provider', name: '', state: 'healthy', detail: '', source: 'probe' },
    ])
    render(<CapabilityHealthPanel pluginId="p" instanceId="i" />)

    const items = screen.getAllByRole('listitem')
    expect(items).toHaveLength(2)

    expect(within(items[0]).getByText('event_source/channel_message')).toBeInTheDocument()
    expect(within(items[0]).getByText('reported by plugin')).toBeInTheDocument()
    expect(within(items[0]).getByTitle('missing scope')).toBeInTheDocument()

    expect(within(items[1]).getByText('tool_provider')).toBeInTheDocument()
    expect(within(items[1]).getByText('checked by host')).toBeInTheDocument()
  })

  it('shows an error line when the fetch fails', () => {
    mockCapabilities('error')
    render(<CapabilityHealthPanel pluginId="p" instanceId="i" />)
    expect(screen.getByText('Could not load capability health.')).toBeInTheDocument()
  })
})
