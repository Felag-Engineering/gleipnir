import type { Meta, StoryObj } from '@storybook/react-vite';
import { useState } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import '@/tokens.css';
import { queryKeys } from '@/hooks/queryKeys';
import type { ApiMcpServer, ApiMcpTool } from '@/api/types';
import { CapabilitiesSection } from './CapabilitiesSection';
import type { CapabilitiesFormState, AssignedTool } from './types';
import decoratorStyles from './CapabilitiesSection.stories.module.css';

const FIXTURE_SERVERS: ApiMcpServer[] = [
  {
    id: 'srv-1',
    name: 'Filesystem Tools',
    url: 'http://mcp-filesystem:8080',
    last_discovered_at: '2026-03-10T12:00:00Z',
    has_drift: false,
    created_at: '2026-03-01T00:00:00Z',
  },
  {
    id: 'srv-2',
    name: 'GitHub Tools',
    url: 'http://mcp-github:8080',
    last_discovered_at: '2026-03-10T12:00:00Z',
    has_drift: false,
    created_at: '2026-03-05T00:00:00Z',
  },
];

const FIXTURE_TOOLS_SRV1: ApiMcpTool[] = [
  {
    id: 'tool-1',
    server_id: 'srv-1',
    name: 'read_file',
    description: 'Read the contents of a file at the given path',
    input_schema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] },
  },
  {
    id: 'tool-2',
    server_id: 'srv-1',
    name: 'write_file',
    description: 'Write content to a file at the given path',
    input_schema: {
      type: 'object',
      properties: { path: { type: 'string' }, content: { type: 'string' } },
      required: ['path', 'content'],
    },
  },
  {
    id: 'tool-3',
    server_id: 'srv-1',
    name: 'list_directory',
    description: 'List files and directories at the given path',
    input_schema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] },
  },
];

const FIXTURE_TOOLS_SRV2: ApiMcpTool[] = [
  {
    id: 'tool-4',
    server_id: 'srv-2',
    name: 'create_issue',
    description: 'Create a new GitHub issue in a repository',
    input_schema: {
      type: 'object',
      properties: {
        repo: { type: 'string' },
        title: { type: 'string' },
        body: { type: 'string' },
      },
      required: ['repo', 'title'],
    },
  },
  {
    id: 'tool-5',
    server_id: 'srv-2',
    name: 'list_issues',
    description: 'List open issues for a GitHub repository',
    input_schema: { type: 'object', properties: { repo: { type: 'string' } }, required: ['repo'] },
  },
];

const FIXTURE_ASSIGNED_TOOLS: AssignedTool[] = [
  {
    toolId: 'tool-1',
    serverId: 'srv-1',
    serverName: 'Filesystem Tools',
    name: 'read_file',
    description: 'Read the contents of a file at the given path',
    approvalRequired: false,
  },
  {
    toolId: 'tool-2',
    serverId: 'srv-1',
    serverName: 'Filesystem Tools',
    name: 'write_file',
    description: 'Write content to a file at the given path',
    approvalRequired: true,
  },
  {
    toolId: 'tool-4',
    serverId: 'srv-2',
    serverName: 'GitHub Tools',
    name: 'create_issue',
    description: 'Create a new GitHub issue in a repository',
    approvalRequired: false,
  },
];

function makeQueryClient(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(queryKeys.servers.all, FIXTURE_SERVERS);
  qc.setQueryData(queryKeys.servers.tools('srv-1'), FIXTURE_TOOLS_SRV1);
  qc.setQueryData(queryKeys.servers.tools('srv-2'), FIXTURE_TOOLS_SRV2);
  return qc;
}

const meta: Meta<typeof CapabilitiesSection> = {
  title: 'PolicyEditor/FormMode/CapabilitiesSection',
  component: CapabilitiesSection,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <div className={decoratorStyles.decorator}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof CapabilitiesSection>;

const DEFAULT_FEEDBACK = { enabled: false, timeout: '', onTimeout: 'fail' };

export const Empty: Story = {
  args: {
    value: { tools: [], feedback: DEFAULT_FEEDBACK },
    onChange: () => {},
  },
};

export const WithTools: Story = {
  args: {
    value: { tools: FIXTURE_ASSIGNED_TOOLS, feedback: DEFAULT_FEEDBACK },
    onChange: () => {},
  },
};

function InteractiveCapabilitiesSection() {
  const [value, setValue] = useState<CapabilitiesFormState>({
    tools: [],
    feedback: DEFAULT_FEEDBACK,
  });
  return <CapabilitiesSection value={value} onChange={setValue} />;
}

export const Interactive: Story = {
  render: () => <InteractiveCapabilitiesSection />,
};
