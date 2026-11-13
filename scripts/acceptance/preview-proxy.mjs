// Read-only preview of the BUILT console (internal/web/static) against a LIVE
// control plane, used by scripts/acceptance/messages-ui.cjs for its live shot.
//
// Why not `pnpm dev`: Vite's dev server pre-bundles dependencies with esbuild,
// and this locked-down Windows host denies esbuild's scratch writes (the same
// reason web/vite.config.ts disables esbuild for `build`). The production
// bundle is exactly what `tw` embeds, so serving it here and proxying the API
// shows the shipped UI without restarting the running console.
//
//   node scripts/acceptance/preview-proxy.mjs
//   TW_PREVIEW_URL=http://127.0.0.1:5173 node scripts/acceptance/messages-ui.cjs
//
// It is a proxy, not a console: it never writes data/, never starts a model,
// and only forwards what the browser asks for. Point TW_CONSOLE_URL somewhere
// else if the console is not on the default loopback port.
import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { dirname, extname, join, normalize, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const STATIC = join(ROOT, 'internal/web/static')
const UPSTREAM = (process.env.TW_CONSOLE_URL || 'http://127.0.0.1:8080').replace(/\/+$/, '')
const PORT = Number(process.env.TW_PREVIEW_PORT || 5173)

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.map': 'application/json',
}

// Only the console's own endpoints are proxied; everything else is the SPA.
const isAPI = (p) =>
  p.startsWith('/api/') ||
  /^\/p\/[^/]+\/api(\/|$)/.test(p) ||
  p === '/login' ||
  p === '/logout' ||
  p === '/healthz'

async function readBody(req) {
  const chunks = []
  for await (const chunk of req) chunks.push(chunk)
  return chunks.length ? Buffer.concat(chunks) : undefined
}

const server = createServer(async (req, res) => {
  try {
    const path = new URL(req.url, 'http://127.0.0.1').pathname
    if (path.startsWith('/static/')) {
      const rel = normalize(path.slice('/static/'.length))
      const file = join(STATIC, rel)
      if (!file.startsWith(STATIC + sep)) {
        res.writeHead(403).end('forbidden')
        return
      }
      const body = await readFile(file)
      res.writeHead(200, {
        'Content-Type': MIME[extname(file)] ?? 'application/octet-stream',
        'Cache-Control': 'no-store',
      })
      res.end(body)
      return
    }
    if (isAPI(path)) {
      const upstream = await fetch(UPSTREAM + req.url, {
        method: req.method,
        headers: { ...req.headers, host: new URL(UPSTREAM).host },
        body: req.method === 'GET' || req.method === 'HEAD' ? undefined : await readBody(req),
        redirect: 'manual',
      })
      const headers = {}
      upstream.headers.forEach((value, key) => {
        if (!['content-encoding', 'transfer-encoding', 'content-length', 'connection'].includes(key)) headers[key] = value
      })
      const body = Buffer.from(await upstream.arrayBuffer())
      res.writeHead(upstream.status, headers)
      res.end(body)
      return
    }
    // Any other path is the SPA shell; it reads the project from the URL.
    const shell = await readFile(join(STATIC, 'index.html'))
    res.writeHead(200, { 'Content-Type': MIME['.html'], 'Cache-Control': 'no-store' })
    res.end(shell)
  } catch (err) {
    res.writeHead(502, { 'Content-Type': 'text/plain; charset=utf-8' })
    res.end(`preview proxy error: ${err instanceof Error ? err.message : String(err)}`)
  }
})

server.listen(PORT, '127.0.0.1', () => {
  console.log(`preview proxy: http://127.0.0.1:${PORT}/p/<project>/?view=messages  (static: internal/web/static, API -> ${UPSTREAM})`)
})
