/*!
 * The histogram.
 *
 * Counts over time, stacked by severity, directly above the results. This is
 * the navigation — drag across it to zoom the time range — and it is the
 * reason a spike is something you can see and click rather than something you
 * have to already suspect and go looking for.
 *
 * Cheap by construction: it touches ts and level and nothing else, so it comes
 * back in milliseconds while the result list is still being read.
 */

import { useMemo, useRef, useState } from 'preact/hooks'
import type { Bucket } from '../lib/api.js'
import { absolute, formatStamp, type Range } from '../lib/timerange.js'

interface Props {
  collapsed: boolean
  onToggle: () => void
  buckets: Bucket[]
  intervalMs: number
  range: { from: Date; to: Date }
  loading: boolean
  onSelect: (range: Range) => void
}

/*!
 * Severity order, low to high, so a stack reads with the worst on top — the
 * bit of a bar you notice first is the bit that should worry you.
 */
const LEVELS = ['TRACE', 'DEBUG', 'INFO', 'NOTICE', 'WARN', 'ERROR', 'CRITICAL', 'ALERT', 'EMERGENCY', 'FATAL']

function levelClass(level: string): string {
  if (!LEVELS.includes(level)) return 'other'
  return level.toLowerCase()
}

export function Histogram({ collapsed, onToggle, buckets, intervalMs, range, loading, onSelect }: Props) {
  const surface = useRef<HTMLDivElement>(null)
  const dragging = useRef<{ from: number; to: number } | null>(null)
  const [drag, setDrag] = useState<{ from: number; to: number } | null>(null)
  const [hover, setHover] = useState<number | null>(null)

  const span = range.to.getTime() - range.from.getTime()

  const peak = useMemo(
    () => buckets.reduce((max, bucket) => Math.max(max, bucket.total), 0),
    [buckets]
  )

  const total = useMemo(
    () => buckets.reduce((sum, bucket) => sum + bucket.total, 0),
    [buckets]
  )

  /*!
   * Bars are positioned by timestamp rather than by index.
   *
   * Empty buckets are absent from the response — a gap is a gap, not a zero —
   * so laying them out by index would silently close every quiet period and
   * make an outage look like normal traffic.
   */
  const bars = useMemo(
    () =>
      buckets.map((bucket) => {
        const at = new Date(bucket.ts).getTime()

        return {
          bucket,
          left: ((at - range.from.getTime()) / span) * 100,
          width: (intervalMs / span) * 100,
          height: peak > 0 ? (bucket.total / peak) * 100 : 0
        }
      }),
    [buckets, intervalMs, peak, range.from, span]
  )

  const positionOf = (event: MouseEvent): number => {
    const element = surface.current
    if (!element) return 0

    const box = element.getBoundingClientRect()
    return Math.min(1, Math.max(0, (event.clientX - box.left) / box.width))
  }

  const timeAt = (fraction: number) => new Date(range.from.getTime() + fraction * span)

  /*!
   * The drag is tracked in a ref, and only mirrored into state for drawing.
   *
   * Reading it from state instead looks equivalent and is not: handlers close
   * over the value from their render, so any two mouse events arriving before
   * a re-render both see the *old* drag — and a fast drag, or one synthesised
   * by a test, silently does nothing. A ref is always current, which makes the
   * behaviour independent of when Preact happens to re-render.
   *
   * The move and release listeners go on the window rather than the surface,
   * because overshooting the chart while selecting is normal and the drag
   * should survive it.
   */
  const onMouseDown = (event: MouseEvent) => {
    if (0 !== event.button) return

    event.preventDefault()

    const at = positionOf(event)
    dragging.current = { from: at, to: at }
    setDrag({ from: at, to: at })

    const move = (moved: MouseEvent) => {
      if (!dragging.current) return

      const to = positionOf(moved)
      dragging.current = { ...dragging.current, to }
      setDrag({ ...dragging.current })
    }

    const release = () => {
      window.removeEventListener('mousemove', move)
      window.removeEventListener('mouseup', release)

      const current = dragging.current
      dragging.current = null
      setDrag(null)

      if (!current) return

      const [lo, hi] = [Math.min(current.from, current.to), Math.max(current.from, current.to)]

      // A click is not a zoom. Below about a percent of travel the user was
      // pointing at something, not selecting a window.
      if (hi - lo < 0.01) return

      onSelect(absolute(timeAt(lo), timeAt(hi)))
    }

    window.addEventListener('mousemove', move)
    window.addEventListener('mouseup', release)
  }

  const onMouseMove = (event: MouseEvent) => setHover(positionOf(event))

  const selection = drag
    ? { left: Math.min(drag.from, drag.to) * 100, width: Math.abs(drag.to - drag.from) * 100 }
    : null

  const hovered = null !== hover ? bars.find((bar) => hover * 100 >= bar.left && hover * 100 < bar.left + bar.width) : undefined

  /*!
   * Collapsed to its header by default, as the reference is.
   *
   * The chart is the best way to find a spike and the worst use of eighty
   * pixels when you already know what you are looking for — which is most
   * visits. Tucking it into a header keeps it one click away without spending
   * a tenth of the window on it, and the count stays on the bar so the header
   * still answers "how much matched".
   */
  return (
    <div class={`histogram ${collapsed ? 'collapsed' : ''} ${loading ? 'loading' : ''}`}>
      <button class="histogram-header" aria-expanded={!collapsed} onClick={onToggle}>
        <i class="histogram-caret" aria-hidden="true" />
        <span class="histogram-title">Timeline</span>
        <span class="histogram-summary">
          {total.toLocaleString()} {1 === total ? 'event' : 'events'}
        </span>
      </button>

      {!collapsed && (
      <div class="histogram-body">
      {/*
        A scale, so the chart says how big the spike actually is. Three rules
        and one number is the least that turns a row of bars into something you
        can read a quantity off — without it the tallest bar means "the most",
        and nothing more.
      */}
      <div class="histogram-scale" aria-hidden="true">
        <span>{peak.toLocaleString()}</span>
        <span>{Math.round(peak / 2).toLocaleString()}</span>
        <span>0</span>
      </div>

      <div
        class="histogram-surface"
        ref={surface}
        onMouseDown={onMouseDown}
        onMouseMove={onMouseMove}
        onMouseLeave={() => setHover(null)}
      >
        <div class="histogram-grid" aria-hidden="true">
          <i /><i /><i />
        </div>

        {bars.map((bar) => (
          <div
            key={bar.bucket.ts}
            class="histogram-bar"
            style={{ left: `${bar.left}%`, width: `${bar.width}%`, height: `${bar.height}%` }}
          >
            {stack(bar.bucket).map((part) => (
              <div
                key={part.level}
                class={`histogram-part level-${levelClass(part.level)}`}
                style={{ height: `${part.share * 100}%` }}
              />
            ))}
          </div>
        ))}

        {selection && (
          <div
            class="histogram-selection"
            style={{ left: `${selection.left}%`, width: `${selection.width}%` }}
          />
        )}

        {hovered && !drag && (
          <div class="histogram-tip" style={{ left: `${hovered.left + hovered.width / 2}%` }}>
            <strong>{hovered.bucket.total.toLocaleString()}</strong>
            <span>{formatStamp(new Date(hovered.bucket.ts))}</span>
          </div>
        )}
      </div>

      <div class="histogram-axis">
        <span>{formatStamp(range.from)}</span>
        <span class="histogram-count">
          {Math.round(intervalMs / 1000)}s buckets
        </span>
        <span>{formatStamp(range.to)}</span>
      </div>
      </div>
      )}
    </div>
  )
}

/** Splits a bucket into stacked shares, worst on top. */
function stack(bucket: Bucket): { level: string; share: number }[] {
  const counts = bucket.level ?? {}
  const named = Object.entries(counts).reduce((sum, [, n]) => sum + n, 0)

  const parts = LEVELS.filter((level) => counts[level] > 0)
    .reverse()
    .map((level) => ({ level, share: counts[level] / bucket.total }))

  // Records with no level at all still have to be drawn, or a bar of unlabelled
  // lines renders as empty space and reads as "nothing happened".
  if (named < bucket.total) {
    parts.push({ level: 'none', share: (bucket.total - named) / bucket.total })
  }

  return parts
}
