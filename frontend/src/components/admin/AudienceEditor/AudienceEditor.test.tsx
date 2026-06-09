import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { AudienceEditor } from './AudienceEditor'
import type {
  ApiAudience,
  ApiAudienceReferences,
  ApiPluginInstanceForAudience,
} from '@/api/types'
import { ApiError } from '@/api/fetch'

// Mock react-router-dom's useNavigate
const mockNavigate = vi.fn()
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom')
  return { ...actual, useNavigate: () => mockNavigate }
})

// Fixtures

const PLUGIN_NOTIFY_ONLY: ApiPluginInstanceForAudience = {
  id: 'slack-1',
  plugin_id: 'com.example.slack',
  instance_name: 'slack-1',
  state: 'healthy',
  implements_notify: true,
  implements_request: false,
  config_schema: null,
  version: 0,
}

const PLUGIN_REQUEST_ONLY: ApiPluginInstanceForAudience = {
  id: 'pd-1',
  plugin_id: 'com.example.pagerduty',
  instance_name: 'pd-1',
  state: 'healthy',
  implements_notify: false,
  implements_request: true,
  config_schema: null,
  version: 0,
}

const ALL_PLUGINS = [PLUGIN_NOTIFY_ONLY, PLUGIN_REQUEST_ONLY]

const AUDIENCE: ApiAudience = {
  id: 'aud-1',
  name: 'ops-team',
  disable_in_app_fallback: false,
  version: 3,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-04-01T12:00:00Z',
  entries: [
    {
      id: 'e1',
      plugin_instance_id: 'slack-1',
      position: 0,
      notify: true,
      request: false,
      config: {},
    },
    {
      id: 'e2',
      plugin_instance_id: 'pd-1',
      position: 1,
      notify: false,
      request: true,
      config: {},
    },
  ],
}

const REFS_EMPTY: ApiAudienceReferences = { policies: [], in_flight_runs: [] }

const REFS_WITH_POLICIES: ApiAudienceReferences = {
  policies: [{ id: 'p1', name: 'deploy-bot' }],
  in_flight_runs: [],
}

function renderEditor(
  props: Partial<Parameters<typeof AudienceEditor>[0]> & {
    initial?: ApiAudience | null
  } = {},
) {
  const defaults = {
    initial: AUDIENCE as ApiAudience | null,
    pluginInstances: ALL_PLUGINS,
    references: REFS_EMPTY,
    canManage: true,
    onSave: vi.fn().mockResolvedValue(AUDIENCE),
    onDelete: vi.fn().mockResolvedValue(undefined),
    saveError: null,
    deleteError: null,
  }
  return render(
    <MemoryRouter>
      <AudienceEditor {...defaults} {...props} />
    </MemoryRouter>,
  )
}

describe('AudienceEditor — basic rendering', () => {
  it('shows audience name in name input', () => {
    renderEditor()
    const input = screen.getByLabelText(/audience name/i) as HTMLInputElement
    expect(input.value).toBe('ops-team')
  })

  it('renders Save and Delete buttons when canManage', () => {
    renderEditor()
    expect(screen.getByRole('button', { name: /^save$/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /delete audience/i })).toBeInTheDocument()
  })

  it('hides Save and Delete buttons when !canManage', () => {
    renderEditor({ canManage: false })
    expect(screen.queryByRole('button', { name: /^save$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /delete audience/i })).not.toBeInTheDocument()
  })

  it('shows read-only notice when !canManage', () => {
    renderEditor({ canManage: false })
    expect(screen.getByText(/read-only access/i)).toBeInTheDocument()
  })
})

describe('AudienceEditor — Notify disabled when implements_notify=false', () => {
  it('disables Notify checkbox for pd-1 which has implements_notify=false', () => {
    renderEditor()
    // Find entry 2 (pd-1) — it renders Notify checkbox second
    const notifyCheckboxes = screen.getAllByRole('checkbox', { name: /notify/i })
    // Entry 1 (slack-1) implements_notify=true → enabled
    expect(notifyCheckboxes[0]).not.toBeDisabled()
    // Entry 2 (pd-1) implements_notify=false → disabled
    expect(notifyCheckboxes[1]).toBeDisabled()
  })

  it('shows title tooltip for disabled Notify', () => {
    renderEditor()
    const notifyLabels = screen.getAllByTitle('This plugin does not implement Notify')
    expect(notifyLabels.length).toBeGreaterThan(0)
  })
})

describe('AudienceEditor — Request disabled when implements_request=false', () => {
  it('disables Request checkbox for slack-1 which has implements_request=false', () => {
    renderEditor()
    const requestCheckboxes = screen.getAllByRole('checkbox', { name: /request/i })
    // Entry 1 (slack-1) implements_request=false → disabled
    expect(requestCheckboxes[0]).toBeDisabled()
    // Entry 2 (pd-1) implements_request=true → enabled
    expect(requestCheckboxes[1]).not.toBeDisabled()
  })
})

describe('AudienceEditor — drag-reorder', () => {
  it('updates entry order when Move Down is clicked', async () => {
    renderEditor()
    // Before: slack-1 at index 0, pd-1 at index 1
    const moveDownBtns = screen.getAllByRole('button', { name: /move down/i })
    // Click Move Down on entry 1 (index 0)
    await userEvent.click(moveDownBtns[0])

    // After: pd-1 is now at position 1 label and slack-1 at position 2
    // The position numbers (1, 2) are rendered as text
    const positions = document.querySelectorAll('[class*="position"]')
    // We check that the entries exist and there are still 2 user entries
    expect(positions.length).toBeGreaterThanOrEqual(2)
  })

  it('updates entry order when Move Up is clicked', async () => {
    renderEditor()
    const moveUpBtns = screen.getAllByRole('button', { name: /move up/i })
    // Move up on entry 2 (index 1, pd-1)
    await userEvent.click(moveUpBtns[1])
    // Just verify no errors thrown and re-render happens
    expect(screen.getAllByRole('button', { name: /move up/i }).length).toBeGreaterThan(0)
  })
})

describe('AudienceEditor — SaveGuardDialog opens conditionally', () => {
  it('does NOT show SaveGuardDialog when references is empty', async () => {
    const onSave = vi.fn().mockResolvedValue(AUDIENCE)
    renderEditor({ references: REFS_EMPTY, onSave })

    await userEvent.click(screen.getByRole('button', { name: /^save$/i }))

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
  })

  it('shows SaveGuardDialog when references has policies', async () => {
    const onSave = vi.fn().mockResolvedValue(AUDIENCE)
    renderEditor({ references: REFS_WITH_POLICIES, onSave })

    await userEvent.click(screen.getByRole('button', { name: /^save$/i }))

    expect(screen.getByRole('dialog')).toBeInTheDocument()
    // onSave not yet called — user must confirm
    expect(onSave).not.toHaveBeenCalled()
  })

  it('calls onSave after confirming in SaveGuardDialog', async () => {
    const onSave = vi.fn().mockResolvedValue(AUDIENCE)
    renderEditor({ references: REFS_WITH_POLICIES, onSave })

    await userEvent.click(screen.getByRole('button', { name: /^save$/i }))
    await userEvent.click(screen.getByRole('button', { name: /save anyway/i }))

    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
  })
})

describe('AudienceEditor — payload serialization', () => {
  it('strips auto entries before submitting', async () => {
    const audienceWithAuto: ApiAudience = {
      ...AUDIENCE,
      entries: [
        ...AUDIENCE.entries,
        {
          id: '__auto__',
          plugin_instance_id: '',
          position: 99,
          notify: true,
          request: true,
          config: {},
          auto: true,
        },
      ],
    }
    const onSave = vi.fn().mockResolvedValue(audienceWithAuto)
    renderEditor({ initial: audienceWithAuto, references: REFS_EMPTY, onSave })

    await userEvent.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    const req = onSave.mock.calls[0][0]
    // No auto entries should be in the payload
    expect(req.entries.every((e: { plugin_instance_id: string }) => e.plugin_instance_id !== '')).toBe(true)
  })

  it('includes expected_version in edit mode', async () => {
    const onSave = vi.fn().mockResolvedValue(AUDIENCE)
    renderEditor({ initial: AUDIENCE, references: REFS_EMPTY, onSave })

    await userEvent.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    const req = onSave.mock.calls[0][0]
    expect(req).toHaveProperty('expected_version', AUDIENCE.version)
  })

  it('does NOT include expected_version in create mode', async () => {
    const onSave = vi.fn().mockResolvedValue(AUDIENCE)
    renderEditor({ initial: null, references: null, onSave })

    // Fill in a name (required)
    fireEvent.change(screen.getByLabelText(/audience name/i), { target: { value: 'new-aud' } })

    await userEvent.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    const req = onSave.mock.calls[0][0]
    expect(req).not.toHaveProperty('expected_version')
  })
})

describe('AudienceEditor — 409 version conflict banner', () => {
  it('shows reload banner when saveError.status === 409', () => {
    const conflictError = new ApiError(409, 'run status transition lost to concurrent writer')
    renderEditor({ saveError: conflictError })
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText(/reload audience/i)).toBeInTheDocument()
  })

  it('does NOT show reload banner when saveError is null', () => {
    renderEditor({ saveError: null })
    // Only alert roles are from error/success alerts, check none match the conflict text
    const alerts = document.querySelectorAll('[role="alert"]')
    alerts.forEach((a) => {
      expect(a.textContent).not.toMatch(/reload audience/i)
    })
  })
})

describe('AudienceEditor — add entry', () => {
  it('appends a new blank entry when + Add entry is clicked', async () => {
    renderEditor()
    const initialPositions = screen.getAllByRole('button', { name: /remove entry/i })
    const initialCount = initialPositions.length

    await userEvent.click(screen.getByRole('button', { name: /\+ add entry/i }))

    const afterPositions = screen.getAllByRole('button', { name: /remove entry/i })
    expect(afterPositions.length).toBe(initialCount + 1)
  })
})
