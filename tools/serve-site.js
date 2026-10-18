/*!
 * serve-site.js — serve site/dist for a local look.
 *
 *   node tools/serve-site.js            http://localhost:8080
 *   node tools/serve-site.js --port 9000
 *
 * Node's own http module and nothing else: the toolchain image installs its
 * CLIs at build time and does not fetch anything at run time, so reaching for
 * a one-line static server from npm would mean a network round trip in the
 * middle of `make site-serve`.
 *
 * It mimics the one Vercel behaviour the page depends on — cleanUrls, so
 * /docs serves docs.html — and nothing else. This is a preview, not a local
 * reimplementation of the host.
 */

import { createReadStream } from 'node:fs'
import { stat } from 'node:fs/promises'
import { createServer } from 'node:http'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('../site/dist/', import.meta.url))

const args = process.argv.slice(2)
const portArg = args.indexOf('--port')
const port = Number(-1 === portArg ? process.env.PORT || 8080 : args[portArg + 1])

const TYPES = {
  '.css': 'text/css; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.png': 'image/png',
  '.svg': 'image/svg+xml',
  '.txt': 'text/plain; charset=utf-8'
}

/**
 * The candidates for one request path, in the order Vercel would try them.
 */
function candidates(pathname) {
  const clean = pathname.replace(/\/+$/, '')

  if ('' === clean) {
    return ['index.html']
  }

  const rel = clean.slice(1)

  return [rel, `${rel}.html`, `${rel}/index.html`]
}

async function resolve(pathname) {
  for (const candidate of candidates(pathname)) {
    // A path that climbs out of the output directory is not a candidate, it
    // is an attempt: resolve it against root and check where it landed.
    const file = new URL(candidate, `file://${root}`)

    if (!fileURLToPath(file).startsWith(root)) {
      continue
    }

    try {
      const info = await stat(file)

      if (info.isFile()) {
        return fileURLToPath(file)
      }
    } catch {
      continue
    }
  }

  return null
}

const server = createServer(async (req, res) => {
  const { pathname } = new URL(req.url, 'http://localhost')
  const file = await resolve(decodeURIComponent(pathname))

  if (!file) {
    res.writeHead(404, { 'content-type': TYPES['.txt'] })
    res.end('not found\n')
    return
  }

  const ext = file.slice(file.lastIndexOf('.'))

  res.writeHead(200, {
    'content-type': TYPES[ext] ?? 'application/octet-stream',
    'cache-control': 'no-store'
  })

  createReadStream(file).pipe(res)
})

server.listen(port, '0.0.0.0', () => {
  console.log(`serving site/dist on http://localhost:${port}/`)
})
