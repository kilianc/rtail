/*!
 * server.ts — the rtail server, minus the command line.
 *
 * Kept apart from rtail-server.ts so the test suite can start one in-process,
 * on an ephemeral port, and shut it down deterministically.
 */

import dgram from 'node:dgram'
import { createServer, type Server as HttpServer } from 'node:http'
import type { AddressInfo } from 'node:net'
import { fileURLToPath } from 'node:url'
import createDebug from 'debug'
import express, { type Express } from 'express'
import { Server as IOServer } from 'socket.io'
import { createStreamStore, decodePayload, type StreamStore } from './streams.ts'
import { webapp } from './webapp.ts'

const debug = createDebug('rtail:server')

// Resolved from this file, which lives one level deeper than the bin scripts —
// `fileURLToPath` rather than `.pathname`, which mangles spaces in a checkout
// path and is outright wrong on Windows.
const ROOT = new URL('../../', import.meta.url)
const DIST_DIR = fileURLToPath(new URL('dist/', ROOT))
const APP_DIR = fileURLToPath(new URL('app/', ROOT))

export const S3_ORIGIN = 'http://rtail.s3-website-us-east-1.amazonaws.com/'
const WEBAPP_TTL = 1000 * 60 * 60 * 6 // 6H

export interface ServerOptions {
  udpHost: string
  udpPort: number
  webHost: string
  webPort: number
  /**
   * Which webapp to serve: unset = the bundled dist/, 'development' = the
   * unminified build in app/, anything else = that version, proxied from S3.
   */
  webVersion?: string | undefined
  /** Lines of history kept in memory, per stream. */
  backlog: number
}

export interface RtailServer {
  app: Express
  http: HttpServer
  io: IOServer
  udp: dgram.Socket
  streams: StreamStore
  /** Resolves once both the HTTP and UDP sockets are bound. */
  listening: Promise<void>
  /** The bound HTTP port — meaningful when 0 was requested. */
  address(): AddressInfo
  close(): Promise<void>
}

/**
 * Mounts whichever build of the webapp the options ask for.
 *
 * Exported for its own sake: the three branches are the only real logic here,
 * and mounting them on a bare express app is far cheaper to assert against
 * than booting the whole server three times.
 */
export function mountWebapp(
  app: Express,
  webVersion?: string,
  deps: { fetch?: typeof globalThis.fetch } = {}
): void {
  if (!webVersion) {
    app.use(express.static(DIST_DIR))
    return
  }

  if ('development' === webVersion) {
    // In development the app is served from /app, but a bare / is what
    // everyone actually types — so send them there instead of a 404.
    app.get('/', (_req, res) => res.redirect(302, '/app/'))

    // No caching in development: the watcher rewrites bundle.js and main.css
    // in place, and a cached stylesheet silently hides the change you just
    // made.
    app.use('/app', express.static(APP_DIR, {
      etag: false,
      lastModified: false,
      setHeaders: (res) => res.setHeader('Cache-Control', 'no-store')
    }))
    return
  }

  app.use(webapp({ origin: S3_ORIGIN + webVersion, ttl: WEBAPP_TTL, fetch: deps.fetch }))
  debug('serving webapp from: %s%s', S3_ORIGIN, webVersion)
}

export function createRtailServer(opts: ServerOptions): RtailServer {
  const isDev = 'development' === opts.webVersion

  const app = express()
  const http = createServer(app)

  mountWebapp(app, opts.webVersion)

  const io = new IOServer(http, {
    serveClient: false,
    path: isDev ? '/app/socket.io' : '/socket.io'
  })

  const streams = createStreamStore(opts.backlog)
  const udp = dgram.createSocket('udp4')

  udp.on('message', (data, remote) => {
    const payload = decodePayload(data)

    if (null === payload) return debug('invalid data sent')

    const { message, isNew } = streams.push(payload, remote)

    if (isNew) io.emit('streams', streams.names())

    debug('%j', message)
    io.to(payload.id).emit('line', message)
  })

  io.on('connection', (socket) => {
    socket.emit('streams', streams.names())

    socket.on('select stream', (stream: string | null) => {
      // Leave whatever stream this socket was watching. Every socket is also a
      // member of a room named after its own id — that one has to stay, or the
      // socket stops receiving anything addressed directly to it.
      for (const room of socket.rooms) {
        if (room !== socket.id) socket.leave(room)
      }

      if (!stream) return

      socket.join(stream)
      socket.emit('backlog', streams.backlog(stream))
    })
  })

  const listening = Promise.all([
    new Promise<void>((resolve, reject) => {
      udp.once('error', reject)
      udp.bind(opts.udpPort, opts.udpHost, resolve)
    }),
    new Promise<void>((resolve, reject) => {
      http.once('error', reject)
      http.listen(opts.webPort, opts.webHost, resolve)
    })
  ]).then(() => {
    debug('UDP  server listening: %s:%s', opts.udpHost, opts.udpPort)
    debug('HTTP server listening: http://%s:%s', opts.webHost, opts.webPort)
  })

  return {
    app,
    http,
    io,
    udp,
    streams,
    listening,
    address: () => http.address() as AddressInfo,
    async close() {
      // Shutdown has to be total, not merely started: a single keep-alive
      // connection or half-closed upgrade left behind holds the event loop
      // open, which shows up as a process that runs its work and then never
      // exits. Hence disconnect, then close, then sweep whatever survived.
      io.disconnectSockets(true)
      await new Promise<void>((resolve) => io.close(() => resolve()))

      // io.close() closes the http server it was attached to, but connections
      // already established on it are not its to drop.
      http.closeAllConnections()
      if (http.listening) await new Promise<void>((resolve) => http.close(() => resolve()))

      await new Promise<void>((resolve) => udp.close(resolve))
    }
  }
}
