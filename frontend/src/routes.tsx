import { createBrowserRouter, Navigate, useParams } from 'react-router-dom'
import Layout from './components/Layout/Layout'
import DashboardPage from './pages/DashboardPage'
import LoginPage from './pages/LoginPage'
import SetupPage from './pages/SetupPage'
import PoliciesPage from './pages/PoliciesPage'
import PolicyEditorPage from './pages/PolicyEditorPage'
import RunDetailPage from './pages/RunDetailPage'
import RunsPage from './pages/RunsPage'
import MCPPage from './pages/MCPPage'
import UsersPage from './pages/UsersPage'
import { RouteErrorFallback } from './components/ErrorBoundary'

function PolicyRunsRedirect() {
  const { id } = useParams<{ id: string }>()
  return <Navigate to={`/runs?policy=${id}`} replace />
}

const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  { path: '/setup', element: <SetupPage /> },
  {
    path: '/',
    element: <Layout />,
    children: [
      { index: true, element: <Navigate to="/dashboard" replace /> },
      { path: 'dashboard', element: <DashboardPage />, errorElement: <RouteErrorFallback /> },
      { path: 'runs', element: <RunsPage />, errorElement: <RouteErrorFallback /> },
      { path: 'policies', element: <PoliciesPage />, errorElement: <RouteErrorFallback /> },
      { path: 'policies/new', element: <PolicyEditorPage />, errorElement: <RouteErrorFallback /> },
      { path: 'policies/:id/runs', element: <PolicyRunsRedirect />, errorElement: <RouteErrorFallback /> },
      { path: 'policies/:id', element: <PolicyEditorPage />, errorElement: <RouteErrorFallback /> },
      { path: 'runs/:id', element: <RunDetailPage />, errorElement: <RouteErrorFallback /> },
      { path: 'tools', element: <MCPPage />, errorElement: <RouteErrorFallback /> },
      { path: 'mcp', element: <Navigate to="/tools" replace /> },
      { path: 'users', element: <UsersPage />, errorElement: <RouteErrorFallback /> },
    ],
  },
])

export default router
