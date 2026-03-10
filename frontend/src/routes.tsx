import { createBrowserRouter, Navigate } from 'react-router-dom'
import Layout from './components/Layout/Layout'
import DashboardPage from './pages/DashboardPage'
import PolicyEditorPage from './pages/PolicyEditorPage'
import RunDetailPage from './pages/RunDetailPage'
import MCPPage from './pages/MCPPage'

const router = createBrowserRouter([
  {
    path: '/',
    element: <Layout />,
    children: [
      { index: true, element: <Navigate to="/dashboard" replace /> },
      { path: 'dashboard', element: <DashboardPage /> },
      { path: 'policies/new', element: <PolicyEditorPage /> },
      { path: 'policies/:id', element: <PolicyEditorPage /> },
      { path: 'runs/:id', element: <RunDetailPage /> },
      { path: 'mcp', element: <MCPPage /> },
    ],
  },
])

export default router
