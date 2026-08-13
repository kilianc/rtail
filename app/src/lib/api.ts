/*!
 * The v2 API client.
 *
 * Every search endpoint takes the same parameters, so they share one builder.
 * Requests carry an AbortSignal because the search bar fires on every
 * keystroke and a stale response arriving after a newer one is how a search UI
 * ends up showing the wrong results.
 */

import type { WireLine } from './types.js'

/** What a query cost, reported by every search response. */
export interface Scanned {
  files: number
  rows: number
  bytes: number
  millis: number
  from: string
  to: string
}

export interface SearchResponse {
  records: WireLine[]
  cursor?: string
  scanned: Scanned
}

export interface Bucket {
  ts: string
  total: number
  level?: Record<string, number>
}

export interface HistogramResponse {
  buckets: Bucket[]
  interval_ms: number
}

export interface FieldValue {
  value: string
  count: number
  share: number
}

export interface Field {
  name: string
  kind: string
  polymorphic?: boolean
  occurrences: number
  stream: string
}

export interface SqlResponse {
  columns: string[]
  rows: unknown[][]
  scanned: Scanned
}

/** Parameters shared by every search endpoint. */
export interface Params {
  q?: string
  stream?: string | null
  from?: string
  to?: string
  limit?: number
  cursor?: string
  order?: 'asc' | 'desc'
}

/**
 * An error the server attributed to the query rather than to itself.
 *
 * `position` is the offset rQL failed at, which is what lets the command bar
 * underline the offending character instead of just going red.
 */
export class QueryError extends Error {
  readonly position?: number
  readonly detail?: string

  constructor(message: string, position?: number, detail?: string) {
    super(message)
    this.name = 'QueryError'
    this.position = position
    this.detail = detail
  }
}

function search(params: Params): URLSearchParams {
  const query = new URLSearchParams()

  if (params.q) query.set('q', params.q)
  if (params.stream) query.set('stream', params.stream)
  if (params.from) query.set('from', params.from)
  if (params.to) query.set('to', params.to)
  if (params.limit) query.set('limit', String(params.limit))
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.order) query.set('order', params.order)

  return query
}

async function get<T>(path: string, params: Params, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`${path}?${search(params)}`, { signal })
  const body = await response.json().catch(() => ({}))

  if (!response.ok) {
    throw new QueryError(
      body.message ?? body.error ?? `${path} failed`,
      body.position,
      body.detail
    )
  }

  return body as T
}

export const api = {
  search: (params: Params, signal?: AbortSignal) =>
    get<SearchResponse>('/v1/search', params, signal),

  histogram: (params: Params & { buckets?: number }, signal?: AbortSignal) => {
    const query = search(params)
    if (params.buckets) query.set('buckets', String(params.buckets))
    return fetch(`/v1/histogram?${query}`, { signal }).then(unwrap<HistogramResponse>)
  },

  fields: (params: Params & { field: string; top?: number }, signal?: AbortSignal) => {
    const query = search(params)
    query.set('field', params.field)
    if (params.top) query.set('top', String(params.top))
    return fetch(`/v1/fields?${query}`, { signal }).then(unwrap<{ values: FieldValue[] }>)
  },

  schema: (stream: string | null, signal?: AbortSignal) => {
    const query = new URLSearchParams()
    if (stream) query.set('stream', stream)
    return fetch(`/v1/schema?${query}`, { signal }).then(unwrap<{ fields: Field[] }>)
  },

  streams: (signal?: AbortSignal) =>
    fetch('/v1/streams', { signal }).then(unwrap<{ streams: string[] }>),

  sql: (sql: string, params: Params, signal?: AbortSignal) =>
    fetch('/v1/sql', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ sql, stream: params.stream, from: params.from, to: params.to }),
      signal
    }).then(unwrap<SqlResponse>)
}

async function unwrap<T>(response: Response): Promise<T> {
  const body = await response.json().catch(() => ({}))

  if (!response.ok) {
    throw new QueryError(body.message ?? body.error ?? 'request failed', body.position, body.detail)
  }

  return body as T
}
