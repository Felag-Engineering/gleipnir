import { Button } from '@/components/Button'
import { useBeginPluginOAuth } from '@/hooks/mutations/plugins'
import { errMessage } from '@/api/fetch'

interface ReauthorizeButtonProps {
  pluginId: string
  instanceId: string
  strategy: string
  // label and pendingLabel allow callers to customise the button text.
  // Defaults preserve the original "Re-authorize" / "Starting…" strings so
  // the existing page-level banner is unaffected.
  label?: string
  pendingLabel?: string
  // onError is called with null before each mutation attempt (to clear any
  // prior error banner) and with the server error message if the mutation
  // fails. The parent component owns error display; the button owns lifecycle.
  onError?: (message: string | null) => void
}

const OAUTH_STRATEGIES = ['oauth2_authcode', 'oauth2_clientcred']

// ReauthorizeButton renders a primary button that restarts the OAuth dance for
// an instance whose token refresh has failed (#228). It only renders for OAuth
// strategies; calling it on a non-OAuth instance is a no-op by design so it
// can safely be dropped into surfaces that may not gate strategy up-front.
//
// The optional `label` and `pendingLabel` props allow CredentialsTab to reuse
// this button for the initial "Authorize" CTA without a separate component.
//
// Pass `onError` to receive mutation errors as a string so the parent can
// render a visible error banner. The callback is always called with null first
// (to reset any previous error) before each attempt.
export function ReauthorizeButton({
  pluginId,
  instanceId,
  strategy,
  label = 'Re-authorize',
  pendingLabel = 'Starting…',
  onError,
}: ReauthorizeButtonProps) {
  const mutation = useBeginPluginOAuth()

  // Defense-in-depth: only OAuth strategies need re-authorization. The page
  // already gates on isOAuthRefreshFailure, so this guard should not normally
  // fire in production.
  if (!OAUTH_STRATEGIES.includes(strategy)) {
    return null
  }

  function handleClick() {
    // Clear any prior error before starting a new attempt.
    onError?.(null)

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
        onError(err) {
          onError?.(errMessage(err, 'OAuth authorization failed.'))
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
      {mutation.isPending ? pendingLabel : label}
    </Button>
  )
}
