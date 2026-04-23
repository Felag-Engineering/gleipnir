import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { AgentEditorPage } from './AgentEditorPage'
import { yamlToFormState, formStateToYaml } from '@/components/AgentEditor/agentEditorUtils'
import { ApiError } from '@/api/fetch'

// --- Mocks ---

vi.mock('@/hooks/queries/policies')
vi.mock('@/hooks/mutations/policies')
vi.mock('@/hooks/queries/servers')

// These imports must come after vi.mock calls
import { usePolicy, usePolicies, useWebhookSecret } from '@/hooks/queries/policies'
import { useSavePolicy, useDeletePolicy, useTriggerPolicy, usePausePolicy, useResumePolicy, useRotateWebhookSecret } from '@/hooks/mutations/policies'
import { useMcpServers } from '@/hooks/queries/servers'

// --- Fixtures ---

const WEBHOOK_YAML = `name: webhook-policy
description: A webhook triggered policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: filesystem.read_file
    - tool: filesystem.write_file
      approval: required
agent:
  task: |
    Process the incoming webhook payload.
  limits:
    max_tokens_per_run: 10000
    max_tool_calls_per_run: 25
  concurrency: skip
`

// VALID_YAML has all required fields so validateFormState returns no issues.
const VALID_YAML = `name: my-agent
trigger:
  type: webhook
model:
  provider: anthropic
  name: claude-opus-4-5
capabilities:
  tools:
    - tool: filesystem.read_file
agent:
  task: Do the thing.
  limits:
    max_tokens_per_run: 10000
    max_tool_calls_per_run: 50
  concurrency: skip
`

const MANUAL_YAML = `name: manual-policy
trigger:
  type: manual
capabilities:
  tools: []
agent:
  task: Run on demand.
  limits:
    max_tokens_per_run: 5000
    max_tool_calls_per_run: 10
  concurrency: queue
`

const SCHEDULED_YAML = `name: scheduled-policy
trigger:
  type: scheduled
  fire_at:
    - '2030-01-01T09:00:00Z'
    - '2030-06-15T12:00:00Z'
capabilities:
  tools:
    - tool: github.list_issues
agent:
  task: Check for new items.
  limits:
    max_tokens_per_run: 8000
    max_tool_calls_per_run: 20
  concurrency: skip
`

// --- Helpers ---

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderEditor(path = '/agents/new', queryClient = makeQueryClient()) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/agents/new" element={<AgentEditorPage />} />
          <Route path="/agents/:id" element={<AgentEditorPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

// mockHooksDefault sets up all mocks consumed by the always-rendered components.
// EditorTopBar, TriggerSection, and CapabilitiesSection always mount, so
// useTriggerPolicy, usePausePolicy, useResumePolicy, useWebhookSecret, and
// useRotateWebhookSecret mocks must remain even if individual tests don't
// exercise those paths directly.
function mockHooksDefault() {
  vi.mocked(usePolicy).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as ReturnType<typeof usePolicy>)

  const mutateAsync = vi.fn().mockResolvedValue({
    id: 'saved-id',
    name: 'test',
    yaml: WEBHOOK_YAML,
    trigger_type: 'webhook',
    folder: '',
    created_at: '',
    updated_at: '',
    warnings: [],
  })

  vi.mocked(useSavePolicy).mockReturnValue({
    mutateAsync,
    isPending: false,
  } as unknown as ReturnType<typeof useSavePolicy>)

  vi.mocked(useDeletePolicy).mockReturnValue({
    mutateAsync: vi.fn().mockResolvedValue(undefined),
    isPending: false,
  } as unknown as ReturnType<typeof useDeletePolicy>)

  vi.mocked(useTriggerPolicy).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
    error: null,
  } as unknown as ReturnType<typeof useTriggerPolicy>)

  vi.mocked(usePausePolicy).mockReturnValue({
    mutateAsync: vi.fn().mockResolvedValue(undefined),
    isPending: false,
  } as unknown as ReturnType<typeof usePausePolicy>)

  vi.mocked(useResumePolicy).mockReturnValue({
    mutateAsync: vi.fn().mockResolvedValue(undefined),
    isPending: false,
  } as unknown as ReturnType<typeof useResumePolicy>)

  vi.mocked(useMcpServers).mockReturnValue({
    data: [],
    isLoading: false,
  } as unknown as ReturnType<typeof useMcpServers>)

  vi.mocked(usePolicies).mockReturnValue({
    data: [],
    status: 'success',
  } as unknown as ReturnType<typeof usePolicies>)

  vi.mocked(useWebhookSecret).mockReturnValue({
    data: undefined,
    isLoading: false,
  } as unknown as ReturnType<typeof useWebhookSecret>)

  vi.mocked(useRotateWebhookSecret).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useRotateWebhookSecret>)

  return { mutateAsync }
}

// --- Tests ---

describe('AgentEditorUtils — formStateToYaml approval output', () => {
  it('emits approval: required in YAML when approvalRequired is true on a tool', () => {
    const state = yamlToFormState(WEBHOOK_YAML)!
    // filesystem.write_file already has approval: required in WEBHOOK_YAML
    // Clone and set approvalRequired = true explicitly on all tools; spread feedback to preserve it
    const modified = {
      ...state,
      capabilities: {
        ...state.capabilities,
        tools: state.capabilities.tools.map(t => ({ ...t, approvalRequired: true })),
      },
    }
    const yaml = formStateToYaml(modified)
    expect(yaml).toContain('approval: required')
  })

  it('does not emit approval in YAML when approvalRequired is false on a tool', () => {
    const state = yamlToFormState(WEBHOOK_YAML)!
    const modified = {
      ...state,
      capabilities: {
        ...state.capabilities,
        tools: state.capabilities.tools.map(t => ({ ...t, approvalRequired: false })),
      },
    }
    const yaml = formStateToYaml(modified)
    expect(yaml).not.toContain('approval: required')
  })
})

describe('AgentEditorUtils — YAML ↔ form round-trip (pure functions)', () => {
  it.each([
    ['webhook', WEBHOOK_YAML],
    ['manual', MANUAL_YAML],
    ['scheduled', SCHEDULED_YAML],
  ])('round-trips a %s policy preserving identity, trigger type, and tool server names', (_label, yaml) => {
    const parsed = yamlToFormState(yaml)
    expect(parsed).not.toBeNull()

    const roundTripped = yamlToFormState(formStateToYaml(parsed!))
    expect(roundTripped).not.toBeNull()

    expect(roundTripped!.identity.name).toBe(parsed!.identity.name)
    expect(roundTripped!.trigger.type).toBe(parsed!.trigger.type)
    expect(roundTripped!.limits.max_tokens_per_run).toBe(parsed!.limits.max_tokens_per_run)
    expect(roundTripped!.limits.max_tool_calls_per_run).toBe(parsed!.limits.max_tool_calls_per_run)
    expect(roundTripped!.concurrency.concurrency).toBe(parsed!.concurrency.concurrency)

    // Tools survive — compare server name and tool name (description is lossy)
    const origTools = parsed!.capabilities.tools
    const rtTools = roundTripped!.capabilities.tools
    expect(rtTools).toHaveLength(origTools.length)
    origTools.forEach((t, i) => {
      expect(rtTools[i].serverName).toBe(t.serverName)
      expect(rtTools[i].name).toBe(t.name)
      expect(rtTools[i].approvalRequired).toBe(t.approvalRequired)
    })
  })

  it('webhook trigger loses no fields after round-trip', () => {
    const parsed = yamlToFormState(WEBHOOK_YAML)!
    expect(parsed.trigger.type).toBe('webhook')
    const rt = yamlToFormState(formStateToYaml(parsed))!
    expect(rt.trigger.type).toBe('webhook')
  })

  it('scheduled fire_at timestamps are preserved after round-trip', () => {
    const parsed = yamlToFormState(SCHEDULED_YAML)!
    if (parsed.trigger.type !== 'scheduled') throw new Error('expected scheduled')
    const rt = yamlToFormState(formStateToYaml(parsed))!
    if (rt.trigger.type !== 'scheduled') throw new Error('expected scheduled after round-trip')
    expect(rt.trigger.fireAt).toEqual(parsed.trigger.fireAt)
  })

  it('cron trigger type is parsed as cron (first-class trigger)', () => {
    const cronParsed = yamlToFormState('name: x\ntrigger:\n  type: cron\n  cron_expr: "0 9 * * 1"\ncapabilities:\n  tools: []\nagent:\n  task: t\n')
    expect(cronParsed?.trigger.type).toBe('cron')
    expect((cronParsed?.trigger as { cronExpr: string }).cronExpr).toBe('0 9 * * 1')
  })

  it('poll trigger type is parsed as poll (first-class trigger)', () => {
    const pollParsed = yamlToFormState('name: x\ntrigger:\n  type: poll\n  interval: 5m\n  checks:\n    - tool: s.t\n      path: "$.status"\n      equals: ok\ncapabilities:\n  tools: []\nagent:\n  task: t\n')
    expect(pollParsed?.trigger.type).toBe('poll')
  })
})

describe('AgentEditorPage — dirty state and save', () => {
  it('editing the name field sets isDirty; saving clears it', async () => {
    // Use an existing-policy route so save does not navigate away.
    // VALID_YAML passes all client-side validation so the save button works.
    vi.mocked(usePolicy).mockReturnValue({
      data: {
        id: 'existing-id',
        name: 'my-agent',
        trigger_type: 'webhook',
        folder: '',
        yaml: VALID_YAML,
        created_at: '',
        updated_at: '',
      },
      status: 'success',
    } as ReturnType<typeof usePolicy>)

    vi.mocked(usePolicies).mockReturnValue({
      data: [],
      status: 'success',
    } as unknown as ReturnType<typeof usePolicies>)

    vi.mocked(useWebhookSecret).mockReturnValue({
      data: undefined,
      isLoading: false,
    } as unknown as ReturnType<typeof useWebhookSecret>)

    vi.mocked(useRotateWebhookSecret).mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useRotateWebhookSecret>)

    const mutateAsync = vi.fn().mockResolvedValue({
      id: 'existing-id',
      name: 'my-agent',
      yaml: VALID_YAML,
      trigger_type: 'webhook',
      folder: '',
      created_at: '',
      updated_at: '',
      warnings: [],
    })

    vi.mocked(useSavePolicy).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useSavePolicy>)

    vi.mocked(useDeletePolicy).mockReturnValue({
      mutateAsync: vi.fn().mockResolvedValue(undefined),
      isPending: false,
    } as unknown as ReturnType<typeof useDeletePolicy>)

    vi.mocked(useTriggerPolicy).mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      error: null,
    } as unknown as ReturnType<typeof useTriggerPolicy>)

    vi.mocked(usePausePolicy).mockReturnValue({
      mutateAsync: vi.fn().mockResolvedValue(undefined),
      isPending: false,
    } as unknown as ReturnType<typeof usePausePolicy>)

    vi.mocked(useResumePolicy).mockReturnValue({
      mutateAsync: vi.fn().mockResolvedValue(undefined),
      isPending: false,
    } as unknown as ReturnType<typeof useResumePolicy>)

    vi.mocked(useMcpServers).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useMcpServers>)

    renderEditor('/agents/existing-id')

    // Initially save button is disabled (not dirty)
    const saveBtn = screen.getByRole('button', { name: 'Save' })
    expect(saveBtn).toBeDisabled()

    // Name is the first text input in the Identity section.
    // PolicyIdentitySection labels don't use htmlFor so we can't use getByLabelText.
    const nameInput = screen.getAllByRole('textbox')[0]
    await userEvent.type(nameInput, '-edited')

    // isDirty is now true → Save button becomes enabled
    await waitFor(() => {
      expect(saveBtn).not.toBeDisabled()
    })

    // Click save
    fireEvent.click(saveBtn)

    // After save resolves, isDirty is cleared → Save button disabled again
    await waitFor(() => {
      expect(mutateAsync).toHaveBeenCalledTimes(1)
    })

    await waitFor(() => {
      expect(saveBtn).toBeDisabled()
    })
  })
})

describe('AgentEditorPage — Ctrl+S always triggers save', () => {
  it('calls mutateAsync on Ctrl+S even when the form has not been changed', async () => {
    const { mutateAsync } = mockHooksDefault()
    // Load a valid policy so client-side validation passes.
    vi.mocked(usePolicy).mockReturnValue({
      data: {
        id: 'valid-id',
        name: 'my-agent',
        trigger_type: 'webhook',
        folder: '',
        yaml: VALID_YAML,
        created_at: '',
        updated_at: '',
      },
      status: 'success',
    } as ReturnType<typeof usePolicy>)
    renderEditor('/agents/valid-id')

    // Ctrl+S fires handleSave unconditionally (does not check canSave/isDirty)
    fireEvent.keyDown(window, { key: 's', ctrlKey: true })

    await act(async () => {
      await Promise.resolve()
    })

    expect(mutateAsync).toHaveBeenCalledTimes(1)
  })
})

describe('AgentEditorPage — Cmd+S / Ctrl+S fires save', () => {
  function renderValidEditor() {
    const { mutateAsync } = mockHooksDefault()
    vi.mocked(usePolicy).mockReturnValue({
      data: {
        id: 'valid-id',
        name: 'my-agent',
        trigger_type: 'webhook',
        folder: '',
        yaml: VALID_YAML,
        created_at: '',
        updated_at: '',
      },
      status: 'success',
    } as ReturnType<typeof usePolicy>)
    renderEditor('/agents/valid-id')
    return { mutateAsync }
  }

  it('fires mutateAsync on Cmd+S when form is dirty', async () => {
    const { mutateAsync } = renderValidEditor()

    // Make form dirty — Name is first textbox (label lacks htmlFor)
    const nameInput = screen.getAllByRole('textbox')[0]
    await userEvent.type(nameInput, '-edited')

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Save' })).not.toBeDisabled()
    })

    // Fire Cmd+S
    fireEvent.keyDown(window, { key: 's', metaKey: true })

    await waitFor(() => {
      expect(mutateAsync).toHaveBeenCalled()
    })
  })

  it('fires mutateAsync on Ctrl+S when form is dirty', async () => {
    const { mutateAsync } = renderValidEditor()

    const nameInput = screen.getAllByRole('textbox')[0]
    await userEvent.type(nameInput, '-edited')

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Save' })).not.toBeDisabled()
    })

    fireEvent.keyDown(window, { key: 's', ctrlKey: true })

    await waitFor(() => {
      expect(mutateAsync).toHaveBeenCalled()
    })
  })
})

describe('AgentEditorPage — agent not found (404)', () => {
  it('renders NotFoundPage with "Agent not found" heading on 404 error', () => {
    mockHooksDefault()
    vi.mocked(usePolicy).mockReturnValue({
      data: undefined,
      status: 'error',
      error: new ApiError(404, 'Not Found'),
    } as unknown as ReturnType<typeof usePolicy>)

    renderEditor('/agents/nonexistent-id')

    expect(screen.getByRole('heading', { name: /agent not found/i })).toBeInTheDocument()
  })

  it('shows "Go to Agents" link pointing to /agents', () => {
    mockHooksDefault()
    vi.mocked(usePolicy).mockReturnValue({
      data: undefined,
      status: 'error',
      error: new ApiError(404, 'Not Found'),
    } as unknown as ReturnType<typeof usePolicy>)

    renderEditor('/agents/nonexistent-id')

    const link = screen.getByRole('link', { name: /go to agents/i })
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute('href', '/agents')
  })

  it('shows "Go to Dashboard" secondary link pointing to /dashboard', () => {
    mockHooksDefault()
    vi.mocked(usePolicy).mockReturnValue({
      data: undefined,
      status: 'error',
      error: new ApiError(404, 'Not Found'),
    } as unknown as ReturnType<typeof usePolicy>)

    renderEditor('/agents/nonexistent-id')

    const link = screen.getByRole('link', { name: /go to dashboard/i })
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute('href', '/dashboard')
  })
})

describe('AgentEditorPage — non-404 error', () => {
  it('shows generic "Failed to load agent." message for 500 errors', () => {
    mockHooksDefault()
    vi.mocked(usePolicy).mockReturnValue({
      data: undefined,
      status: 'error',
      error: new ApiError(500, 'Internal Server Error'),
    } as unknown as ReturnType<typeof usePolicy>)

    renderEditor('/agents/nonexistent-id')

    expect(screen.getByText('Failed to load agent.')).toBeInTheDocument()
  })

  it('does not show the NotFoundPage heading for 500 errors', () => {
    mockHooksDefault()
    vi.mocked(usePolicy).mockReturnValue({
      data: undefined,
      status: 'error',
      error: new ApiError(500, 'Internal Server Error'),
    } as unknown as ReturnType<typeof usePolicy>)

    renderEditor('/agents/nonexistent-id')

    expect(screen.queryByRole('heading', { name: /agent not found/i })).not.toBeInTheDocument()
  })
})

describe('AgentEditorPage — Run now button', () => {
  it('shows Run now button for a saved policy with trigger_type manual', () => {
    mockHooksDefault()
    vi.mocked(usePolicy).mockReturnValue({
      data: {
        id: 'manual-id',
        name: 'manual-policy',
        trigger_type: 'manual',
        folder: '',
        yaml: MANUAL_YAML,
        created_at: '',
        updated_at: '',
      },
      status: 'success',
    } as ReturnType<typeof usePolicy>)

    renderEditor('/agents/manual-id')

    expect(screen.getByText('Run now')).toBeInTheDocument()
  })

  it('shows Run now button for a saved policy with trigger_type webhook', () => {
    mockHooksDefault()
    vi.mocked(usePolicy).mockReturnValue({
      data: {
        id: 'webhook-id',
        name: 'webhook-policy',
        trigger_type: 'webhook',
        folder: '',
        yaml: WEBHOOK_YAML,
        created_at: '',
        updated_at: '',
      },
      status: 'success',
    } as ReturnType<typeof usePolicy>)

    renderEditor('/agents/webhook-id')

    expect(screen.getByText('Run now')).toBeInTheDocument()
  })

  it('shows Run now button for a saved policy with trigger_type scheduled', () => {
    mockHooksDefault()
    vi.mocked(usePolicy).mockReturnValue({
      data: {
        id: 'scheduled-id',
        name: 'scheduled-policy',
        trigger_type: 'scheduled',
        folder: '',
        yaml: SCHEDULED_YAML,
        created_at: '',
        updated_at: '',
      },
      status: 'success',
    } as ReturnType<typeof usePolicy>)

    renderEditor('/agents/scheduled-id')

    expect(screen.getByText('Run now')).toBeInTheDocument()
  })

  it('does not show Run now button for new agent (create mode)', () => {
    mockHooksDefault()

    renderEditor('/agents/new')

    expect(screen.queryByText('Run now')).not.toBeInTheDocument()
  })

  it('opens TriggerRunModal when Run now is clicked', async () => {
    mockHooksDefault()
    vi.mocked(usePolicy).mockReturnValue({
      data: {
        id: 'manual-id',
        name: 'manual-policy',
        trigger_type: 'manual',
        folder: '',
        yaml: MANUAL_YAML,
        created_at: '',
        updated_at: '',
      },
      status: 'success',
    } as ReturnType<typeof usePolicy>)

    renderEditor('/agents/manual-id')

    fireEvent.click(screen.getByText('Run now'))

    await waitFor(() => {
      expect(screen.getByText(/Run "manual-policy"/)).toBeInTheDocument()
    })
  })
})

describe('AgentEditorPage — client-side validation', () => {
  it('shows banner and inline error for empty name, does not call the API', async () => {
    const { mutateAsync } = mockHooksDefault()
    // Load a valid policy, then clear the name so the single "name is required"
    // issue fires (and not a cascade of other missing-field issues).
    vi.mocked(usePolicy).mockReturnValue({
      data: {
        id: 'valid-id',
        name: 'my-agent',
        trigger_type: 'webhook',
        folder: '',
        yaml: VALID_YAML,
        created_at: '',
        updated_at: '',
      },
      status: 'success',
    } as ReturnType<typeof usePolicy>)
    renderEditor('/agents/valid-id')

    // Clear the name field
    const nameInput = screen.getAllByRole('textbox')[0]
    await userEvent.clear(nameInput)

    // Attempt save via keyboard shortcut
    fireEvent.keyDown(window, { key: 's', ctrlKey: true })

    await waitFor(() => {
      // Both the ErrorBanner and the inline FieldError have role="alert"
      const alerts = screen.getAllByRole('alert')
      expect(alerts.length).toBeGreaterThan(0)
      expect(screen.getAllByText(/name is required/i).length).toBeGreaterThan(0)
    })

    // mutateAsync must not have been called (API not hit)
    expect(mutateAsync).not.toHaveBeenCalled()
  })

  it('clears banner and calls the API when the form is valid', async () => {
    const { mutateAsync } = mockHooksDefault()
    // Load a valid policy — all fields are set, no client errors expected.
    vi.mocked(usePolicy).mockReturnValue({
      data: {
        id: 'valid-id',
        name: 'my-agent',
        trigger_type: 'webhook',
        folder: '',
        yaml: VALID_YAML,
        created_at: '',
        updated_at: '',
      },
      status: 'success',
    } as ReturnType<typeof usePolicy>)
    renderEditor('/agents/valid-id')

    fireEvent.keyDown(window, { key: 's', ctrlKey: true })

    await waitFor(() => {
      expect(mutateAsync).toHaveBeenCalledTimes(1)
    })
  })
})

describe('AgentEditorPage — server validation errors', () => {
  function loadValidPolicy() {
    vi.mocked(usePolicy).mockReturnValue({
      data: {
        id: 'valid-id',
        name: 'my-agent',
        trigger_type: 'webhook',
        folder: '',
        yaml: VALID_YAML,
        created_at: '',
        updated_at: '',
      },
      status: 'success',
    } as ReturnType<typeof usePolicy>)
  }

  it('shows banner with server issue when server returns issues[]', async () => {
    mockHooksDefault()
    loadValidPolicy()
    const serverIssues = [{ field: 'model.provider', message: 'model.provider is required' }]
    vi.mocked(useSavePolicy).mockReturnValue({
      mutateAsync: vi.fn().mockRejectedValue(
        new ApiError(400, 'policy validation failed', 'model.provider is required', serverIssues),
      ),
      isPending: false,
    } as unknown as ReturnType<typeof useSavePolicy>)

    renderEditor('/agents/valid-id')

    fireEvent.keyDown(window, { key: 's', ctrlKey: true })

    await waitFor(() => {
      expect(screen.getAllByRole('alert').length).toBeGreaterThan(0)
      expect(screen.getAllByText(/model\.provider is required/).length).toBeGreaterThan(0)
    })
  })

  it('shows detail string in banner when server returns legacy response (no issues[])', async () => {
    mockHooksDefault()
    loadValidPolicy()
    vi.mocked(useSavePolicy).mockReturnValue({
      mutateAsync: vi.fn().mockRejectedValue(
        new ApiError(400, 'policy validation failed', 'policy already exists'),
      ),
      isPending: false,
    } as unknown as ReturnType<typeof useSavePolicy>)

    renderEditor('/agents/valid-id')

    fireEvent.keyDown(window, { key: 's', ctrlKey: true })

    await waitFor(() => {
      expect(screen.getAllByRole('alert').length).toBeGreaterThan(0)
      expect(screen.getByText(/policy already exists/)).toBeInTheDocument()
    })
  })
})
