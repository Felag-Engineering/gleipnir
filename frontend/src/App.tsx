import { RouterProvider } from 'react-router-dom'
import router from './routes'
import QueryProvider from './api/QueryProvider'
import { ErrorBoundary } from './components/ErrorBoundary'

export default function App() {
  return (
    <ErrorBoundary>
      <QueryProvider>
        <RouterProvider router={router} />
      </QueryProvider>
    </ErrorBoundary>
  )
}
