import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent, act } from '@testing-library/react'
import type { ApiPluginOption } from '@/api/types'
import { AsyncCombobox } from './AsyncCombobox'

// ── fixtures ──────────────────────────────────────────────────────────────────

const CHANNELS: ApiPluginOption[] = [
  { value: 'C001', label: '#general', group: 'Joined' },
  { value: 'C002', label: '#alerts', group: 'Joined' },
  { value: 'C003', label: '#restricted (not joined)', group: 'Not joined', disabled: true },
]

// makeSearch returns a vitest mock that resolves to `opts` immediately.
function makeSearch(opts: ApiPluginOption[] = CHANNELS) {
  return vi.fn((_q: string): Promise<ApiPluginOption[]> => Promise.resolve(opts))
}

// openDropdown fires focus on the combobox input and waits for the listbox.
async function openDropdown(input: HTMLElement) {
  await act(async () => {
    fireEvent.focus(input)
  })
  await waitFor(() => {
    expect(screen.getByRole('listbox')).toBeInTheDocument()
  })
}

afterEach(() => {
  vi.restoreAllMocks()
})

// ── single-select ─────────────────────────────────────────────────────────────

describe('AsyncCombobox (single)', () => {
  it('calls onSearch on focus and renders returned options', async () => {
    const onSearch = makeSearch()
    const onChange = vi.fn()
    render(
      <AsyncCombobox id="test" value="" onChange={onChange} onSearch={onSearch} />,
    )

    const input = screen.getByRole('combobox')
    await openDropdown(input)

    expect(onSearch).toHaveBeenCalledWith('')
    expect(screen.getByText('#general')).toBeInTheDocument()
    expect(screen.getByText('#alerts')).toBeInTheDocument()
  })

  it('debounces search on keystroke (does not call immediately, calls after delay)', async () => {
    vi.useFakeTimers()

    const onSearch = vi.fn((_q: string): Promise<ApiPluginOption[]> =>
      Promise.resolve(CHANNELS.filter((c) => c.label.includes(_q))),
    )
    const onChange = vi.fn()
    render(
      <AsyncCombobox id="test" value="" onChange={onChange} onSearch={onSearch} />,
    )

    const input = screen.getByRole('combobox')
    // Simulate user typing; use fireEvent to avoid userEvent's own timer interactions.
    fireEvent.change(input, { target: { value: 'gen' } })

    // onSearch should not be called yet (debounce pending).
    expect(onSearch).not.toHaveBeenCalledWith('gen')

    // Advance past the debounce window.
    await act(async () => {
      vi.runAllTimers()
    })

    expect(onSearch).toHaveBeenCalledWith('gen')
    vi.useRealTimers()
  })

  it('calls onChange with the selected value on option mousedown', async () => {
    const onSearch = makeSearch()
    const onChange = vi.fn()
    render(
      <AsyncCombobox id="test" value="" onChange={onChange} onSearch={onSearch} />,
    )

    const input = screen.getByRole('combobox')
    await openDropdown(input)

    // Options use onMouseDown with e.preventDefault() to avoid losing focus.
    await act(async () => {
      fireEvent.mouseDown(screen.getByText('#general'))
    })

    expect(onChange).toHaveBeenCalledWith('C001')
  })

  it('does not call onChange for disabled options', async () => {
    const onSearch = makeSearch()
    const onChange = vi.fn()
    render(
      <AsyncCombobox id="test" value="" onChange={onChange} onSearch={onSearch} />,
    )

    const input = screen.getByRole('combobox')
    await openDropdown(input)

    await act(async () => {
      fireEvent.mouseDown(screen.getByText('#restricted (not joined)'))
    })

    expect(onChange).not.toHaveBeenCalled()
  })

  it('renders degraded fallback when degraded=true (no combobox, plain text)', () => {
    const onChange = vi.fn()
    render(
      <AsyncCombobox id="test" value="C001" onChange={onChange} onSearch={makeSearch()} degraded />,
    )

    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    const input = screen.getByRole('textbox')
    expect(input).toBeInTheDocument()
    expect(screen.getByText(/dynamic search unavailable/i)).toBeInTheDocument()
  })

  it('calls onChange with raw string when typing in degraded mode', () => {
    const onChange = vi.fn()
    render(
      <AsyncCombobox id="test" value="" onChange={onChange} onSearch={makeSearch()} degraded />,
    )

    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'C999' } })
    expect(onChange).toHaveBeenLastCalledWith('C999')
  })
})

// ── multi-select ──────────────────────────────────────────────────────────────

describe('AsyncCombobox (multi)', () => {
  it('renders chip remove buttons for each selected value', async () => {
    const onSearch = makeSearch()
    const onChange = vi.fn()
    render(
      <AsyncCombobox
        id="test"
        value={['C001', 'C002']}
        onChange={onChange}
        onSearch={onSearch}
        multi
      />,
    )

    // Open dropdown to load labels so chips can be resolved.
    await openDropdown(screen.getByRole('combobox'))

    // Two chips → two remove buttons.
    expect(screen.getAllByRole('button', { name: /remove/i })).toHaveLength(2)
  })

  it('adds a value on option mousedown in multi mode', async () => {
    const onSearch = makeSearch()
    const onChange = vi.fn()
    render(
      <AsyncCombobox
        id="test"
        value={['C001']}
        onChange={onChange}
        onSearch={onSearch}
        multi
      />,
    )

    await openDropdown(screen.getByRole('combobox'))

    await act(async () => {
      fireEvent.mouseDown(screen.getByText('#alerts'))
    })

    expect(onChange).toHaveBeenCalledWith(['C001', 'C002'])
  })

  it('deselects an already-selected option on mousedown in multi mode', async () => {
    const onSearch = makeSearch()
    const onChange = vi.fn()
    render(
      <AsyncCombobox
        id="test"
        value={['C001', 'C002']}
        onChange={onChange}
        onSearch={onSearch}
        multi
      />,
    )

    await openDropdown(screen.getByRole('combobox'))

    // #general (C001) is selected; clicking it in the listbox deselects it.
    // Use getAllByRole to target the listbox option (first option = #general).
    await act(async () => {
      const options = screen.getAllByRole('option')
      // First option in the listbox is #general (C001).
      fireEvent.mouseDown(options[0])
    })

    expect(onChange).toHaveBeenCalledWith(['C002'])
  })

  it('removes a chip when its remove button is clicked', async () => {
    const onSearch = makeSearch()
    const onChange = vi.fn()
    render(
      <AsyncCombobox
        id="test"
        value={['C001', 'C002']}
        onChange={onChange}
        onSearch={onSearch}
        multi
      />,
    )

    // Load options to resolve chip labels.
    await openDropdown(screen.getByRole('combobox'))

    const removeButtons = screen.getAllByRole('button', { name: /remove/i })
    // mouseDown on the remove button (same preventDefault pattern as options).
    await act(async () => {
      fireEvent.mouseDown(removeButtons[0])
    })

    expect(onChange).toHaveBeenCalled()
    // The first chip was removed; the remaining array should not contain both IDs.
    const calledWith = onChange.mock.calls[0][0] as string[]
    expect(calledWith).toHaveLength(1)
  })

  it('renders degraded fallback in multi mode with comma-separated IDs', () => {
    const onChange = vi.fn()
    render(
      <AsyncCombobox
        id="test"
        value={['C001', 'C002']}
        onChange={onChange}
        onSearch={makeSearch()}
        multi
        degraded
      />,
    )

    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    const input = screen.getByRole('textbox')
    expect(input).toHaveValue('C001, C002')
    expect(screen.getByText(/dynamic search unavailable/i)).toBeInTheDocument()
  })

  it('calls onChange with array when typing in multi degraded mode', () => {
    const onChange = vi.fn()
    render(
      <AsyncCombobox
        id="test"
        value={[]}
        onChange={onChange}
        onSearch={makeSearch()}
        multi
        degraded
      />,
    )

    // Type comma-separated IDs.
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'C001, C002' } })
    expect(onChange).toHaveBeenLastCalledWith(['C001', 'C002'])
  })
})
