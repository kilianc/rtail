/*!
 * Time ranges.
 *
 * A relative range stays relative: "last 6 hours" is sent as `-6h` and the
 * server resolves it against its own clock. A browser whose clock is ten
 * minutes fast would otherwise ask for a window that has not happened yet and
 * quietly get nothing back.
 *
 * An absolute range — what you get by dragging on the histogram — is pinned,
 * so a link to it shows the same thing tomorrow.
 */

export interface Range {
  /** A relative offset like `-6h`, or an absolute ISO instant. */
  from: string
  /** Absolute ISO instant, or empty for "now". */
  to: string
}

export interface Preset {
  label: string
  short: string
  range: Range
}

export const PRESETS: Preset[] = [
  { label: 'Last 5 minutes', short: '5m', range: { from: '-5m', to: '' } },
  { label: 'Last 15 minutes', short: '15m', range: { from: '-15m', to: '' } },
  { label: 'Last hour', short: '1h', range: { from: '-1h', to: '' } },
  { label: 'Last 6 hours', short: '6h', range: { from: '-6h', to: '' } },
  { label: 'Last 24 hours', short: '24h', range: { from: '-24h', to: '' } },
  { label: 'Last 3 days', short: '3d', range: { from: '-72h', to: '' } },
  { label: 'Last 7 days', short: '7d', range: { from: '-168h', to: '' } }
]

export const DEFAULT_RANGE: Range = { from: '-1h', to: '' }

/** True when the range floats with the clock, and so can keep streaming. */
export function isRelative(range: Range): boolean {
  return range.from.startsWith('-') && '' === range.to
}

/*!
 * Resolves a range to concrete instants, for drawing and for labels.
 *
 * Never returns an Invalid Date. A malformed bound in a hand-edited URL should
 * degrade to the default window, not produce a value that throws the moment
 * anything tries to format it.
 */
export function resolve(range: Range): { from: Date; to: Date } {
  const to = valid(range.to ? new Date(range.to) : new Date()) ?? new Date()

  const from =
    valid(
      range.from.startsWith('-')
        ? new Date(to.getTime() - parseOffset(range.from))
        : new Date(range.from)
    ) ?? new Date(to.getTime() - parseOffset(DEFAULT_RANGE.from))

  return { from, to }
}

function valid(date: Date): Date | null {
  return Number.isNaN(date.getTime()) ? null : date
}

/** Parses `-6h` / `-90m` / `-3d` into milliseconds. */
export function parseOffset(value: string): number {
  const match = /^-(\d+(?:\.\d+)?)([smhdw])$/.exec(value.trim())
  if (!match) return 60 * 60 * 1000

  const amount = Number(match[1])
  const unit = { s: 1e3, m: 6e4, h: 3.6e6, d: 8.64e7, w: 6.048e8 }[match[2]] ?? 3.6e6

  return amount * unit
}

/** A short human label for the current range. */
export function label(range: Range): string {
  const preset = PRESETS.find((p) => p.range.from === range.from && p.range.to === range.to)
  if (preset) return preset.label

  if (isRelative(range)) return `Last ${range.from.slice(1)}`

  const { from, to } = resolve(range)
  return `${formatStamp(from)} → ${formatStamp(to)}`
}

const stamp = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false
})

export function formatStamp(date: Date): string {
  return stamp.format(date).replace(',', '')
}

/** Builds an absolute range, as produced by dragging on the histogram. */
export function absolute(from: Date, to: Date): Range {
  return { from: from.toISOString(), to: to.toISOString() }
}

/** Widens a range by a factor on each side, for "zoom out". */
export function zoomOut(range: Range, factor = 1): Range {
  const { from, to } = resolve(range)
  const span = to.getTime() - from.getTime()

  return absolute(new Date(from.getTime() - span * factor), new Date(to.getTime() + span * factor))
}
