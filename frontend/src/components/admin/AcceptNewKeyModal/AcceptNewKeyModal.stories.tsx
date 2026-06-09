import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { AcceptNewKeyModal } from './AcceptNewKeyModal'

function makeQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

// Fixture fingerprints — 8-byte hex values matching the Minisign key ID format.
const OLD_FINGERPRINT = 'a1b2c3d4e5f6a7b8'
const NEW_FINGERPRINT = 'deadbeef01020304'
// Minimal valid-looking base64 for story purposes.
const CANDIDATE_B64 = 'dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXkK'

const meta: Meta<typeof AcceptNewKeyModal> = {
  title: 'Admin/AcceptNewKeyModal',
  component: AcceptNewKeyModal,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof AcceptNewKeyModal>

// Primary — idle state showing both fingerprints and the warning.
export const Primary: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins/:id/accept-new-key', () =>
          HttpResponse.json({
            data: { accepted_pubkey_fingerprint: NEW_FINGERPRINT, instances_unblocked: 1 },
          }),
        ),
      ],
    },
  },
  args: {
    pluginId: 'plugin-abc',
    oldPubkeyFingerprint: OLD_FINGERPRINT,
    newPubkeyFingerprint: NEW_FINGERPRINT,
    candidatePubkeyB64: CANDIDATE_B64,
    onClose: () => {},
  },
}

// Loading — button is in loading state (simulate slow response).
export const Loading: Story = {
  parameters: {
    msw: {
      handlers: [
        // Never resolves — keeps the UI in loading state.
        http.post('/api/v1/admin/plugins/:id/accept-new-key', () => new Promise(() => {})),
      ],
    },
  },
  args: {
    pluginId: 'plugin-abc',
    oldPubkeyFingerprint: OLD_FINGERPRINT,
    newPubkeyFingerprint: NEW_FINGERPRINT,
    candidatePubkeyB64: CANDIDATE_B64,
    onClose: () => {},
  },
}

// Error — the backend rejects the candidate pubkey.
export const WithError: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins/:id/accept-new-key', () =>
          HttpResponse.json(
            { error: 'Pubkey verification failed', detail: 'Candidate key does not match the expected format.' },
            { status: 400 },
          ),
        ),
      ],
    },
  },
  args: {
    pluginId: 'plugin-abc',
    oldPubkeyFingerprint: OLD_FINGERPRINT,
    newPubkeyFingerprint: NEW_FINGERPRINT,
    candidatePubkeyB64: CANDIDATE_B64,
    onClose: () => {},
  },
}

// PreviouslyUnsigned — plugin was unsigned on first install; old fingerprint is empty.
// The chip → modal wiring in a future /admin/plugins page should handle this gracefully.
export const PreviouslyUnsigned: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins/:id/accept-new-key', () =>
          HttpResponse.json({
            data: { accepted_pubkey_fingerprint: NEW_FINGERPRINT, instances_unblocked: 2 },
          }),
        ),
      ],
    },
  },
  args: {
    pluginId: 'plugin-unsigned',
    oldPubkeyFingerprint: '',
    newPubkeyFingerprint: NEW_FINGERPRINT,
    candidatePubkeyB64: CANDIDATE_B64,
    onClose: () => {},
  },
}
