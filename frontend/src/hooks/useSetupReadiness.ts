import { useModels } from '@/hooks/queries/users'
import { useMcpServers } from '@/hooks/queries/servers'
import { usePolicies } from '@/hooks/queries/policies'

export interface SetupReadiness {
  hasModel: boolean
  hasServer: boolean
  hasAgent: boolean
  isLoading: boolean
  isError: boolean
  nextStep: 'model' | 'server' | 'agent' | 'ready'
}

export function useSetupReadiness(): SetupReadiness {
  const models = useModels()
  const servers = useMcpServers()
  const policies = usePolicies()

  const isLoading = models.isLoading || servers.isLoading || policies.isLoading
  const isError = models.isError || servers.isError || policies.isError

  // The API can return [{provider: 'anthropic', models: []}] when no API key
  // is configured, so we must check that at least one group has models — a
  // length check on the outer array would give a false positive.
  const hasModel = models.data?.some(g => g.models.length > 0) ?? false
  const hasServer = (servers.data?.length ?? 0) > 0
  const hasAgent = (policies.data?.length ?? 0) > 0

  let nextStep: SetupReadiness['nextStep']
  if (!hasModel) {
    nextStep = 'model'
  } else if (!hasServer) {
    nextStep = 'server'
  } else if (!hasAgent) {
    nextStep = 'agent'
  } else {
    nextStep = 'ready'
  }

  return { hasModel, hasServer, hasAgent, isLoading, isError, nextStep }
}
