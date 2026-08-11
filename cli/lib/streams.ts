/*!
 * streams.ts — the in-memory ring of recent lines, one per stream.
 *
 * This is the whole persistence story: rtail deliberately keeps nothing on
 * disk, so a stream is just its last `size` lines.
 */

/** What a client sends over UDP. */
export interface IncomingPayload {
  id: string
  timestamp: number
  content: unknown
}

/** What the server broadcasts to browsers, enriched with the sender. */
export interface WireLine {
  timestamp: number
  streamid: string
  host: string
  port: number
  content: unknown
  /** `typeof content`; 'object' means the client parsed a structured payload. */
  type: string
}

export interface Remote {
  address: string
  port: number
}

export interface PushResult {
  message: WireLine
  /** True when this datagram created the stream, so listeners need telling. */
  isNew: boolean
}

export interface StreamStore {
  names(): string[]
  backlog(id: string): WireLine[]
  push(payload: IncomingPayload, remote: Remote): PushResult
}

/**
 * Decodes a datagram, or returns null if it is not a payload we understand.
 *
 * Anything at all can arrive on a UDP port; a malformed packet must never take
 * the server down, and must not create a phantom stream either.
 */
export function decodePayload(data: Uint8Array): IncomingPayload | null {
  let parsed: unknown

  try {
    parsed = JSON.parse(Buffer.from(data).toString('utf8'))
  } catch {
    return null
  }

  if (null === parsed || 'object' !== typeof parsed) return null

  const { id, timestamp } = parsed as Record<string, unknown>

  // The id names the stream and is used as a socket.io room, so it has to be a
  // non-empty string; the rest is free-form.
  if ('string' !== typeof id || '' === id) return null

  return {
    id,
    timestamp: 'number' === typeof timestamp ? timestamp : Date.now(),
    content: (parsed as Record<string, unknown>).content
  }
}

export function createStreamStore(size: number): StreamStore {
  const streams = new Map<string, WireLine[]>()

  return {
    names: () => [...streams.keys()],

    backlog: (id) => streams.get(id) ?? [],

    push(payload, remote) {
      let backlog = streams.get(payload.id)
      const isNew = undefined === backlog

      if (undefined === backlog) {
        backlog = []
        streams.set(payload.id, backlog)
      }

      const message: WireLine = {
        timestamp: payload.timestamp,
        streamid: payload.id,
        host: remote.address,
        port: remote.port,
        content: payload.content,
        type: typeof payload.content
      }

      if (backlog.length >= size) backlog.shift()
      backlog.push(message)

      return { message, isNew }
    }
  }
}
