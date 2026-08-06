import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { queryKeys } from '@/hooks/queryKeys'
import type { ApiMcpServer, ApiMcpTool, ApiPluginInstanceForAudience } from '@/api/types'
import { CapabilitiesSection } from './CapabilitiesSection'
import type { CapabilitiesFormState, AssignedTool, FeedbackFormState } from './types'
import { formStateToYaml } from '@/components/AgentEditor/agentEditorUtils'

// --- Fixtures (mirrored from CapabilitiesSection.stories.tsx) ---

const FIXTURE_SERVERS: ApiMcpServer[] = [
  {
    id: 'srv-1',
    name: 'Filesystem Tools',
    url: 'http://mcp-filesystem:8080',
    last_discovered_at: '2026-03-10T12:00:00Z',
    has_drift: false,
    created_at: '2026-03-01T00:00:00Z',
    is_arcade_gateway: false,
    trust_tier: 'external' as const,
    plugin_instance_id: null,
    editable: true,
    protocol_version: '2026-07-28',
  },
  {
    id: 'srv-2',
    name: 'GitHub Tools',
    url: 'http://mcp-github:8080',
    last_discovered_at: '2026-03-10T12:00:00Z',
    has_drift: false,
    created_at: '2026-03-05T00:00:00Z',
    is_arcade_gateway: false,
    trust_tier: 'external' as const,
    plugin_instance_id: null,
    editable: true,
    protocol_version: null,
  },
]

const FIXTURE_TOOLS_SRV1: ApiMcpTool[] = [
  {
    id: 'tool-1',
    server_id: 'srv-1',
    name: 'read_file',
    description: 'Read the contents of a file at the given path',
    input_schema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] },
    enabled: true,
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
    enabled: true,
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
    enabled: true,
  },
]

function makeQueryClient(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.servers.all, FIXTURE_SERVERS)
  qc.setQueryData(queryKeys.servers.toolsAll('srv-1'), FIXTURE_TOOLS_SRV1)
  qc.setQueryData(queryKeys.servers.toolsAll('srv-2'), FIXTURE_TOOLS_SRV2)
  // Always seed an empty pluginInstances entry so the usePluginInstancesForAudience
  // hook doesn't fire a background fetch (which would clear cached state mid-assertion).
  qc.setQueryData(queryKeys.admin.pluginInstances, [])
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
        source: 'mcp',
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
    qc.setQueryData(queryKeys.servers.toolsAll('srv-1'), FIXTURE_TOOLS_SRV1)
    qc.setQueryData(queryKeys.servers.toolsAll('srv-2'), FIXTURE_TOOLS_SRV2)
    // Seed empty pluginInstances to prevent background refetch.
    qc.setQueryData(queryKeys.admin.pluginInstances, [])
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
  it('renders "Feedback request" heading', () => {
    renderSection()
    expect(screen.getByText('Feedback request')).toBeInTheDocument()
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
        source: 'mcp',
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
        source: 'mcp',
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
        source: 'mcp',
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
        source: 'mcp',
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
        source: 'mcp',
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
        source: 'mcp',
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
        source: 'mcp',
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

describe('CapabilitiesSection — disabled tool warning', () => {
  // A tool fixture that is disabled on the server side.
  const DISABLED_TOOL: ApiMcpTool = {
    id: 'tool-disabled',
    server_id: 'srv-1',
    name: 'write_file',
    description: 'Write content to a file at the given path',
    input_schema: { type: 'object' },
    enabled: false,
  }

  function makeDisabledQueryClient(): QueryClient {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
    qc.setQueryData(queryKeys.servers.all, FIXTURE_SERVERS)
    // srv-1 has one disabled tool (write_file)
    qc.setQueryData(queryKeys.servers.toolsAll('srv-1'), [
      ...FIXTURE_TOOLS_SRV1.filter(t => t.name !== 'write_file'),
      DISABLED_TOOL,
    ])
    qc.setQueryData(queryKeys.servers.toolsAll('srv-2'), FIXTURE_TOOLS_SRV2)
    // Seed empty pluginInstances to prevent background refetch.
    qc.setQueryData(queryKeys.admin.pluginInstances, [])
    return qc
  }

  it('shows disabled badge on a granted tool that is currently disabled', async () => {
    const assignedTools: AssignedTool[] = [
      {
        toolId: 'tool-disabled', // UUID-based toolId (added via picker)
        serverId: 'srv-1',
        serverName: 'Filesystem Tools',
        name: 'write_file',
        description: 'Write content to a file',
        source: 'mcp',
        approvalRequired: false,
        approvalTimeout: '',
      },
    ]

    render(
      <QueryClientProvider client={makeDisabledQueryClient()}>
        <ControlledCapabilitiesSection initialTools={assignedTools} />
      </QueryClientProvider>,
    )

    // The badge has a tooltip (title) distinguishing it from the feedback "Disabled" label.
    await waitFor(() => {
      const badge = screen.getByTitle(/Tool is disabled/)
      expect(badge).toBeInTheDocument()
      expect(badge).toHaveTextContent('Disabled')
    })

    // The row wrapping element should have data-disabled="true"
    const row = document.querySelector('[data-disabled="true"]')
    expect(row).not.toBeNull()
  })

  it('shows disabled badge when toolId is the YAML dot-notation composite key', async () => {
    const assignedTools: AssignedTool[] = [
      {
        // yamlToFormState sets toolId to the full dot-notation string
        toolId: 'Filesystem Tools.write_file',
        serverId: 'Filesystem Tools',
        serverName: 'Filesystem Tools',
        name: 'write_file',
        description: 'Write content to a file',
        source: 'mcp',
        approvalRequired: false,
        approvalTimeout: '',
      },
    ]

    render(
      <QueryClientProvider client={makeDisabledQueryClient()}>
        <ControlledCapabilitiesSection initialTools={assignedTools} />
      </QueryClientProvider>,
    )

    await waitFor(() => {
      expect(screen.getByTitle(/Tool is disabled/)).toBeInTheDocument()
    })
  })

  it('does not show disabled badge for a granted tool that is enabled', async () => {
    const assignedTools: AssignedTool[] = [
      {
        toolId: 'tool-1', // read_file is enabled in the fixture
        serverId: 'srv-1',
        serverName: 'Filesystem Tools',
        name: 'read_file',
        description: 'Read the contents of a file',
        source: 'mcp',
        approvalRequired: false,
        approvalTimeout: '',
      },
    ]

    render(
      <QueryClientProvider client={makeDisabledQueryClient()}>
        <ControlledCapabilitiesSection initialTools={assignedTools} />
      </QueryClientProvider>,
    )

    // Allow any async updates to settle
    await waitFor(() => {
      expect(screen.getByText('Filesystem Tools.read_file')).toBeInTheDocument()
    })

    // The disabled badge has a distinctive title attribute; the feedback label "Disabled" is separate.
    expect(screen.queryByTitle(/Tool is disabled/)).not.toBeInTheDocument()
    expect(document.querySelector('[data-disabled="true"]')).toBeNull()
  })
})

// --- Plugin tool tests ---

const FIXTURE_SLACK_INSTANCE: ApiPluginInstanceForAudience = {
  id: 'inst-slack-1',
  plugin_id: 'plugin-slack',
  instance_name: 'slack-e2e',
  state: 'healthy',
  implements_notify: true,
  implements_request: true,
  config_schema: null,
  version: 1,
  tools: [
    { name: 'send_message', description: 'Send a message to a Slack channel' },
    { name: 'list_channels', description: 'List all accessible Slack channels' },
  ],
}

function makePluginQueryClient(): QueryClient {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  qc.setQueryData(queryKeys.servers.all, FIXTURE_SERVERS)
  qc.setQueryData(queryKeys.servers.toolsAll('srv-1'), FIXTURE_TOOLS_SRV1)
  qc.setQueryData(queryKeys.servers.toolsAll('srv-2'), FIXTURE_TOOLS_SRV2)
  qc.setQueryData(queryKeys.admin.pluginInstances, [FIXTURE_SLACK_INSTANCE])
  return qc
}

describe('CapabilitiesSection — plugin tools', () => {
  it('(a) picker lists the plugin tool with plugin:slack-e2e label and displayName slack-e2e.send_message', async () => {
    render(
      <QueryClientProvider client={makePluginQueryClient()}>
        <ControlledCapabilitiesSection />
      </QueryClientProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: '+ Add tool from registry' }))

    await waitFor(() => {
      expect(screen.getByText('slack-e2e.send_message')).toBeInTheDocument()
    })

    // Source label for plugin tool should be present in the picker
    expect(screen.getAllByText('plugin:slack-e2e').length).toBeGreaterThan(0)
  })

  it('(b) selecting a plugin tool stores AssignedTool with toolId === "slack-e2e.send_message" and source: "plugin"', async () => {
    const onChange = vi.fn()
    render(
      <QueryClientProvider client={makePluginQueryClient()}>
        <ControlledCapabilitiesSection onChange={onChange} />
      </QueryClientProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: '+ Add tool from registry' }))

    await waitFor(() => {
      expect(screen.getByText('slack-e2e.send_message')).toBeInTheDocument()
    })

    fireEvent.click(screen.getByText('slack-e2e.send_message'))

    await waitFor(() => {
      expect(onChange).toHaveBeenCalledTimes(1)
    })

    const lastCall = onChange.mock.calls[0][0] as CapabilitiesFormState
    expect(lastCall.tools).toHaveLength(1)
    const tool = lastCall.tools[0]
    expect(tool.toolId).toBe('slack-e2e.send_message')
    expect(tool.source).toBe('plugin')
    expect(tool.serverName).toBe('slack-e2e')
    expect(tool.name).toBe('send_message')
  })

  it('(c) loading a policy with an existing slack-e2e.send_message grant shows plugin source label', async () => {
    const assignedTools: AssignedTool[] = [
      {
        toolId: 'slack-e2e.send_message',
        serverId: 'slack-e2e',
        serverName: 'slack-e2e',
        name: 'send_message',
        description: 'Send a message to a Slack channel',
        source: 'mcp', // parse-time default; reconciled at render
        approvalRequired: false,
        approvalTimeout: '',
      },
    ]

    render(
      <QueryClientProvider client={makePluginQueryClient()}>
        <ControlledCapabilitiesSection initialTools={assignedTools} />
      </QueryClientProvider>,
    )

    // Wait for plugin instances to be resolved and source label to appear
    await waitFor(() => {
      expect(screen.getByText('slack-e2e.send_message')).toBeInTheDocument()
    })

    // The plugin source label should appear on the assigned row
    expect(screen.getByText('plugin:slack-e2e')).toBeInTheDocument()
  })

  it('(d) round-trip: plugin-sourced AssignedTool emits tool: slack-e2e.send_message unchanged', () => {
    const pluginTool: AssignedTool = {
      toolId: 'slack-e2e.send_message',
      serverId: 'inst-slack-1',
      serverName: 'slack-e2e',
      name: 'send_message',
      description: 'Send a message to a Slack channel',
      source: 'plugin',
      approvalRequired: false,
      approvalTimeout: '',
    }
    // Build a minimal FormState that includes the plugin tool
    const state = {
      identity: { name: 'p', description: '', folder: '' },
      trigger: { type: 'manual' as const },
      capabilities: {
        tools: [pluginTool],
        feedback: { enabled: false, timeout: '', onTimeout: 'fail' },
      },
      audience: { name: '' },
      task: { task: 'do things' },
      limits: { max_tokens_per_run: 20000, max_tool_calls_per_run: 50 },
      concurrency: { concurrency: 'skip' as const, queueDepth: 0 },
      model: { provider: 'anthropic', model: 'claude-3-5-haiku-latest' },
    }
    const yaml = formStateToYaml(state)
    expect(yaml).toContain('tool: slack-e2e.send_message')
  })

  it('(e) stale grant gone-instance.foo renders unknown badge and is NOT dropped', async () => {
    const assignedTools: AssignedTool[] = [
      {
        toolId: 'gone-instance.foo',
        serverId: 'gone-instance',
        serverName: 'gone-instance',
        name: 'foo',
        description: '',
        source: 'mcp',
        approvalRequired: false,
        approvalTimeout: '',
      },
    ]

    render(
      <QueryClientProvider client={makePluginQueryClient()}>
        <ControlledCapabilitiesSection initialTools={assignedTools} />
      </QueryClientProvider>,
    )

    // Row is present (grant is not dropped)
    await waitFor(() => {
      expect(screen.getByText('gone-instance.foo')).toBeInTheDocument()
    })

    // Unknown source badge is shown
    expect(screen.getByTitle(/Source server or plugin instance not found/)).toBeInTheDocument()
    expect(screen.getByText('Unknown source')).toBeInTheDocument()
  })
})
