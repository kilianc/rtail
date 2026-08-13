/*!
 * The whole view, in the URL.
 *
 * Every state that changes what you are looking at — the stream, the filter,
 * the time range, whether it is streaming — lives in the address bar, so any
 * view can be pasted into a chat and opened by someone else exactly as it was.
 * That is the cheapest collaboration feature an observability tool can have
 * and the one people miss most when it is absent.
 *
 * Hash rather than path, because the server serves a single page and a hash
 * needs no rewrite rules in whatever proxy ends up in front of it.
 */

import { DEFAULT_RANGE, type Range } from './timerange.js'

export interface ViewState {
  stream: string | null
  query: string
  range: Range
  /** Streaming, as opposed to looking at history. */
  live: boolean
  /** Fields promoted out of the summary into columns of their own. */
  columns: string[]
  /** Which language the query is written in. */
  lang: 'rql' | 'sql'
}

export const DEFAULT_STATE: ViewState = {
  stream: null,
  query: '',
  range: DEFAULT_RANGE,
  live: true,
  columns: [],
  lang: 'rql'
}

export function read(): ViewState {
  const hash = window.location.hash.replace(/^#\/?/, '')
  const params = new URLSearchParams(hash)

  // A bare `#/streams/name` is v1's URL. Honouring it means old bookmarks and
  // every link in the old README still land somewhere sensible.
  const legacy = /^streams\/(.+)$/.exec(hash)
  if (legacy) {
    return { ...DEFAULT_STATE, stream: decodeURIComponent(legacy[1]) }
  }

  return {
    stream: params.get('stream') || null,
    query: params.get('q') ?? '',
    range: {
      from: params.get('from') || DEFAULT_RANGE.from,
      to: params.get('to') ?? ''
    },
    live: 'false' !== params.get('live'),
    // Comma-separated, because a field name cannot contain one and the URL
    // stays readable — `cols=service,latency_ms` says what it is at a glance.
    columns: (params.get('cols') ?? '').split(',').map((name) => name.trim()).filter(Boolean),
    lang: 'sql' === params.get('lang') ? 'sql' : 'rql'
  }
}

export function write(state: ViewState): void {
  const params = new URLSearchParams()

  if (state.stream) params.set('stream', state.stream)
  if (state.query) params.set('q', state.query)
  if (state.range.from !== DEFAULT_RANGE.from) params.set('from', state.range.from)
  if (state.range.to) params.set('to', state.range.to)
  if (!state.live) params.set('live', 'false')
  if (state.columns.length) params.set('cols', state.columns.join(','))
  if ('sql' === state.lang) params.set('lang', 'sql')

  const next = `#/${params}`

  if (window.location.hash === next) return

  // replaceState, not pushState: typing in a search box should not put forty
  // entries in the back stack.
  window.history.replaceState(null, '', next || '#/')
}

/** Subscribes to back/forward and manual URL edits. */
export function onChange(handler: (state: ViewState) => void): () => void {
  const listener = () => handler(read())

  window.addEventListener('hashchange', listener)

  return () => window.removeEventListener('hashchange', listener)
}
