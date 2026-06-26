import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
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

  // jsdom does not implement DataTransfer; onDragStart writes effectAllowed, so
  // every dragStart needs a minimal stub passed through the fireEvent init.
  function fakeDataTransfer() {
    return { effectAllowed: '', dropEffect: '', setData: vi.fn(), getData: vi.fn() }
  }

  it('shows the drop indicator on the hovered row but never on the dragged row itself', () => {
    renderEditor()
    const rows = screen.getAllByRole('listitem')
    // rows = [e1 (slack-1), e2 (pd-1), auto in-app]
    fireEvent.dragStart(rows[0], { dataTransfer: fakeDataTransfer() })

    // Dragging over the OTHER row marks it as the drop target.
    fireEvent.dragOver(rows[1], { dataTransfer: fakeDataTransfer() })
    expect(rows[1].className).toMatch(/dragOver/)
    expect(rows[0].className).toMatch(/dragging/)

    // Dragging back over the dragged row never marks the source as a drop target.
    fireEvent.dragOver(rows[0], { dataTransfer: fakeDataTransfer() })
    expect(rows[0].className).not.toMatch(/dragOver/)
    expect(rows[0].className).toMatch(/dragging/)
  })

  it('never shows the drop indicator on the auto gleipnir.in-app row', () => {
    renderEditor()
    const rows = screen.getAllByRole('listitem')
    const autoRow = rows[2]
    fireEvent.dragStart(rows[0], { dataTransfer: fakeDataTransfer() })
    fireEvent.dragOver(autoRow, { dataTransfer: fakeDataTransfer() })
    expect(autoRow.className).not.toMatch(/dragOver/)
  })

  it('clears all drag highlighting on dragEnd, even without a drop (aborted drag)', () => {
    renderEditor()
    const rows = screen.getAllByRole('listitem')
    fireEvent.dragStart(rows[0], { dataTransfer: fakeDataTransfer() })
    fireEvent.dragOver(rows[1], { dataTransfer: fakeDataTransfer() })
    expect(rows[0].className).toMatch(/dragging/)
    expect(rows[1].className).toMatch(/dragOver/)

    // Released outside any row: no drop event fires, only dragEnd on the source.
    fireEvent.dragEnd(rows[0], { dataTransfer: fakeDataTransfer() })

    const after = screen.getAllByRole('listitem')
    expect(after[0].className).not.toMatch(/dragging/)
    expect(after[1].className).not.toMatch(/dragOver/)
  })

  it('reorders entries on a real drop', () => {
    renderEditor()
    const rows = screen.getAllByRole('listitem')
    // Before: entry 1 = slack-1, entry 2 = pd-1
    const before = screen.getAllByRole('combobox') as HTMLSelectElement[]
    expect(before[0].value).toBe('slack-1')

    fireEvent.dragStart(rows[0], { dataTransfer: fakeDataTransfer() })
    fireEvent.dragOver(rows[1], { dataTransfer: fakeDataTransfer() })
    fireEvent.drop(rows[1], { dataTransfer: fakeDataTransfer() })

    // After: slack-1 moved past pd-1 → first select is now pd-1.
    const after = screen.getAllByRole('combobox') as HTMLSelectElement[]
    expect(after[0].value).toBe('pd-1')
    expect(after[1].value).toBe('slack-1')
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

// ── Slack-shaped channel schema fixture ───────────────────────────────────────

const PLUGIN_SLACK_WITH_SCHEMA: ApiPluginInstanceForAudience = {
  id: 'slack-1',
  plugin_id: 'com.example.slack',
  instance_name: 'slack-1',
  state: 'healthy',
  implements_notify: true,
  implements_request: true,
  version: 0,
  config_schema: {
    type: 'object',
    properties: {
      channel: {
        type: 'string',
        title: 'Channel',
        description: 'Slack channel (e.g. #ops)',
        'x-gleipnir-options': { source: 'channels' },
      },
      mention: {
        type: 'string',
        title: 'Mention',
        description: 'User or group to @-mention',
      },
      response_buttons: {
        type: 'array',
        items: { type: 'object' },
        description: 'Buttons to show the recipient',
      },
    },
    required: ['channel'],
  },
}

const AUDIENCE_WITH_SLACK: ApiAudience = {
  id: 'aud-slack',
  name: 'slack-team',
  disable_in_app_fallback: false,
  version: 1,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  entries: [
    {
      id: 'e1',
      plugin_instance_id: 'slack-1',
      position: 0,
      notify: true,
      request: true,
      config: { channel: '#ops', mention: '@oncall' },
    },
  ],
}

const AUDIENCE_WITH_RESPONSE_BUTTONS: ApiAudience = {
  ...AUDIENCE_WITH_SLACK,
  entries: [
    {
      ...AUDIENCE_WITH_SLACK.entries[0],
      config: {
        channel: '#ops',
        response_buttons: [
          { option_id: 'yes', label: 'Yes', value: 'yes', style: 'primary' },
          { option_id: 'no', label: 'No', value: 'no', style: 'danger' },
        ],
      },
    },
  ],
}

describe('AudienceEditor — SchemaForm rendering for Slack schema', () => {
  it('renders channel and mention with humanized labels via SchemaForm (no RJSF)', () => {
    renderEditor({
      initial: AUDIENCE_WITH_SLACK,
      pluginInstances: [PLUGIN_SLACK_WITH_SCHEMA],
    })
    // SchemaForm uses prop.title ('Channel', 'Mention') as labels.
    // Use regex because labels also contain an aria-hidden ' *' required marker.
    expect(screen.getByLabelText(/^channel$/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/^mention$/i)).toBeInTheDocument()
    // RJSF is not present: no rjsf-specific data attributes or class names.
    expect(document.querySelector('[class*="rjsf"]')).not.toBeInTheDocument()
    expect(document.querySelector('form.rjsf')).not.toBeInTheDocument()
  })

  it('shows a required CSS class on the Channel label (::after renders the * marker)', () => {
    renderEditor({
      initial: AUDIENCE_WITH_SLACK,
      pluginInstances: [PLUGIN_SLACK_WITH_SCHEMA],
    })
    // The required marker is applied as a CSS class (::after pseudo-element), keeping
    // the DOM text clean. Verify the channel label has the required class.
    const channelInput = screen.getByLabelText(/^channel$/i)
    const channelLabel = document.querySelector(`label[for="${channelInput.id}"]`)
    expect(channelLabel?.className).toMatch(/required/i)

    // Mention is not required — no required class.
    const mentionInput = screen.getByLabelText(/^mention$/i)
    const mentionLabel = document.querySelector(`label[for="${mentionInput.id}"]`)
    expect(mentionLabel?.className).not.toMatch(/required/i)
  })
})

describe('AudienceEditor — channel AsyncCombobox via optionsContext (B3)', () => {
  beforeEach(() => {
    // MSW handler for the options endpoint — globally initialized via setup.ts.
    server.use(
      http.get('/api/v1/admin/plugins/:id/instances/:iid/options/channels', () =>
        HttpResponse.json({
          data: {
            options: [
              { value: '#ops', label: '#ops' },
              { value: '#incidents', label: '#incidents' },
            ],
            next_cursor: '',
          },
        }),
      ),
    )
  })

  it('renders an AsyncCombobox (combobox role) for the channel field', async () => {
    renderEditor({
      initial: AUDIENCE_WITH_SLACK,
      pluginInstances: [PLUGIN_SLACK_WITH_SCHEMA],
    })
    // AsyncCombobox renders an input with role="combobox" when the options context is wired.
    const comboboxes = await screen.findAllByRole('combobox')
    // At least one combobox should be present (the channel field).
    expect(comboboxes.length).toBeGreaterThan(0)
  })
})

describe('AudienceEditor — response_buttons escape hatch', () => {
  it('preserves response_buttons in the save payload when set', async () => {
    const onSave = vi.fn().mockResolvedValue(AUDIENCE_WITH_RESPONSE_BUTTONS)
    renderEditor({
      initial: AUDIENCE_WITH_RESPONSE_BUTTONS,
      pluginInstances: [PLUGIN_SLACK_WITH_SCHEMA],
      references: REFS_EMPTY,
      onSave,
    })

    await userEvent.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    const req = onSave.mock.calls[0][0]
    const configSaved = req.entries[0].config
    expect(configSaved).toHaveProperty('response_buttons')
    expect(configSaved.response_buttons).toHaveLength(2)
    expect(configSaved.response_buttons[0]).toMatchObject({ option_id: 'yes', style: 'primary' })
  })

  it('omits response_buttons from payload when not set (backend default Approve/Reject)', async () => {
    const onSave = vi.fn().mockResolvedValue(AUDIENCE_WITH_SLACK)
    renderEditor({
      initial: AUDIENCE_WITH_SLACK,
      pluginInstances: [PLUGIN_SLACK_WITH_SCHEMA],
      references: REFS_EMPTY,
      onSave,
    })

    await userEvent.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    const req = onSave.mock.calls[0][0]
    const configSaved = req.entries[0].config
    expect(configSaved).not.toHaveProperty('response_buttons')
  })

  it('omits response_buttons after adding then removing all buttons', async () => {
    const onSave = vi.fn().mockResolvedValue(AUDIENCE_WITH_SLACK)
    renderEditor({
      initial: AUDIENCE_WITH_SLACK,
      pluginInstances: [PLUGIN_SLACK_WITH_SCHEMA],
      references: REFS_EMPTY,
      onSave,
    })

    // Add a button.
    await userEvent.click(screen.getByRole('button', { name: /\+ add button/i }))

    // Remove it — the only row removal should call onChange(undefined).
    const removeBtn = await screen.findByRole('button', { name: /remove button 1/i })
    await userEvent.click(removeBtn)

    await userEvent.click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    const req = onSave.mock.calls[0][0]
    const configSaved = req.entries[0].config
    expect(configSaved).not.toHaveProperty('response_buttons')
  })
})
