import test, { after, before, describe } from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import type { AddressInfo } from 'node:net'
import { connectClient, type TestClient } from '../helpers/socket.ts'
import { runClient, startServer, type ServerProcess } from '../helpers/spawn.ts'
import type { WireLine } from '../../cli/lib/streams.ts'

/** Grabs two free ports by binding and immediately releasing them. */
async function freePorts(count: number): Promise<number[]> {
  const ports: number[] = []

  for (let i = 0; i < count; i++) {
    const server = createServer()
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
    ports.push((server.address() as AddressInfo).port)
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }

  return ports
}

describe('rtail-server (server bin), end to end with the client bin', () => {
  let server: ServerProcess
  let webPort: number
  let udpPort: number
  const clients: TestClient[] = []

  before(async () => {
    ;[webPort, udpPort] = (await freePorts(2)) as [number, number]

    server = await startServer(
      [
        '--web-port', String(webPort),
        '--udp-port', String(udpPort),
        '--web-host', '127.0.0.1',
        '--udp-host', '127.0.0.1'
      ],
      webPort
    )
  })

  after(async () => {
    for (const client of clients) client.close()
    await server.stop()
  })

  async function browser(): Promise<TestClient> {
    const client = await connectClient(`http://127.0.0.1:${webPort}`)
    clients.push(client)
    return client
  }

  test('serves the built webapp', async () => {
    const res = await fetch(`http://127.0.0.1:${webPort}/index.html`)

    assert.equal(res.status, 200)
    assert.match(await res.text(), /<html/i)
  })

  test('carries a line from the client bin through to a browser', async () => {
    const watcher = await browser()

    // Nothing selected yet, so this only has to reach the server.
    await runClient(['--port', String(udpPort), '--id', 'e2e', '--mute'], 'first line\n')

    const streams = await watcher.next<string[]>('streams', 10_000)
    assert.ok(streams.includes('e2e') || (await watcher.next<string[]>('streams')).includes('e2e'))

    const reader = await browser()
    reader.emit('select stream', 'e2e')

    const backlog = await reader.next<WireLine[]>('backlog')

    assert.deepEqual(backlog.map((line) => line.content), ['first line'])
  })

  test('delivers a live line to a subscribed browser', async () => {
    const reader = await browser()

    reader.emit('select stream', 'live-e2e')
    await reader.next('backlog')

    await runClient(['--port', String(udpPort), '--id', 'live-e2e', '--mute'], 'live line\n')

    const line = await reader.next<WireLine>('line', 10_000)

    assert.equal(line.content, 'live line')
    assert.equal(line.streamid, 'live-e2e')
  })
})
