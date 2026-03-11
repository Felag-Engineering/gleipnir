import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'

import { PolicyEditorPage } from './PolicyEditorPage'
import { yamlToFormState, formStateToYaml } from './policyEditorUtils'

// --- Mocks ---

vi.mock('@/hooks/usePolicy')
vi.mock('@/hooks/useSavePolicy')
vi.mock('@/hooks/useDeletePolicy')
vi.mock('@/hooks/useMcpServers')

vi.mock('@/components/PolicyEditor/YamlEditor/YamlEditor', () => ({
  YamlEditor: ({ value, onChange, onValidityChange }: {
    value: string
    onChange: (v: string) => void
    onValidityChange: (valid: boolean) => void
  }) => {
    // Validity heuristic: strings starting with ":" are invalid YAML markers
    React.useEffect(() => {
      onValidityChange(!value.startsWith(':'))
    }, [value, onValidityChange])
    return React.createElement('textarea', {
      'data-testid': 'yaml-editor',
      value: value ?? '',
      onChange: (e: React.ChangeEvent<HTMLTextAreaElement>) => onChange(e.target.value),
    })
  },
}))

import { usePolicy } from '@/hooks/usePolicy'
import { useSavePolicy } from '@/hooks/useSavePolicy'
import { useDeletePolicy } from '@/hooks/useDeletePolicy'
import { useMcpServers } from '@/hooks/useMcpServers'

// --- Fixtures ---

const WEBHOOK_YAML = `name: webhook-policy
description: A webhook triggered policy
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: filesystem.read_file
  actuators:
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

const CRON_YAML = `name: cron-policy
trigger:
  type: cron
  schedule: '0 * * * *'
capabilities:
  sensors: []
  actuators: []
agent:
  task: Run hourly checks.
  limits:
    max_tokens_per_run: 5000
    max_tool_calls_per_run: 10
  concurrency: queue
`

const POLL_YAML = `name: poll-policy
trigger:
  type: poll
  interval: 10m
  request:
    url: https://example.com/api
    method: GET
  filter: '.items | length > 0'
capabilities:
  sensors:
    - tool: github.list_issues
  actuators: []
agent:
  task: Check for new items.
  limits:
    max_tokens_per_run: 8000
    max_tool_calls_per_run: 20
  concurrency: replace
`

// --- Helpers ---

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderEditor(path = '/policies/new', queryClient = makeQueryClient()) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/policies/new" element={<PolicyEditorPage />} />
          <Route path="/policies/:id" element={<PolicyEditorPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

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

  vi.mocked(useMcpServers).mockReturnValue({
    data: [],
    isLoading: false,
  } as unknown as ReturnType<typeof useMcpServers>)

  return { mutateAsync }
}

// --- Tests ---

describe('PolicyEditorUtils — YAML ↔ form round-trip (pure functions)', () => {
  it.each([
    ['webhook', WEBHOOK_YAML],
    ['cron', CRON_YAML],
    ['poll', POLL_YAML],
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
      expect(rtTools[i].role).toBe(t.role)
      expect(rtTools[i].approvalRequired).toBe(t.approvalRequired)
    })
  })

  it('webhook trigger loses no fields after round-trip', () => {
    const parsed = yamlToFormState(WEBHOOK_YAML)!
    expect(parsed.trigger.type).toBe('webhook')
    const rt = yamlToFormState(formStateToYaml(parsed))!
    expect(rt.trigger.type).toBe('webhook')
  })

  it('cron schedule is preserved after round-trip', () => {
    const parsed = yamlToFormState(CRON_YAML)!
    if (parsed.trigger.type !== 'cron') throw new Error('expected cron')
    const rt = yamlToFormState(formStateToYaml(parsed))!
    if (rt.trigger.type !== 'cron') throw new Error('expected cron after round-trip')
    expect(rt.trigger.schedule).toBe(parsed.trigger.schedule)
  })

  it('poll interval and url are preserved after round-trip', () => {
    const parsed = yamlToFormState(POLL_YAML)!
    if (parsed.trigger.type !== 'poll') throw new Error('expected poll')
    const rt = yamlToFormState(formStateToYaml(parsed))!
    if (rt.trigger.type !== 'poll') throw new Error('expected poll after round-trip')
    expect(rt.trigger.interval).toBe(parsed.trigger.interval)
    expect(rt.trigger.request.url).toBe(parsed.trigger.request.url)
  })
})

describe('PolicyEditorPage — mode toggle with invalid YAML', () => {
  beforeEach(() => {
    mockHooksDefault()
  })

  it('shows error and stays in YAML mode when switching to form with malformed YAML', async () => {
    renderEditor()

    // Switch to YAML mode
    fireEvent.click(screen.getByRole('button', { name: 'YAML' }))

    // Type invalid YAML (starts with ":" — our mock heuristic for invalid)
    const editor = screen.getByTestId('yaml-editor')
    fireEvent.change(editor, { target: { value: ': invalid yaml content' } })

    // Wait for the validity effect to fire (marks yamlValid=false)
    await waitFor(() => {
      // Attempt to switch back to form mode
      fireEvent.click(screen.getByRole('button', { name: 'Form' }))
    })

    // Error message should appear
    await waitFor(() => {
      expect(
        screen.getByText('Cannot switch to Form mode: YAML is malformed or missing required fields.'),
      ).toBeInTheDocument()
    })

    // Should still be in YAML mode — textarea is visible
    expect(screen.getByTestId('yaml-editor')).toBeInTheDocument()
  })
})

describe('PolicyEditorPage — dirty state and save', () => {
  it('editing the name field sets isDirty; saving clears it', async () => {
    // Use an existing-policy route so save does not navigate away
    vi.mocked(usePolicy).mockReturnValue({
      data: {
        id: 'existing-id',
        name: 'existing-policy',
        trigger_type: 'webhook',
        folder: '',
        yaml: WEBHOOK_YAML,
        created_at: '',
        updated_at: '',
      },
      status: 'success',
    } as ReturnType<typeof usePolicy>)

    const mutateAsync = vi.fn().mockResolvedValue({
      id: 'existing-id',
      name: 'existing-policy',
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

    vi.mocked(useMcpServers).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useMcpServers>)

    renderEditor('/policies/existing-id')

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

describe('PolicyEditorPage — save blocking with malformed YAML', () => {
  beforeEach(() => {
    mockHooksDefault()
  })

  it('disables Save button when YAML mode has malformed content', async () => {
    renderEditor()

    // Switch to YAML mode
    fireEvent.click(screen.getByRole('button', { name: 'YAML' }))

    const editor = screen.getByTestId('yaml-editor')
    const saveBtn = screen.getByRole('button', { name: 'Save' })

    // Type valid YAML first to make dirty + valid
    await act(async () => {
      fireEvent.change(editor, { target: { value: 'name: valid-policy\ntrigger:\n  type: webhook\n' } })
    })

    // Save should be enabled (isDirty=true, yamlValid=true)
    await waitFor(() => {
      expect(saveBtn).not.toBeDisabled()
    })

    // Now type invalid YAML
    await act(async () => {
      fireEvent.change(editor, { target: { value: ': broken yaml' } })
    })

    // Save should be disabled (yamlValid=false → canSave=false)
    await waitFor(() => {
      expect(saveBtn).toBeDisabled()
    })
  })
})

describe('PolicyEditorPage — Cmd+S / Ctrl+S fires save', () => {
  it('fires mutateAsync on Cmd+S when form is dirty', async () => {
    const { mutateAsync } = mockHooksDefault()
    renderEditor()

    // Make form dirty — Name is first textbox (label lacks htmlFor)
    const nameInput = screen.getAllByRole('textbox')[0]
    await userEvent.type(nameInput, 'test')

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
    const { mutateAsync } = mockHooksDefault()
    renderEditor()

    const nameInput = screen.getAllByRole('textbox')[0]
    await userEvent.type(nameInput, 'test')

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Save' })).not.toBeDisabled()
    })

    fireEvent.keyDown(window, { key: 's', ctrlKey: true })

    await waitFor(() => {
      expect(mutateAsync).toHaveBeenCalled()
    })
  })
})
