export const queryKeys = {
  policies: {
    all: ['policies'] as const,
    detail: (id: string) => ['policies', id] as const,
  },
  runs: {
    all: ['runs'] as const,
    detail: (id: string) => ['runs', id] as const,
    steps: (id: string) => ['runs', id, 'steps'] as const,
    list: (params: Record<string, string>) => ['runs', 'list', params] as const,
    latestByPolicy: (policyId: string) => ['runs', 'latestByPolicy', policyId] as const,
  },
  servers: {
    all: ['servers'] as const,
    tools: (serverId: string) => ['servers', serverId, 'tools'] as const,
  },
  stats: {
    all: ['stats'] as const,
  },
  approvals: {
    all: ['approvals'] as const,
  },
} as const
