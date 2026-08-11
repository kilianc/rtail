import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { after, before, describe, it } from 'node:test'
import express from 'express'
import { webapp } from '../cli/lib/webapp.js'

/**
 * The published-webapp proxy, exercised against a local origin.
 *
 * The old test for this pointed at a live S3 bucket from 2015, so it tested
 * the network as much as the code and would fail the moment the bucket went
 * away.
 */
describe('webapp proxy', () => {
  let origin
  let originPort
  let proxy
  let proxyPort
  let originHits = 0

  before(async () => {
    origin = createServer((req, res) => {
      originHits++

      if ('/missing' === req.url) {
        res.writeHead(404)
        return res.end('nope')
      }

      res.writeHead(200, { 'content-type': 'text/html' })
      res.end(`<html>${req.url}</html>`)
    })

    await new Promise((resolve) => origin.listen(0, '127.0.0.1', resolve))
    originPort = origin.address().port

    const app = express()
    app.use(webapp({ origin: `http://127.0.0.1:${originPort}`, ttl: 60_000 }))
    proxy = createServer(app)

    await new Promise((resolve) => proxy.listen(0, '127.0.0.1', resolve))
    proxyPort = proxy.address().port
  })

  after(async () => {
    await new Promise((resolve) => proxy.close(resolve))
    await new Promise((resolve) => origin.close(resolve))
  })

  const get = (path) => fetch(`http://127.0.0.1:${proxyPort}${path}`)

  it('proxies a document from the origin', async () => {
    const res = await get('/index.html')

    assert.equal(res.status, 200)
    assert.match(await res.text(), /index\.html/)
    assert.equal(res.headers.get('content-type'), 'text/html')
  })

  it('serves repeat requests from cache', async () => {
    await get('/cached.html')
    const hitsAfterFirst = originHits

    await get('/cached.html')

    assert.equal(originHits, hitsAfterFirst, 'origin should not be hit twice')
  })

  it('passes through an upstream error status', async () => {
    const res = await get('/missing')

    assert.equal(res.status, 404)
  })

  it('surfaces an unreachable origin as a 5xx rather than crashing', async () => {
    const app = express()
    // Port 1 is reserved and nothing listens there.
    app.use(webapp({ origin: 'http://127.0.0.1:1', ttl: 60_000 }))

    const server = createServer(app)
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))

    try {
      const res = await fetch(`http://127.0.0.1:${server.address().port}/index.html`)
      assert.ok(res.status >= 500, `expected 5xx, got ${res.status}`)
    } finally {
      await new Promise((resolve) => server.close(resolve))
    }
  })
})
