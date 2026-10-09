import { describe, it, expect } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
import { CapabilitySnapshotCard } from './CapabilitySnapshotCard'
import type { CapabilitySnapshotContent } from './types'

const TWO_REAL_TOOLS: CapabilitySnapshotContent = [
  { server_name: 'fs', tool_name: 'read_file', approval: 'none', timeout: 30, on_timeout: 'fail' },
  { server_name: 'fs', tool_name: 'write_file', approval: 'required', timeout: 60, on_timeout: 'fail' },
]

const WITH_FEEDBACK: CapabilitySnapshotContent = [
  ...TWO_REAL_TOOLS,
  { server_name: 'gleipnir', tool_name: 'ask_operator', approval: 'none', timeout: 0, on_timeout: '' },
]

const V2_WITH_FEEDBACK: CapabilitySnapshotContent = {
  provider: 'anthropic',
  model: 'claude-sonnet-4-6',
  tools: [
    { server_name: 'fs', tool_name: 'read_file', approval: 'none', timeout: 30, on_timeout: 'fail' },
    { server_name: 'fs', tool_name: 'write_file', approval: 'required', timeout: 60, on_timeout: 'fail' },
    { server_name: 'gleipnir', tool_name: 'ask_operator', approval: 'none', timeout: 0, on_timeout: '' },
  ],
}

const TWO_SERVERS: CapabilitySnapshotContent = [
  { server_name: 'relay', tool_name: 'list_nodes', approval: 'none', timeout: 30, on_timeout: 'fail' },
  { server_name: 'fs', tool_name: 'read_file', approval: 'none', timeout: 30, on_timeout: 'fail' },
  { server_name: 'relay', tool_name: 'run_operation', approval: 'none', timeout: 30, on_timeout: 'fail' },
]

function card() {
  return screen.getByRole('region', { name: /capability snapshot/i })
}

describe('CapabilitySnapshotCard — tool list is visible without interaction', () => {
  it('lists every tool name without a click', () => {
    render(<CapabilitySnapshotCard content={TWO_REAL_TOOLS} />)
    expect(screen.getByText('read_file')).toBeInTheDocument()
    expect(screen.getByText('write_file')).toBeInTheDocument()
  })

  it('groups tools by server, in snapshot order', () => {
    render(<CapabilitySnapshotCard content={TWO_SERVERS} />)
    const servers = screen.getAllByRole('term').map(el => el.textContent)
    expect(servers).toEqual(['relay', 'fs'])
    const relayTools = within(screen.getAllByRole('definition')[0]).getAllByRole('listitem').map(el => el.textContent)
    expect(relayTools).toEqual(['list_nodes', 'run_operation'])
  })

  it('marks approval-required tools', () => {
    render(<CapabilitySnapshotCard content={TWO_REAL_TOOLS} />)
    const items = screen.getAllByRole('listitem')
    expect(items.find(li => li.textContent?.startsWith('write_file'))?.textContent).toContain('approval')
    expect(items.find(li => li.textContent?.startsWith('read_file'))?.textContent).not.toContain('approval')
  })

  it('says so when no tools were registered', () => {
    render(<CapabilitySnapshotCard content={[]} />)
    expect(screen.getByText(/0 tools/)).toBeInTheDocument()
    expect(screen.getByText(/no tools were registered/i)).toBeInTheDocument()
  })

  it('carries the anchor id the run header scrolls to', () => {
    render(<CapabilitySnapshotCard content={TWO_REAL_TOOLS} />)
    expect(card().id).toBe('capability-snapshot')
  })

  it('keeps the system prompt behind a toggle', () => {
    render(<CapabilitySnapshotCard content={TWO_REAL_TOOLS} systemPrompt="You are a fleet agent." />)
    expect(screen.queryByText('You are a fleet agent.')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /system prompt/i }))
    expect(screen.getByText('You are a fleet agent.')).toBeInTheDocument()
  })
})

describe('CapabilitySnapshotCard — feedback filtering (legacy array shape)', () => {
  it('shows "2 tools" when snapshot has 2 real tools + ask_operator', () => {
    render(<CapabilitySnapshotCard content={WITH_FEEDBACK} />)
    expect(screen.getByText(/2 tools/)).toBeInTheDocument()
    expect(screen.queryByText(/3 tools/)).not.toBeInTheDocument()
  })

  it('shows feedback indicator when ask_operator is present', () => {
    render(<CapabilitySnapshotCard content={WITH_FEEDBACK} />)
    expect(screen.getByText('Feedback')).toBeInTheDocument()
  })

  it('omits ask_operator from the tool list', () => {
    render(<CapabilitySnapshotCard content={WITH_FEEDBACK} />)
    expect(screen.getByText('read_file')).toBeInTheDocument()
    expect(screen.getByText('write_file')).toBeInTheDocument()
    expect(screen.queryByText('ask_operator')).not.toBeInTheDocument()
    // gleipnir server name must not appear as a server group
    expect(screen.queryByText('gleipnir')).not.toBeInTheDocument()
  })

  it('shows "2 tools" for a snapshot without feedback (no regression)', () => {
    render(<CapabilitySnapshotCard content={TWO_REAL_TOOLS} />)
    expect(screen.getByText(/2 tools/)).toBeInTheDocument()
    expect(screen.queryByText('Feedback')).not.toBeInTheDocument()
  })
})

describe('CapabilitySnapshotCard — feedback filtering (V2 object shape)', () => {
  it('shows "2 tools" for V2 snapshot with ask_operator', () => {
    render(<CapabilitySnapshotCard content={V2_WITH_FEEDBACK} />)
    expect(screen.getByText(/2 tools/)).toBeInTheDocument()
    expect(screen.queryByText(/3 tools/)).not.toBeInTheDocument()
  })

  it('shows feedback indicator (V2 shape)', () => {
    render(<CapabilitySnapshotCard content={V2_WITH_FEEDBACK} />)
    expect(screen.getByText('Feedback')).toBeInTheDocument()
  })

  it('omits ask_operator from the tool list (V2 shape)', () => {
    render(<CapabilitySnapshotCard content={V2_WITH_FEEDBACK} />)
    expect(screen.queryByText('ask_operator')).not.toBeInTheDocument()
    expect(screen.queryByText('gleipnir')).not.toBeInTheDocument()
  })

  it('shows provider and model in the card header (V2 shape)', () => {
    render(<CapabilitySnapshotCard content={V2_WITH_FEEDBACK} />)
    expect(card().textContent).toContain('Anthropic')
    expect(card().textContent).toContain('claude-sonnet-4-6')
  })
})

describe('CapabilitySnapshotCard — outbound headers', () => {
  it('lists header-bearing parameters under the tool', () => {
    render(
      <CapabilitySnapshotCard
        content={[
          {
            server_name: 'fs', tool_name: 'read_file', approval: 'none', timeout: 30, on_timeout: 'fail',
            outbound_headers: [{ parameter: 'tenant', header: 'X-Tenant-Id' }],
          },
          { server_name: 'fs', tool_name: 'write_file', approval: 'none', timeout: 30, on_timeout: 'fail' },
        ]}
      />,
    )
    const note = screen.getByText(/sends outbound headers/i)
    expect(note).toHaveTextContent('read_file sends outbound headers: X-Tenant-Id (from tenant)')
    expect(screen.getAllByText(/outbound headers/i)).toHaveLength(1)
  })

  it('says so when the declaration was rejected', () => {
    render(
      <CapabilitySnapshotCard
        content={[
          {
            server_name: 'fs', tool_name: 'read_file', approval: 'none', timeout: 30, on_timeout: 'fail',
            outbound_headers_rejected: true,
          },
        ]}
      />,
    )
    expect(screen.getByText(/was rejected, so none are sent/i)).toBeInTheDocument()
  })

  it('renders old-format snapshots without any note', () => {
    render(<CapabilitySnapshotCard content={TWO_REAL_TOOLS} />)
    expect(screen.queryByText(/outbound header/i)).not.toBeInTheDocument()
  })
})

describe('CapabilitySnapshotCard — argument enforcement chip', () => {
  const tool = { server_name: 'fs', approval: 'none' as const, timeout: 30, on_timeout: 'fail' }

  it('flags only the grants whose argument checking is reduced', () => {
    render(
      <CapabilitySnapshotCard
        content={[
          { ...tool, tool_name: 'read_file', arg_enforcement: 'exact' },
          { ...tool, tool_name: 'write_file', arg_enforcement: 'schema_uncompilable' },
          { ...tool, tool_name: 'legacy_tool' },
        ]}
      />,
    )
    expect(screen.getAllByText('Reduced argument checking')).toHaveLength(1)
  })
})
