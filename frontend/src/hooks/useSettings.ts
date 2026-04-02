import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch, apiFetchVoid } from '@/api/fetch'
import { queryKeys } from './queryKeys'

export interface ApiPreferences {
  default_model?: string
  timezone?: string
  date_format?: string
}

export interface ApiSession {
  id: string
  user_agent: string
  ip_address: string
  created_at: string
  expires_at: string
  is_current: boolean
}

export function usePreferences() {
  return useQuery({
    queryKey: queryKeys.preferences.all,
    queryFn: () => apiFetch<ApiPreferences>('/settings/preferences'),
  })
}

export function useUpdatePreferences() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (prefs: ApiPreferences) =>
      apiFetch<ApiPreferences>('/settings/preferences', {
        method: 'PUT',
        body: JSON.stringify(prefs),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.preferences.all })
    },
  })
}

export function useSessions() {
  return useQuery({
    queryKey: queryKeys.sessions.all,
    queryFn: () => apiFetch<ApiSession[]>('/auth/sessions'),
  })
}

export function useRevokeSession() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (sessionId: string) =>
      apiFetchVoid(`/auth/sessions/${encodeURIComponent(sessionId)}`, {
        method: 'DELETE',
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.sessions.all })
    },
  })
}

export function useChangePassword() {
  return useMutation({
    mutationFn: (params: { current_password: string; new_password: string }) =>
      apiFetchVoid('/auth/password', {
        method: 'POST',
        body: JSON.stringify(params),
      }),
  })
}
