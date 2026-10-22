/*!
 * Server-sent events wiring.
 *
 * v2 replaced socket.io with SSE. The traffic was only ever one-directional —
 * everything the client tells the server is a subscription, and that is a URL —
 * so a websocket upgrade and a 40KB client bought nothing. EventSource
 * reconnects on its own, which is most of what socket.io was there for.
 *
 * The subscription carries the filter as well as the stream, so a filtered live
 * tail is the server evaluating the same rQL the search bar sends to
 * /v1/search, against the ingest stream, rather than the browser discarding
 * most of what it was sent.
 */

import type { WireLine } from './types.js'

interface ServerEvents {
  connect: () => void
  streams: (streams: string[]) => void
  backlog: (lines: WireLine[] | null) => void
  line: (line: WireLine) => void
  error: (message: string, position?: number) => void
}

type Handlers = { [K in keyof ServerEvents]: ServerEvents[K][] }

export interface Connection {
  on<K extends keyof ServerEvents>(event: K, handler: ServerEvents[K]): void
  /** Points the feed at a stream and filter. Re-subscribing to the same pair is a no-op. */
  subscribe(stream: string | null, query: string): void
  /** Re-opens the current subscription, pulling a fresh backlog. */
  reconnect(): void
  close(): void
}

export function connect(): Connection {
  const handlers: Handlers = { connect: [], streams: [], backlog: [], line: [], error: [] }

  let source: EventSource | null = null
  /*!
   * The subscription currently open, as two fields rather than one string.
   *
   * It used to be packed into `<stream>\0<query>` and taken apart again by
   * `reconnect`, which split on a space — so it got back the whole packed
   * string, NUL and all, and re-subscribed to a stream named
   * `api-gateway\u0000`. The server has no stream by that name, so it
   * answered with an empty backlog and every line on screen vanished.
   * Nothing needs the packed form except the no-op guard in `subscribe`,
   * and that can compare the two fields directly.
   */
  let current: { stream: string | null; query: string } | null = null
  let closed = false

  /*!
   * The retry EventSource will not do.
   *
   * It re-opens after a dropped connection by itself, but an HTTP error is
   * final to it: a 502 from a proxy while the server restarts closes the feed
   * for good, and the tab sat on a tail that would never move again. So a
   * feed closed by anything but a rejected query is re-opened here, backing
   * off from a second to half a minute, and the count resets once one opens.
   */
  let retry: ReturnType<typeof setTimeout> | undefined
  let attempt = 0
  /** The fetch asking why the feed closed, so it can be called off. */
  let probe: AbortController | null = null

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
   * The subscription *is* the URL, so changing it means a new connection —
   * which is why `subscribe` refuses to re-open the pair already open: doing
   * so would drop the feed and pull the backlog again for nothing. Anything
   * left over from the feed this replaces — a retry waiting to fire, a probe
   * still in flight — is cancelled first, or it would act on this one.
   */
  function open(stream: string | null, query: string): void {
    source?.close()
    clearTimeout(retry)
    probe?.abort()
    probe = null

    if (closed) return

    current = { stream, query }

    const url = new URL('/v1/tail', window.location.origin)
    if (stream) url.searchParams.set('stream', stream)
    if (query) url.searchParams.set('q', query)

    const feed = new EventSource(url)
    source = feed

    source.addEventListener('open', () => {
      attempt = 0
      dispatch('connect')
    })

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

    /*!
     * An SSE error carries no detail — the spec gives the page a bare event —
     * so a rejected filter cannot be reported through it. Rather than guess,
     * the request is replayed with fetch, which does return the reason, and
     * only a genuine 4xx is surfaced; anything else is retried. A dropped
     * connection stays silent because EventSource is already retrying it, and
     * only a feed it has given up on (CLOSED) is asked about at all.
     *
     * The replay is a real request for the tail. The server does not read
     * Accept, so when the query is fine it answers 200 with an event stream
     * that would otherwise stay open beside the real one for the life of the
     * tab — so a success is cancelled as soon as it is seen, and the whole
     * probe is aborted if this feed is replaced or closed before it lands.
     */
    source.addEventListener('error', () => {
      if (closed || source !== feed || EventSource.CLOSED !== feed.readyState) return

      const controller = new AbortController()
      probe = controller

      const later = () => {
        if (controller.signal.aborted) return
        retry = setTimeout(() => open(stream, query), Math.min(30_000, 1_000 * 2 ** attempt++))
      }

      fetch(url, { headers: { accept: 'application/json' }, signal: controller.signal })
        .then(async (response) => {
          if (response.status < 400 || response.status >= 500) {
            response.body?.cancel().catch(() => {})
            later()
            return
          }

          const body = await response.json().catch(() => ({}))
          if (controller.signal.aborted) return

          // The position is what lets the command bar underline the offending
          // character, so it has to survive this detour.
          dispatch('error', body.message ?? body.error ?? 'the live feed was rejected', body.position)
        })
        .catch(later)
    })
  }

  return {
    on(event, handler) {
      handlers[event].push(handler)
    },

    subscribe(stream, query) {
      if (source && current && current.stream === stream && current.query === query) return
      open(stream, query)
    },

    /*!
     * Re-opens the current subscription.
     *
     * Used when resuming a held feed. Whatever arrived while it was held was
     * counted rather than kept — keeping it would mean an unbounded buffer for
     * a tab somebody left open over lunch — so resuming pulls the server's
     * backlog and lands on the same bounded window a fresh tab would get.
     */
    reconnect() {
      if (current) open(current.stream, current.query)
    },

    close() {
      closed = true
      clearTimeout(retry)
      probe?.abort()
      probe = null
      source?.close()
      source = null
    }
  }
}
