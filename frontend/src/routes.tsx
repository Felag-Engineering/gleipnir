import { createBrowserRouter, Navigate } from 'react-router-dom'
import Layout from './components/Layout/Layout'
import DashboardPage from './pages/DashboardPage'
import PoliciesPage from './pages/PoliciesPage'
import PolicyEditorPage from './pages/PolicyEditorPage'
import PolicyRunsPage from './pages/PolicyRunsPage'
import RunDetailPage from './pages/RunDetailPage'
import RunsPage from './pages/RunsPage'
import MCPPage from './pages/MCPPage'
import { RouteErrorFallback } from './components/ErrorBoundary'

const router = createBrowserRouter([
  {
    path: '/',
    element: <Layout />,
    children: [
      { index: true, element: <Navigate to="/dashboard" replace /> },
      { path: 'dashboard', element: <DashboardPage />, errorElement: <RouteErrorFallback /> },
      { path: 'runs', element: <RunsPage />, errorElement: <RouteErrorFallback /> },
      { path: 'policies', element: <PoliciesPage />, errorElement: <RouteErrorFallback /> },
      { path: 'policies/new', element: <PolicyEditorPage />, errorElement: <RouteErrorFallback /> },
      { path: 'policies/:id/runs', element: <PolicyRunsPage />, errorElement: <RouteErrorFallback /> },
      { path: 'policies/:id', element: <PolicyEditorPage />, errorElement: <RouteErrorFallback /> },
      { path: 'runs/:id', element: <RunDetailPage />, errorElement: <RouteErrorFallback /> },
      { path: 'tools', element: <MCPPage />, errorElement: <RouteErrorFallback /> },
      { path: 'mcp', element: <Navigate to="/tools" replace /> },
    ],
  },
])

export default router
