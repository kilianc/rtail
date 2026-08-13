/**
 * A log line as returned by rtail-server, over SSE or from a search.
 *
 * The first six keys are v1's wire format and are frozen; the rest are the v2
 * envelope the explorer needs. Both endpoints emit the same shape, which is
 * what lets one result list render streaming and historical results without
 * caring which it is looking at.
 */
export interface WireLine {
  timestamp: number
  streamid: string
  host: string
  port: number
  content: unknown
  /** `typeof content` as computed server-side; 'object' means a parsed payload. */
  type: string

  /** Monotonic per store; unique with timestamp, and the pagination key. */
  seq: number
  ingest_ts: number
  /** Normalized severity name, absent when the line carried nothing level-shaped. */
  level?: string
  msg?: string
  /** Promoted root-level keys of the payload. */
  fields?: Record<string, unknown>
}

/** A wire line plus its rendered HTML, computed once on arrival. */
export interface Line extends WireLine {
  /** Pre-rendered, already-escaped HTML for the line body. */
  html: string
  /** Plain text used for the regexp filter. */
  text: string
  /** Monotonic id, so the list can be keyed without relying on timestamps. */
  key: number
}

export type Theme = 'dark' | 'light'

export interface Prefs {
  theme: Theme
  /** 1-based index into the monospace stacks in _fonts.scss. */
  fontFamily: number
  /** 1-based index into the font sizes in _fonts.scss. */
  fontSize: number
  favorites: string[]
}
