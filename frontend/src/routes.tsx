import { createBrowserRouter, Navigate, useParams } from 'react-router-dom'
import Layout from '@/components/Layout'
import DashboardPage from './pages/DashboardPage'
import LoginPage from './pages/LoginPage'
import SetupPage from './pages/SetupPage'
import AgentsPage from './pages/AgentsPage'
import AgentEditorPage from './pages/AgentEditorPage'
import RunDetailPage from './pages/RunDetailPage'
import RunsPage from './pages/RunsPage'
import MCPPage from './pages/MCPPage'
import UsersPage from './pages/UsersPage'
import SettingsPage from './pages/SettingsPage'
import AdminModelsPage from './pages/AdminModelsPage'
import AdminSystemPage from './pages/AdminSystemPage'
import NotFoundPage from './pages/NotFoundPage'
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
    errorElement: <RouteErrorFallback />,
    children: [
      { index: true, element: <Navigate to="/dashboard" replace /> },
      { path: 'dashboard', element: <DashboardPage />, errorElement: <RouteErrorFallback /> },
      { path: 'runs', element: <RunsPage />, errorElement: <RouteErrorFallback /> },
      { path: 'agents', element: <AgentsPage />, errorElement: <RouteErrorFallback /> },
      { path: 'agents/new', element: <AgentEditorPage />, errorElement: <RouteErrorFallback /> },
      { path: 'agents/:id/runs', element: <PolicyRunsRedirect />, errorElement: <RouteErrorFallback /> },
      { path: 'agents/:id', element: <AgentEditorPage />, errorElement: <RouteErrorFallback /> },
      { path: 'runs/:id', element: <RunDetailPage />, errorElement: <RouteErrorFallback /> },
      { path: 'tools', element: <MCPPage />, errorElement: <RouteErrorFallback /> },
      { path: 'mcp', element: <Navigate to="/tools" replace /> },
      { path: 'users', element: <Navigate to="/admin/users" replace /> },
      { path: 'settings', element: <SettingsPage />, errorElement: <RouteErrorFallback /> },
      { path: 'settings/system', element: <Navigate to="/admin/system" replace /> },
      { path: 'admin/users', element: <UsersPage />, errorElement: <RouteErrorFallback /> },
      { path: 'admin/models', element: <AdminModelsPage />, errorElement: <RouteErrorFallback /> },
      { path: 'admin/system', element: <AdminSystemPage />, errorElement: <RouteErrorFallback /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
])

export default router
