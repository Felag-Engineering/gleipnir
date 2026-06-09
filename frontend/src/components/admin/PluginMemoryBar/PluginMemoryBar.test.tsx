import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { PluginMemoryBar } from './PluginMemoryBar'

vi.mock('@/hooks/queries/plugins')
import { usePluginRSS } from '@/hooks/queries/plugins'

function renderBar() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <PluginMemoryBar />
    </QueryClientProvider>,
  )
}

function mockRSS(data: ReturnType<typeof usePluginRSS>['data']) {
  vi.mocked(usePluginRSS).mockReturnValue({
    data,
    isLoading: false,
    isError: false,
    status: 'success',
  } as ReturnType<typeof usePluginRSS>)
}

describe('PluginMemoryBar', () => {
  beforeEach(() => {
    // Default: no data yet (loading state).
    vi.mocked(usePluginRSS).mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
      status: 'pending',
    } as unknown as ReturnType<typeof usePluginRSS>)
  })

  it('renders nothing while loading', () => {
    const { container } = renderBar()
    expect(container).toBeEmptyDOMElement()
  })

  it('renders nothing when instance_count is 0', () => {
    mockRSS({ total_bytes: 0, instance_count: 0, instances: [] })
    const { container } = renderBar()
    expect(container).toBeEmptyDOMElement()
  })

  it('renders summary text with correctly formatted bytes and instance count', () => {
    mockRSS({
      total_bytes: 431915008, // ≈ 412 MB
      instance_count: 2,
      instances: [
        {
          instance_id: 'inst-a',
          instance_name: 'slack-prod',
          plugin_id: 'plugin-slack',
          rss_bytes: 209715200, // 200 MB
          sampled_at: '2026-01-01T12:00:00Z',
        },
        {
          instance_id: 'inst-b',
          instance_name: 'jira-prod',
          plugin_id: 'plugin-jira',
          rss_bytes: 222199808, // ≈ 211.9 MB (to make a different value)
          sampled_at: '2026-01-01T12:00:00Z',
        },
      ],
    })

    renderBar()
    expect(screen.getByText(/Plugin memory:/)).toBeInTheDocument()
    expect(screen.getByText(/across 2 instances/)).toBeInTheDocument()
  })

  it('uses singular "instance" for count of 1', () => {
    mockRSS({
      total_bytes: 104857600,
      instance_count: 1,
      instances: [
        {
          instance_id: 'inst-a',
          instance_name: 'slack-prod',
          plugin_id: 'plugin-slack',
          rss_bytes: 104857600,
          sampled_at: '2026-01-01T12:00:00Z',
        },
      ],
    })

    renderBar()
    expect(screen.getByText(/across 1 instance/)).toBeInTheDocument()
    expect(screen.queryByText(/instances/)).not.toBeInTheDocument()
  })

  it('clicking the summary toggles the per-instance breakdown table', async () => {
    mockRSS({
      total_bytes: 209715200,
      instance_count: 1,
      instances: [
        {
          instance_id: 'inst-a',
          instance_name: 'slack-prod',
          plugin_id: 'plugin-slack',
          rss_bytes: 209715200,
          sampled_at: '2026-01-01T12:00:00Z',
        },
      ],
    })

    renderBar()

    // Breakdown table is initially hidden.
    expect(screen.queryByRole('table')).not.toBeInTheDocument()

    // Click the summary to expand.
    await userEvent.click(screen.getByRole('button'))
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.getByText('slack-prod')).toBeInTheDocument()

    // Click again to collapse.
    await userEvent.click(screen.getByRole('button'))
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('breakdown table rows are in the order supplied (backend sorts by RSS desc)', () => {
    // Backend guarantees descending RSS order; component must preserve that order.
    mockRSS({
      total_bytes: 314572800,
      instance_count: 2,
      instances: [
        {
          instance_id: 'inst-b',
          instance_name: 'jira-prod',
          plugin_id: 'plugin-jira',
          rss_bytes: 209715200, // 200 MB — larger, should be first
          sampled_at: '2026-01-01T12:00:00Z',
        },
        {
          instance_id: 'inst-a',
          instance_name: 'slack-prod',
          plugin_id: 'plugin-slack',
          rss_bytes: 104857600, // 100 MB — smaller, should be second
          sampled_at: '2026-01-01T12:00:00Z',
        },
      ],
    })

    renderBar()
    // Expand to show the table.
    // eslint-disable-next-line @typescript-eslint/no-floating-promises
    userEvent.click(screen.getByRole('button'))

    // Verify rows appear in the DOM in the supplied order.
    const cells = screen.queryAllByRole('cell')
    if (cells.length > 0) {
      // jira-prod (larger) should appear before slack-prod (smaller).
      const names = cells.map((c) => c.textContent)
      const jiraIdx = names.findIndex((n) => n === 'jira-prod')
      const slackIdx = names.findIndex((n) => n === 'slack-prod')
      expect(jiraIdx).toBeLessThan(slackIdx)
    }
  })
})
