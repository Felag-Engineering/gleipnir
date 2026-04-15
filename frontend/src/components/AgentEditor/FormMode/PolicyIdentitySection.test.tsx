import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { PolicyIdentitySection } from './PolicyIdentitySection'
import type { IdentityFormState } from './types'

const DEFAULT_VALUE: IdentityFormState = { name: '', description: '', folder: '' }

describe('PolicyIdentitySection — folder combobox', () => {
  it('renders a plain input with no datalist when existingFolders is empty', () => {
    render(
      <PolicyIdentitySection value={DEFAULT_VALUE} onChange={vi.fn()} existingFolders={[]} />,
    )
    const folderInput = screen.getByPlaceholderText('Ungrouped')
    expect(folderInput).not.toHaveAttribute('list')
    expect(document.querySelector('datalist')).toBeNull()
  })

  it('renders a datalist with suggestions when existingFolders has entries', () => {
    render(
      <PolicyIdentitySection
        value={DEFAULT_VALUE}
        onChange={vi.fn()}
        existingFolders={['deployments', 'monitoring']}
      />,
    )
    const folderInput = screen.getByPlaceholderText('Ungrouped')
    expect(folderInput).toHaveAttribute('list', 'policy-folder-suggestions')

    const datalist = document.querySelector('datalist#policy-folder-suggestions')
    expect(datalist).not.toBeNull()

    const options = datalist!.querySelectorAll('option')
    expect(options).toHaveLength(2)
    expect(options[0]).toHaveValue('deployments')
    expect(options[1]).toHaveValue('monitoring')
  })

  it('omits datalist when existingFolders prop is not provided', () => {
    render(<PolicyIdentitySection value={DEFAULT_VALUE} onChange={vi.fn()} />)
    expect(document.querySelector('datalist')).toBeNull()
  })
})
