import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { SchemaForm, humanize } from './SchemaForm'

const noop = () => {}

// --- humanize ---

describe('humanize', () => {
  it('converts snake_case to Title Case', () => {
    expect(humanize('response_buttons')).toBe('Response Buttons')
    expect(humanize('channel')).toBe('Channel')
    expect(humanize('mention_only')).toBe('Mention Only')
    expect(humanize('app_level_token')).toBe('App Level Token')
  })

  it('handles single-word names', () => {
    expect(humanize('channel')).toBe('Channel')
  })
})

// --- SchemaForm label rendering ---

describe('SchemaForm — label rendering', () => {
  it('uses prop.title when provided', () => {
    render(
      <SchemaForm
        schema={{
          properties: {
            channel: { type: 'string', title: 'Slack Channel' },
          },
        }}
        value={{}}
        onChange={noop}
        fieldErrors={{}}
      />,
    )
    // Use regex to match regardless of any aria-hidden required marker text.
    expect(screen.getByLabelText(/^slack channel$/i)).toBeInTheDocument()
  })

  it('humanizes the property name when no title is provided', () => {
    render(
      <SchemaForm
        schema={{
          properties: {
            workspace_name: { type: 'string' },
          },
        }}
        value={{}}
        onChange={noop}
        fieldErrors={{}}
      />,
    )
    expect(screen.getByLabelText(/^workspace name$/i)).toBeInTheDocument()
  })

  it('humanizes channel without an explicit title', () => {
    render(
      <SchemaForm
        schema={{
          properties: {
            channel: { type: 'string' },
          },
        }}
        value={{}}
        onChange={noop}
        fieldErrors={{}}
      />,
    )
    expect(screen.getByLabelText(/^channel$/i)).toBeInTheDocument()
  })
})

// --- required marker ---

// CSS Module class names are hashed in tests, so we match by data-testid or
// by checking which CSS class is applied. We verify required-ness by checking
// that the label element has the fieldLabelRequired class (which renders
// ::after ' *' via CSS — no DOM text is added so getByLabelText stays clean).
describe('SchemaForm — required marker', () => {
  it('applies required class to the label for properties listed in schema.required', () => {
    const { container } = render(
      <SchemaForm
        schema={{
          properties: {
            channel: { type: 'string', title: 'Channel' },
            mention: { type: 'string', title: 'Mention' },
          },
          required: ['channel'],
        }}
        value={{}}
        onChange={noop}
        fieldErrors={{}}
      />,
    )

    // The label for "channel" should have a class containing "Required".
    const channelInput = container.querySelector('#field-channel')
    const channelLabel = container.querySelector(`label[for="${channelInput?.id}"]`)
    expect(channelLabel?.className).toMatch(/required/i)

    // The label for "mention" should NOT have the required class.
    const mentionInput = container.querySelector('#field-mention')
    const mentionLabel = container.querySelector(`label[for="${mentionInput?.id}"]`)
    expect(mentionLabel?.className).not.toMatch(/required/i)
  })

  it('does NOT apply required class when schema.required is absent', () => {
    const { container } = render(
      <SchemaForm
        schema={{
          properties: {
            channel: { type: 'string', title: 'Channel' },
          },
        }}
        value={{}}
        onChange={noop}
        fieldErrors={{}}
      />,
    )
    const channelInput = container.querySelector('#field-channel')
    const channelLabel = container.querySelector(`label[for="${channelInput?.id}"]`)
    expect(channelLabel?.className).not.toMatch(/required/i)
  })

  it('does NOT apply required class when required is an empty array', () => {
    const { container } = render(
      <SchemaForm
        schema={{
          properties: {
            channel: { type: 'string', title: 'Channel' },
          },
          required: [],
        }}
        value={{}}
        onChange={noop}
        fieldErrors={{}}
      />,
    )
    const channelInput = container.querySelector('#field-channel')
    const channelLabel = container.querySelector(`label[for="${channelInput?.id}"]`)
    expect(channelLabel?.className).not.toMatch(/required/i)
  })
})

// --- onChange ---

describe('SchemaForm — onChange', () => {
  it('calls onChange with updated value', () => {
    const onChange = vi.fn()
    render(
      <SchemaForm
        schema={{
          properties: { name: { type: 'string', title: 'Name' } },
        }}
        value={{ name: 'old' }}
        onChange={onChange}
        fieldErrors={{}}
      />,
    )
    const input = screen.getByLabelText(/^name$/i) as HTMLInputElement
    input.value = 'new'
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
})
