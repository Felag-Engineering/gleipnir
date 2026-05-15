import { Button } from '@/components/Button'
import { useBeginPluginOAuth } from '@/hooks/mutations/plugins'

interface ReauthorizeButtonProps {
  pluginId: string
  instanceId: string
  strategy: string
}

const OAUTH_STRATEGIES = ['oauth2_authcode', 'oauth2_clientcred']

// ReauthorizeButton renders a primary button that restarts the OAuth dance for
// an instance whose token refresh has failed (#228). It only renders for OAuth
// strategies; calling it on a non-OAuth instance is a no-op by design so it
// can safely be dropped into surfaces that may not gate strategy up-front.
export function ReauthorizeButton({ pluginId, instanceId, strategy }: ReauthorizeButtonProps) {
  const mutation = useBeginPluginOAuth()

  // Defense-in-depth: only OAuth strategies need re-authorization. The page
  // already gates on isOAuthRefreshFailure, so this guard should not normally
  // fire in production.
  if (!OAUTH_STRATEGIES.includes(strategy)) {
    return null
  }

  function handleClick() {
    const returnUrl = window.location.pathname + window.location.search
    mutation.mutate(
      { pluginId, instanceId, returnUrl },
      {
        onSuccess(data) {
          if (data.authorize_url) {
            // authcode: redirect the browser to the provider's authorization page.
            window.location.href = data.authorize_url
          }
          // clientcred: exchange is synchronous; query invalidation in the
          // mutation's onSuccess already refreshes instance state.
        },
      },
    )
  }

  return (
    <Button
      variant="primary"
      size="small"
      onClick={handleClick}
      disabled={mutation.isPending}
    >
      {mutation.isPending ? 'Starting…' : 'Re-authorize'}
    </Button>
  )
}
