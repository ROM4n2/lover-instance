import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The build is written straight into the Go server's embed/lookup directory so
// `npm run build` is the only step between source and a servable dashboard.
// That directory (and frontend/dist) is gitignored — design decision D3 keeps
// build artifacts out of the repository, so the two never drift.
//
// base: '/' because the Go static handler serves the bundle from the site root
// and its hashed asset names start with /assets/. The dev proxy forwards /api
// to the running Go server, so `npm run dev` talks to the real backend instead
// of a mock.
export default defineConfig({
  plugins: [vue()],
  base: '/',
  build: {
    outDir: '../backend-go/internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8787',
        changeOrigin: false,
      },
    },
  },
})
