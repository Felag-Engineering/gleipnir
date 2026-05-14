import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ApiPluginInstanceForAudience } from '@/api/types'
import { SubscribedBindingConfig } from './SubscribedBindingConfig'

// Mock the mutation hook to avoid real fetch calls in unit tests.
vi.mock('@/hooks/mutations/bindingTest')
import { useTestBindingAgainstSamples } from '@/hooks/mutations/bindingTest'

// --- Fixtures ---

const SLACK_INSTANCE: ApiPluginInstanceForAudience = {
  id: 'inst-slack',
  plugin_id: 'plugin-slack',
  instance_name: 'slack-prod',
  state: 'healthy',
  implements_notify: true,
  implements_request: false,
  config_schema: null,
  event_kinds: [
    {
      kind: 'channel_message',
      description: 'A message in a channel',
      binding_schema: {
        type: 'object',
        properties: {
          channel: { type: 'string' },
          mention_only: { type: 'boolean' },
        },
      },
      examples: [
        { name: 'incident-channel', payload: { channel: '#incidents', text: 'alert' } },
        { name: 'general-channel', payload: { channel: '#general', text: 'hello' } },
      ],
    },
  ],
}

const NO_EXAMPLES_INSTANCE: ApiPluginInstanceForAudience = {
  ...SLACK_INSTANCE,
  id: 'inst-no-ex',
  instance_name: 'no-examples-inst',
  event_kinds: [
    {
      kind: 'channel_message',
      description: 'A message in a channel',
      binding_schema: SLACK_INSTANCE.event_kinds![0].binding_schema,
      examples: [],
    },
  ],
}

function makeWrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
  return Wrapper
}

function mockMutationDefault() {
  vi.mocked(useTestBindingAgainstSamples).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
    isError: false,
    error: null,
    data: undefined,
  } as unknown as ReturnType<typeof useTestBindingAgainstSamples>)
}

beforeEach(() => {
  vi.clearAllMocks()
  mockMutationDefault()
})

describe('SubscribedBindingConfig — rendering', () => {
  it('renders binding field inputs from the schema', () => {
    render(
      <SubscribedBindingConfig
        source="slack-prod"
        eventKind="channel_message"
        binding={{}}
        onChange={vi.fn()}
        pluginInstances={[SLACK_INSTANCE]}
      />,
      { wrapper: makeWrapper() },
    )

    expect(screen.getByRole('textbox')).toBeInTheDocument()
    expect(screen.getByLabelText(/channel/i)).toBeInTheDocument()
  })

  it('renders example names in the results area after a test', () => {
    vi.mocked(useTestBindingAgainstSamples).mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      isError: false,
      error: null,
      data: { results: [{ match: true }, { match: false }] },
    } as unknown as ReturnType<typeof useTestBindingAgainstSamples>)

    render(
      <SubscribedBindingConfig
        source="slack-prod"
        eventKind="channel_message"
        binding={{ channel: '#incidents' }}
        onChange={vi.fn()}
        pluginInstances={[SLACK_INSTANCE]}
      />,
      { wrapper: makeWrapper() },
    )

    expect(screen.getByText('incident-channel')).toBeInTheDocument()
    expect(screen.getByText('general-channel')).toBeInTheDocument()
    expect(screen.getByText('matched')).toBeInTheDocument()
    expect(screen.getByText('did not match')).toBeInTheDocument()
  })

  it('shows the test button enabled when examples exist', () => {
    render(
      <SubscribedBindingConfig
        source="slack-prod"
        eventKind="channel_message"
        binding={{}}
        onChange={vi.fn()}
        pluginInstances={[SLACK_INSTANCE]}
      />,
      { wrapper: makeWrapper() },
    )

    const btn = screen.getByRole('button', { name: /test against sample/i })
    expect(btn).not.toBeDisabled()
  })

  it('disables the button and shows tooltip when no examples', () => {
    render(
      <SubscribedBindingConfig
        source="no-examples-inst"
        eventKind="channel_message"
        binding={{}}
        onChange={vi.fn()}
        pluginInstances={[NO_EXAMPLES_INSTANCE]}
      />,
      { wrapper: makeWrapper() },
    )

    const btn = screen.getByRole('button', { name: /test against sample/i })
    expect(btn).toBeDisabled()
    expect(screen.getByText(/Plugin has no examples/)).toBeInTheDocument()
  })
})

describe('SubscribedBindingConfig — click sends all payloads', () => {
  it('calls mutate with binding and all example payloads', async () => {
    const mutate = vi.fn()
    vi.mocked(useTestBindingAgainstSamples).mockReturnValue({
      mutate,
      isPending: false,
      isError: false,
      error: null,
      data: undefined,
    } as unknown as ReturnType<typeof useTestBindingAgainstSamples>)

    const binding = { channel: '#incidents' }
    render(
      <SubscribedBindingConfig
        source="slack-prod"
        eventKind="channel_message"
        binding={binding}
        onChange={vi.fn()}
        pluginInstances={[SLACK_INSTANCE]}
      />,
      { wrapper: makeWrapper() },
    )

    fireEvent.click(screen.getByRole('button', { name: /test against sample/i }))

    await waitFor(() => {
      expect(mutate).toHaveBeenCalledWith({
        binding,
        payloads: [
          { channel: '#incidents', text: 'alert' },
          { channel: '#general', text: 'hello' },
        ],
      })
    })
  })
})

describe('SubscribedBindingConfig — 400 compile error surfacing', () => {
  it('displays the error detail from a compile error', () => {
    // Simulate an error with a `detail` property, matching ApiError shape.
    const err = Object.assign(new Error('binding compile error'), {
      name: 'ApiError',
      status: 400,
      detail: 'binding: invalid regular expression (Go RE2 required)',
    })

    vi.mocked(useTestBindingAgainstSamples).mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      isError: true,
      error: err,
      data: undefined,
    } as unknown as ReturnType<typeof useTestBindingAgainstSamples>)

    render(
      <SubscribedBindingConfig
        source="slack-prod"
        eventKind="channel_message"
        binding={{ pattern: '[bad(' }}
        onChange={vi.fn()}
        pluginInstances={[SLACK_INSTANCE]}
      />,
      { wrapper: makeWrapper() },
    )

    expect(screen.getByText(/binding: invalid regular expression/)).toBeInTheDocument()
  })
})
