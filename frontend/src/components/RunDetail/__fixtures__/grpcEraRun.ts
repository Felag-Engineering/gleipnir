// MUST-STAY-GREEN COMPAT FIXTURE for the v1 -> v2 plugin cutover (#963).
//
// Run steps exactly as the v1 gRPC plugin runtime recorded them; the Go twin
// is internal/testutil/fixtures/grpc_era_runs.go and the two must stay in
// step. The #1004 purge of v1 plugin code must re-run grpcEraRun.test.tsx
// against this file. A failure means history written under v1 no longer
// renders; fix the renderer, not this fixture.
import type { ApiRunStep } from '@/api/types'

function step(n: number, type: string, content: unknown): ApiRunStep {
  return {
    id: `stp-grpc-era-${String(n).padStart(2, '0')}`,
    run_id: 'run-grpc-era',
    step_number: n,
    type,
    content: JSON.stringify(content),
    token_cost: 0,
    created_at: '2026-03-10T12:00:00Z',
  }
}

export const GRPC_ERA_STEPS: ApiRunStep[] = [
  step(0, 'capability_snapshot', {
    provider: 'anthropic',
    model: 'claude-opus-4-5',
    tools: [
      { server_name: 'slack-ops', tool_name: 'post_message', approval: 'none', timeout: 0, on_timeout: '', source: 'plugin:slack-ops@7' },
      { server_name: 'slack-ops', tool_name: 'delete_channel', approval: 'required', timeout: 3600000000000, on_timeout: 'fail', source: 'plugin:slack-ops@7' },
      { server_name: 'gleipnir', tool_name: 'ask_operator', approval: 'none', timeout: 0, on_timeout: '' },
    ],
  }),
  step(1, 'thought', { text: 'I will post a notice, then clean up the channel.' }),
  step(2, 'tool_call', { tool_name: 'slack-ops.post_message', server_id: 'slack-ops', input: { channel: 'ops', text: 'disk full' } }),
  step(3, 'tool_result', { tool_name: 'slack-ops.post_message', output: '{"ok":true,"ts":"1710072000.000100"}', is_error: false }),
  step(4, 'approval_request', { approval_id: 'apr-grpc-era', tool: 'slack-ops.delete_channel', input: { channel: 'old-alerts' } }),
  step(5, 'tool_call', { tool_name: 'slack-ops.delete_channel', server_id: 'slack-ops', input: { channel: 'old-alerts' } }),
  step(6, 'tool_result', { tool_name: 'slack-ops.delete_channel', output: 'channel archived', is_error: false }),
  step(7, 'feedback_request', { feedback_id: 'fbk-grpc-era', tool: 'gleipnir.ask_operator', message: 'Which channel should I archive?', expires_at: '2026-03-10T12:33:00Z' }),
  step(8, 'feedback_response', { feedback_id: 'fbk-grpc-era', response: 'old-alerts' }),
  step(9, 'error', { message: 'plugin pre-ack failed: instance unreachable', code: 'feedback_dispatch_error', kind: 'feedback_dispatch_error', instance: 'slack-ops', request_id: 'ppr-grpc-era-preack' }),
  step(10, 'error', { message: 'plugin request timed out after 1m0s', code: 'plugin_request_timeout', kind: 'plugin_request_timeout', request_id: 'ppr-grpc-era-timeout', tool_name: 'gleipnir.ask_operator' }),
  step(11, 'complete', { message: 'agent completed task' }),
]
