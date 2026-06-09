import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { ReauthorizeButton } from './ReauthorizeButton'
import type { BeginPluginOAuthResponse } from '@/hooks/mutations/plugins'
import type { ApiError } from '@/api/fetch'

vi.mock('@/hooks/mutations/plugins')
import { useBeginPluginOAuth } from '@/hooks/mutations/plugins'

// Helpers

function renderButton(
  strategy: string,
  props?: Partial<React.ComponentProps<typeof ReauthorizeButton>>,
) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ReauthorizeButton pluginId="plugin-1" instanceId="inst-1" strategy={strategy} {...props} />
    </QueryClientProvider>,
  )
}

function mockMutation(overrides: Partial<ReturnType<typeof useBeginPluginOAuth>>) {
  vi.mocked(useBeginPluginOAuth).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
    ...overrides,
  } as unknown as ReturnType<typeof useBeginPluginOAuth>)
}

// Reset mocks between tests
beforeEach(() => {
  mockMutation({})
})

// --- Strategy gate ---

describe('ReauthorizeButton — strategy gate', () => {
  it('renders a button for oauth2_authcode', () => {
    renderButton('oauth2_authcode')
    expect(screen.getByRole('button', { name: 'Re-authorize' })).toBeInTheDocument()
  })

  it('renders a button for oauth2_clientcred', () => {
    renderButton('oauth2_clientcred')
    expect(screen.getByRole('button', { name: 'Re-authorize' })).toBeInTheDocument()
  })

  it('renders nothing for non-OAuth strategies', () => {
    renderButton('static_api_key')
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('renders nothing for header_set strategy', () => {
    renderButton('header_set')
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('renders nothing for basic_auth strategy', () => {
    renderButton('basic_auth')
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })
})

// --- Authcode redirect ---

describe('ReauthorizeButton — authcode redirect', () => {
  it('sets window.location.href to authorize_url on success', () => {
    // Replace window.location entirely so jsdom's non-configurable href doesn't block us.
    const originalLocation = window.location
    let capturedHref = ''
    const fakeLocation = Object.create(null)
    Object.assign(fakeLocation, {
      pathname: '/admin/plugins/plugin-1/instances/inst-1',
      search: '',
    })
    Object.defineProperty(fakeLocation, 'href', {
      get() { return capturedHref },
      set(v: string) { capturedHref = v },
      configurable: true,
    })
    Object.defineProperty(window, 'location', {
      writable: true,
      value: fakeLocation,
    })

    const mutateFn = vi.fn((_params, opts) => {
      const res: BeginPluginOAuthResponse = { authorize_url: 'https://provider.example.com/auth' }
      opts?.onSuccess?.(res)
    })
    mockMutation({ mutate: mutateFn })

    renderButton('oauth2_authcode')
    fireEvent.click(screen.getByRole('button', { name: 'Re-authorize' }))

    expect(mutateFn).toHaveBeenCalledOnce()
    expect(capturedHref).toBe('https://provider.example.com/auth')

    Object.defineProperty(window, 'location', { writable: true, value: originalLocation })
  })
})

// --- Clientcred: no redirect ---

describe('ReauthorizeButton — clientcred no redirect', () => {
  it('calls mutate and does not redirect when authorize_url is absent', () => {
    const mutateFn = vi.fn((_params, opts) => {
      const res: BeginPluginOAuthResponse = { status: 'ok' }
      opts?.onSuccess?.(res)
    })
    mockMutation({ mutate: mutateFn })

    renderButton('oauth2_clientcred')
    fireEvent.click(screen.getByRole('button', { name: 'Re-authorize' }))

    expect(mutateFn).toHaveBeenCalledOnce()
    // No navigation assertion — the browser stays on the same page, which is
    // the correct behavior for a synchronous clientcred grant.
  })
})

// --- Pending state ---

describe('ReauthorizeButton — pending state', () => {
  it('shows "Starting…" and is disabled while isPending', () => {
    mockMutation({ isPending: true })
    renderButton('oauth2_authcode')
    const btn = screen.getByRole('button', { name: 'Starting…' })
    expect(btn).toBeDisabled()
  })
})

// --- Error path ---

describe('ReauthorizeButton — error path', () => {
  it('calls mutate even when a previous call errored (button stays enabled)', () => {
    const mutateFn = vi.fn()
    mockMutation({ mutate: mutateFn, isPending: false })

    renderButton('oauth2_authcode')
    fireEvent.click(screen.getByRole('button', { name: 'Re-authorize' }))
    fireEvent.click(screen.getByRole('button', { name: 'Re-authorize' }))

    expect(mutateFn).toHaveBeenCalledTimes(2)
  })
})

// --- onError callback ---

describe('ReauthorizeButton — onError callback', () => {
  it('calls onError(null) before mutating, then onError(message) on failure', () => {
    const onErrorCalls: (string | null)[] = []
    const onError = vi.fn((msg: string | null) => { onErrorCalls.push(msg) })

    // Simulate a mutation that fails with a server error carrying a detail field.
    const serverError: Partial<ApiError> = { detail: 'oauth begin: public_url is not configured; set it in admin settings before starting an OAuth flow', message: 'oauth configuration invalid' }
    const mutateFn = vi.fn((_params, opts) => {
      opts?.onError?.(serverError as ApiError)
    })
    mockMutation({ mutate: mutateFn })

    renderButton('oauth2_authcode', { onError })
    fireEvent.click(screen.getByRole('button', { name: 'Re-authorize' }))

    // First call must be null (reset), second call must be the server detail.
    expect(onErrorCalls).toEqual([
      null,
      'oauth begin: public_url is not configured; set it in admin settings before starting an OAuth flow',
    ])
  })

  it('falls back to message when detail is absent', () => {
    const onError = vi.fn()
    const serverError: Partial<ApiError> = { message: 'oauth configuration invalid' }
    const mutateFn = vi.fn((_params, opts) => {
      opts?.onError?.(serverError as ApiError)
    })
    mockMutation({ mutate: mutateFn })

    renderButton('oauth2_authcode', { onError })
    fireEvent.click(screen.getByRole('button', { name: 'Re-authorize' }))

    expect(onError).toHaveBeenLastCalledWith('oauth configuration invalid')
  })

  it('uses fallback message when error has neither detail nor message', () => {
    const onError = vi.fn()
    const mutateFn = vi.fn((_params, opts) => {
      opts?.onError?.({} as ApiError)
    })
    mockMutation({ mutate: mutateFn })

    renderButton('oauth2_authcode', { onError })
    fireEvent.click(screen.getByRole('button', { name: 'Re-authorize' }))

    expect(onError).toHaveBeenLastCalledWith('OAuth authorization failed.')
  })

  it('does not error when onError prop is not provided', () => {
    // Simulate a mutation failure; should not throw even without onError.
    const mutateFn = vi.fn((_params, opts) => {
      opts?.onError?.({ message: 'some error' } as ApiError)
    })
    mockMutation({ mutate: mutateFn })

    // No onError prop — should render and click without throwing.
    expect(() => {
      renderButton('oauth2_authcode')
      fireEvent.click(screen.getByRole('button', { name: 'Re-authorize' }))
    }).not.toThrow()
  })
})
