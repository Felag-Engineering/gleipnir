import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { queryKeys } from '@/hooks/queryKeys'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import { CapabilitiesSection } from './CapabilitiesSection'
import type { CapabilitiesFormState, AssignedTool, FeedbackFormState } from './types'

// --- Fixtures (mirrored from CapabilitiesSection.stories.tsx) ---

const FIXTURE_SERVERS: ApiMcpServer[] = [
  {
    id: 'srv-1',
    name: 'Filesystem Tools',
    url: 'http://mcp-filesystem:8080',
    last_discovered_at: '2026-03-10T12:00:00Z',
    has_drift: false,
    created_at: '2026-03-01T00:00:00Z',
  },
  {
    id: 'srv-2',
    name: 'GitHub Tools',
    url: 'http://mcp-github:8080',
    last_discovered_at: '2026-03-10T12:00:00Z',
    has_drift: false,
    created_at: '2026-03-05T00:00:00Z',
  },
]

const FIXTURE_TOOLS_SRV1: ApiMcpTool[] = [
  {
    id: 'tool-1',
    server_id: 'srv-1',
    name: 'read_file',
    description: 'Read the contents of a file at the given path',
    input_schema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] },
  },
  {
    id: 'tool-2',
    server_id: 'srv-1',
    name: 'write_file',
    description: 'Write content to a file at the given path',
    input_schema: {
      type: 'object',
      properties: { path: { type: 'string' }, content: { type: 'string' } },
      required: ['path', 'content'],
    },
  },
]

const FIXTURE_TOOLS_SRV2: ApiMcpTool[] = [
  {
    id: 'tool-4',
    server_id: 'srv-2',
    name: 'create_issue',
    description: 'Create a new GitHub issue in a repository',
    input_schema: {
      type: 'object',
      properties: { repo: { type: 'string' }, title: { type: 'string' } },
      required: ['repo', 'title'],
    },
  },
]

function makeQueryClient(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.servers.all, FIXTURE_SERVERS)
  qc.setQueryData(queryKeys.servers.tools('srv-1'), FIXTURE_TOOLS_SRV1)
  qc.setQueryData(queryKeys.servers.tools('srv-2'), FIXTURE_TOOLS_SRV2)
  return qc
}

const DEFAULT_FEEDBACK: FeedbackFormState = { enabled: false, timeout: '', onTimeout: 'fail' }

// Controlled wrapper so we can track onChange calls and reflect state changes
function ControlledCapabilitiesSection({
  initialTools = [],
  initialFeedback = DEFAULT_FEEDBACK,
  onChange,
}: {
  initialTools?: AssignedTool[]
  initialFeedback?: FeedbackFormState
  onChange?: (next: CapabilitiesFormState) => void
}) {
  const [value, setValue] = useState<CapabilitiesFormState>({
    tools: initialTools,
    feedback: initialFeedback,
  })

  function handleChange(next: CapabilitiesFormState) {
    setValue(next)
    onChange?.(next)
  }

  return <CapabilitiesSection value={value} onChange={handleChange} />
}

function renderSection(
  initialTools: AssignedTool[] = [],
  onChange?: (next: CapabilitiesFormState) => void,
) {
  return render(
    <QueryClientProvider client={makeQueryClient()}>
      <ControlledCapabilitiesSection initialTools={initialTools} onChange={onChange} />
    </QueryClientProvider>,
  )
}

// --- Tests ---

describe('CapabilitiesSection — tool picker add', () => {
  it('clicking a search result adds it to the assigned tools list', async () => {
    renderSection()

    // Initially empty
    expect(screen.getByText(/no tools added yet/i)).toBeInTheDocument()

    // Open search panel
    fireEvent.click(screen.getByRole('button', { name: '+ Add tool from registry' }))

    // Search panel appears
    await waitFor(() => {
      expect(screen.getByPlaceholderText(/filter by tool name/i)).toBeInTheDocument()
    })

    // Tool results are listed (query data seeded into QueryClient)
    await waitFor(() => {
      expect(screen.getByText('Filesystem Tools.read_file')).toBeInTheDocument()
    })

    // Click the first result
    fireEvent.click(screen.getByText('Filesystem Tools.read_file'))

    // Tool appears in assigned list
    await waitFor(() => {
      expect(screen.getByText('Filesystem Tools.read_file')).toBeInTheDocument()
    })

    // Empty state should be gone
    expect(screen.queryByText(/no tools added yet/i)).toBeNull()

    // Search panel should be closed
    expect(screen.queryByPlaceholderText(/filter by tool name/i)).toBeNull()
  })
})

describe('CapabilitiesSection — tool picker remove', () => {
  it('clicking the remove button removes the tool from the list', async () => {
    const assignedTools: AssignedTool[] = [
      {
        toolId: 'tool-1',
        serverId: 'srv-1',
        serverName: 'Filesystem Tools',
        name: 'read_file',
        description: 'Read the contents of a file at the given path',
        approvalRequired: false,
        approvalTimeout: '',
      },
    ]

    renderSection(assignedTools)

    // Tool is in the list
    expect(screen.getByText('Filesystem Tools.read_file')).toBeInTheDocument()

    // Click the remove button
    const removeBtn = screen.getByRole('button', { name: /remove filesystem tools\.read_file/i })
    fireEvent.click(removeBtn)

    // Tool is gone
    await waitFor(() => {
      expect(screen.queryByText('Filesystem Tools.read_file')).toBeNull()
    })

    // Empty state shown
    expect(screen.getByText(/no tools added yet/i)).toBeInTheDocument()
  })
})

describe('CapabilitiesSection — tool picker search filter', () => {
  // Use staleTime: Infinity so seeded QueryClient data is never refetched.
  // The component's useQueries would otherwise fire background fetches that
  // fail (no MSW handler) and clear the cached tool list before assertions run.
  function makeStaleQueryClient(): QueryClient {
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    })
    qc.setQueryData(queryKeys.servers.all, FIXTURE_SERVERS)
    qc.setQueryData(queryKeys.servers.tools('srv-1'), FIXTURE_TOOLS_SRV1)
    qc.setQueryData(queryKeys.servers.tools('srv-2'), FIXTURE_TOOLS_SRV2)
    return qc
  }

  it('filters results by tool name as user types', async () => {
    render(
      <QueryClientProvider client={makeStaleQueryClient()}>
        <ControlledCapabilitiesSection />
      </QueryClientProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: '+ Add tool from registry' }))

    await waitFor(() => {
      expect(screen.getByPlaceholderText(/filter by tool name/i)).toBeInTheDocument()
    })

    // Both tools from srv-1 are visible initially
    await waitFor(() => {
      expect(screen.getByText('Filesystem Tools.read_file')).toBeInTheDocument()
      expect(screen.getByText('Filesystem Tools.write_file')).toBeInTheDocument()
    })

    // Type to filter — should only show write_file
    fireEvent.change(screen.getByPlaceholderText(/filter by tool name/i), {
      target: { value: 'write' },
    })

    await waitFor(() => {
      expect(screen.getByText('Filesystem Tools.write_file')).toBeInTheDocument()
      expect(screen.queryByText('Filesystem Tools.read_file')).not.toBeInTheDocument()
    })
  })

  it('shows "No tools match" when filter has no results', async () => {
    render(
      <QueryClientProvider client={makeStaleQueryClient()}>
        <ControlledCapabilitiesSection />
      </QueryClientProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: '+ Add tool from registry' }))

    await waitFor(() => {
      expect(screen.getByPlaceholderText(/filter by tool name/i)).toBeInTheDocument()
    })

    fireEvent.change(screen.getByPlaceholderText(/filter by tool name/i), {
      target: { value: 'xyznonexistent' },
    })

    await waitFor(() => {
      expect(screen.getByText(/no tools match/i)).toBeInTheDocument()
    })
  })
})

describe('CapabilitiesSection — feedback section', () => {
  it('renders "Feedback" heading', () => {
    renderSection()
    expect(screen.getByText('Feedback')).toBeInTheDocument()
  })

  it('toggling the feedback switch calls onChange with feedback.enabled flipped AND tools preserved', async () => {
    const onChange = vi.fn()
    const initialTools: AssignedTool[] = [
      {
        toolId: 'tool-1',
        serverId: 'srv-1',
        serverName: 'Filesystem Tools',
        name: 'read_file',
        description: 'Read a file',
        approvalRequired: false,
        approvalTimeout: '',
      },
    ]
    render(
      <QueryClientProvider client={makeQueryClient()}>
        <ControlledCapabilitiesSection
          initialTools={initialTools}
          initialFeedback={{ enabled: false, timeout: '', onTimeout: 'fail' }}
          onChange={onChange}
        />
      </QueryClientProvider>,
    )

    // Click the feedback toggle (role="switch" — there are two: one for feedback, approval
    // toggle is inside individual tool rows; here we look for the feedback-specific one
    // by filtering switches that are NOT inside tool rows)
    const switches = screen.getAllByRole('switch')
    // The feedback switch is the one not associated with a specific tool name,
    // i.e. the last one or the one outside the tool list. Since there's one tool
    // with an approval toggle and one feedback toggle, find the feedback one by title.
    const feedbackSwitch = switches.find(s =>
      s.getAttribute('title')?.includes('Feedback') ||
      s.getAttribute('title')?.includes('feedback')
    )
    expect(feedbackSwitch).toBeDefined()
    fireEvent.click(feedbackSwitch!)

    await waitFor(() => {
      expect(onChange).toHaveBeenCalledTimes(1)
    })

    const lastCall = onChange.mock.calls[0][0] as CapabilitiesFormState
    // feedback.enabled must be flipped
    expect(lastCall.feedback.enabled).toBe(true)
    // tools must be preserved
    expect(lastCall.tools).toHaveLength(1)
    expect(lastCall.tools[0].toolId).toBe('tool-1')
  })

  it('shows timeout input when feedback is enabled', () => {
    render(
      <QueryClientProvider client={makeQueryClient()}>
        <ControlledCapabilitiesSection
          initialFeedback={{ enabled: true, timeout: '', onTimeout: 'fail' }}
        />
      </QueryClientProvider>,
    )
    expect(screen.getByPlaceholderText('e.g. 30m')).toBeInTheDocument()
  })

  it('does not show timeout input when feedback is disabled', () => {
    renderSection()
    expect(screen.queryByPlaceholderText('e.g. 30m')).toBeNull()
  })

  it('typing in timeout input calls onChange with updated feedback.timeout', async () => {
    const onChange = vi.fn()
    render(
      <QueryClientProvider client={makeQueryClient()}>
        <ControlledCapabilitiesSection
          initialFeedback={{ enabled: true, timeout: '', onTimeout: 'fail' }}
          onChange={onChange}
        />
      </QueryClientProvider>,
    )

    const timeoutInput = screen.getByPlaceholderText('e.g. 30m')
    fireEvent.change(timeoutInput, { target: { value: '1h' } })

    await waitFor(() => {
      expect(onChange).toHaveBeenCalledTimes(1)
    })

    const lastCall = onChange.mock.calls[0][0] as CapabilitiesFormState
    expect(lastCall.feedback.timeout).toBe('1h')
  })
})

describe('CapabilitiesSection — approval toggle', () => {
  it('toggling approval on a tool calls onChange with approvalRequired flipped', async () => {
    const onChange = vi.fn()
    const assignedTools: AssignedTool[] = [
      {
        toolId: 'tool-2',
        serverId: 'srv-1',
        serverName: 'Filesystem Tools',
        name: 'write_file',
        description: 'Write content to a file at the given path',
        approvalRequired: false,
        approvalTimeout: '',
      },
    ]

    renderSection(assignedTools, onChange)

    // Find the approval toggle switch by its title attribute (distinct from the feedback toggle)
    const toggle = screen.getByTitle('No approval required — click to enable')
    expect(toggle).toHaveAttribute('aria-checked', 'false')

    // Click it
    fireEvent.click(toggle)

    await waitFor(() => {
      expect(onChange).toHaveBeenCalledTimes(1)
    })

    const lastCall = onChange.mock.calls[0][0] as CapabilitiesFormState
    expect(lastCall.tools[0].approvalRequired).toBe(true)
  })

  it('toggling approval off sets approvalRequired to false', async () => {
    const onChange = vi.fn()
    const assignedTools: AssignedTool[] = [
      {
        toolId: 'tool-2',
        serverId: 'srv-1',
        serverName: 'Filesystem Tools',
        name: 'write_file',
        description: 'Write content to a file at the given path',
        approvalRequired: true,
        approvalTimeout: '',
      },
    ]

    renderSection(assignedTools, onChange)

    const toggle = screen.getByTitle('Approval required — click to disable')
    expect(toggle).toHaveAttribute('aria-checked', 'true')

    fireEvent.click(toggle)

    await waitFor(() => {
      expect(onChange).toHaveBeenCalledTimes(1)
    })

    const lastCall = onChange.mock.calls[0][0] as CapabilitiesFormState
    expect(lastCall.tools[0].approvalRequired).toBe(false)
  })
})

describe('CapabilitiesSection — approval timeout input', () => {
  it('shows approval timeout input when approvalRequired is true', () => {
    const assignedTools: AssignedTool[] = [
      {
        toolId: 'tool-2',
        serverId: 'srv-1',
        serverName: 'Filesystem Tools',
        name: 'write_file',
        description: 'Write content to a file at the given path',
        approvalRequired: true,
        approvalTimeout: '',
      },
    ]
    renderSection(assignedTools)
    expect(screen.getByPlaceholderText('e.g. 30m')).toBeInTheDocument()
  })

  it('does not show approval timeout input when approvalRequired is false', () => {
    const assignedTools: AssignedTool[] = [
      {
        toolId: 'tool-2',
        serverId: 'srv-1',
        serverName: 'Filesystem Tools',
        name: 'write_file',
        description: 'Write content to a file at the given path',
        approvalRequired: false,
        approvalTimeout: '',
      },
    ]
    renderSection(assignedTools)
    // No timeout input when approval is off
    expect(screen.queryByPlaceholderText('e.g. 30m')).toBeNull()
  })

  it('typing in the approval timeout input calls onChange with updated approvalTimeout', async () => {
    const onChange = vi.fn()
    const assignedTools: AssignedTool[] = [
      {
        toolId: 'tool-2',
        serverId: 'srv-1',
        serverName: 'Filesystem Tools',
        name: 'write_file',
        description: 'Write content to a file at the given path',
        approvalRequired: true,
        approvalTimeout: '',
      },
    ]

    renderSection(assignedTools, onChange)

    const timeoutInput = screen.getByPlaceholderText('e.g. 30m')
    fireEvent.change(timeoutInput, { target: { value: '30m' } })

    await waitFor(() => {
      expect(onChange).toHaveBeenCalledTimes(1)
    })

    const lastCall = onChange.mock.calls[0][0] as CapabilitiesFormState
    expect(lastCall.tools[0].approvalTimeout).toBe('30m')
  })

  it('toggling approval off preserves approvalTimeout in state (state preservation rule)', async () => {
    // handleToggleApproval does NOT reset approvalTimeout when toggling off.
    // The serializer omits it from YAML when approval is off — this test verifies
    // the state side: the timeout value survives the toggle so re-enabling approval
    // shows the previously typed timeout.
    const onChange = vi.fn()
    const assignedTools: AssignedTool[] = [
      {
        toolId: 'tool-2',
        serverId: 'srv-1',
        serverName: 'Filesystem Tools',
        name: 'write_file',
        description: 'Write content to a file at the given path',
        approvalRequired: true,
        approvalTimeout: '30m',
      },
    ]

    renderSection(assignedTools, onChange)

    const toggle = screen.getByTitle('Approval required — click to disable')
    fireEvent.click(toggle)

    await waitFor(() => {
      expect(onChange).toHaveBeenCalledTimes(1)
    })

    const lastCall = onChange.mock.calls[0][0] as CapabilitiesFormState
    expect(lastCall.tools[0].approvalRequired).toBe(false)
    // approvalTimeout is preserved even though approval is off
    expect(lastCall.tools[0].approvalTimeout).toBe('30m')
  })
})
