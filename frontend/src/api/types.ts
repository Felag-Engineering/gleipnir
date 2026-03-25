// Matches policy_handler.go → runSummary
export interface ApiRunSummary {
  id: string
  status: string
  started_at: string
  token_cost: number
}

// Matches policy_handler.go → policyListItem
export interface ApiPolicyListItem {
  id: string
  name: string
  trigger_type: string
  folder: string
  created_at: string
  updated_at: string
  paused_at: string | null
  latest_run: ApiRunSummary | null
}

// Matches trigger/runs_handler.go → PaginatedRunsResponse struct
export interface ApiRunsResponse {
  runs: ApiRun[]
  total: number
}

// Matches trigger/runs_handler.go → RunSummary struct
export interface ApiRun {
  id: string
  policy_id: string
  policy_name?: string
  status: string
  trigger_type: string
  trigger_payload?: string
  started_at: string
  completed_at: string | null
  token_cost: number
  error: string | null
  created_at: string
  system_prompt: string | null
}

// Matches trigger/runs_handler.go → StepSummary struct
export interface ApiRunStep {
  id: string
  run_id: string
  step_number: number
  type: string
  content: string
  token_cost: number
  created_at: string
}

// Matches policy_handler.go → policyDetail (GET /api/v1/policies/:id)
export interface ApiPolicyDetail {
  id: string
  name: string
  trigger_type: string
  folder: string
  yaml: string
  created_at: string
  updated_at: string
  paused_at: string | null
}

// Matches policy_handler.go → policyMutateResponse (POST/PUT response)
export interface ApiPolicySaveResponse extends ApiPolicyDetail {
  warnings: string[]
}

// Matches mcp_handler.go → mcpServerResponse (GET /api/v1/mcp/servers)
export interface ApiMcpServer {
  id: string
  name: string
  url: string
  last_discovered_at: string | null
  has_drift: boolean
  created_at: string
}

// Matches mcp_handler.go → mcpServerCreateResponse (POST /api/v1/mcp/servers)
export interface ApiMcpServerCreateResponse extends ApiMcpServer {
  discovery_error?: string | null
}

// Matches api/stats_handler.go → DashboardStats (GET /api/v1/stats)
export interface ApiStats {
  active_runs: number
  pending_approvals: number
  policy_count: number
  tokens_last_24h: number
}

// Matches auth/handler.go → userResponse (GET /api/v1/users)
export interface ApiUser {
  id: string
  username: string
  roles: string[]
  created_at: string
  deactivated_at: string | null
}

// Matches mcp_handler.go → mcpToolResponse (GET /api/v1/mcp/servers/:id/tools)
export interface ApiMcpTool {
  id: string
  server_id: string
  name: string
  description: string
  capability_role: 'sensor' | 'actuator' | 'feedback'
  input_schema: Record<string, unknown>
}
