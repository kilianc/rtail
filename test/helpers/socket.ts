/*!
 * socket.ts — a socket.io client that cannot miss an event.
 *
 * The server emits `streams` the instant a socket connects. Attaching a
 * listener after awaiting `connect` is a race: on a fast loopback the event
 * has usually already been delivered, and the test then waits for something
 * that will never come again.
 *
 * So every listener is attached at construction and each event is queued.
 * `next()` takes from the queue when something is already there, and only
 * waits when the queue is empty.
 */

import { setTimeout as delay } from 'node:timers/promises'
import { io as ioClient, type Socket } from 'socket.io-client'

/**
 * Waits, best effort, until the server has no engine.io clients left.
 *
 * Closing a server while a long-poll is still in flight makes engine.io arm a
 * grace timer in Polling.doClose that nothing ever clears, and that one timer
 * keeps the process alive well past the end of the suite. Letting the
 * disconnect land first avoids arming it at all.
 *
 * Deliberately does not throw: this is teardown hygiene, not an assertion.
 */
export async function settleConnections(
  server: { engine: { clientsCount: number } },
  timeoutMs = 2000
): Promise<void> {
  const deadline = Date.now() + timeoutMs

  while (server.engine.clientsCount > 0 && Date.now() < deadline) await delay(20)
}

/** The events rtail-server sends to a browser. */
const EVENTS = ['streams', 'backlog', 'line'] as const

export interface TestClient {
  socket: Socket
  /** The next occurrence of `event`, queued ones first. */
  next<T>(event: (typeof EVENTS)[number], timeoutMs?: number): Promise<T>
  /** Everything received for `event` so far. */
  received<T>(event: (typeof EVENTS)[number]): T[]
  /** Discards anything already queued, so `next()` waits for a fresh event. */
  clear(event: (typeof EVENTS)[number]): void
  emit(event: string, ...args: unknown[]): void
  close(): void
}

export async function connectClient(url: string): Promise<TestClient> {
  const socket = ioClient(url, { transports: ['websocket'], forceNew: true })

  const queues = new Map<string, unknown[]>()
  const waiters = new Map<string, ((value: unknown) => void)[]>()
  const all = new Map<string, unknown[]>()

  for (const event of EVENTS) {
    queues.set(event, [])
    waiters.set(event, [])
    all.set(event, [])

    socket.on(event, (value: unknown) => {
      all.get(event)!.push(value)

      const waiting = waiters.get(event)?.shift()
      if (waiting) waiting(value)
      else queues.get(event)!.push(value)
    })
  }

  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`timed out connecting to ${url}`)), 5000)

    socket.once('connect', () => {
      clearTimeout(timer)
      resolve()
    })

    socket.once('connect_error', (err: Error) => {
      clearTimeout(timer)
      reject(new Error(`connect_error: ${err.message}`))
    })
  })

  return {
    socket,

    next<T>(event: (typeof EVENTS)[number], timeoutMs = 5000): Promise<T> {
      const queued = queues.get(event)!

      if (queued.length) return Promise.resolve(queued.shift() as T)

      return new Promise<T>((resolve, reject) => {
        const timer = setTimeout(
          () => reject(new Error(`timed out waiting for "${event}"`)),
          timeoutMs
        )

        waiters.get(event)!.push((value) => {
          clearTimeout(timer)
          resolve(value as T)
        })
      })
    },

    received: <T>(event: (typeof EVENTS)[number]) => all.get(event)!.slice() as T[],

    clear(event) {
      queues.set(event, [])
    },

    emit: (event, ...args) => void socket.emit(event, ...args),

    close: () => socket.close()
  }
}
