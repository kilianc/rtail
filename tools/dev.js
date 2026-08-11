/*!
 * dev.js — everything you need to look at the app locally.
 *
 * Builds and watches the webapp, runs the real rtail-server, and feeds it a
 * few demo streams so there is something to render.
 */

import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const root = new URL('../', import.meta.url)
const path = (rel) => fileURLToPath(new URL(rel, root))

const PORT = process.env.WEB_PORT || '8888'
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

console.log(`==> starting rtail-server on 0.0.0.0:${PORT}`)
// 0.0.0.0 so the port is reachable from the host through Docker's NAT.
run('node', [
  path('cli/rtail-server.js'),
  '--web-version', 'development',
  '--web-host', '0.0.0.0',
  '--web-port', PORT
], { stdio: 'inherit' })

// Give the UDP listener a moment before the producers start talking to it.
setTimeout(() => {
  console.log('==> starting demo streams')

  for (const name of DEMO_STREAMS) {
    const producer = run('node', [path('tools/demo-logs.js'), name], { stdio: ['ignore', 'pipe', 'inherit'] })
    const client = run('node', [path('cli/rtail-client.js'), '--id', name, '--mute'], {
      stdio: ['pipe', 'inherit', 'inherit']
    })

    producer.stdout.pipe(client.stdin)
  }

  console.log('')
  console.log(`  rTail is up:  http://localhost:${PORT}/app`)
  console.log('  Ctrl-C to stop.')
  console.log('')
}, 1000)
