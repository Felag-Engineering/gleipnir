import { useNavigate, useRouteError } from 'react-router-dom'
import ErrorFallback from './ErrorFallback'

export default function RouteErrorFallback() {
  const error = useRouteError()
  const navigate = useNavigate()

  // navigate(0) triggers a full page reload, which is the only reliable way
  // to reset state after a render crash inside the router.
  const reset = () => navigate(0)

  return <ErrorFallback error={error} resetErrorBoundary={reset} />
}
