/*!
 * dev.js — everything you need to look at the app locally.
 *
 * Builds and watches the webapp, runs the Go rtail-server against those files
 * on disk, and feeds it a few demo streams so there is something to render.
 *
 * The server binary is built by the Makefile before this runs; asset changes
 * are picked up live because --web-root serves app/ from disk rather than the
 * copy embedded in the binary. Go changes need a restart.
 */

import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const root = new URL('../', import.meta.url)
const path = (rel) => fileURLToPath(new URL(rel, root))

const PORT = process.env.WEB_PORT || '8888'

// The UDP listener needs its own per-worktree port too: two checkouts both
// binding 9999 would fight, and whichever lost would silently receive no logs.
const UDP_PORT = process.env.UDP_PORT || '9999'

const SERVER = process.env.RTAIL_SERVER_BIN || path('bin/rtail-server')

const DEMO_STREAMS = ['api-gateway', 'worker-billing', 'nginx-access']

const children = []

function run(command, args, options = {}) {
  const child = spawn(command, args, { cwd: path('.'), ...options })
  children.push(child)
  return child
}

function shutdown() {
  for (const child of children) child.kill('SIGTERM')
  process.exit(0)
}

process.on('SIGINT', shutdown)
process.on('SIGTERM', shutdown)

console.log('==> building and watching assets')
run('node', [path('tools/build.js'), '--watch'], { stdio: 'inherit' })

console.log(`==> starting rtail-server on 0.0.0.0:${PORT} (udp ${UDP_PORT})`)

// 0.0.0.0 so the port is reachable from the host through Docker's NAT.
const server = run(SERVER, [
  '--web-root', path('app'),
  '--web-host', '0.0.0.0',
  '--web-port', PORT,
  '--udp-host', '0.0.0.0',
  '--udp-port', UDP_PORT
], { stdio: 'inherit' })

server.on('error', (err) => {
  if ('ENOENT' === err.code) {
    console.error(`\n  ${SERVER} is missing. Build it first:\n\n    make server\n`)
    shutdown()
  }
})

// Give the UDP listener a moment before the producers start talking to it.
setTimeout(() => {
  console.log('==> starting demo streams')

  for (const name of DEMO_STREAMS) {
    const producer = run('node', [path('tools/demo-logs.js'), name], { stdio: ['ignore', 'pipe', 'inherit'] })
    const client = run('node', [
      path('cli/rtail-client.js'),
      '--id', name,
      '--port', UDP_PORT,
      '--mute'
    ], { stdio: ['pipe', 'inherit', 'inherit'] })

    producer.stdout.pipe(client.stdin)
  }

  console.log('')
  console.log(`  rTail is up:  http://localhost:${PORT}/`)
  console.log('  Ctrl-C to stop.')
  console.log('')
}, 1000)
