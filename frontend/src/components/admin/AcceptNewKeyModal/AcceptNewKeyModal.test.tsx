import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { AcceptNewKeyModal } from './AcceptNewKeyModal'

const OLD_FP = 'a1b2c3d4e5f6a7b8'
const NEW_FP = 'deadbeef01020304'
// Minimal base64 string for the candidate pubkey field.
const CANDIDATE_B64 = 'dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXkK'

function makeClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
}

function renderModal(onClose = vi.fn()) {
  return render(
    <QueryClientProvider client={makeClient()}>
      <AcceptNewKeyModal
        pluginId="plugin-test"
        oldPubkeyFingerprint={OLD_FP}
        newPubkeyFingerprint={NEW_FP}
        candidatePubkeyB64={CANDIDATE_B64}
        onClose={onClose}
      />
    </QueryClientProvider>,
  )
}

describe('AcceptNewKeyModal', () => {
  it('renders both key fingerprints', () => {
    renderModal()
    expect(screen.getByText(OLD_FP)).toBeInTheDocument()
    expect(screen.getByText(NEW_FP)).toBeInTheDocument()
  })

  it('shows a warning about verifying the key out-of-band', () => {
    renderModal()
    expect(screen.getByText(/different signing key/i)).toBeInTheDocument()
  })

  it('calls mutation with the correct candidate_pubkey on accept', async () => {
    let capturedBody: unknown
    server.use(
      http.post('/api/v1/admin/plugins/plugin-test/accept-new-key', async ({ request }) => {
        capturedBody = await request.json()
        return HttpResponse.json({
          data: { accepted_pubkey_fingerprint: NEW_FP, instances_unblocked: 1 },
        })
      }),
    )

    const onClose = vi.fn()
    renderModal(onClose)

    await userEvent.click(screen.getByRole('button', { name: /accept new key/i }))

    await waitFor(() => expect(onClose).toHaveBeenCalledOnce())
    expect(capturedBody).toEqual({ candidate_pubkey: CANDIDATE_B64 })
  })

  it('surfaces error message inline on API failure', async () => {
    server.use(
      http.post('/api/v1/admin/plugins/plugin-test/accept-new-key', () =>
        HttpResponse.json(
          { error: 'Bad request', detail: 'Candidate key is malformed.' },
          { status: 400 },
        ),
      ),
    )

    renderModal()
    await userEvent.click(screen.getByRole('button', { name: /accept new key/i }))

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent('Candidate key is malformed.'),
    )
  })

  it('calls onClose when Cancel is clicked', async () => {
    // No API handler needed — cancel never hits the network.
    server.use(
      http.post('/api/v1/admin/plugins/:id/accept-new-key', () =>
        HttpResponse.json({ data: { accepted_pubkey_fingerprint: NEW_FP, instances_unblocked: 0 } }),
      ),
    )
    const onClose = vi.fn()
    renderModal(onClose)
    await userEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(onClose).toHaveBeenCalledOnce()
  })
})
