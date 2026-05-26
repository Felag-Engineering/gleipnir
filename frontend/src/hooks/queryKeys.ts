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
    // enabled-only tool list (legacy; no longer consumed by active components)
    tools: (serverId: string) => ['servers', serverId, 'tools'] as const,
    // all tools including disabled — consumed by the Tools management page and CapabilitiesSection
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
    audiences: ['admin', 'audiences'] as const,
    audienceDetail: (id: string) => ['admin', 'audiences', id] as const,
    audienceReferences: (id: string) => ['admin', 'audiences', id, 'references'] as const,
    pluginInstances: ['admin', 'plugin-instances'] as const,
  },
  config: {
    all: ['config'] as const,
  },
  plugins: {
    // list() is invalidated after a successful install so that any future
    // read-side hook that consumes it picks up the new plugin immediately.
    list: () => ['admin', 'plugins'] as const,
    // detail(pluginId) is invalidated after approve/reject.
    detail: (pluginId: string) => ['admin', 'plugins', pluginId] as const,
    // instances(pluginId) is invalidated after a successful create-instance.
    instances: (pluginId: string) =>
      ['admin', 'plugins', pluginId, 'instances'] as const,
    instance: (pluginId: string, instanceId: string) =>
      ['admin', 'plugins', pluginId, 'instances', instanceId] as const,
    credentials: (pluginId: string, instanceId: string) =>
      ['admin', 'plugins', pluginId, 'instances', instanceId, 'credentials'] as const,
  },
} as const
