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
  { server_name: 'fs-server', tool_name: 'read_file', approval: 'none', timeout: 30, on_timeout: 'fail' },
  { server_name: 'fs-server', tool_name: 'list_files', approval: 'none', timeout: 30, on_timeout: 'fail' },
  { server_name: 'fs-server', tool_name: 'write_file', approval: 'required', timeout: 60, on_timeout: 'fail' },
  { server_name: 'slack-server', tool_name: 'send_message', approval: 'required', timeout: 15, on_timeout: 'fail' },
  { server_name: 'slack-server', tool_name: 'post_feedback', approval: 'none', timeout: 300, on_timeout: 'skip' },
]

export const Default: Story = {
  args: { content: MIXED_TOOLS },
}

export const SingleTool: Story = {
  args: {
    content: [{ server_name: 'fs-server', tool_name: 'read_file', approval: 'none', timeout: 30, on_timeout: 'fail' }],
  },
}

export const ManyTools: Story = {
  args: {
    content: [
      ...MIXED_TOOLS,
      { server_name: 'gh-server', tool_name: 'list_prs', approval: 'none', timeout: 30, on_timeout: 'fail' },
      { server_name: 'gh-server', tool_name: 'merge_pr', approval: 'required', timeout: 60, on_timeout: 'fail' },
      { server_name: 'gh-server', tool_name: 'comment', approval: 'none', timeout: 30, on_timeout: 'fail' },
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
        { server_name: 'fs-server', tool_name: 'read_file', approval: 'none', timeout: 30, on_timeout: 'fail' },
      ],
    },
  },
}

export const V2WithGemini: Story = {
  args: {
    content: {
      provider: 'google',
      model: 'gemini-2.5-flash',
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
