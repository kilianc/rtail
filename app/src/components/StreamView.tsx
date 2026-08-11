import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import { defaultExpanded, formatTimestamp, renderFields } from '../lib/format.ts'
import { highlight } from '../lib/highlight.ts'
import type { Needle } from '../lib/query.ts'
import type { JsonView, Line } from '../lib/types.ts'

interface Props {
  activeStream: string | null
  /** Already filtered and ordered by App, which also counts them for the bar. */
  lines: Line[]
  /** What to mark in each line; empty when the filter box is empty. */
  needles: Needle[]
  /** JSON paths to show instead of the whole payload. */
  fields: string[]
  jsonView: JsonView
  /** Only the scroll anchor: App has already put the lines in this order. */
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
  needles,
  fields,
  jsonView,
  ascending,
  timestampsHidden,
  paused,
  onToggleTimestamps,
  onPause,
  onResume
}: Props) {
  const scrollerRef = useRef<HTMLDivElement>(null)

  // Which individual payloads the user has opened or closed by hand. Keyed by
  // line, so a row that has been opened stays open as lines arrive around it.
  const [toggled, setToggled] = useState<Record<number, boolean>>({})

  const toggle = useCallback((key: number, expanded: boolean) => {
    setToggled((current) => ({ ...current, [key]: expanded }))
  }, [])

  // Keys are monotonic and the buffer is capped, so the map would only ever
  // grow; a stream switch is the natural place to drop it.
  useEffect(() => setToggled({}), [activeStream])

  const rows = useMemo(() => {
    return lines.map((line) => {
      const expandable = null !== line.htmlCompact

      // With fields picked, the projection *is* the collapsed form — otherwise
      // "expand small payloads" would quietly undo the extraction.
      const projected = fields.length ? renderFields(line.content, fields) : null
      const fallback = projected ? false : defaultExpanded(jsonView, line)
      const expanded = expandable && (toggled[line.key] ?? fallback)

      const html = expanded ? line.html : (projected ?? line.htmlCompact ?? line.html)

      return { line, expandable, expanded, html: highlight(html, needles) }
    })
  }, [lines, needles, fields, jsonView, toggled])

  // Follow the tail. Skipped while paused so the viewport stays where the user
  // scrolled to.
  useLayoutEffect(() => {
    const el = scrollerRef.current
    if (!el || paused) return
    el.scrollTop = ascending ? el.scrollHeight : 0
  }, [rows, ascending, paused])

  // Scrolling away from the tail pauses the stream; the server stops sending
  // until the user resumes.
  useEffect(() => {
    const el = scrollerRef.current
    if (!el) return

    const onWheel = () => onPause()
    el.addEventListener('wheel', onWheel, { passive: true })

    return () => el.removeEventListener('wheel', onWheel)
    // activeStream matters even though it is not read here: the scroller only
    // exists once a stream is selected. Keyed on onPause alone, this ran once
    // against a null ref and never again, so scroll-to-pause did nothing for
    // anyone who picked their stream from the empty state.
  }, [onPause, activeStream])

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
        {rows.map(({ line, expandable, expanded, html }) => (
          <div class="stream-line" key={line.key}>
            {!timestampsHidden && (
              <div class="stream-line-timestamp">{formatTimestamp(line.timestamp)}</div>
            )}

            <div
              class={`stream-line-content ${line.type} ${
                expandable ? (expanded ? 'json expanded' : 'json collapsed') : ''
              }`}
            >
              {expandable && (
                <button
                  class="json-toggle"
                  title={expanded ? 'Collapse payload' : 'Expand payload'}
                  aria-label={expanded ? 'Collapse payload' : 'Expand payload'}
                  aria-expanded={expanded}
                  onClick={() => toggle(line.key, !expanded)}
                />
              )}

              <div class="stream-line-body" dangerouslySetInnerHTML={{ __html: html }} />
            </div>
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
