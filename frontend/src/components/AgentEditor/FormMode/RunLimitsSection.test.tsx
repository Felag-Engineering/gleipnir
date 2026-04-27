import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { useState } from 'react'
import { RunLimitsSection } from './RunLimitsSection'
import type { RunLimitsFormState } from './types'

function renderSection(
  value: RunLimitsFormState,
  onChange = vi.fn(),
) {
  return render(<RunLimitsSection value={value} onChange={onChange} />)
}

// Wraps RunLimitsSection in local state so prop updates from onChange are
// reflected back into the component, letting us test the full controlled loop.
function ControlledRunLimitsSection({ initial }: { initial: RunLimitsFormState }) {
  const [value, setValue] = useState(initial)
  return <RunLimitsSection value={value} onChange={setValue} />
}

describe('RunLimitsSection — labels', () => {
  it('renders labels with the (0 = unlimited) suffix', () => {
    renderSection({ max_tokens_per_run: 20000, max_tool_calls_per_run: 50 })
    expect(screen.getByText('Max tokens per run (0 = unlimited)')).toBeTruthy()
    expect(screen.getByText('Max tool calls per run (0 = unlimited)')).toBeTruthy()
  })
})

describe('RunLimitsSection — clearing the input', () => {
  it('calls onChange with 0 when the user clears the tokens field', () => {
    const onChange = vi.fn()
    renderSection({ max_tokens_per_run: 20000, max_tool_calls_per_run: 50 }, onChange)
    const [tokensInput] = screen.getAllByRole('textbox')
    fireEvent.change(tokensInput, { target: { value: '' } })
    expect(onChange).toHaveBeenLastCalledWith({ max_tokens_per_run: 0, max_tool_calls_per_run: 50 })
  })

  it('calls onChange with 0 when the user clears the tool calls field', () => {
    const onChange = vi.fn()
    renderSection({ max_tokens_per_run: 20000, max_tool_calls_per_run: 50 }, onChange)
    const [, toolCallsInput] = screen.getAllByRole('textbox')
    fireEvent.change(toolCallsInput, { target: { value: '' } })
    expect(onChange).toHaveBeenLastCalledWith({ max_tokens_per_run: 20000, max_tool_calls_per_run: 0 })
  })
})

describe('RunLimitsSection — accepting 0', () => {
  it('calls onChange with 0 and shows the Unlimited hint under tokens', () => {
    render(<ControlledRunLimitsSection initial={{ max_tokens_per_run: 20000, max_tool_calls_per_run: 50 }} />)
    const [tokensInput] = screen.getAllByRole('textbox')
    fireEvent.change(tokensInput, { target: { value: '0' } })
    // After the controlled update, the prop is 0, so the hint appears.
    expect(screen.getByText('Unlimited')).toBeTruthy()
  })

  it('does not show the Unlimited hint for the field that is still positive', () => {
    render(<ControlledRunLimitsSection initial={{ max_tokens_per_run: 0, max_tool_calls_per_run: 50 }} />)
    // Only one Unlimited hint should be present (for tokens).
    const hints = screen.getAllByText('Unlimited')
    expect(hints).toHaveLength(1)
  })
})

describe('RunLimitsSection — non-digit stripping', () => {
  it('strips non-digit characters and commits numeric value', () => {
    const onChange = vi.fn()
    renderSection({ max_tokens_per_run: 100, max_tool_calls_per_run: 10 }, onChange)
    const [tokensInput] = screen.getAllByRole('textbox')
    fireEvent.change(tokensInput, { target: { value: '12abc34' } })
    expect(onChange).toHaveBeenLastCalledWith({ max_tokens_per_run: 1234, max_tool_calls_per_run: 10 })
    expect((tokensInput as HTMLInputElement).value).toBe('1234')
  })

  it('strips a leading minus sign (paste of negative number becomes positive)', () => {
    const onChange = vi.fn()
    renderSection({ max_tokens_per_run: 100, max_tool_calls_per_run: 10 }, onChange)
    const [tokensInput] = screen.getAllByRole('textbox')
    fireEvent.change(tokensInput, { target: { value: '-5' } })
    expect(onChange).toHaveBeenLastCalledWith({ max_tokens_per_run: 5, max_tool_calls_per_run: 10 })
    expect((tokensInput as HTMLInputElement).value).toBe('5')
  })
})

describe('RunLimitsSection — blur coercion', () => {
  it('displays "0" after blur when the field is empty', () => {
    render(<ControlledRunLimitsSection initial={{ max_tokens_per_run: 20000, max_tool_calls_per_run: 50 }} />)
    const [tokensInput] = screen.getAllByRole('textbox')
    fireEvent.change(tokensInput, { target: { value: '' } })
    fireEvent.blur(tokensInput)
    expect((tokensInput as HTMLInputElement).value).toBe('0')
  })
})

describe('RunLimitsSection — external prop change resync', () => {
  it('updates the input value when the parent supplies new prop values', () => {
    const onChange = vi.fn()
    const { rerender } = render(
      <RunLimitsSection value={{ max_tokens_per_run: 20000, max_tool_calls_per_run: 50 }} onChange={onChange} />,
    )
    const [tokensInput] = screen.getAllByRole('textbox')
    expect((tokensInput as HTMLInputElement).value).toBe('20000')

    rerender(
      <RunLimitsSection value={{ max_tokens_per_run: 5000, max_tool_calls_per_run: 50 }} onChange={onChange} />,
    )
    expect((tokensInput as HTMLInputElement).value).toBe('5000')
  })
})

describe('RunLimitsSection — Unlimited hint driven by prop', () => {
  it('shows Unlimited under tokens when prop is 0', () => {
    renderSection({ max_tokens_per_run: 0, max_tool_calls_per_run: 50 })
    // Both hint divs exist; only the one for tokens has text content.
    expect(screen.getByText('Unlimited')).toBeTruthy()
  })

  it('shows Unlimited under tool calls when prop is 0', () => {
    renderSection({ max_tokens_per_run: 20000, max_tool_calls_per_run: 0 })
    expect(screen.getByText('Unlimited')).toBeTruthy()
  })

  it('shows no Unlimited hint when both props are positive', () => {
    renderSection({ max_tokens_per_run: 20000, max_tool_calls_per_run: 50 })
    expect(screen.queryByText('Unlimited')).toBeNull()
  })

  it('shows two Unlimited hints when both props are 0', () => {
    renderSection({ max_tokens_per_run: 0, max_tool_calls_per_run: 0 })
    expect(screen.getAllByText('Unlimited')).toHaveLength(2)
  })
})
