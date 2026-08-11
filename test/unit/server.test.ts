import test, { after, before, describe } from 'node:test'
import assert from 'node:assert/strict'
import dgram from 'node:dgram'
import { setTimeout as delay } from 'node:timers/promises'
import express from 'express'
import { createRtailServer, mountWebapp, type RtailServer } from '../../cli/lib/server.ts'
import type { WireLine } from '../../cli/lib/streams.ts'
import { connectClient, type TestClient } from '../helpers/socket.ts'

/** Sends one datagram to the server's UDP port and resolves when it is away. */
function send(port: number, payload: unknown): Promise<void> {
  const socket = dgram.createSocket('udp4')
  const buffer = Buffer.from('string' === typeof payload ? payload : JSON.stringify(payload))

  return new Promise((resolve, reject) => {
    socket.send(buffer, 0, buffer.length, port, '127.0.0.1', (err) => {
      socket.close()
      err ? reject(err) : resolve()
    })
  })
}

describe('mountWebapp', () => {
  /** Mounts on a bare express app and returns the base URL. */
  async function serve(webVersion?: string, fetchImpl?: typeof globalThis.fetch) {
    const app = express()
    mountWebapp(app, webVersion, { fetch: fetchImpl })

    const server = app.listen(0, '127.0.0.1')
    await new Promise((resolve) => server.once('listening', resolve))

    const { port } = server.address() as { port: number }

    return {
      url: `http://127.0.0.1:${port}`,
      close: () => new Promise<void>((resolve) => server.close(() => resolve()))
    }
  }

  test('serves the built webapp by default', async () => {
    const server = await serve()
    const res = await fetch(`${server.url}/index.html`)

    assert.equal(res.status, 200, 'expected dist/index.html — run `npm run dist` first')
    assert.match(await res.text(), /<html/i)

    await server.close()
  })

  test('redirects the root to /app in development', async () => {
    const server = await serve('development')
    const res = await fetch(`${server.url}/`, { redirect: 'manual' })

    assert.equal(res.status, 302)
    assert.equal(res.headers.get('location'), '/app/')

    await server.close()
  })

  test('lands on the app when the development redirect is followed', async () => {
    const server = await serve('development')
    const res = await fetch(`${server.url}/`)

    assert.equal(res.status, 200)
    assert.match(await res.text(), /<html/i)

    await server.close()
  })

  test('does not let the browser cache development assets', async () => {
    const server = await serve('development')
    const res = await fetch(`${server.url}/app/index.html`)

    assert.equal(res.headers.get('cache-control'), 'no-store')
    assert.equal(res.headers.get('etag'), null)
    assert.equal(res.headers.get('last-modified'), null)

    await server.close()
  })

  test('proxies a pinned version from the published build', async () => {
    // Stubbed rather than reaching S3: a unit test must not depend on the
    // network, and the real bucket answers 403 for a missing key anyway.
    const requested: string[] = []
    const stub = (async (input: string | URL | Request) => {
      requested.push(String(input))
      return new Response('proxied', { headers: { 'content-type': 'text/plain' } })
    }) as typeof globalThis.fetch

    const server = await serve('0.1.3', stub)
    const res = await fetch(`${server.url}/index.html`)

    assert.equal(await res.text(), 'proxied')
    assert.deepEqual(requested, [
      'http://rtail.s3-website-us-east-1.amazonaws.com/0.1.3/index.html'
    ])

    await server.close()
  })
})

describe('rtail-server', () => {
  let server: RtailServer
  let url: string
  let udpPort: number
  const clients: TestClient[] = []

  before(async () => {
    server = createRtailServer({
      udpHost: '127.0.0.1',
      udpPort: 0,
      webHost: '127.0.0.1',
      webPort: 0,
      backlog: 3
    })

    await server.listening

    url = `http://127.0.0.1:${server.address().port}`
    udpPort = (server.udp.address() as { port: number }).port
  })

  after(async () => {
    for (const client of clients) client.close()
    await server.close()
  })

  /** Opens a browser-side connection and registers it for teardown. */
  async function client(): Promise<TestClient> {
    const connected = await connectClient(url)
    clients.push(connected)
    return connected
  }

  test('announces the streams list on connect', async () => {
    const browser = await client()

    assert.ok(Array.isArray(await browser.next<string[]>('streams')))
  })

  test('announces a stream the first time it is seen', async () => {
    const browser = await client()
    await browser.next('streams')

    await send(udpPort, { id: 'announce-me', timestamp: Date.now(), content: 'hello' })

    assert.ok((await browser.next<string[]>('streams')).includes('announce-me'))
  })

  test('announces a stream only once', async () => {
    const browser = await client()
    await browser.next('streams')

    await send(udpPort, { id: 'once-only', timestamp: 1, content: 'a' })
    await browser.next('streams')

    await send(udpPort, { id: 'once-only', timestamp: 2, content: 'b' })
    await delay(200)

    assert.equal(
      browser.received('streams').length,
      2,
      'a second line on a known stream should not re-announce it'
    )
  })

  test('replays the backlog when a stream is selected', async () => {
    await send(udpPort, { id: 'replay', timestamp: 1, content: 'one' })
    await send(udpPort, { id: 'replay', timestamp: 2, content: 'two' })

    const browser = await client()
    browser.emit('select stream', 'replay')

    const backlog = await browser.next<WireLine[]>('backlog')

    assert.deepEqual(backlog.map((line) => line.content), ['one', 'two'])
  })

  test('replays an empty backlog for an unknown stream', async () => {
    const browser = await client()
    browser.emit('select stream', 'never-seen')

    assert.deepEqual(await browser.next<WireLine[]>('backlog'), [])
  })

  test('caps the replayed backlog at --backlog lines', async () => {
    for (let i = 1; i <= 5; i++) {
      await send(udpPort, { id: 'capped', timestamp: i, content: i })
    }

    const browser = await client()
    browser.emit('select stream', 'capped')

    const backlog = await browser.next<WireLine[]>('backlog')

    assert.deepEqual(backlog.map((line) => line.content), [3, 4, 5])
  })

  test('delivers live lines for the selected stream', async () => {
    const browser = await client()
    browser.emit('select stream', 'live')
    await browser.next('backlog')

    await send(udpPort, { id: 'live', timestamp: 9, content: 'fresh' })

    const line = await browser.next<WireLine>('line')

    assert.equal(line.content, 'fresh')
    assert.equal(line.streamid, 'live')
    assert.equal(line.host, '127.0.0.1')
    assert.equal(line.type, 'string')
    assert.ok(line.port > 0, 'expected the sender port to be recorded')
  })

  test('stops delivering the previous stream after switching', async () => {
    const browser = await client()

    browser.emit('select stream', 'first')
    await browser.next('backlog')

    browser.emit('select stream', 'second')
    await browser.next('backlog')

    await send(udpPort, { id: 'first', timestamp: 1, content: 'ignored' })
    await send(udpPort, { id: 'second', timestamp: 2, content: 'wanted' })
    await delay(200)

    assert.deepEqual(
      browser.received<WireLine>('line').map((line) => line.streamid),
      ['second']
    )
  })

  test('unsubscribes on a null selection, which is how pause works', async () => {
    const browser = await client()

    browser.emit('select stream', 'pausable')
    await browser.next('backlog')

    browser.emit('select stream', null)

    await send(udpPort, { id: 'pausable', timestamp: 1, content: 'while paused' })
    await delay(200)

    assert.deepEqual(browser.received('line'), [], 'a paused socket should receive nothing')
  })

  test('keeps each subscriber isolated', async () => {
    const a = await client()
    const b = await client()

    a.emit('select stream', 'for-a')
    await a.next('backlog')

    b.emit('select stream', 'for-b')
    await b.next('backlog')

    await send(udpPort, { id: 'for-a', timestamp: 1, content: 'a-line' })
    await delay(200)

    assert.equal(a.received('line').length, 1)
    assert.equal(b.received('line').length, 0)
  })

  test('ignores malformed UDP payloads', async () => {
    const before = await client()
    const baseline = await before.next<string[]>('streams')

    await send(udpPort, 'this is not json')
    await send(udpPort, JSON.stringify({ no: 'id' }))
    await send(udpPort, JSON.stringify('just a string'))
    await delay(200)

    const after = await client()
    const current = await after.next<string[]>('streams')

    assert.deepEqual(current.sort(), baseline.sort(), 'no phantom stream should appear')
  })
})
