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
  model: string
  tool_count: number
  tool_refs: string[]
  avg_token_cost: number
  run_count: number
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
  model: string
  policy_updated_at?: string
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
  pending_feedback: number
  policy_count: number
  tokens_last_24h: number
}

// Matches api/timeseries_handler.go → TimeSeriesBucket
export interface ApiTimeSeriesBucket {
  timestamp: string
  completed: number
  failed: number
  waiting_for_approval: number
  waiting_for_feedback: number
  cost_by_model: Record<string, number>
}

// Matches api/timeseries_handler.go → TimeSeriesResponse
export interface ApiTimeSeriesResponse {
  buckets: ApiTimeSeriesBucket[]
}

// Matches api/attention_handler.go → AttentionItem
export interface ApiAttentionItem {
  type: 'approval' | 'feedback' | 'failure'
  request_id: string
  run_id: string
  policy_id: string
  policy_name: string
  tool_name: string
  message: string
  expires_at: string | null
  created_at: string
}

// Matches api/attention_handler.go → AttentionResponse
export interface ApiAttentionResponse {
  items: ApiAttentionItem[]
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
  input_schema: Record<string, unknown>
}

// --- Settings ---

// Matches auth/settings_handler.go → map[string]string (GET/PUT /api/v1/settings/preferences)
export interface ApiPreferences {
  timezone?: string
  date_format?: string
}

// Matches api/policy_webhook_handler.go → map[string]string{"secret": ...}
// Returned by POST /api/v1/policies/:id/webhook/rotate and
// GET /api/v1/policies/:id/webhook/secret.
export interface WebhookSecretResponse {
  secret: string
}

// Matches auth/handler.go → sessionResponse (GET /api/v1/auth/sessions)
export interface ApiSession {
  id: string
  user_agent: string
  ip_address: string
  created_at: string
  expires_at: string
  is_current: boolean
}

// --- Approvals ---

// Matches trigger/runs_handler.go → ApprovalDecisionRequest (POST /api/v1/runs/:id/approval)
export interface ApproveRunRequest {
  runId: string
  decision: 'approved' | 'denied'
}

// Matches trigger/runs_handler.go → map[string]string approval response
export interface ApproveRunResponse {
  run_id: string
  decision: string
}

// --- Feedback ---

// Matches trigger/runs_handler.go → FeedbackDecisionRequest (POST /api/v1/runs/:id/feedback)
export interface SubmitFeedbackRequest {
  runId: string
  response: string
  feedbackId?: string
}

// Matches trigger/runs_handler.go → map[string]string feedback response
export interface SubmitFeedbackResponse {
  run_id: string
}

// --- Policy trigger ---

// Matches trigger/manual.go → Handle (POST /api/v1/policies/:id/trigger)
export interface TriggerPolicyRequest {
  policyId: string
  message?: string
}

// Matches trigger/manual.go → map[string]string {"run_id": ...}
export interface TriggerPolicyResponse {
  run_id: string
}

// --- MCP servers ---

// Matches api/mcp_handler.go → Create body (POST /api/v1/mcp/servers)
export interface AddMcpServerRequest {
  name: string
  url: string
}

// Matches api/mcp_handler.go → testConnectionResponse (POST /api/v1/mcp/servers/test)
export interface TestMcpConnectionRequest {
  url: string
}

// ok=true means the handshake succeeded; ok=false means the server was unreachable
// or returned an error. HTTP 200 is always returned — error is in the body.
export interface TestMcpConnectionResponse {
  ok: boolean
  tool_count: number
  tools: string[]
  error: string
}

// --- Users ---

// Matches auth/handler.go → createUserRequest (POST /api/v1/users)
export interface CreateUserRequest {
  username: string
  password: string
  roles: string[]
}

// Matches auth/handler.go → updateUserRequest (PATCH /api/v1/users/:id)
export interface UpdateUserRequest {
  id: string
  deactivated?: boolean
  roles?: string[]
}

// --- Admin ---

// Matches admin/handler.go → providerStatus (GET /api/v1/admin/providers)
export interface ApiProviderStatus {
  name: string
  has_key: boolean
  masked_key?: string
}

// Matches admin/handler.go → modelSetting (GET /api/v1/admin/models)
export interface ApiModelSetting {
  provider: string
  model_name: string
  enabled: boolean
  updated_at: string
}

// Matches admin/handler.go → allModelResponse (GET /api/v1/admin/models/all)
export interface ApiAllModelEntry {
  provider: string
  model_name: string
  display_name: string
  enabled: boolean
}

// Matches admin/handler.go → map[string]string (GET/PUT /api/v1/admin/settings)
export interface ApiSystemSettings {
  [key: string]: string
}

// Matches admin/handler.go → GetPublicConfig (GET /api/v1/config)
// Non-sensitive config returned to all authenticated users (no admin role required).
export interface ApiPublicConfig {
  public_url: string
}

// Matches admin/handler.go → systemInfo (GET /api/v1/admin/system-info)
export interface ApiSystemInfo {
  version: string
  uptime: string
  db_size: string
  mcp_servers: number
  policies: number
  users: number
}

// --- OpenAI-compatible providers ---

// Matches internal/admin/openai_compat_handler.go → providerResponse (GET/POST/PUT /api/v1/admin/openai-providers)
export interface ApiOpenAICompatProvider {
  id: number
  name: string
  base_url: string
  masked_key: string
  models_endpoint_available: boolean
  created_at: string
  updated_at: string
}

// Matches internal/admin/openai_compat_handler.go → upsertRequest body (POST/PUT /api/v1/admin/openai-providers)
export interface ApiOpenAICompatProviderUpsert {
  name: string
  base_url: string
  api_key: string
}

// Matches internal/admin/openai_compat_handler.go → testResponse (POST /api/v1/admin/openai-providers/:id/test)
export interface ApiOpenAICompatProviderTestResult {
  ok: boolean
  models_endpoint_available?: boolean
  error?: string
}
