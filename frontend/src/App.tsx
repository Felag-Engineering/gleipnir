import { RouterProvider } from 'react-router-dom'
import router from './routes'
import QueryProvider from './api/QueryProvider'
import { ErrorBoundary } from './components/ErrorBoundary'
import { ToastProvider } from './components/Toast'

export default function App() {
  return (
    <ErrorBoundary>
      <QueryProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </QueryProvider>
    </ErrorBoundary>
  )
}
