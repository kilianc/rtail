/** A log line as broadcast by rtail-server. */
export interface WireLine {
  timestamp: number
  streamid: string
  host: string
  port: number
  content: unknown
  /** `typeof content` as computed server-side; 'object' means a parsed payload. */
  type: string
}

/** A wire line plus its rendered HTML, computed once on arrival. */
export interface Line extends WireLine {
  /** Pre-rendered, already-escaped HTML for the line body. */
  html: string
  /** The same payload on one line; null for anything that is not an object. */
  htmlCompact: string | null
  /** True when the expanded payload is taller than the auto-collapse cutoff. */
  bulky: boolean
  /** Plain text used for the filter. */
  text: string
  /** Monotonic id, so the list can be keyed without relying on timestamps. */
  key: number
}

export type Theme = 'dark' | 'light'

/** How object lines render before anyone touches an individual one. */
export type JsonView = 'auto' | 'collapsed' | 'expanded'

export interface Prefs {
  theme: Theme
  /** 1-based index into the monospace stacks in _fonts.scss. */
  fontFamily: number
  /** 1-based index into the font sizes in _fonts.scss. */
  fontSize: number
  /** true = oldest first. */
  ascending: boolean
  sidebarWidth: number
  favorites: string[]
  /** Streams whose timestamp column is collapsed. */
  hiddenTimestamps: string[]
  jsonView: JsonView
  /**
   * Extracted JSON paths, per stream — the fields are a property of the shape
   * a stream logs, so they follow the stream rather than the window.
   */
  fields: Record<string, string[]>
}
