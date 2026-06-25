import { describe, it, expect } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
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

describe('CapabilitySnapshotCard — feedback filtering (legacy array shape)', () => {
  it('shows "2 tools" when snapshot has 2 real tools + ask_operator', () => {
    render(<CapabilitySnapshotCard content={WITH_FEEDBACK} />)
    expect(screen.getByText(/2 tools/)).toBeInTheDocument()
    expect(screen.queryByText(/3 tools/)).not.toBeInTheDocument()
  })

  it('shows feedback indicator when ask_operator is present', async () => {
    render(<CapabilitySnapshotCard content={WITH_FEEDBACK} />)
    // Expand to see the feedback indicator
    fireEvent.click(screen.getByRole('button', { name: /capability snapshot/i }))
    await waitFor(() => {
      expect(screen.getByText('Feedback')).toBeInTheDocument()
    })
  })

  it('omits ask_operator from the tool table', async () => {
    render(<CapabilitySnapshotCard content={WITH_FEEDBACK} />)
    fireEvent.click(screen.getByRole('button', { name: /capability snapshot/i }))
    await waitFor(() => {
      expect(screen.getByRole('table')).toBeInTheDocument()
    })
    expect(screen.getByText('read_file')).toBeInTheDocument()
    expect(screen.getByText('write_file')).toBeInTheDocument()
    expect(screen.queryByText('ask_operator')).not.toBeInTheDocument()
    // gleipnir server name must not appear in the table
    expect(screen.queryByText('gleipnir')).not.toBeInTheDocument()
  })

  it('shows "2 tools" for a snapshot without feedback (no regression)', () => {
    render(<CapabilitySnapshotCard content={TWO_REAL_TOOLS} />)
    expect(screen.getByText(/2 tools/)).toBeInTheDocument()
    expect(screen.queryByText('Feedback')).not.toBeInTheDocument()
  })

  it('does NOT show feedback indicator when ask_operator is absent', async () => {
    render(<CapabilitySnapshotCard content={TWO_REAL_TOOLS} />)
    fireEvent.click(screen.getByRole('button', { name: /capability snapshot/i }))
    await waitFor(() => {
      expect(screen.getByRole('table')).toBeInTheDocument()
    })
    expect(screen.queryByText('Feedback')).not.toBeInTheDocument()
  })
})

describe('CapabilitySnapshotCard — feedback filtering (V2 object shape)', () => {
  it('shows "2 tools" for V2 snapshot with ask_operator', () => {
    render(<CapabilitySnapshotCard content={V2_WITH_FEEDBACK} />)
    expect(screen.getByText(/2 tools/)).toBeInTheDocument()
    expect(screen.queryByText(/3 tools/)).not.toBeInTheDocument()
  })

  it('shows feedback indicator when expanded (V2 shape)', async () => {
    render(<CapabilitySnapshotCard content={V2_WITH_FEEDBACK} />)
    fireEvent.click(screen.getByRole('button', { name: /capability snapshot/i }))
    await waitFor(() => {
      expect(screen.getByText('Feedback')).toBeInTheDocument()
    })
  })

  it('omits ask_operator from table (V2 shape)', async () => {
    render(<CapabilitySnapshotCard content={V2_WITH_FEEDBACK} />)
    fireEvent.click(screen.getByRole('button', { name: /capability snapshot/i }))
    await waitFor(() => {
      expect(screen.getByRole('table')).toBeInTheDocument()
    })
    expect(screen.queryByText('ask_operator')).not.toBeInTheDocument()
    expect(screen.queryByText('gleipnir')).not.toBeInTheDocument()
  })

  it('still shows provider and model in collapsed summary (V2 shape)', () => {
    render(<CapabilitySnapshotCard content={V2_WITH_FEEDBACK} />)
    const summary = screen.getByRole('button', { name: /capability snapshot/i })
    expect(summary.textContent).toContain('Anthropic')
    expect(summary.textContent).toContain('claude-sonnet-4-6')
  })
})
