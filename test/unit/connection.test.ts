import test, { after, before, describe } from 'node:test'
import assert from 'node:assert/strict'
import { createRtailServer, type RtailServer } from '../../cli/lib/server.ts'
import { setupDom, type Dom } from '../helpers/dom.ts'
import { settleConnections } from '../helpers/socket.ts'

/**
 * connect() reads window.location, so the DOM has to exist — and it has to
 * exist *before* socket.io-client is imported, because the client picks its
 * transports based on the environment it finds at module load. Hence the
 * dynamic import inside the test rather than at the top of the file.
 */
describe('connection', () => {
  let server: RtailServer
  let dom: Dom

  before(async () => {
    server = createRtailServer({
      udpHost: '127.0.0.1',
      udpPort: 0,
      webHost: '127.0.0.1',
      webPort: 0,
      backlog: 10
    })

    await server.listening
  })

  after(async () => {
    dom?.cleanup()
    await settleConnections(server.io)
    await server.close()
  })

  test('connects back to the origin that served the app', async () => {
    const url = `http://127.0.0.1:${server.address().port}/`
    dom = setupDom(url)

    const { connect } = await import('../../app/src/lib/connection.ts')
    const socket = connect()

    const streams = await new Promise<string[]>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('timed out waiting for streams')), 5000)

      socket.on('streams', (value) => {
        clearTimeout(timer)
        resolve(value)
      })

      socket.on('connect_error', (err) => {
        clearTimeout(timer)
        reject(new Error(`connect_error: ${err.message}`))
      })
    })

    assert.ok(Array.isArray(streams))

    socket.close()
  })

  test('derives the socket.io path from the page the app was served from', async () => {
    // In development the app lives under /app, and socket.io is mounted
    // alongside it — so the path cannot be hardcoded to /socket.io.
    const url = `http://127.0.0.1:${server.address().port}/app/`
    dom?.cleanup()
    dom = setupDom(url)

    const { connect } = await import('../../app/src/lib/connection.ts')
    const socket = connect()

    assert.equal(socket.io.opts.path, '/app/socket.io')

    socket.close()
  })
})
