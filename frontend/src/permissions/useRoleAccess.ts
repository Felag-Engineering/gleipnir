import { useCurrentUser } from '@/hooks/queries/users'
import { canAccess, canSetUp, type AppRoute } from './roleAccess'

export interface RoleAccess {
  // False until /auth/me has answered. Everything below is false until then,
  // so a role-gated query never fires on a guess.
  isKnown: boolean
  canAccess: (route: AppRoute) => boolean
  canSetUp: boolean
}

export function useRoleAccess(): RoleAccess {
  const { data: currentUser } = useCurrentUser()
  const roles = currentUser?.roles ?? []
  return {
    isKnown: currentUser !== undefined,
    canAccess: (route: AppRoute) => canAccess(roles, route),
    canSetUp: canSetUp(roles),
  }
}
