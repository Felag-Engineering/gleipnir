import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { SimplifiedBadge, simplifiedLabel } from './SimplifiedBadge'

describe('simplifiedLabel', () => {
  it('maps provider lists to the expected label', () => {
    const cases: Array<[string[], string]> = [
      [[], ''],
      [['google'], 'Simplified for Google'],
      // formatProviderName must be used — "OpenAI", never "Openai".
      [['google', 'openai'], 'Simplified for Google, OpenAI'],
      [['google', 'openai', 'mistral'], 'Simplified for 3 providers'],
    ]
    for (const [providers, expected] of cases) {
      expect(simplifiedLabel(providers)).toBe(expected)
    }
  })
})

describe('SimplifiedBadge', () => {
  it('renders null for an empty provider list', () => {
    const { container } = render(<SimplifiedBadge providers={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders the label and a full tooltip for one provider', () => {
    render(<SimplifiedBadge providers={['google']} />)
    const badge = screen.getByText('Simplified for Google')
    expect(badge).toHaveAttribute(
      'title',
      "Gleipnir shows a simplified version of this tool's parameters to Google. " +
        'This runs entirely downstream of enforcement, so it only changes what the model is shown and ' +
        'never widens what the tool is allowed to receive.',
    )
  })

  it('renders the collapsed count label but still names every provider in the tooltip for 3+', () => {
    render(<SimplifiedBadge providers={['google', 'openai', 'mistral']} />)
    const badge = screen.getByText('Simplified for 3 providers')
    expect(badge).toHaveAttribute(
      'title',
      "Gleipnir shows a simplified version of this tool's parameters to Google, OpenAI and Mistral. " +
        'This runs entirely downstream of enforcement, so it only changes what the model is shown and ' +
        'never widens what the tool is allowed to receive.',
    )
  })
})
