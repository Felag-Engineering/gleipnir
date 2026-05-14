import type { Meta, StoryObj } from '@storybook/react-vite'
import { fn } from 'storybook/test'
import { useState } from 'react'
import '@/tokens.css'
import { TriggerPicker } from './TriggerPicker'
import type { TriggerPickerValue } from './TriggerPicker'
import type { ApiPluginInstanceForAudience } from '@/api/types'
import decoratorStyles from './TriggerPicker.stories.module.css'

// --- Fixtures ---

const SLACK_PROD: ApiPluginInstanceForAudience = {
  id: 'inst-1',
  plugin_id: 'plugin-slack',
  plugin_name: 'Slack',
  instance_name: 'slack-prod',
  state: 'healthy',
  implements_notify: true,
  implements_request: false,
  config_schema: null,
  version: 0,
  event_kinds: [
    { kind: 'channel_message', description: 'A message posted in a channel' },
    { kind: 'direct_message', description: 'A direct message to the bot' },
    { kind: 'reaction_added', description: 'A reaction was added to a message' },
  ],
}

const SLACK_PERSONAL: ApiPluginInstanceForAudience = {
  id: 'inst-2',
  plugin_id: 'plugin-slack',
  plugin_name: 'Slack',
  instance_name: 'slack-personal',
  state: 'healthy',
  implements_notify: true,
  implements_request: false,
  config_schema: null,
  version: 0,
  event_kinds: [
    { kind: 'channel_message', description: 'A message posted in a channel' },
    { kind: 'direct_message', description: 'A direct message to the bot' },
  ],
}

const PAGERDUTY: ApiPluginInstanceForAudience = {
  id: 'inst-3',
  plugin_id: 'plugin-pd',
  plugin_name: 'PagerDuty',
  instance_name: 'pagerduty',
  state: 'healthy',
  implements_notify: true,
  implements_request: true,
  config_schema: null,
  version: 0,
  event_kinds: [
    { kind: 'incident_triggered', description: 'Incident triggered in PagerDuty' },
    { kind: 'incident_acknowledged', description: 'Incident acknowledged by responder' },
    { kind: 'incident_resolved', description: 'Incident resolved' },
  ],
}

// --- Meta ---

const meta: Meta<typeof TriggerPicker> = {
  title: 'PolicyEditor/FormMode/TriggerPicker',
  component: TriggerPicker,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.decorator}>
        <Story />
      </div>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof TriggerPicker>

// --- Stories ---

// NoPlugins — only built-in triggers visible.
export const NoPlugins: Story = {
  args: {
    value: { kind: 'builtin', type: 'webhook' },
    onChange: fn(),
    pluginInstances: [],
    loading: false,
  },
}

// OnePluginInstance — Slack with 3 event kinds.
export const OnePluginInstance: Story = {
  args: {
    value: { kind: 'builtin', type: 'webhook' },
    onChange: fn(),
    pluginInstances: [SLACK_PROD],
    loading: false,
  },
}

// MultiplePluginInstances — two Slack instances (disambiguated) + PagerDuty.
export const MultiplePluginInstances: Story = {
  args: {
    value: { kind: 'builtin', type: 'webhook' },
    onChange: fn(),
    pluginInstances: [SLACK_PROD, SLACK_PERSONAL, PAGERDUTY],
    loading: false,
  },
}

// SearchFiltering — opens with "message" pre-filled so the filter is visible.
function SearchFilteringStory() {
  const [value, setValue] = useState<TriggerPickerValue>({ kind: 'builtin', type: 'webhook' })
  return (
    <TriggerPicker
      value={value}
      onChange={setValue}
      pluginInstances={[SLACK_PROD, PAGERDUTY]}
      loading={false}
    />
  )
}

export const SearchFiltering: Story = {
  render: () => <SearchFilteringStory />,
}

// SubscribedSelected — picker shows the resolved event label.
export const SubscribedSelected: Story = {
  args: {
    value: { kind: 'subscribed', source: 'slack-prod', eventKind: 'channel_message' },
    onChange: fn(),
    pluginInstances: [SLACK_PROD],
    loading: false,
  },
}

// Loading — skeleton rows visible while plugin instances are fetching.
export const Loading: Story = {
  args: {
    value: null,
    onChange: fn(),
    pluginInstances: undefined,
    loading: true,
  },
}

// Interactive — stateful wrapper so the picker is fully exercisable.
function InteractivePicker() {
  const [value, setValue] = useState<TriggerPickerValue>({ kind: 'builtin', type: 'webhook' })
  return (
    <TriggerPicker
      value={value}
      onChange={setValue}
      pluginInstances={[SLACK_PROD, SLACK_PERSONAL, PAGERDUTY]}
      loading={false}
    />
  )
}

export const Interactive: Story = {
  render: () => <InteractivePicker />,
}
