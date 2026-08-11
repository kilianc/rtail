import { useEffect, useLayoutEffect, useMemo, useRef } from 'preact/hooks'
import { buildFilter, formatTimestamp } from '../lib/format.js'
import type { Line } from '../lib/types.js'

interface Props {
  activeStream: string | null
  lines: Line[]
  filter: string
  ascending: boolean
  timestampsHidden: boolean
  paused: boolean
  onToggleTimestamps: () => void
  onPause: () => void
  onResume: () => void
}

export function StreamView({
  activeStream,
  lines,
  filter,
  ascending,
  timestampsHidden,
  paused,
  onToggleTimestamps,
  onPause,
  onResume
}: Props) {
  const scrollerRef = useRef<HTMLDivElement>(null)

  // Lines arrive in order, so insertion order is the source of truth. The old
  // implementation re-sorted by timestamp on every digest, which reordered
  // lines that shared a millisecond.
  const visible = useMemo(() => {
    const matches = buildFilter(filter)
    const filtered = lines.filter(matches)
    return ascending ? filtered : filtered.slice().reverse()
  }, [lines, filter, ascending])

  // Follow the tail. Skipped while paused so the viewport stays where the user
  // scrolled to.
  useLayoutEffect(() => {
    const el = scrollerRef.current
    if (!el || paused) return
    el.scrollTop = ascending ? el.scrollHeight : 0
  }, [visible, ascending, paused])

  // Scrolling away from the tail pauses the stream; the server stops sending
  // until the user resumes.
  useEffect(() => {
    const el = scrollerRef.current
    if (!el) return

    const onWheel = () => onPause()
    el.addEventListener('wheel', onWheel, { passive: true })

    return () => el.removeEventListener('wheel', onWheel)
  }, [onPause])

  useEffect(() => {
    if (!paused) return

    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      if (target && /^(INPUT|TEXTAREA)$/.test(target.tagName)) return
      if (' ' !== event.key) return
      event.preventDefault()
      onResume()
    }

    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [paused, onResume])

  if (!activeStream) {
    return (
      <div class="stream-view">
        <div class="stream-empty">
          <div class="stream-empty-mark" />
          <p>Select a stream to start tailing</p>
        </div>
      </div>
    )
  }

  return (
    // .no-timestamps sets --gutter-w to 0 for everything inside, so the
    // column and the control that sits on its edge cannot drift apart.
    <div class={`stream-view ${timestampsHidden ? 'no-timestamps' : ''}`}>
      <div class="stream-lines" ref={scrollerRef}>
        {visible.map((line) => (
          <div class="stream-line" key={line.key}>
            {!timestampsHidden && (
              <div class="stream-line-timestamp">{formatTimestamp(line.timestamp)}</div>
            )}
            <div
              class={`stream-line-content ${line.type}`}
              dangerouslySetInnerHTML={{ __html: line.html }}
            />
          </div>
        ))}
      </div>

      {/*
        Sits exactly on the timestamp column's edge. Both this and the column
        read --gutter-w, which drops to 0 via .no-timestamps — so no measuring
        and no chance of the two drifting apart.
      */}
      <button
        class={`btn-toggle-timestamp ${timestampsHidden ? 'closed' : ''}`}
        title="Toggle timestamp column"
        aria-label="Toggle timestamp column"
        aria-pressed={timestampsHidden}
        onClick={onToggleTimestamps}
      />

      {paused && (
        <button class="btn-resume" onClick={onResume}>
          Resume
        </button>
      )}
    </div>
  )
}
