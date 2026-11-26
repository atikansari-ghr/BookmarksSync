import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// The built UI is embedded into the Go server (internal/webui) and served under
// /auth/, the path family reserved for the sign-in and security pages.
export default defineConfig({
  base: '/auth/',
  plugins: [react()],
  build: {
    outDir: '../internal/webui/static',
    emptyOutDir: true,
    assetsDir: 'assets',
    sourcemap: false,
    // No inline scripts or styles: the server sends a strict Content-Security-Policy.
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    restoreMocks: true,
    unstubGlobals: true,
  },
})
