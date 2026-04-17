import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, waitFor } from '@testing-library/react'

// Mock hooks before importing ModelSection
vi.mock('@/hooks/queries/users')
vi.mock('@/hooks/queries/config')

import { useModels } from '@/hooks/queries/users'
import { usePublicConfig } from '@/hooks/queries/config'
import { ModelSection } from './ModelSection'

const MOCK_PROVIDERS = [
  {
    provider: 'anthropic',
    models: [
      { name: 'claude-sonnet-4-6', display_name: 'Claude Sonnet 4.6' },
      { name: 'claude-opus-4-6', display_name: 'Claude Opus 4.6' },
    ],
  },
  {
    provider: 'google',
    models: [{ name: 'gemini-pro', display_name: 'Gemini Pro' }],
  },
]

function mockNoDefault() {
  vi.mocked(useModels).mockReturnValue({
    data: MOCK_PROVIDERS,
    isLoading: false,
    isError: false,
  } as unknown as ReturnType<typeof useModels>)

  vi.mocked(usePublicConfig).mockReturnValue({
    data: { public_url: '', default_model: null },
  } as unknown as ReturnType<typeof usePublicConfig>)
}

beforeEach(() => {
  mockNoDefault()
})

describe('ModelSection — no system default configured', () => {
  it('does not call onChange when initial value is empty and no default is configured', async () => {
    const onChange = vi.fn()
    render(<ModelSection value={{ provider: '', model: '' }} onChange={onChange} />)

    // Wait one tick for effects to settle — onChange must NOT be called
    await new Promise((r) => setTimeout(r, 0))
    expect(onChange).not.toHaveBeenCalled()
  })
})

describe('ModelSection — system default configured', () => {
  it('calls onChange with the system default when initial value is empty', async () => {
    vi.mocked(usePublicConfig).mockReturnValue({
      data: { public_url: '', default_model: { provider: 'anthropic', name: 'claude-sonnet-4-6' } },
    } as unknown as ReturnType<typeof usePublicConfig>)

    const onChange = vi.fn()
    render(<ModelSection value={{ provider: '', model: '' }} onChange={onChange} />)

    await waitFor(() => {
      expect(onChange).toHaveBeenCalledWith({ provider: 'anthropic', model: 'claude-sonnet-4-6' })
    })
  })

  it('does NOT call onChange when current value is already set (preserves existing choice)', async () => {
    vi.mocked(usePublicConfig).mockReturnValue({
      data: { public_url: '', default_model: { provider: 'anthropic', name: 'claude-sonnet-4-6' } },
    } as unknown as ReturnType<typeof usePublicConfig>)

    const onChange = vi.fn()
    render(<ModelSection value={{ provider: 'google', model: 'gemini-pro' }} onChange={onChange} />)

    // Wait for effects — onChange must NOT be called because a model is already chosen
    await new Promise((r) => setTimeout(r, 0))
    expect(onChange).not.toHaveBeenCalled()
  })
})
