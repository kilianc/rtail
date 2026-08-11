/*!
 * socket.io wiring.
 */

import { io, type Socket } from 'socket.io-client'
import type { WireLine } from './types.js'

interface ServerEvents {
  streams: (streams: string[]) => void
  backlog: (lines: WireLine[] | null) => void
  line: (line: WireLine) => void
}

interface ClientEvents {
  'select stream': (stream: string | null) => void
}

export type Connection = Socket<ServerEvents, ClientEvents>

/**
 * Connects back to the origin that served the app.
 *
 * The server mounts socket.io under the app's own path in development, so the
 * path is derived from the document location rather than hardcoded.
 */
export function connect(): Connection {
  return io(window.location.origin, {
    path: window.location.pathname.replace(/\/?$/, '/') + 'socket.io'
  })
}
