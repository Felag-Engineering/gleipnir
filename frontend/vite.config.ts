import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    // Proxy /api to the Go backend during local development.
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
})
