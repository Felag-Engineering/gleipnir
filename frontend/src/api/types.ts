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
  trigger_source?: string
  trigger_event_kind?: string
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
  next_fire_at: string | null
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
  approval_expires_at?: string
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
  auth_header_keys?: string[] // sorted header names; values are never returned
  is_arcade_gateway: boolean
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
  slack_user_id: string | null
}

// Matches mcp_handler.go → mcpToolResponse (GET /api/v1/mcp/servers/:id/tools)
export interface ApiMcpTool {
  id: string
  server_id: string
  name: string
  description: string
  input_schema: Record<string, unknown>
  enabled: boolean
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
  auth_headers?: { key: string; value: string }[]
}

// Matches api/mcp_handler.go → Update body (PUT /api/v1/mcp/servers/:id)
// Auth headers are NOT included — use SetMcpServerHeaderRequest instead.
export interface UpdateMcpServerRequest {
  name: string
  url: string
}

// Matches api/mcp_handler.go → SetAuthHeader body (PUT /api/v1/mcp/servers/:id/headers/:name)
export type SetMcpServerHeaderRequest = { value: string }

// Matches api/mcp_handler.go → testConnectionResponse (POST /api/v1/mcp/servers/test)
export interface TestMcpConnectionRequest {
  url: string
  auth_headers?: { key: string; value: string }[]
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
  slack_user_id?: string | null
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

// Matches admin/handler.go → defaultModel within publicConfigResponse (GET /api/v1/config)
export interface ApiDefaultModel {
  provider: string
  name: string
}

// Matches admin/handler.go → publicConfigResponse (GET /api/v1/config)
// Non-sensitive config returned to all authenticated users (no admin role required).
export interface ApiPublicConfig {
  public_url: string
  default_model: ApiDefaultModel | null
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

// Matches api/arcade_handler.go → response shapes (ADR-040)
export type ApiArcadeAuthorizeResponse =
  | { status: 'completed' }
  | { status: 'pending'; url: string; auth_id: string }
  | { status: 'failed'; error?: string }

export interface ArcadeAuthorizeRequest {
  toolkit: string
}

export interface ArcadeAuthorizeWaitRequest {
  toolkit: string
  auth_id: string
}

// Matches internal/model/plugin_health.go → PluginHealthState.
// Eleven states across three severity tiers:
//   Green  — healthy
//   Yellow — degraded but operational
//   Red    — non-functional
export type PluginHealthState =
  | 'healthy'
  | 'unsigned_permissive'
  | 'pending_key_approval'
  | 'pending_manifest_approval'
  | 'pending_config_migration'
  | 'pending_reauthorize'
  | 'unhealthy'
  | 'circuit_broken'
  | 'verification_error'
  | 'signature_invalid'
  | 'crashed'
  | 'inactive'

// Matches internal/admin/plugin_handler.go → instanceResponse.
export interface ApiPluginInstance {
  id: string
  plugin_id: string
  instance_name: string
  state: PluginHealthState
  detail: string | null
  version: number
  updated_at: string
}

// Payload embedded in a plugin_pubkey_mismatch audit event's payload_json field.
// Sourced by AcceptNewKeyModal to obtain the candidate pubkey and display fingerprints.
export interface ApiPluginPubkeyMismatchAuditPayload {
  plugin_id: string
  name: string
  old_pubkey_fingerprint: string
  new_pubkey_fingerprint: string
  // Base64-encoded Minisign signing.pub bytes — passed as-is to accept-new-key.
  new_pubkey_b64: string
  version: string
}

// Matches internal/admin/plugin_handler.go → acceptNewKeyResponse.
export interface ApiAcceptNewKeyResponse {
  accepted_pubkey_fingerprint: string
  instances_unblocked: number
}

// Matches internal/admin/plugin_handler.go → acceptManifestResponse.
export interface ApiAcceptManifestResponse {
  accepted_manifest_version: string
  instances_unblocked: number
  instances_pending_config: number
}

// --- Audiences ---

// Matches internal/http/api/audience_handler.go → audienceListItemDTO (GET /api/v1/admin/audiences).
export interface ApiAudienceListItem {
  id: string
  name: string
  entry_count: number
  referenced_by_policy_count: number
  has_in_flight_runs: boolean
  disable_in_app_fallback: boolean
  version: number
  created_at: string
  updated_at: string
}

// Matches internal/http/api/audience_handler.go → audienceEntryDTO.
// `auto: true` is set on the synthetic gleipnir.in-app fallback entry (not stored in the DB).
export interface ApiAudienceEntry {
  id: string
  plugin_instance_id: string
  position: number
  notify: boolean
  request: boolean
  config: Record<string, unknown>
  auto?: boolean
}

// Matches internal/http/api/audience_handler.go → audienceDTO (GET /api/v1/admin/audiences/:id).
export interface ApiAudience {
  id: string
  name: string
  disable_in_app_fallback: boolean
  version: number
  created_at: string
  updated_at: string
  entries: ApiAudienceEntry[]
}

// Matches internal/http/api/audience_handler.go → policyRefDTO.
export interface ApiAudiencePolicyRef {
  id: string
  name: string
}

// Matches internal/http/api/audience_handler.go → inFlightRunDTO.
export interface ApiAudienceInFlightRun {
  id: string
  policy_id: string
  status: string
}

// Matches internal/http/api/audience_handler.go → audienceReferencesDTO
// (GET /api/v1/admin/audiences/:id/references).
export interface ApiAudienceReferences {
  policies: ApiAudiencePolicyRef[]
  in_flight_runs: ApiAudienceInFlightRun[]
}

// ApiPluginEventKind is a single event kind from a plugin's manifest, as returned
// by GET /api/v1/admin/plugin-instances. Used by the trigger picker (#219) to
// show plugin-sourced trigger options alongside built-in types.
export interface ApiPluginEventKind {
  kind: string
  description: string
  // binding_schema is the JSON Schema for per-policy binding fields.
  // Omitted when the plugin declares no binding constraints for this event kind.
  binding_schema?: unknown
  // examples are named sample payloads for "Test against sample" (spec §7.5).
  // Omitted when the plugin declares no examples.
  examples?: { name: string; payload: Record<string, unknown> }[]
}

// Matches internal/http/api/audience_handler.go (GET /api/v1/admin/plugin-instances).
// Consumers: audience editor (#290) and trigger picker (#219).
export interface ApiPluginInstanceForAudience {
  id: string
  plugin_id: string
  // plugin_name is the human-readable name from the plugin manifest (e.g. "Slack").
  plugin_name?: string
  instance_name: string
  state: PluginHealthState
  implements_notify: boolean
  implements_request: boolean
  config_schema: Record<string, unknown> | null
  // event_kinds is populated from the plugin's manifest EventKinds declarations.
  // Empty array when the plugin does not declare a TriggerService.
  event_kinds?: ApiPluginEventKind[]
  // subscription_schema is the manifest-level JSON Schema for the coarse
  // subscription scope (spec §4.3). Absent when the plugin has no subscription_schema.
  subscription_schema?: unknown
  // subscription_scope is the current instance-level scope value.
  subscription_scope?: Record<string, unknown>
  // version is the CAS version of the instance row, used for optimistic
  // concurrency control on subscription scope saves.
  version: number
  // auth_strategy is the manifest-declared credential strategy
  // (e.g. "oauth2_authcode", "oauth2_clientcred", "static_api_key").
  // Empty string when the manifest has no auth section. Used by the
  // Re-authorize banner to determine which OAuth flow to restart (#228).
  auth_strategy?: string
  // health_detail is the detail string from the most recent health transition.
  // Absent when the instance has no detail. Used together with auth_strategy
  // and state to gate the Re-authorize banner (#228).
  health_detail?: string
  // last_oauth_callback_url is the OAuth callback URL recorded the last time
  // this instance completed an OAuth flow. Absent when never authorized. Used
  // by the admin/plugins page to show which URL needs updating after a
  // public_url change (#230).
  last_oauth_callback_url?: string
  // plugin_version is the plugin's SemVer from the manifest (e.g. "1.0.0").
  // Used by the admin/plugins page to display the version on each plugin card.
  plugin_version?: string
  // services lists the gRPC services declared by the plugin manifest
  // (e.g. ["tool", "trigger", "channel"]). Used by the admin/plugins page
  // to render service badges on each plugin card.
  services?: string[]
  // tools lists the tool declarations from the plugin manifest (ToolService
  // plugins only). Each entry has name and description. Absent when the
  // plugin declares no tools.
  tools?: { name: string; description: string }[]
}

// ApiPluginInstanceDetail matches internal/admin/plugin_handler.go → instanceResponse.
// Returned by GET /api/v1/admin/plugins/{id}/instances/{iid}.
// config_json is a JSON-encoded map — the client parses it for display.
// Secret fields in config_json are redacted to "***" (ADR-049).
// config_schema is the manifest's instance-level JSON Schema (verbatim — schema
// annotations like x-gleipnir-secret drive redaction of VALUES in config_json,
// the schema itself is metadata and never redacted). null when the manifest
// declares no config_schema.
export interface ApiPluginInstanceDetail {
  id: string
  plugin_id: string
  instance_name: string
  state: string
  detail: string | null
  version: number
  updated_at: string
  subscription_scope_json: string
  config_json: string
  config_schema: Record<string, unknown> | null
}

// ApiInstalledPlugin matches the installResponse struct returned by
// POST /api/v1/admin/plugins (internal/admin/plugin_handler.go:installResponse).
export interface ApiInstalledPlugin {
  id: string
  name: string
  version: string
  status: string
}

// ApiCreatedPluginInstance matches the createInstanceResponse struct returned by
// POST /api/v1/admin/plugins/{id}/instances (internal/admin/plugin_handler.go:createInstanceResponse).
// health_detail is typed as string | null | undefined to cover both the "field
// omitted" (undefined) and "field present but null" wire shapes from the server.
// Always guard reads with: if (inst.health_detail) { ... }
export interface ApiCreatedPluginInstance {
  id: string
  plugin_id: string
  instance_name: string
  health_state: string
  health_detail?: string | null
  version: number
  created_at: string
  updated_at: string
}

// Request body interfaces for audience mutations.
// Matches internal/http/api/audience_handler.go.

export interface AudienceEntryInput {
  plugin_instance_id: string
  notify: boolean
  request: boolean
  config: Record<string, unknown>
}

export interface AudienceCreateRequest {
  name: string
  disable_in_app_fallback: boolean
  entries: AudienceEntryInput[]
}

export interface AudienceUpdateRequest {
  name: string
  disable_in_app_fallback: boolean
  // Must be `expected_version` to match audienceSaveRequest.ExpectedVersion's
  // json:"expected_version,omitempty" tag in audience_handler.go.
  expected_version: number
  entries: AudienceEntryInput[]
}

// Matches internal/http/api/binding_test_handler.go → bindingTestRequest.
// POST /api/v1/admin/plugin-instances/{iid}/event-kinds/{kind}/test-binding.
// The client sends payloads back (stateless; avoids hot-reload drift between
// the list call and this test call).
export interface ApiBindingTestRequest {
  binding: Record<string, unknown>
  payloads: Record<string, unknown>[]
}

// Matches internal/http/api/binding_test_handler.go → bindingTestResponse.
export interface ApiBindingTestResponse {
  results: { match: boolean; error?: string }[]
}

// PluginAuthStrategy mirrors the AuthStrategy* constants in plugin-sdk/manifest.
// Used to type-narrow credential form rendering in CredentialsTab.
export type PluginAuthStrategy =
  | 'none'
  | 'static_api_key'
  | 'header_set'
  | 'basic_auth'
  | 'oauth2_authcode'
  | 'oauth2_clientcred'

// Matches internal/admin/plugin_handler.go → pluginListItemResponse (GET /api/v1/admin/plugins).
export interface ApiPluginListItem {
  id: string
  name: string
  version: string
  description?: string
  status: string // 'pending_review' | 'active' | 'removed'
  services: string[] // e.g. ['tool', 'trigger']
  pubkey_fingerprint?: string
  has_sbom: boolean
  instance_count: number
  created_at: string
}

// Matches internal/admin/plugin_handler.go → pluginDetailResponse (GET /api/v1/admin/plugins/:id).
export interface ApiPluginDetail {
  id: string
  name: string
  version: string
  description?: string
  author?: string
  license?: string
  status: string
  services: string[]
  tier2_capabilities?: string[]
  auth_strategy: string
  has_oauth_defaults: boolean
  pubkey_fingerprint?: string
  has_sbom: boolean
  created_at: string
}

// Matches internal/admin/plugin_handler.go → approvePluginResponse
// (POST /api/v1/admin/plugins/:id/approve).
export interface ApiApprovePluginResponse {
  id: string
  name: string
  version: string
  status: string
}

// Matches internal/admin/plugin_handler.go → rejectPluginResponse
// (POST /api/v1/admin/plugins/:id/reject).
export interface ApiRejectPluginResponse {
  id: string
  name: string
  status: string
}

// ApiRedactedCredentials mirrors internal/plugin/oauth.RedactedCredentials
// (internal/plugin/oauth/credentials.go). Secret values are NEVER present —
// only presence flags (has_api_key, has_password, has_client_secret, has_token)
// and non-secret metadata.
export interface ApiRedactedCredentials {
  strategy: string

  // static_api_key fields
  header_name?: string
  scheme?: string
  has_api_key?: boolean

  // header_set fields
  header_names?: string[]

  // basic_auth fields
  username?: string
  has_password?: boolean

  // oauth2_* fields
  client_id?: string
  has_client_secret?: boolean
  authorization_url?: string
  token_url?: string
  scopes?: string[]
  has_token?: boolean
  // token_expires_at is an RFC3339Nano UTC timestamp. Absent when the token is
  // missing or has a zero expiry.
  token_expires_at?: string
}

// ApiPluginOption is a single selectable item from a plugin's options provider.
// Matches admin.apiOption → plugin_options.go.
export interface ApiPluginOption {
  value: string
  label: string
  group?: string
  disabled?: boolean
}

// ApiPluginOptionsResponse is the payload returned by
// GET /api/v1/admin/plugins/:id/instances/:iid/options/:source.
// Matches admin.pluginOptionsResponse → plugin_options.go.
export interface ApiPluginOptionsResponse {
  options: ApiPluginOption[]
  next_cursor: string
  // degraded is true when the plugin subprocess is unhealthy, inactive, or has
  // no ConfigOptionsService registered. The UI should fall back to a plain text
  // input when degraded is true.
  degraded?: boolean
}

// Matches admin.pluginRSSInstance → GetPluginRSS handler.
export interface ApiPluginRSSInstance {
  instance_id: string
  instance_name: string
  plugin_id: string
  rss_bytes: number
  sampled_at: string
}

// Matches admin.pluginRSSResponse → GetPluginRSS handler.
export interface ApiPluginRSS {
  total_bytes: number
  instance_count: number
  instances: ApiPluginRSSInstance[]
}
