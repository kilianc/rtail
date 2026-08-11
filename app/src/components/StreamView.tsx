import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import { buildFilter, formatTimestamp } from '../lib/format.js'
import type { Line } from '../lib/types.js'

interface Props {
  activeStream: string | null
  lines: Line[]
  ascending: boolean
  isFavorite: boolean
  timestampsHidden: boolean
  paused: boolean
  onToggleFavorite: () => void
  onToggleTimestamps: () => void
  onPause: () => void
  onResume: () => void
}

export function StreamView({
  activeStream,
  lines,
  ascending,
  isFavorite,
  timestampsHidden,
  paused,
  onToggleFavorite,
  onToggleTimestamps,
  onPause,
  onResume
}: Props) {
  const [pattern, setPattern] = useState('')
  const scrollerRef = useRef<HTMLDivElement>(null)
  const timestampRef = useRef<HTMLDivElement>(null)
  const [timestampWidth, setTimestampWidth] = useState(0)

  // Lines arrive in order, so insertion order is the source of truth. The old
  // implementation re-sorted by timestamp on every digest, which reordered
  // lines that shared a millisecond.
  const visible = useMemo(() => {
    const matches = buildFilter(pattern)
    const filtered = lines.filter(matches)
    return ascending ? filtered : filtered.slice().reverse()
  }, [lines, pattern, ascending])

  // Follow the tail. Skipped while paused so the viewport stays where the user
  // scrolled to.
  useLayoutEffect(() => {
    const el = scrollerRef.current
    if (!el || paused) return
    el.scrollTop = ascending ? el.scrollHeight : 0
  }, [visible, ascending, paused])

  // Position the collapse control on the timestamp column's edge. The old
  // template bound this to a scope property that was never assigned, so the
  // button always sat at the far left of the viewport.
  useLayoutEffect(() => {
    setTimestampWidth(timestampsHidden ? 0 : (timestampRef.current?.offsetWidth ?? 0))
  }, [visible, timestampsHidden])

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

  return (
    <div class="stream-view">
      <div class="stream-header">
        {activeStream && (
          <button class="stream-title" onClick={onToggleFavorite}>
            <i class={`stream-title-favorite ${isFavorite ? 'on' : ''}`} />
            {activeStream}
          </button>
        )}

        <div class="filter-box">
          <input
            type="text"
            placeholder="filter stream (regexp allowed)"
            aria-label="Filter stream"
            value={pattern}
            onInput={(event) => setPattern(event.currentTarget.value)}
          />
        </div>
      </div>

      <div class="stream-lines" ref={scrollerRef}>
        {visible.map((line, index) => (
          <div class="stream-line" key={line.key}>
            {!timestampsHidden && (
              <div class="stream-line-timestamp" ref={0 === index ? timestampRef : undefined}>
                {formatTimestamp(line.timestamp)}
              </div>
            )}
            <div
              class={`stream-line-content ${line.type}`}
              dangerouslySetInnerHTML={{ __html: line.html }}
            />
          </div>
        ))}
      </div>

      {activeStream && (
        <button
          class={`btn-toggle-timestamp ${timestampsHidden ? 'closed' : ''}`}
          style={{ left: `${timestampWidth}px` }}
          title="Toggle timestamp column"
          aria-label="Toggle timestamp column"
          onClick={onToggleTimestamps}
        />
      )}

      {paused && (
        <button class="btn-resume" onClick={onResume}>
          Resume
        </button>
      )}
    </div>
  )
}
