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
    // A candidate that parses as a URL of its own — `/http:x` — is not
    // resolved against root at all, and fileURLToPath throws on anything that
    // is not file:, so the scheme is checked before the path is.
    const file = new URL(candidate, `file://${root}`)

    if ('file:' !== file.protocol || !fileURLToPath(file).startsWith(root)) {
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
  let file

  /*!
   * A path that cannot be read is a bad request, not a crash.
   *
   * The handler is async, so anything thrown in it is an unhandled rejection,
   * and that takes the whole process down: one `/%` — a malformed escape,
   * which decodeURIComponent throws on — ended the preview for every tab.
   */
  try {
    const { pathname } = new URL(req.url, 'http://localhost')
    file = await resolve(decodeURIComponent(pathname))
  } catch {
    res.writeHead(400, { 'content-type': TYPES['.txt'] })
    res.end('bad request\n')
    return
  }

  if (!file) {
    res.writeHead(404, { 'content-type': TYPES['.txt'] })
    res.end('not found\n')
    return
  }

  const ext = file.slice(file.lastIndexOf('.'))

  /*!
   * A file that cannot be read after all is a 500, not a crash.
   *
   * It can vanish or turn unreadable between the stat and the open — a
   * rebuild clearing site/dist under an open tab, a permission change — and a
   * stream's 'error' with nobody listening is thrown, which took the process
   * down like the malformed escape above used to. So the 200 waits for the
   * file to actually open, an error before then gets an answer, and one after
   * it cuts the response short. A client that leaves early releases the file.
   */
  const stream = createReadStream(file)

  stream.on('open', () => {
    res.writeHead(200, {
      'content-type': TYPES[ext] ?? 'application/octet-stream',
      'cache-control': 'no-store'
    })

    stream.pipe(res)
  })

  stream.on('error', () => {
    if (res.headersSent) {
      res.destroy()
      return
    }

    res.writeHead(500, { 'content-type': TYPES['.txt'] })
    res.end('could not read file\n')
  })

  res.on('close', () => stream.destroy())
})

server.listen(port, '0.0.0.0', () => {
  console.log(`serving site/dist on http://localhost:${port}/`)
})
