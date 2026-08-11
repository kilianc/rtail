/*!
 * udp.ts — a throwaway UDP sink for the client tests.
 */

import dgram from 'node:dgram'
import type { AddressInfo } from 'node:net'
import { setTimeout as delay } from 'node:timers/promises'

export interface Sink {
  port: number
  /** Every datagram received, JSON-parsed; unparseable ones land in `raw`. */
  messages: unknown[]
  raw: string[]
  waitFor(count: number, timeoutMs?: number): Promise<void>
  close(): Promise<void>
}

/** Binds an ephemeral UDP port and collects everything sent to it. */
export async function listen(host = '127.0.0.1'): Promise<Sink> {
  const socket = dgram.createSocket('udp4')
  const messages: unknown[] = []
  const raw: string[] = []

  socket.on('message', (data) => {
    const text = data.toString('utf8')
    try {
      messages.push(JSON.parse(text))
    } catch {
      raw.push(text)
    }
  })

  await new Promise<void>((resolve, reject) => {
    socket.once('error', reject)
    // Port 0 lets the OS pick, so parallel test files never collide.
    socket.bind(0, host, resolve)
  })

  return {
    port: (socket.address() as AddressInfo).port,
    messages,
    raw,

    async waitFor(count, timeoutMs = 5000) {
      const deadline = Date.now() + timeoutMs

      while (messages.length + raw.length < count) {
        if (Date.now() > deadline) {
          throw new Error(`timed out waiting for ${count} datagram(s); got ${messages.length}`)
        }
        await delay(10)
      }
    },

    close: () => new Promise<void>((resolve) => socket.close(resolve))
  }
}
