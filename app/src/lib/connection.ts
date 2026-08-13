/*!
 * Server-sent events wiring.
 *
 * v2 replaced socket.io with SSE. The traffic was only ever one-directional —
 * the single thing the client told the server was which stream it wanted, and
 * that is now just a query parameter — so a websocket upgrade and a 40KB
 * client bought nothing. EventSource reconnects on its own, which is most of
 * what socket.io was there for.
 *
 * The surface below is deliberately the same on/emit/close shape socket.io
 * had, so App did not need to change.
 */

import type { WireLine } from './types.js'

interface ServerEvents {
  connect: () => void
  streams: (streams: string[]) => void
  backlog: (lines: WireLine[] | null) => void
  line: (line: WireLine) => void
}

type Handlers = { [K in keyof ServerEvents]: ServerEvents[K][] }

export interface Connection {
  on<K extends keyof ServerEvents>(event: K, handler: ServerEvents[K]): void
  /** Switches the subscription. Null unsubscribes but keeps hearing about streams. */
  emit(event: 'select stream', stream: string | null): void
  close(): void
}

export function connect(): Connection {
  const handlers: Handlers = { connect: [], streams: [], backlog: [], line: [] }

  let source: EventSource | null = null
  let current: string | null = null
  let closed = false

  const dispatch = <K extends keyof ServerEvents>(
    event: K,
    ...args: Parameters<ServerEvents[K]>
  ) => {
    for (const handler of handlers[event]) {
      ;(handler as (...a: unknown[]) => void)(...args)
    }
  }

  // A malformed frame must not take the connection down with it.
  const parse = <T>(data: string): T | null => {
    try {
      return JSON.parse(data) as T
    } catch {
      return null
    }
  }

  /**
   * Opens (or re-opens) the feed.
   *
   * Selecting a stream *is* the subscription, so switching means a new
   * connection. That is also why the guard in `emit` matters: EventSource
   * re-opens by itself after a drop, App re-selects its stream on 'connect',
   * and without the guard those two would chase each other forever.
   */
  function open(stream: string | null): void {
    source?.close()

    if (closed) return

    current = stream

    const url = new URL('/v1/tail', window.location.origin)
    if (stream) url.searchParams.set('stream', stream)

    source = new EventSource(url)

    source.addEventListener('open', () => dispatch('connect'))

    source.addEventListener('streams', (event) => {
      const streams = parse<string[]>(event.data)
      if (streams) dispatch('streams', streams)
    })

    source.addEventListener('backlog', (event) => {
      dispatch('backlog', parse<WireLine[]>(event.data) ?? [])
    })

    source.addEventListener('line', (event) => {
      const line = parse<WireLine>(event.data)
      if (line) dispatch('line', line)
    })

    // EventSource retries on its own; there is nothing useful to do here that
    // it is not already doing.
    source.addEventListener('error', () => {})
  }

  // Connect straight away so the stream list is populated even before anything
  // is selected — the same thing socket.io did on connection. App re-selects
  // on mount, which the guard below turns into a no-op when it matches.
  open(null)

  return {
    on(event, handler) {
      handlers[event].push(handler)
    },

    emit(_event, stream) {
      if (source && stream === current) return
      open(stream)
    },

    close() {
      closed = true
      source?.close()
      source = null
    }
  }
}
