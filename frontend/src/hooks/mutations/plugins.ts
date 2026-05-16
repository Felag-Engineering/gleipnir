import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch, apiFetchVoid } from '@/api/fetch'
import type { ApiAcceptNewKeyResponse } from '@/api/types'
import { queryKeys } from '../queryKeys'

// BeginPluginOAuthParams carries the inputs for POST .../oauth/begin.
export interface BeginPluginOAuthParams {
  pluginId: string
  instanceId: string
  returnUrl: string
}

// BeginPluginOAuthResponse is the server response from POST .../oauth/begin.
// For oauth2_authcode the server returns authorize_url; the browser must
// navigate there to continue the OAuth dance.
// For oauth2_clientcred the exchange is synchronous and the server returns
// status: "ok" with no authorize_url.
export interface BeginPluginOAuthResponse {
  authorize_url?: string
  status?: string
}

// useBeginPluginOAuth posts to the begin-oauth endpoint for an instance.
// On success it invalidates the plugin-instances query so re-authorization
// state (health_state, health_detail) is refreshed across the UI.
export function useBeginPluginOAuth() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ pluginId, instanceId, returnUrl }: BeginPluginOAuthParams) =>
      apiFetch<BeginPluginOAuthResponse>(
        `/admin/plugins/${encodeURIComponent(pluginId)}/instances/${encodeURIComponent(instanceId)}/oauth/begin`,
        {
          method: 'POST',
          body: JSON.stringify({ return_url: returnUrl }),
        },
      ),
    onSuccess: () => {
      // Refresh instance list so health state reflects any synchronous change
      // (clientcred) or a prior refresh-failure banner clears after authcode
      // round-trip.
      void queryClient.invalidateQueries({
        queryKey: queryKeys.admin.pluginInstances,
      })
    },
  })
}

// SetInstanceSubscriptionScopeParams carries the payload for
// PUT /api/v1/admin/plugins/{id}/instances/{iid}/subscription-scope.
export interface SetInstanceSubscriptionScopeParams {
  pluginId: string
  instanceId: string
  scope: Record<string, unknown>
  expectedVersion: number
}

interface AcceptNewKeyParams {
  pluginId: string
  candidatePubkey: string
}

// useAcceptPluginNewKey posts the candidate pubkey to the backend, which
// rotates the plugin's trusted_pubkey and unblocks any pending_key_approval
// instances. On success, the plugin instance query cache is invalidated so
// health chips refresh automatically.
export function useAcceptPluginNewKey() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ pluginId, candidatePubkey }: AcceptNewKeyParams) =>
      apiFetch<ApiAcceptNewKeyResponse>(`/admin/plugins/${encodeURIComponent(pluginId)}/accept-new-key`, {
        method: 'POST',
        body: JSON.stringify({ candidate_pubkey: candidatePubkey }),
      }),
    onSuccess: (_data, { pluginId }) => {
      // Invalidate all instance queries for this plugin so health chips reflect
      // the transition to healthy.
      void queryClient.invalidateQueries({
        queryKey: ['admin', 'plugins', pluginId],
      })
    },
  })
}

// ── Credential mutations ──────────────────────────────────────────────────────
//
// All five mutations invalidate queryKeys.plugins.credentials(...) so the
// CredentialsTab re-fetches the updated redacted view. They do NOT invalidate
// admin.pluginInstances — credentials do not change instance health state.

interface PluginInstanceRef {
  pluginId: string
  instanceId: string
}

// useSetPluginStaticAPIKey writes the static_api_key credential blob.
// PUT /api/v1/admin/plugins/{id}/instances/{iid}/credentials/static-api-key
export function useSetPluginStaticAPIKey() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({
      pluginId,
      instanceId,
      header_name,
      scheme,
      api_key,
    }: PluginInstanceRef & { header_name: string; scheme?: string; api_key: string }) =>
      apiFetchVoid(
        `/admin/plugins/${encodeURIComponent(pluginId)}/instances/${encodeURIComponent(instanceId)}/credentials/static-api-key`,
        {
          method: 'PUT',
          body: JSON.stringify({ header_name, scheme, api_key }),
        },
      ),
    onSuccess: (_data, { pluginId, instanceId }) => {
      void queryClient.invalidateQueries({
        queryKey: queryKeys.plugins.credentials(pluginId, instanceId),
      })
    },
  })
}

// useSetPluginHeader writes a single named header for the header_set strategy.
// PUT /api/v1/admin/plugins/{id}/instances/{iid}/credentials/headers/{name}
export function useSetPluginHeader() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({
      pluginId,
      instanceId,
      name,
      value,
    }: PluginInstanceRef & { name: string; value: string }) =>
      apiFetchVoid(
        `/admin/plugins/${encodeURIComponent(pluginId)}/instances/${encodeURIComponent(instanceId)}/credentials/headers/${encodeURIComponent(name)}`,
        {
          method: 'PUT',
          body: JSON.stringify({ value }),
        },
      ),
    onSuccess: (_data, { pluginId, instanceId }) => {
      void queryClient.invalidateQueries({
        queryKey: queryKeys.plugins.credentials(pluginId, instanceId),
      })
    },
  })
}

// useDeletePluginHeader removes a single named header from the header_set credential blob.
// DELETE /api/v1/admin/plugins/{id}/instances/{iid}/credentials/headers/{name}
export function useDeletePluginHeader() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({
      pluginId,
      instanceId,
      name,
    }: PluginInstanceRef & { name: string }) =>
      apiFetchVoid(
        `/admin/plugins/${encodeURIComponent(pluginId)}/instances/${encodeURIComponent(instanceId)}/credentials/headers/${encodeURIComponent(name)}`,
        { method: 'DELETE' },
      ),
    onSuccess: (_data, { pluginId, instanceId }) => {
      void queryClient.invalidateQueries({
        queryKey: queryKeys.plugins.credentials(pluginId, instanceId),
      })
    },
  })
}

// useSetPluginBasicAuth writes the basic_auth credential blob.
// PUT /api/v1/admin/plugins/{id}/instances/{iid}/credentials/basic-auth
export function useSetPluginBasicAuth() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({
      pluginId,
      instanceId,
      username,
      password,
    }: PluginInstanceRef & { username: string; password: string }) =>
      apiFetchVoid(
        `/admin/plugins/${encodeURIComponent(pluginId)}/instances/${encodeURIComponent(instanceId)}/credentials/basic-auth`,
        {
          method: 'PUT',
          body: JSON.stringify({ username, password }),
        },
      ),
    onSuccess: (_data, { pluginId, instanceId }) => {
      void queryClient.invalidateQueries({
        queryKey: queryKeys.plugins.credentials(pluginId, instanceId),
      })
    },
  })
}

// useClearPluginCredentials wipes the credential blob while preserving Strategy.
// DELETE /api/v1/admin/plugins/{id}/instances/{iid}/credentials
export function useClearPluginCredentials() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ pluginId, instanceId }: PluginInstanceRef) =>
      apiFetchVoid(
        `/admin/plugins/${encodeURIComponent(pluginId)}/instances/${encodeURIComponent(instanceId)}/credentials`,
        { method: 'DELETE' },
      ),
    onSuccess: (_data, { pluginId, instanceId }) => {
      void queryClient.invalidateQueries({
        queryKey: queryKeys.plugins.credentials(pluginId, instanceId),
      })
    },
  })
}

// ── Subscription scope ────────────────────────────────────────────────────────

// useSetInstanceSubscriptionScope submits a new subscription scope for a plugin
// instance. On success it invalidates the plugin-instances list so the audience
// editor and trigger picker reflect the updated scope.
export function useSetInstanceSubscriptionScope() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ pluginId, instanceId, scope, expectedVersion }: SetInstanceSubscriptionScopeParams) =>
      apiFetch<unknown>(
        `/admin/plugins/${encodeURIComponent(pluginId)}/instances/${encodeURIComponent(instanceId)}/subscription-scope`,
        {
          method: 'PUT',
          body: JSON.stringify({ scope, expected_version: expectedVersion }),
        },
      ),
    onSuccess: () => {
      // Invalidate the list so the Subscriptions tab re-fetches the new
      // version + scope on next render.
      void queryClient.invalidateQueries({
        queryKey: queryKeys.admin.pluginInstances,
      })
    },
  })
}
