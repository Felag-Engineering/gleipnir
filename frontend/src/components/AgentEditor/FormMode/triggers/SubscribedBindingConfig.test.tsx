import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router'
import type { ApiPluginInstanceForAudience } from '@/api/types'
import { SubscribedBindingConfig } from './SubscribedBindingConfig'

// Mock the mutation hook to avoid real fetch calls in unit tests.
vi.mock('@/hooks/mutations/bindingTest')
import { useTestBindingAgainstSamples } from '@/hooks/mutations/bindingTest'

// Mock usePluginOptions so OptionsBindingField (AsyncCombobox) never fires
// real requests. Tests that assert combobox rendering don't need real data.
vi.mock('@/hooks/usePluginOptions', () => ({
  usePluginOptions: vi.fn().mockReturnValue({ data: undefined, isLoading: false }),
}))

// --- Fixtures ---

// SLACK_INSTANCE has:
//   - a plain text field (`text`) to keep the role="textbox" assertion valid
//   - a channel_id field with x-gleipnir-options so it renders role="combobox"
//   - title/description on both fields
//   - a guidance string for the "How this fires" block
const SLACK_INSTANCE: ApiPluginInstanceForAudience = {
  id: 'inst-slack',
  plugin_id: 'plugin-slack',
  instance_name: 'slack-prod',
  state: 'healthy',
  implements_notify: true,
  implements_request: false,
  config_schema: null,
  version: 0,
  event_kinds: [
    {
      kind: 'channel_message',
      description: 'A message in a channel',
      guidance: 'A human posts a message in a channel the instance is watching.',
      binding_schema: {
        type: 'object',
        properties: {
          channel_id: {
            type: 'string',
            title: 'Channel',
            description: 'Exact match on the Slack channel. Pick from the searchable list.',
            'x-gleipnir-options': { source: 'channels' },
          },
          text: {
            type: 'string',
            format: 'contains',
            title: 'Text contains',
            description: 'Case-sensitive substring anywhere in the message body.',
          },
          mention_only: { type: 'boolean', title: 'Mention-only', description: 'Fire only when the bot is @-mentioned.' },
        },
      },
      examples: [
        { name: 'incident-channel', payload: { channel_id: 'C09INCIDENT', text: 'alert' } },
        { name: 'general-channel', payload: { channel_id: 'C012ABCDEF', text: 'hello' } },
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
    return (
      <MemoryRouter>
        <QueryClientProvider client={qc}>{children}</QueryClientProvider>
      </MemoryRouter>
    )
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

describe('SubscribedBindingConfig — guidance block', () => {
  it('renders "How this fires" heading and guidance text when guidance is present', () => {
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

    expect(screen.getByText('How this fires')).toBeInTheDocument()
    expect(
      screen.getByText('A human posts a message in a channel the instance is watching.'),
    ).toBeInTheDocument()
  })

  it('does not render the guidance block when guidance is absent', () => {
    const instanceNoGuidance: ApiPluginInstanceForAudience = {
      ...SLACK_INSTANCE,
      instance_name: 'no-guidance-inst',
      event_kinds: [
        {
          kind: 'channel_message',
          description: 'A message in a channel',
          // no guidance field
          binding_schema: SLACK_INSTANCE.event_kinds![0].binding_schema,
          examples: [],
        },
      ],
    }

    render(
      <SubscribedBindingConfig
        source="no-guidance-inst"
        eventKind="channel_message"
        binding={{}}
        onChange={vi.fn()}
        pluginInstances={[instanceNoGuidance]}
      />,
      { wrapper: makeWrapper() },
    )

    expect(screen.queryByText('How this fires')).toBeNull()
  })
})

describe('SubscribedBindingConfig — rendering', () => {
  it('renders the text field as a textbox and channel_id as a combobox', () => {
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

    // The plain `text` field renders a text input.
    expect(screen.getByRole('textbox')).toBeInTheDocument()
    // The `channel_id` field with x-gleipnir-options renders a combobox.
    expect(screen.getByRole('combobox')).toBeInTheDocument()
  })

  it('renders prop.title as the label (not the raw key)', () => {
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

    // The `text` field has title "Text contains" — should appear, not "text".
    expect(screen.getByText('Text contains')).toBeInTheDocument()
    // The `channel_id` field has title "Channel" — should appear.
    expect(screen.getAllByText('Channel').length).toBeGreaterThan(0)
  })

  it('renders prop.description as a caption below the field', () => {
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

    expect(
      screen.getByText('Case-sensitive substring anywhere in the message body.'),
    ).toBeInTheDocument()
    expect(
      screen.getByText('Exact match on the Slack channel. Pick from the searchable list.'),
    ).toBeInTheDocument()
  })

  it('does not render the (contains) or (regex) format suffix', () => {
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

    expect(screen.queryByText(/\(contains\)/)).toBeNull()
    expect(screen.queryByText(/\(regex\)/)).toBeNull()
  })

  it('renders the scope↔binding explainer with a link to the plugin instance page', () => {
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

    const link = screen.getByRole('link', { name: /subscription scope/i })
    expect(link).toBeInTheDocument()
    expect(link.getAttribute('href')).toContain('/admin/plugins/plugin-slack/instances/inst-slack')
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
        binding={{ channel_id: 'C09INCIDENT' }}
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

    const binding = { channel_id: 'C09INCIDENT' }
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
          { channel_id: 'C09INCIDENT', text: 'alert' },
          { channel_id: 'C012ABCDEF', text: 'hello' },
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
        binding={{ text: '[bad(' }}
        onChange={vi.fn()}
        pluginInstances={[SLACK_INSTANCE]}
      />,
      { wrapper: makeWrapper() },
    )

    expect(screen.getByText(/binding: invalid regular expression/)).toBeInTheDocument()
  })
})
