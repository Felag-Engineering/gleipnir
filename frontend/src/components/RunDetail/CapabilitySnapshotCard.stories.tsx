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
  { ServerName: 'fs-server', ToolName: 'read_file', Role: 'tool', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
  { ServerName: 'fs-server', ToolName: 'list_files', Role: 'tool', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
  { ServerName: 'fs-server', ToolName: 'write_file', Role: 'tool', Approval: 'required', Timeout: 60, OnTimeout: 'fail' },
  { ServerName: 'slack-server', ToolName: 'send_message', Role: 'tool', Approval: 'required', Timeout: 15, OnTimeout: 'fail' },
  { ServerName: 'slack-server', ToolName: 'post_feedback', Role: 'feedback', Approval: 'none', Timeout: 300, OnTimeout: 'skip' },
]

export const Default: Story = {
  args: { content: MIXED_TOOLS },
}

export const ToolOnly: Story = {
  args: {
    content: MIXED_TOOLS.filter((t) => t.Role === 'tool'),
  },
}

export const SingleTool: Story = {
  args: {
    content: [{ ServerName: 'fs-server', ToolName: 'read_file', Role: 'tool', Approval: 'none', Timeout: 30, OnTimeout: 'fail' }],
  },
}

export const ManyTools: Story = {
  args: {
    content: [
      ...MIXED_TOOLS,
      { ServerName: 'gh-server', ToolName: 'list_prs', Role: 'tool', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
      { ServerName: 'gh-server', ToolName: 'merge_pr', Role: 'tool', Approval: 'required', Timeout: 60, OnTimeout: 'fail' },
      { ServerName: 'gh-server', ToolName: 'comment', Role: 'tool', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
    ],
  },
}

export const Empty: Story = {
  args: { content: [] },
}

// V2 shape stories — capability snapshots written after ADR-023 include the model field.

const MIXED_TOOLS_V2: CapabilitySnapshotV2 = {
  provider: 'anthropic',
  model: 'claude-sonnet-4-6',
  tools: MIXED_TOOLS,
}

export const V2WithSonnet: Story = {
  args: { content: MIXED_TOOLS_V2 },
}

export const V2WithOpus: Story = {
  args: { content: { provider: 'anthropic', model: 'claude-opus-4-6', tools: MIXED_TOOLS } },
}

export const V2WithHaiku: Story = {
  args: {
    content: {
      provider: 'anthropic',
      model: 'claude-haiku-4-5-20251001',
      tools: [
        { ServerName: 'fs-server', ToolName: 'read_file', Role: 'tool', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
      ],
    },
  },
}

export const V2WithGemini: Story = {
  args: {
    content: {
      provider: 'google',
      model: 'gemini-2.0-flash',
      tools: MIXED_TOOLS,
    },
  },
}

export const V2WithoutProvider: Story = {
  args: {
    content: {
      model: 'claude-sonnet-4-6',
      tools: MIXED_TOOLS,
    },
  },
}

const SAMPLE_SYSTEM_PROMPT = `You are an autonomous agent running under the Gleipnir orchestrator.

## Your task
Monitor the filesystem for changes and report anomalies.

## Capabilities
You have access to the following tools:
- fs-server.read_file (tool): Read file contents
- fs-server.list_files (tool): List directory contents
- fs-server.write_file (tool, approval required): Write file contents

## Constraints
- You must not access files outside /data
- You must request approval before writing any file
- Token budget: 50000 tokens per run`

export const WithSystemPrompt: Story = {
  args: {
    content: MIXED_TOOLS_V2,
    systemPrompt: SAMPLE_SYSTEM_PROMPT,
  },
}

export const WithNullSystemPrompt: Story = {
  args: {
    content: MIXED_TOOLS_V2,
    systemPrompt: null,
  },
}
