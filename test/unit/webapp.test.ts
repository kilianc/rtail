import test, { describe } from 'node:test'
import assert from 'node:assert/strict'
import express from 'express'
import { webapp } from '../../cli/lib/webapp.ts'

/** Mounts the middleware on a throwaway server and returns its base URL. */
async function serve(handler: express.RequestHandler) {
  const app = express()
  app.use(handler)

  const server = app.listen(0, '127.0.0.1')
  await new Promise((resolve) => server.once('listening', resolve))

  const { port } = server.address() as { port: number }

  return {
    url: `http://127.0.0.1:${port}`,
    close: () => new Promise<void>((resolve) => server.close(() => resolve()))
  }
}

/** A fetch stub that records calls and replies from a fixture table. */
function stubFetch(replies: Record<string, () => Response | Promise<Response>>) {
  const calls: string[] = []

  const impl = (async (input: string | URL | Request) => {
    const url = String(input)
    calls.push(url)

    const path = new URL(url).pathname
    const reply = replies[path]

    if (!reply) throw new TypeError('fetch failed')

    return reply()
  }) as typeof globalThis.fetch

  return { impl, calls }
}

describe('webapp proxy', () => {
  test('proxies a document from the origin', async () => {
    const { impl } = stubFetch({
      '/index.html': () =>
        new Response('<h1>hi</h1>', { headers: { 'content-type': 'text/html' } })
    })

    const server = await serve(webapp({ origin: 'http://origin.test', ttl: 60_000, fetch: impl }))
    const res = await fetch(`${server.url}/index.html`)

    assert.equal(res.status, 200)
    assert.equal(res.headers.get('content-type'), 'text/html')
    assert.equal(await res.text(), '<h1>hi</h1>')

    await server.close()
  })

  test('serves repeat requests from cache', async () => {
    const { impl, calls } = stubFetch({
      '/app.js': () => new Response('console.log(1)', { headers: { 'content-type': 'text/javascript' } })
    })

    const server = await serve(webapp({ origin: 'http://origin.test', ttl: 60_000, fetch: impl }))

    const first = await fetch(`${server.url}/app.js`)
    const second = await fetch(`${server.url}/app.js`)

    assert.equal(await first.text(), 'console.log(1)')
    assert.equal(await second.text(), 'console.log(1)')
    assert.equal(calls.length, 1, 'expected the second request to be served from cache')
    assert.equal(second.headers.get('content-type'), 'text/javascript')

    await server.close()
  })

  test('forwards only the whitelisted upstream headers', async () => {
    const { impl } = stubFetch({
      '/a.css': () =>
        new Response('body{}', {
          headers: {
            'content-type': 'text/css',
            etag: 'W/"abc"',
            'set-cookie': 'session=leak',
            'x-amz-request-id': 'nope'
          }
        })
    })

    const server = await serve(webapp({ origin: 'http://origin.test', ttl: 60_000, fetch: impl }))
    const res = await fetch(`${server.url}/a.css`)

    assert.equal(res.headers.get('etag'), 'W/"abc"')
    assert.equal(res.headers.get('content-type'), 'text/css')
    assert.equal(res.headers.get('x-amz-request-id'), null, 'upstream noise should not be forwarded')

    await server.close()
  })

  test('passes through an upstream error status', async () => {
    const { impl } = stubFetch({
      '/missing.js': () => new Response('nope', { status: 404 })
    })

    const server = await serve(webapp({ origin: 'http://origin.test', ttl: 60_000, fetch: impl }))
    const res = await fetch(`${server.url}/missing.js`)

    assert.equal(res.status, 404)

    await server.close()
  })

  test('surfaces an unreachable origin as a 5xx rather than crashing', async () => {
    // No fixture for this path, so the stub throws the way a real DNS or
    // connection failure would.
    const { impl } = stubFetch({})

    const server = await serve(webapp({ origin: 'http://origin.test', ttl: 60_000, fetch: impl }))
    const res = await fetch(`${server.url}/anything`)

    assert.ok(res.status >= 500, `expected a 5xx, got ${res.status}`)

    await server.close()
  })

  test('re-fetches once the ttl has elapsed', async () => {
    let body = 'first'
    const { impl, calls } = stubFetch({
      '/v.txt': () => new Response(body, { headers: { 'content-type': 'text/plain' } })
    })

    // A 10ms ttl so the sweep runs inside the test rather than in six hours.
    const server = await serve(webapp({ origin: 'http://origin.test', ttl: 10, fetch: impl }))

    assert.equal(await (await fetch(`${server.url}/v.txt`)).text(), 'first')

    body = 'second'
    await new Promise((resolve) => setTimeout(resolve, 40))

    assert.equal(await (await fetch(`${server.url}/v.txt`)).text(), 'second')
    assert.equal(calls.length, 2, 'expected the cache to have been swept')

    await server.close()
  })
})
