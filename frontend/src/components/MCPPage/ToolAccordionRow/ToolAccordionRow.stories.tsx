import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ToolAccordionRow } from './ToolAccordionRow'

const meta: Meta<typeof ToolAccordionRow> = {
  title: 'ToolsPage/ToolAccordionRow',
  component: ToolAccordionRow,
}

export default meta
type Story = StoryObj<typeof ToolAccordionRow>

export const Collapsed: Story = {
  args: {
    tool: {
      id: 't1', server_id: 'srv-1', name: 'echo',
      description: 'Echo the provided message back unchanged.',
      input_schema: {
        properties: { message: { title: 'Message', type: 'string' } },
        required: ['message'], type: 'object',
      },
      enabled: true,
    },
    expanded: false,
    onToggle: () => {},
  },
}

export const ExpandedSingleParam: Story = {
  args: {
    ...Collapsed.args,
    expanded: true,
  },
}

export const ExpandedMultiParam: Story = {
  args: {
    tool: {
      id: 't3', server_id: 'srv-1', name: 'send_notification',
      description: 'Simulate sending a notification to a channel.',
      input_schema: {
        properties: { channel: { title: 'Channel', type: 'string' }, message: { title: 'Message', type: 'string' } },
        required: ['channel', 'message'], type: 'object',
      },
      enabled: true,
    },
    expanded: true,
    onToggle: () => {},
  },
}

export const NoParams: Story = {
  args: {
    tool: {
      id: 't2', server_id: 'srv-1', name: 'get_current_time',
      description: 'Return the current UTC time as an ISO 8601 string.',
      input_schema: { properties: {}, type: 'object' },
      enabled: true,
    },
    expanded: true,
    onToggle: () => {},
  },
}

export const Disabled: Story = {
  args: {
    tool: {
      id: 't4', server_id: 'srv-1', name: 'delete_everything',
      description: 'Destructive tool pending security review.',
      input_schema: { properties: {}, type: 'object' },
      enabled: false,
    },
    expanded: true,
    onToggle: () => {},
    canManage: true,
    onSetEnabled: () => {},
  },
}

export const EnabledToggleable: Story = {
  args: {
    ...ExpandedSingleParam.args,
    canManage: true,
    onSetEnabled: () => {},
  },
}

export const EnabledReadOnly: Story = {
  args: {
    ...ExpandedSingleParam.args,
    canManage: false,
  },
}
