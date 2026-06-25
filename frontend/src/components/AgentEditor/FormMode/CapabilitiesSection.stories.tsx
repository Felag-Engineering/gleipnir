import type { Meta, StoryObj } from '@storybook/react-vite';
import { useState } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import '@/tokens.css';
import { queryKeys } from '@/hooks/queryKeys';
import type { ApiMcpServer, ApiMcpTool, ApiPluginInstanceForAudience } from '@/api/types';
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
    is_arcade_gateway: false,
  },
  {
    id: 'srv-2',
    name: 'GitHub Tools',
    url: 'http://mcp-github:8080',
    last_discovered_at: '2026-03-10T12:00:00Z',
    has_drift: false,
    created_at: '2026-03-05T00:00:00Z',
    is_arcade_gateway: false,
  },
];

const FIXTURE_TOOLS_SRV1: ApiMcpTool[] = [
  {
    id: 'tool-1',
    server_id: 'srv-1',
    name: 'read_file',
    description: 'Read the contents of a file at the given path',
    input_schema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] },
    enabled: true,
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
    enabled: true,
  },
  {
    id: 'tool-3',
    server_id: 'srv-1',
    name: 'list_directory',
    description: 'List files and directories at the given path',
    input_schema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] },
    enabled: true,
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
    enabled: true,
  },
  {
    id: 'tool-5',
    server_id: 'srv-2',
    name: 'list_issues',
    description: 'List open issues for a GitHub repository',
    input_schema: { type: 'object', properties: { repo: { type: 'string' } }, required: ['repo'] },
    enabled: true,
  },
];

// Slack plugin instance fixture for WithPluginTools story.
const FIXTURE_SLACK_INSTANCE: ApiPluginInstanceForAudience = {
  id: 'inst-slack-1',
  plugin_id: 'plugin-slack',
  instance_name: 'slack-e2e',
  plugin_name: 'Slack',
  state: 'healthy',
  implements_notify: true,
  implements_request: true,
  config_schema: null,
  version: 1,
  services: ['tool', 'trigger', 'channel'],
  tools: [
    { name: 'send_message', description: 'Send a message to a Slack channel' },
    { name: 'list_channels', description: 'List all accessible Slack channels' },
    { name: 'list_users', description: 'List all users in the Slack workspace' },
  ],
};

const FIXTURE_ASSIGNED_TOOLS: AssignedTool[] = [
  {
    toolId: 'tool-1',
    serverId: 'srv-1',
    serverName: 'Filesystem Tools',
    name: 'read_file',
    description: 'Read the contents of a file at the given path',
    source: 'mcp',
    approvalRequired: false,
    approvalTimeout: '',
  },
  {
    toolId: 'tool-2',
    serverId: 'srv-1',
    serverName: 'Filesystem Tools',
    name: 'write_file',
    description: 'Write content to a file at the given path',
    source: 'mcp',
    approvalRequired: true,
    approvalTimeout: '',
  },
  {
    toolId: 'tool-4',
    serverId: 'srv-2',
    serverName: 'GitHub Tools',
    name: 'create_issue',
    description: 'Create a new GitHub issue in a repository',
    source: 'mcp',
    approvalRequired: false,
    approvalTimeout: '',
  },
];

// makeQueryClient seeds MCP tool data AND an empty pluginInstances entry so the
// usePluginInstancesForAudience hook doesn't background-fetch and clear cached state.
function makeQueryClient(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(queryKeys.servers.all, FIXTURE_SERVERS);
  qc.setQueryData(queryKeys.servers.toolsAll('srv-1'), FIXTURE_TOOLS_SRV1);
  qc.setQueryData(queryKeys.servers.toolsAll('srv-2'), FIXTURE_TOOLS_SRV2);
  // Seed empty pluginInstances to prevent background refetch from clearing state.
  qc.setQueryData(queryKeys.admin.pluginInstances, []);
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

// Shows the approval timeout input rendered alongside the approval toggle.
export const WithApprovalTimeout: Story = {
  args: {
    value: {
      tools: [
        {
          toolId: 'tool-2',
          serverId: 'srv-1',
          serverName: 'Filesystem Tools',
          name: 'write_file',
          description: 'Write content to a file at the given path',
          source: 'mcp',
          approvalRequired: true,
          approvalTimeout: '30m',
        },
      ],
      feedback: DEFAULT_FEEDBACK,
    },
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

// WithDisabledTool seeds the cache with a disabled tool so the warning badge
// renders on the assigned-tool row. The tool is still shown in the registry
// add panel (intentional — see Key decisions in plan.md).
export const WithDisabledTool: Story = {
  decorators: [
    (Story) => {
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      qc.setQueryData(queryKeys.servers.all, FIXTURE_SERVERS);
      qc.setQueryData(queryKeys.servers.toolsAll('srv-1'), [
        ...FIXTURE_TOOLS_SRV1.filter(t => t.name !== 'write_file'),
        {
          id: 'tool-2',
          server_id: 'srv-1',
          name: 'write_file',
          description: 'Write content to a file at the given path',
          input_schema: { type: 'object' },
          enabled: false,
        },
      ]);
      qc.setQueryData(queryKeys.servers.toolsAll('srv-2'), FIXTURE_TOOLS_SRV2);
      // Seed empty pluginInstances to prevent background refetch.
      qc.setQueryData(queryKeys.admin.pluginInstances, []);
      return (
        <QueryClientProvider client={qc}>
          <div className={decoratorStyles.decorator}>
            <Story />
          </div>
        </QueryClientProvider>
      );
    },
  ],
  args: {
    value: {
      tools: [
        {
          toolId: 'tool-2',
          serverId: 'srv-1',
          serverName: 'Filesystem Tools',
          name: 'write_file',
          description: 'Write content to a file at the given path',
          source: 'mcp',
          approvalRequired: false,
          approvalTimeout: '',
        },
        {
          toolId: 'tool-1',
          serverId: 'srv-1',
          serverName: 'Filesystem Tools',
          name: 'read_file',
          description: 'Read the contents of a file at the given path',
          source: 'mcp',
          approvalRequired: false,
          approvalTimeout: '',
        },
      ],
      feedback: DEFAULT_FEEDBACK,
    },
    onChange: () => {},
  },
};

// WithPluginTools shows a mix of MCP and plugin tools granted to a policy,
// including a plugin tool pre-assigned from a Slack instance.
export const WithPluginTools: Story = {
  decorators: [
    (Story) => {
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      qc.setQueryData(queryKeys.servers.all, FIXTURE_SERVERS);
      qc.setQueryData(queryKeys.servers.toolsAll('srv-1'), FIXTURE_TOOLS_SRV1);
      qc.setQueryData(queryKeys.servers.toolsAll('srv-2'), FIXTURE_TOOLS_SRV2);
      qc.setQueryData(queryKeys.admin.pluginInstances, [FIXTURE_SLACK_INSTANCE]);
      return (
        <QueryClientProvider client={qc}>
          <div className={decoratorStyles.decorator}>
            <Story />
          </div>
        </QueryClientProvider>
      );
    },
  ],
  args: {
    value: {
      tools: [
        {
          toolId: 'tool-1',
          serverId: 'srv-1',
          serverName: 'Filesystem Tools',
          name: 'read_file',
          description: 'Read the contents of a file at the given path',
          source: 'mcp',
          approvalRequired: false,
          approvalTimeout: '',
        },
        {
          toolId: 'slack-e2e.send_message',
          serverId: 'inst-slack-1',
          serverName: 'slack-e2e',
          name: 'send_message',
          description: 'Send a message to a Slack channel',
          source: 'plugin',
          approvalRequired: false,
          approvalTimeout: '',
        },
        {
          toolId: 'slack-e2e.list_channels',
          serverId: 'inst-slack-1',
          serverName: 'slack-e2e',
          name: 'list_channels',
          description: 'List all accessible Slack channels',
          source: 'plugin',
          approvalRequired: true,
          approvalTimeout: '30m',
        },
      ],
      feedback: DEFAULT_FEEDBACK,
    },
    onChange: () => {},
  },
};
