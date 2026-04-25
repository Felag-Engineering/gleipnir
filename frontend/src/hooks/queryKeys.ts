export const queryKeys = {
  policies: {
    all: ['policies'] as const,
    detail: (id: string) => ['policies', id] as const,
    webhookSecret: (id: string) => ['policies', id, 'webhook-secret'] as const,
  },
  runs: {
    all: ['runs'] as const,
    detail: (id: string) => ['runs', id] as const,
    steps: (id: string) => ['runs', id, 'steps'] as const,
    list: (params: Record<string, string>) => ['runs', 'list', params] as const,
  },
  servers: {
    all: ['servers'] as const,
    // enabled-only tool list — consumed by the policy form (CapabilitiesSection)
    tools: (serverId: string) => ['servers', serverId, 'tools'] as const,
    // unfiltered tool list including disabled — consumed by the Tools management page only
    toolsAll: (serverId: string) => ['servers', serverId, 'tools', 'all'] as const,
  },
  stats: {
    all: ['stats'] as const,
    timeseries: (window: string) => ['stats', 'timeseries', window] as const,
  },
  attention: {
    all: ['attention'] as const,
  },
  approvals: {
    all: ['approvals'] as const,
  },
  users: {
    all: ['users'] as const,
  },
  currentUser: {
    all: ['currentUser'] as const,
  },
  models: {
    all: ['models'] as const,
  },
  preferences: {
    all: ['preferences'] as const,
  },
  sessions: {
    all: ['sessions'] as const,
  },
  admin: {
    providers: ['admin', 'providers'] as const,
    models: ['admin', 'models'] as const,
    modelsAll: ['admin', 'models', 'all'] as const,
    settings: ['admin', 'settings'] as const,
    systemInfo: ['admin', 'system-info'] as const,
    openaiCompatProviders: ['admin', 'openai-compat-providers'] as const,
  },
  config: {
    all: ['config'] as const,
  },
} as const
