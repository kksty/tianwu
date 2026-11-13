import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react-swc'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

// The bundle lands inside the Go package so `go build` embeds it unchanged
// (internal/web/static). Base is absolute because the console is served under
// /p/<project>/ while its assets live at /static/.
//
// cyber-ui is a submodule consumed as source (docs/12): no build of its own,
// no published package, just tsconfig paths + these aliases.
//
// Build toolchain note: esbuild is switched off on purpose (SWC for TS/JSX,
// terser for minification, lightningcss for CSS). esbuild shells out to a native
// binary that writes its input to a temp file and deletes it; on locked-down
// Windows hosts that delete is denied and the build dies on its own scratch
// file. SWC and lightningcss are in-process addons, so nothing to delete.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  esbuild: false,
  base: '/static/',
  resolve: {
    alias: [
      {
        find: '@cyber/ui',
        replacement: fileURLToPath(new URL('../cyber-ui/packages/ui/src', import.meta.url)),
      },
      {
        find: '@cyber/theme',
        replacement: fileURLToPath(new URL('../cyber-ui/packages/theme/src', import.meta.url)),
      },
      { find: '@', replacement: fileURLToPath(new URL('./src', import.meta.url)) },
    ],
    dedupe: ['react', 'react-dom', 'react/jsx-runtime', 'react/jsx-dev-runtime'],
  },
  build: {
    outDir: '../internal/web/static',
    emptyOutDir: true,
    assetsDir: 'assets',
    sourcemap: false,
    // A local Go process serves this page, never an old browser.
    target: 'esnext',
    minify: 'terser',
    cssMinify: 'lightningcss',
    terserOptions: {
      compress: { drop_console: false, drop_debugger: true },
    },
    chunkSizeWarningLimit: 900,
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    // `go run ./cmd/tw` owns the API; the dev server owns the page. Only the
    // per-project API calls are forwarded, so /p/<id>/ still renders here.
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '^/p/[^/]+/api': 'http://127.0.0.1:8080',
    },
  },
})
