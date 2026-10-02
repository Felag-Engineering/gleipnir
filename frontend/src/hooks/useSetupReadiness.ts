import { useModels } from '@/hooks/queries/users'
import { useMcpServers } from '@/hooks/queries/servers'
import { usePolicies } from '@/hooks/queries/policies'
import { usePluginInstancesForAudience } from '@/hooks/queries/admin'

export interface SetupReadiness {
  hasModel: boolean
  hasToolSource: boolean
  hasAgent: boolean
  isLoading: boolean
  isError: boolean
  nextStep: 'model' | 'tools' | 'agent' | 'ready'
}

// Pass `enabled: false` for a role that cannot act on the setup steps (see
// canSetUp in permissions/roleAccess.ts): no readiness request is sent, every
// flag reads false, and nothing is loading or errored. Callers must then not
// present the result as "this instance is unconfigured".
export function useSetupReadiness({ enabled = true }: { enabled?: boolean } = {}): SetupReadiness {
  const models = useModels({ enabled })
  const servers = useMcpServers({ enabled })
  const policies = usePolicies({ enabled })
  // Plugin instances enrich the tools step: a tool-providing plugin satisfies
  // the same step as an MCP server. Errors here degrade to hasToolPlugin=false
  // rather than propagating to isError — a 403 from non-admin roles must not
  // break the checklist for the whole dashboard.
  const plugins = usePluginInstancesForAudience({ enabled })

  const isLoading = models.isLoading || servers.isLoading || policies.isLoading || plugins.isLoading
  const isError = models.isError || servers.isError || policies.isError

  // The API can return [{provider: 'anthropic', models: []}] when no API key
  // is configured, so we must check that at least one group has models — a
  // length check on the outer array would give a false positive.
  const hasModel = models.data?.some(g => g.models.length > 0) ?? false
  const hasServer = (servers.data?.length ?? 0) > 0
  const hasToolPlugin = (plugins.data ?? []).some(i => i.services?.includes('tool') ?? false)
  const hasToolSource = hasServer || hasToolPlugin
  const hasAgent = (policies.data?.length ?? 0) > 0

  let nextStep: SetupReadiness['nextStep']
  if (!hasModel) {
    nextStep = 'model'
  } else if (!hasToolSource) {
    nextStep = 'tools'
  } else if (!hasAgent) {
    nextStep = 'agent'
  } else {
    nextStep = 'ready'
  }

  return { hasModel, hasToolSource, hasAgent, isLoading, isError, nextStep }
}
