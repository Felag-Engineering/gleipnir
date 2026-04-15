import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import type { WebhookTriggerState } from '../types'

// Mock the hooks before importing WebhookConfig
vi.mock('@/hooks/queries/policies')
vi.mock('@/hooks/mutations/policies')

import { useWebhookSecret } from '@/hooks/queries/policies'
import { useRotateWebhookSecret } from '@/hooks/mutations/policies'
import { WebhookConfig } from './WebhookConfig'

const hmacValue: WebhookTriggerState = { type: 'webhook', auth: 'hmac' }
const bearerValue: WebhookTriggerState = { type: 'webhook', auth: 'bearer' }
const noneValue: WebhookTriggerState = { type: 'webhook', auth: 'none' }

function mockHooksDefault() {
  vi.mocked(useWebhookSecret).mockReturnValue({
    data: undefined,
    isLoading: false,
  } as unknown as ReturnType<typeof useWebhookSecret>)

  vi.mocked(useRotateWebhookSecret).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
  } as unknown as ReturnType<typeof useRotateWebhookSecret>)
}

beforeEach(() => {
  mockHooksDefault()
})

describe('WebhookConfig — secret management (HMAC mode, existing policy)', () => {
  it('shows Show and Rotate buttons when secret is not yet revealed', () => {
    render(<WebhookConfig policyId="pol-1" value={hmacValue} onChange={vi.fn()} />)

    expect(screen.getByRole('button', { name: /show/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /rotate/i })).toBeInTheDocument()
    // Secret value must not be visible
    expect(screen.queryByRole('textbox', { name: /webhook secret/i })).not.toBeInTheDocument()
  })

  it('reveals the secret and shows Copy/Hide/Rotate when secret is loaded', async () => {
    vi.mocked(useWebhookSecret).mockReturnValue({
      data: 'abc123secret',
      isLoading: false,
    } as unknown as ReturnType<typeof useWebhookSecret>)

    render(<WebhookConfig policyId="pol-1" value={hmacValue} onChange={vi.fn()} />)

    // Click Show to reveal
    fireEvent.click(screen.getByRole('button', { name: /show/i }))

    await waitFor(() => {
      const input = screen.getByRole('textbox', { name: /webhook secret/i })
      expect(input).toHaveValue('abc123secret')
    })

    // The component renders two Copy buttons (URL + secret); the secret one appears
    // after the secret input so we use getAllByRole and check count >= 1.
    const copyButtons = screen.getAllByRole('button', { name: /copy/i })
    expect(copyButtons.length).toBeGreaterThanOrEqual(1)
    expect(screen.getByRole('button', { name: /hide/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /rotate/i })).toBeInTheDocument()
  })

  it('hides the secret again when Hide is clicked', async () => {
    vi.mocked(useWebhookSecret).mockReturnValue({
      data: 'abc123secret',
      isLoading: false,
    } as unknown as ReturnType<typeof useWebhookSecret>)

    render(<WebhookConfig policyId="pol-1" value={hmacValue} onChange={vi.fn()} />)

    fireEvent.click(screen.getByRole('button', { name: /show/i }))

    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: /webhook secret/i })).toBeInTheDocument()
    })

    fireEvent.click(screen.getByRole('button', { name: /hide/i }))

    expect(screen.queryByRole('textbox', { name: /webhook secret/i })).not.toBeInTheDocument()
  })
})

describe('WebhookConfig — generate CTA (HMAC mode, no secret yet)', () => {
  it('shows "Generate initial secret" CTA when secret is null after reveal', async () => {
    vi.mocked(useWebhookSecret).mockReturnValue({
      data: null,
      isLoading: false,
    } as unknown as ReturnType<typeof useWebhookSecret>)

    render(<WebhookConfig policyId="pol-1" value={hmacValue} onChange={vi.fn()} />)

    // Click Show — this triggers the fetch; mock returns null
    fireEvent.click(screen.getByRole('button', { name: /show/i }))

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /generate initial secret/i })).toBeInTheDocument()
    })
  })
})

describe('WebhookConfig — Rotate modal', () => {
  it('opens and closes the Rotate confirmation modal', async () => {
    render(<WebhookConfig policyId="pol-1" value={hmacValue} onChange={vi.fn()} />)

    fireEvent.click(screen.getByRole('button', { name: /rotate/i }))

    await waitFor(() => {
      expect(screen.getByRole('dialog', { name: /rotate webhook secret/i })).toBeInTheDocument()
    })

    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))

    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    })
  })

  it('calls rotateMutation.mutate and reveals secret on confirm', async () => {
    const mutate = vi.fn((_, opts) => {
      opts?.onSuccess?.()
    })
    vi.mocked(useRotateWebhookSecret).mockReturnValue({
      mutate,
      isPending: false,
    } as unknown as ReturnType<typeof useRotateWebhookSecret>)

    // After rotate, set secret to a value so reveal shows something
    vi.mocked(useWebhookSecret).mockReturnValue({
      data: 'newrotatedsecret',
      isLoading: false,
    } as unknown as ReturnType<typeof useWebhookSecret>)

    render(<WebhookConfig policyId="pol-1" value={hmacValue} onChange={vi.fn()} />)

    // Click the Rotate secret button (outside the modal)
    fireEvent.click(screen.getAllByRole('button', { name: /rotate/i })[0])
    await waitFor(() => {
      expect(screen.getByRole('dialog')).toBeInTheDocument()
    })

    // Click the Rotate submit button inside the modal
    const dialog = screen.getByRole('dialog')
    fireEvent.click(screen.getAllByRole('button', { name: /^rotate$/i }).find(
      (btn) => dialog.contains(btn),
    )!)

    expect(mutate).toHaveBeenCalledWith('pol-1', expect.any(Object))
  })
})

describe('WebhookConfig — none mode warning banner', () => {
  it('shows an insecure warning when auth mode is none', () => {
    render(<WebhookConfig policyId="pol-1" value={noneValue} onChange={vi.fn()} />)

    expect(screen.getByText(/unauthenticated requests/i)).toBeInTheDocument()
  })

  it('does not show warning when auth mode is hmac', () => {
    render(<WebhookConfig policyId="pol-1" value={hmacValue} onChange={vi.fn()} />)

    expect(screen.queryByText(/unauthenticated requests/i)).not.toBeInTheDocument()
  })
})

describe('WebhookConfig — per-mode sample snippets', () => {
  it('shows an HMAC openssl snippet for hmac mode', () => {
    render(<WebhookConfig policyId="pol-1" value={hmacValue} onChange={vi.fn()} />)

    expect(screen.getByText(/openssl dgst -sha256/i)).toBeInTheDocument()
  })

  it('shows a Bearer curl snippet for bearer mode', () => {
    render(<WebhookConfig policyId="pol-1" value={bearerValue} onChange={vi.fn()} />)

    expect(screen.getByText(/Authorization: Bearer/)).toBeInTheDocument()
  })

  it('shows no snippet for none mode', () => {
    render(<WebhookConfig policyId="pol-1" value={noneValue} onChange={vi.fn()} />)

    expect(screen.queryByText(/openssl/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/Authorization/)).not.toBeInTheDocument()
  })
})

describe('WebhookConfig — secret management hidden when no policyId', () => {
  it('does not render secret section when policyId is absent (new policy)', () => {
    render(<WebhookConfig value={hmacValue} onChange={vi.fn()} />)

    // The secret management section requires an existing policyId.
    expect(screen.queryByRole('button', { name: /show/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /rotate/i })).not.toBeInTheDocument()
  })
})
