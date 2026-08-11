#!/usr/bin/env node

/*!
 * rtail-server.js
 * Created by Kilian Ciuffolo on Oct 26, 2014
 */

import dgram from 'node:dgram'
import { createServer } from 'node:http'
import createDebug from 'debug'
import express from 'express'
import { Server } from 'socket.io'
import updateNotifier from 'update-notifier'
import yargs from 'yargs'
import { hideBin } from 'yargs/helpers'
import { pkg } from './lib/pkg.js'
import { webapp } from './lib/webapp.js'

const debug = createDebug('rtail:server')

/*!
 * inform the user of updates
 */
updateNotifier({ pkg }).notify()

/*!
 * parsing argv
 */
const argv = yargs(hideBin(process.argv))
  .usage('Usage: rtail-server [OPTIONS]')
  .example('rtail-server --web-port 8080', 'Use custom HTTP port')
  .example('rtail-server --udp-port 8080', 'Use custom UDP port')
  .example('rtail-server --web-version stable', 'Always uses latest stable webapp')
  .example('rtail-server --web-version unstable', 'Always uses latest develop webapp')
  .example('rtail-server --web-version 0.1.3', 'Use webapp v0.1.3')
  .option('udp-host', {
    alias: 'uh',
    type: 'string',
    default: '127.0.0.1',
    describe: 'The listening UDP hostname'
  })
  .option('udp-port', {
    alias: 'up',
    type: 'number',
    default: 9999,
    describe: 'The listening UDP port'
  })
  .option('web-host', {
    alias: 'wh',
    type: 'string',
    default: '127.0.0.1',
    describe: 'The listening HTTP hostname'
  })
  .option('web-port', {
    alias: 'wp',
    type: 'number',
    default: 8888,
    describe: 'The listening HTTP port'
  })
  .option('web-version', {
    type: 'string',
    describe: 'Define web app version to serve'
  })
  .option('backlog', {
    alias: 'b',
    type: 'number',
    default: 100,
    describe: 'Lines of history kept in memory, per stream'
  })
  .check((args) => {
    if (!Number.isInteger(args.backlog) || args.backlog < 1) {
      throw new Error('--backlog must be a positive integer')
    }
    return true
  })
  // Every option is also settable as RTAIL_*, e.g. RTAIL_WEB_HOST. The
  // container image sets the listen hosts this way so that `docker run rtail`
  // with extra flags appends to the command instead of replacing it.
  .env('RTAIL')
  .help('help')
  .alias('help', 'h')
  .version(pkg.version)
  .alias('version', 'v')
  .strict()
  .parseSync()

const BACKLOG_SIZE = argv.backlog

const app = express()
const http = createServer(app)

/*!
 * serve the webapp
 *
 * In development the bundled app is served straight from disk; otherwise it
 * comes from the prebuilt dist/, or is proxied from the published S3 build.
 */
const isDev = 'development' === argv.webVersion

if (!argv.webVersion) {
  app.use(express.static(new URL('../dist', import.meta.url).pathname))
} else if (isDev) {
  // In development the app is served from /app, but a bare / is what everyone
  // actually types — so send them there instead of a 404.
  app.get('/', (_req, res) => res.redirect(302, '/app/'))

  // No caching in development: the watcher rewrites bundle.js and main.css in
  // place, and a cached stylesheet silently hides the change you just made.
  app.use('/app', express.static(new URL('../app', import.meta.url).pathname, {
    etag: false,
    lastModified: false,
    setHeaders: (res) => res.setHeader('Cache-Control', 'no-store')
  }))
} else {
  app.use(webapp({
    origin: 'http://rtail.s3-website-us-east-1.amazonaws.com/' + argv.webVersion,
    ttl: 1000 * 60 * 60 * 6 // 6H
  }))

  debug('serving webapp from: http://rtail.s3-website-us-east-1.amazonaws.com/%s', argv.webVersion)
}

const io = new Server(http, {
  serveClient: false,
  path: isDev ? '/app/socket.io' : '/socket.io'
})

/*!
 * UDP socket setup
 */
const streams = new Map()
const udp = dgram.createSocket('udp4')

udp.on('message', (data, remote) => {
  let payload

  try {
    payload = JSON.parse(data)
  } catch {
    return debug('invalid data sent')
  }

  if (!streams.has(payload.id)) {
    streams.set(payload.id, [])
    io.emit('streams', [...streams.keys()])
  }

  const message = {
    timestamp: payload.timestamp,
    streamid: payload.id,
    host: remote.address,
    port: remote.port,
    content: payload.content,
    type: typeof payload.content
  }

  const backlog = streams.get(payload.id)
  if (backlog.length >= BACKLOG_SIZE) backlog.shift()
  backlog.push(message)

  debug('%j', message)
  io.to(payload.id).emit('line', message)
})

/*!
 * socket.io
 */
io.on('connection', (socket) => {
  socket.emit('streams', [...streams.keys()])

  socket.on('select stream', (stream) => {
    // Leave whatever stream this socket was watching. Every socket is also a
    // member of a room named after its own id — that one has to stay, or the
    // socket stops receiving anything addressed directly to it.
    for (const room of socket.rooms) {
      if (room !== socket.id) socket.leave(room)
    }

    if (!stream) return

    socket.join(stream)
    socket.emit('backlog', streams.get(stream) ?? [])
  })
})

/*!
 * listen!
 */
udp.bind(argv.udpPort, argv.udpHost)
http.listen(argv.webPort, argv.webHost)

debug('UDP  server listening: %s:%s', argv.udpHost, argv.udpPort)
debug('HTTP server listening: http://%s:%s', argv.webHost, argv.webPort)
