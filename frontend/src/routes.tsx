import { lazy, Suspense } from 'react'
import { createBrowserRouter, Navigate, useParams } from 'react-router'
import Layout from '@/components/Layout'
import { RouteErrorFallback } from './components/ErrorBoundary'
import { PageFallback } from '@/components/PageFallback'

// Every page is its own chunk so the entry bundle carries only the shell.
const DashboardPage = lazy(() => import('./pages/DashboardPage'))
const LoginPage = lazy(() => import('./pages/LoginPage'))
const SetupPage = lazy(() => import('./pages/SetupPage'))
const AgentsPage = lazy(() => import('./pages/AgentsPage'))
const AgentEditorPage = lazy(() => import('./pages/AgentEditorPage'))
const RunDetailPage = lazy(() => import('./pages/RunDetailPage'))
const RunsPage = lazy(() => import('./pages/RunsPage'))
const MCPPage = lazy(() => import('./pages/MCPPage'))
const UsersPage = lazy(() => import('./pages/UsersPage'))
const SettingsPage = lazy(() => import('./pages/SettingsPage'))
const AdminModelsPage = lazy(() => import('./pages/AdminModelsPage'))
const AdminSystemPage = lazy(() => import('./pages/AdminSystemPage'))
const AdminAudiencesPage = lazy(() => import('./pages/AdminAudiencesPage'))
const AdminAudienceDetailPage = lazy(() => import('./pages/AdminAudienceDetailPage'))
const AdminAudienceNewPage = lazy(() => import('./pages/AdminAudienceNewPage'))
const AdminPluginInstancePage = lazy(() => import('./pages/AdminPluginInstancePage'))
const AdminPluginsPage = lazy(() => import('./pages/AdminPluginsPage'))
const PluginReviewPage = lazy(() => import('./pages/PluginReviewPage'))
const NotFoundPage = lazy(() => import('./pages/NotFoundPage'))

function PolicyRunsRedirect() {
  const { id } = useParams<{ id: string }>()
  return <Navigate to={`/runs?policy=${id}`} replace />
}

const router = createBrowserRouter([
  {
    path: '/login',
    element: (
      <Suspense fallback={<PageFallback />}>
        <LoginPage />
      </Suspense>
    ),
  },
  {
    path: '/setup',
    element: (
      <Suspense fallback={<PageFallback />}>
        <SetupPage />
      </Suspense>
    ),
  },
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
      { path: 'admin/audiences', element: <AdminAudiencesPage />, errorElement: <RouteErrorFallback /> },
      { path: 'admin/audiences/new', element: <AdminAudienceNewPage />, errorElement: <RouteErrorFallback /> },
      { path: 'admin/audiences/:id', element: <AdminAudienceDetailPage />, errorElement: <RouteErrorFallback /> },
      { path: 'admin/plugins', element: <AdminPluginsPage />, errorElement: <RouteErrorFallback /> },
      { path: 'admin/plugins/:id/review', element: <PluginReviewPage />, errorElement: <RouteErrorFallback /> },
      {
        path: 'admin/plugins/:id/instances/:iid',
        element: <AdminPluginInstancePage />,
        errorElement: <RouteErrorFallback />,
      },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
])

export default router
