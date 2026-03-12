import { createBrowserRouter, Navigate } from 'react-router-dom'
import Layout from './components/Layout/Layout'
import DashboardPage from './pages/DashboardPage'
import PolicyEditorPage from './pages/PolicyEditorPage'
import PolicyRunsPage from './pages/PolicyRunsPage'
import RunDetailPage from './pages/RunDetailPage'
import MCPPage from './pages/MCPPage'
import { RouteErrorFallback } from './components/ErrorBoundary'

const router = createBrowserRouter([
  {
    path: '/',
    element: <Layout />,
    children: [
      { index: true, element: <Navigate to="/dashboard" replace /> },
      { path: 'dashboard', element: <DashboardPage />, errorElement: <RouteErrorFallback /> },
      { path: 'policies/new', element: <PolicyEditorPage />, errorElement: <RouteErrorFallback /> },
      { path: 'policies/:id/runs', element: <PolicyRunsPage />, errorElement: <RouteErrorFallback /> },
      { path: 'policies/:id', element: <PolicyEditorPage />, errorElement: <RouteErrorFallback /> },
      { path: 'runs/:id', element: <RunDetailPage />, errorElement: <RouteErrorFallback /> },
      { path: 'mcp', element: <MCPPage />, errorElement: <RouteErrorFallback /> },
    ],
  },
])

export default router
