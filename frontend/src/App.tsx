import { RouterProvider } from 'react-router-dom'
import router from './routes'
import QueryProvider from './api/QueryProvider'

export default function App() {
  return (
    <QueryProvider>
      <RouterProvider router={router} />
    </QueryProvider>
  )
}
