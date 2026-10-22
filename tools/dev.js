/*!
 * dev.js — everything you need to look at the app locally.
 *
 * Builds and watches the webapp, runs the Go rtail-server against those files
 * on disk, and feeds it a few demo streams so there is something to render.
 *
 * Both binaries are built by the Makefile before this runs; asset changes
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

// The client is a Go binary too, built by the Makefile alongside the server.
// It used to be cli/rtail-client.js, which is why the demo streams below ran
// under `node`.
const CLIENT = process.env.RTAIL_CLIENT_BIN || path('bin/rtail')

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

/*!
 * 0.0.0.0 so the port is reachable from the host through Docker's NAT, and
 * --insecure because the server refuses that without a token.
 *
 * The refusal is right on a host and the wrong question in here: a container's
 * loopback is its own, so binding 127.0.0.1 would make the published port
 * unreachable and 0.0.0.0 means "reachable through whatever -p published",
 * not "public". The runtime image answers it the same way with
 * RTAIL_INSECURE=true; this is the development half of that.
 */
const server = run(SERVER, [
  '--web-root', path('app'),
  '--web-host', '0.0.0.0',
  '--web-port', PORT,
  '--udp-host', '0.0.0.0',
  '--udp-port', UDP_PORT,
  /*!
   * Durable storage, in development, by default.
   *
   * Without --data the server is memory-only: no catalog, so the field
   * explorer is empty, the histogram is flat and every search answers "search
   * needs durable storage". That is three of the four things on the screen
   * switched off in the command you run to look at the product — and it reads
   * as a broken UI rather than as a mode. `make clean` removes the directory.
   */
  '--data', path('.dev-data'),
  '--insecure'
], { stdio: 'inherit' })

server.on('error', (err) => {
  if ('ENOENT' === err.code) {
    console.error(`\n  ${SERVER} is missing. Build it first:\n\n    make server\n`)
    shutdown()
  }
})

/*!
 * If the server dies, stop — do not go on to print "rTail is up".
 *
 * It exited immediately when --insecure was missing and this script announced
 * a URL anyway, so the first symptom of a broken server was a browser that
 * would not connect to something the terminal said was running. A dev script
 * that lies about the state of the thing it started is worse than one that
 * crashes.
 */
server.on('exit', (code) => {
  if (0 !== code && null !== code) {
    console.error(`\n  rtail-server exited with code ${code}; stopping.\n`)
    shutdown()
  }
})

// Give the UDP listener a moment before the producers start talking to it.
setTimeout(() => {
  console.log('==> starting demo streams')

  for (const name of DEMO_STREAMS) {
    const producer = run('node', [path('tools/demo-logs.js'), name], { stdio: ['ignore', 'pipe', 'inherit'] })
    const client = run(CLIENT, [
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
