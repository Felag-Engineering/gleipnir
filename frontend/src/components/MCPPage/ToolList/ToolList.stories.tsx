import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ToolList } from './ToolList'
import type { ApiMcpTool } from '@/api/types'

const meta: Meta<typeof ToolList> = {
  title: 'ToolsPage/ToolList',
  component: ToolList,
}

export default meta
type Story = StoryObj<typeof ToolList>

const tools: ApiMcpTool[] = [
  { id: 't1', server_id: 'srv1', name: 'kubectl.get_pods', description: 'List pods.', capability_role: 'tool', input_schema: { namespace: { type: 'string' } } },
  { id: 't2', server_id: 'srv1', name: 'kubectl.delete_pod', description: 'Delete a pod.', capability_role: 'tool', input_schema: { namespace: { type: 'string' }, pod: { type: 'string' } } },
]

export const Loaded: Story = {
  args: { tools, isLoading: false, onRoleChange: () => {}, updatingToolId: null },
}

export const Loading: Story = {
  args: { tools: undefined, isLoading: true, onRoleChange: () => {}, updatingToolId: null },
}

export const Empty: Story = {
  args: { tools: [], isLoading: false, onRoleChange: () => {}, updatingToolId: null },
}
