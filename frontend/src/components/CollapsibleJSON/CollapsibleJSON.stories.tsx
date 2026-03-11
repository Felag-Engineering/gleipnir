import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { CollapsibleJSON } from './CollapsibleJSON'

const meta: Meta<typeof CollapsibleJSON> = {
  title: 'Shared/CollapsibleJSON',
  component: CollapsibleJSON,
  argTypes: {
    defaultCollapsed: { control: 'boolean' },
  },
}

export default meta
type Story = StoryObj<typeof CollapsibleJSON>

const SHORT_VALUE = { status: 'ok', count: 3 }

const LONG_VALUE = {
  tool: 'read_file',
  input: {
    path: '/var/log/app.log',
    lines: 100,
    encoding: 'utf-8',
    follow: false,
    filter: null,
  },
  output: {
    content: 'Mar 10 12:00:01 app[1234]: INFO starting up\nMar 10 12:00:02 app[1234]: INFO ready',
    truncated: false,
    bytes_read: 1024,
  },
}

export const ShortCollapsed: Story = {
  args: { value: SHORT_VALUE, defaultCollapsed: true },
}

export const ShortExpanded: Story = {
  args: { value: SHORT_VALUE, defaultCollapsed: false },
}

export const LongCollapsed: Story = {
  args: { value: LONG_VALUE, defaultCollapsed: true },
}

export const LongExpanded: Story = {
  args: { value: LONG_VALUE, defaultCollapsed: false },
}

export const PrimitiveString: Story = {
  args: { value: 'plain text output from tool', defaultCollapsed: false },
}

export const NestedArray: Story = {
  args: {
    value: [
      { id: 1, name: 'Alpha', active: true },
      { id: 2, name: 'Beta', active: false },
      { id: 3, name: 'Gamma', active: true },
    ],
    defaultCollapsed: true,
  },
}
