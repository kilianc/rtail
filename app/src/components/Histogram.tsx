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

export function Histogram({ collapsed, buckets, intervalMs, range, loading, onSelect }: Props) {
  const surface = useRef<HTMLDivElement>(null)
  const dragging = useRef<{ from: number; to: number } | null>(null)
  const [drag, setDrag] = useState<{ from: number; to: number } | null>(null)
  const [hover, setHover] = useState<number | null>(null)

  const span = range.to.getTime() - range.from.getTime()

  const peak = useMemo(
    () => buckets.reduce((max, bucket) => Math.max(max, bucket.total), 0),
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
   * Open by default, and folded from the top bar rather than from a header of
   * its own.
   *
   * It had a disclosure row above it — a strip of chrome whose only job was to
   * say "Timeline" and hold a caret, permanently spending a line of the window
   * to describe the thing directly below it. The toggle belongs with the other
   * view switches in the top bar, which leaves the chart itself to be chart.
   */
  if (collapsed) return null

  const ticks = [0, 0.25, 0.5, 0.75, 1].map((at) => timeAt(at))

  return (
    <div class={`histogram ${loading ? 'loading' : ''}`}>
      {/*
        One line of scale, in words: what is being counted, over what bucket,
        and how tall the tallest bar is. It replaced a three-number axis down
        the left edge, which cost a gutter to say the same thing less clearly —
        the number anyone reads off a histogram is the peak.
      */}
      <div class="histogram-head">
        <span class="histogram-metric">count / {bucketLabel(intervalMs)}</span>
        <span class="histogram-dot" aria-hidden="true">·</span>
        <span class="histogram-hint">drag to zoom</span>
        <span class="histogram-gap" />
        <span class="histogram-peak">peak {peak.toLocaleString()}</span>
      </div>

      <div
        class="histogram-surface"
        ref={surface}
        onMouseDown={onMouseDown}
        onMouseMove={onMouseMove}
        onMouseLeave={() => setHover(null)}
      >
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

      {/*
        Five evenly spaced ticks rather than the two ends. The window is the
        thing being navigated, and reading "about a third of the way along" off
        two labels is arithmetic nobody should be doing mid-incident.
      */}
      <div class="histogram-axis" aria-hidden="true">
        {ticks.map((at, index) => (
          <span key={index}>{formatStamp(at)}</span>
        ))}
      </div>
    </div>
  )
}

/*!
 * The bucket width, as a human says it.
 *
 * The server picks a bucket from the window, so this is read, never chosen —
 * and "count / 1m" is the sentence that makes a bar height mean something.
 */
function bucketLabel(ms: number): string {
  const seconds = Math.max(1, Math.round(ms / 1000))

  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h`
  return `${Math.round(seconds / 86400)}d`
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
