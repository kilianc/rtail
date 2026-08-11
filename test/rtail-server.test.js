import assert from 'node:assert/strict'
import dgram from 'node:dgram'
import { after, before, describe, it } from 'node:test'
import { io } from 'socket.io-client'
import { delay, runClient, startServer, waitFor, waitForHttp } from './util.js'

const UDP_PORT = 9981
const WEB_PORT = 8881

/** Feeds `lines` into a named stream through the real client. */
function feed(stream, lines) {
  return runClient(['--port', String(UDP_PORT), '--name', stream, '--mute'], lines.join('\n') + '\n')
}

/** Connects a websocket client and records everything the server pushes. */
function observe(port = WEB_PORT) {
  const socket = io(`http://127.0.0.1:${port}`, { path: '/socket.io' })
  const streams = []
  const lines = []
  const backlogs = []

  socket.on('streams', (data) => streams.push(data))
  socket.on('line', (data) => lines.push(data))
  socket.on('backlog', (data) => backlogs.push(data))

  return { socket, streams, lines, backlogs }
}

describe('rtail-server', () => {
  let server

  before(async () => {
    server = await startServer([
      '--udp-port', String(UDP_PORT),
      '--web-port', String(WEB_PORT)
    ], { webPort: WEB_PORT })
  })

  after(async () => {
    await server.stop()
  })

  it('announces the streams list on connect', async () => {
    await feed('alpha', ['1', '2'])
    await feed('beta', ['1'])

    const ws = observe()

    try {
      await waitFor(() => ws.streams.some((s) => s.includes('alpha') && s.includes('beta')), 'both streams')
    } finally {
      ws.socket.close()
    }
  })

  it('replays the backlog when a stream is selected', async () => {
    const ws = observe()

    try {
      await waitFor(() => ws.streams.length > 0, 'the streams list')
      ws.socket.emit('select stream', 'alpha')

      await waitFor(() => ws.backlogs.length > 0, 'a backlog')
      assert.ok(ws.backlogs[0].length >= 2)
      assert.ok(ws.backlogs[0].every((line) => 'alpha' === line.streamid))
    } finally {
      ws.socket.close()
    }
  })

  it('delivers live lines for the selected stream', async () => {
    const ws = observe()

    try {
      await waitFor(() => ws.streams.length > 0, 'the streams list')
      ws.socket.emit('select stream', 'alpha')
      await waitFor(() => ws.backlogs.length > 0, 'a backlog')

      await feed('alpha', ['live-one', 'live-two'])
      await waitFor(() => ws.lines.length >= 2, 'two live lines')

      assert.deepEqual(ws.lines.slice(0, 2).map((l) => l.content), ['live-one', 'live-two'])
    } finally {
      ws.socket.close()
    }
  })

  // Regression test. `socket.rooms` is a Set that always contains the socket's
  // own id; leaving every room including that one breaks direct delivery, and
  // leaving none of them means the socket keeps receiving its previous stream.
  // The original code did the latter, so switching streams interleaved output.
  it('stops delivering the previous stream after switching', async () => {
    const ws = observe()

    try {
      await waitFor(() => ws.streams.length > 0, 'the streams list')

      ws.socket.emit('select stream', 'alpha')
      await waitFor(() => ws.backlogs.length > 0, 'the alpha backlog')

      ws.socket.emit('select stream', 'beta')
      await waitFor(() => ws.backlogs.length > 1, 'the beta backlog')

      ws.lines.length = 0

      await feed('alpha', ['should-not-arrive'])
      await feed('beta', ['should-arrive'])

      await waitFor(() => ws.lines.some((l) => 'should-arrive' === l.content), 'the beta line')
      // Give any stray alpha delivery a chance to show up before asserting.
      await delay(250)

      assert.deepEqual(ws.lines.map((l) => l.streamid), ['beta'])
    } finally {
      ws.socket.close()
    }
  })

  it('ignores malformed UDP payloads', async () => {
    const socket = dgram.createSocket('udp4')
    const buffer = Buffer.from('not json')

    await new Promise((resolve) =>
      socket.send(buffer, 0, buffer.length, UDP_PORT, '127.0.0.1', () => socket.close(resolve))
    )

    // The server must still be answering afterwards.
    await waitForHttp(`http://127.0.0.1:${WEB_PORT}/`)
  })
})

describe('rtail-server webapp', () => {
  const port = 8882
  let server

  before(async () => {
    server = await startServer([
      '--udp-port', '9982',
      '--web-port', String(port),
      '--web-version', 'development'
    ], { webPort: port })
  })

  after(async () => {
    await server.stop()
  })

  it('serves the webapp shell', async () => {
    const res = await fetch(`http://127.0.0.1:${port}/app/`)
    const body = await res.text()

    assert.equal(res.status, 200)
    assert.match(body, /id="root"/)
    assert.match(body, /bundle\.js/)
  })

  it('does not let the browser cache development assets', async () => {
    const res = await fetch(`http://127.0.0.1:${port}/app/`)

    assert.equal(res.headers.get('cache-control'), 'no-store')
  })
})
