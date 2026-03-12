import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import type { CapabilitySnapshotContent, CapabilitySnapshotV2 } from './types'
import { CapabilitySnapshotCard } from './CapabilitySnapshotCard'

const meta: Meta<typeof CapabilitySnapshotCard> = {
  title: 'RunDetail/CapabilitySnapshotCard',
  component: CapabilitySnapshotCard,
}

export default meta
type Story = StoryObj<typeof CapabilitySnapshotCard>

const MIXED_TOOLS: CapabilitySnapshotContent = [
  { ServerName: 'fs-server', ToolName: 'read_file', Role: 'sensor', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
  { ServerName: 'fs-server', ToolName: 'list_files', Role: 'sensor', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
  { ServerName: 'fs-server', ToolName: 'write_file', Role: 'actuator', Approval: 'required', Timeout: 60, OnTimeout: 'fail' },
  { ServerName: 'slack-server', ToolName: 'send_message', Role: 'actuator', Approval: 'required', Timeout: 15, OnTimeout: 'fail' },
  { ServerName: 'slack-server', ToolName: 'post_feedback', Role: 'feedback', Approval: 'none', Timeout: 300, OnTimeout: 'skip' },
]

export const Default: Story = {
  args: { content: MIXED_TOOLS },
}

export const SensorOnly: Story = {
  args: {
    content: MIXED_TOOLS.filter((t) => t.Role === 'sensor'),
  },
}

export const SingleTool: Story = {
  args: {
    content: [{ ServerName: 'fs-server', ToolName: 'read_file', Role: 'sensor', Approval: 'none', Timeout: 30, OnTimeout: 'fail' }],
  },
}

export const ManyTools: Story = {
  args: {
    content: [
      ...MIXED_TOOLS,
      { ServerName: 'gh-server', ToolName: 'list_prs', Role: 'sensor', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
      { ServerName: 'gh-server', ToolName: 'merge_pr', Role: 'actuator', Approval: 'required', Timeout: 60, OnTimeout: 'fail' },
      { ServerName: 'gh-server', ToolName: 'comment', Role: 'actuator', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
    ],
  },
}

export const Empty: Story = {
  args: { content: [] },
}

// V2 shape stories — capability snapshots written after ADR-023 include the model field.

const MIXED_TOOLS_V2: CapabilitySnapshotV2 = {
  model: 'claude-sonnet-4-6',
  tools: MIXED_TOOLS,
}

export const V2WithSonnet: Story = {
  args: { content: MIXED_TOOLS_V2 },
}

export const V2WithOpus: Story = {
  args: { content: { model: 'claude-opus-4-6', tools: MIXED_TOOLS } },
}

export const V2WithHaiku: Story = {
  args: {
    content: {
      model: 'claude-haiku-4-5-20251001',
      tools: [
        { ServerName: 'fs-server', ToolName: 'read_file', Role: 'sensor', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
      ],
    },
  },
}
