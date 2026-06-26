import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ResponseButtonsEditor } from './ResponseButtonsEditor'
import type { ResponseButton } from './ResponseButtonsEditor'

const ONE_BUTTON: ResponseButton[] = [
  { option_id: 'approve', label: 'Approve', value: 'approved', style: 'primary' },
]

const TWO_BUTTONS: ResponseButton[] = [
  { option_id: 'approve', label: 'Approve', value: 'approved', style: 'primary' },
  { option_id: 'reject', label: 'Reject', value: 'rejected', style: 'danger' },
]

describe('ResponseButtonsEditor — empty state', () => {
  it('shows "+ Add button" when value is undefined', () => {
    render(<ResponseButtonsEditor value={undefined} onChange={vi.fn()} />)
    expect(screen.getByRole('button', { name: /\+ add button/i })).toBeInTheDocument()
  })

  it('shows the default note when value is undefined', () => {
    render(<ResponseButtonsEditor value={undefined} onChange={vi.fn()} />)
    expect(screen.getByText(/defaults to approve \/ reject if omitted/i)).toBeInTheDocument()
  })

  it('shows "+ Add button" when value is an empty array', () => {
    render(<ResponseButtonsEditor value={[]} onChange={vi.fn()} />)
    expect(screen.getByRole('button', { name: /\+ add button/i })).toBeInTheDocument()
  })
})

describe('ResponseButtonsEditor — add a button', () => {
  it('calls onChange with a single blank button when + Add button is clicked from empty', async () => {
    const onChange = vi.fn()
    render(<ResponseButtonsEditor value={undefined} onChange={onChange} />)
    await userEvent.click(screen.getByRole('button', { name: /\+ add button/i }))
    expect(onChange).toHaveBeenCalledOnce()
    const [next] = onChange.mock.calls[0]
    expect(next).toHaveLength(1)
    expect(next[0]).toMatchObject({ option_id: '', label: '', value: '' })
  })

  it('appends a blank button to an existing list', async () => {
    const onChange = vi.fn()
    render(<ResponseButtonsEditor value={ONE_BUTTON} onChange={onChange} />)
    await userEvent.click(screen.getByRole('button', { name: /\+ add button/i }))
    expect(onChange).toHaveBeenCalledOnce()
    const [next] = onChange.mock.calls[0]
    expect(next).toHaveLength(2)
  })
})

describe('ResponseButtonsEditor — remove a button', () => {
  it('calls onChange with undefined when the only row is removed', async () => {
    const onChange = vi.fn()
    render(<ResponseButtonsEditor value={ONE_BUTTON} onChange={onChange} />)
    await userEvent.click(screen.getByRole('button', { name: /remove button 1/i }))
    expect(onChange).toHaveBeenCalledOnce()
    expect(onChange.mock.calls[0][0]).toBeUndefined()
  })

  it('calls onChange with remaining buttons when one of two rows is removed', async () => {
    const onChange = vi.fn()
    render(<ResponseButtonsEditor value={TWO_BUTTONS} onChange={onChange} />)
    const removeBtns = screen.getAllByRole('button', { name: /remove button/i })
    await userEvent.click(removeBtns[0])
    expect(onChange).toHaveBeenCalledOnce()
    const [next] = onChange.mock.calls[0]
    expect(next).toHaveLength(1)
    expect(next[0].option_id).toBe('reject')
  })
})

describe('ResponseButtonsEditor — field editing', () => {
  it('renders all fields for a button row', () => {
    render(<ResponseButtonsEditor value={ONE_BUTTON} onChange={vi.fn()} />)
    expect(screen.getByLabelText(/^id$/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/^label$/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/^value$/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/^style$/i)).toBeInTheDocument()
  })

  it('updates option_id on change', async () => {
    const onChange = vi.fn()
    render(<ResponseButtonsEditor value={ONE_BUTTON} onChange={onChange} />)
    const idInput = screen.getByLabelText(/^id$/i) as HTMLInputElement
    fireEvent.change(idInput, { target: { value: 'confirm' } })
    expect(onChange).toHaveBeenCalledOnce()
    const [next] = onChange.mock.calls[0]
    expect(next[0].option_id).toBe('confirm')
  })

  it('omits style key when blank is selected (S4)', async () => {
    const onChange = vi.fn()
    render(<ResponseButtonsEditor value={ONE_BUTTON} onChange={onChange} />)
    const styleSelect = screen.getByLabelText(/^style$/i) as HTMLSelectElement
    // Select the blank option
    fireEvent.change(styleSelect, { target: { value: '' } })
    expect(onChange).toHaveBeenCalledOnce()
    const [next] = onChange.mock.calls[0]
    expect(next[0]).not.toHaveProperty('style')
  })
})

describe('ResponseButtonsEditor — disabled state', () => {
  it('disables all inputs and buttons when disabled=true', () => {
    render(<ResponseButtonsEditor value={ONE_BUTTON} onChange={vi.fn()} disabled />)
    const inputs = screen.getAllByRole('textbox')
    inputs.forEach((input) => expect(input).toBeDisabled())
    expect(screen.getByRole('button', { name: /\+ add button/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /remove button 1/i })).toBeDisabled()
  })
})
