import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ToolRow } from './ToolRow'
import type { ApiMcpTool } from '@/api/types'

const meta: Meta<typeof ToolRow> = {
  title: 'MCPPage/ToolRow',
  component: ToolRow,
}

export default meta
type Story = StoryObj<typeof ToolRow>

const sensorTool: ApiMcpTool = {
  id: 't1',
  server_id: 'srv1',
  name: 'kubectl.get_pods',
  description: 'List pods across namespaces with status and restart counts.',
  capability_role: 'sensor',
  input_schema: { namespace: { type: 'string', required: true } },
}

export const Sensor: Story = {
  args: { tool: sensorTool, onRoleChange: () => {}, isUpdating: false },
}

export const Actuator: Story = {
  args: {
    tool: { ...sensorTool, name: 'kubectl.delete_pod', capability_role: 'actuator' },
    onRoleChange: () => {},
    isUpdating: false,
  },
}

export const Feedback: Story = {
  args: {
    tool: { ...sensorTool, name: 'slack.notify', capability_role: 'feedback' },
    onRoleChange: () => {},
    isUpdating: false,
  },
}
