/*!
 * The result list.
 *
 * Virtualized, because a log viewer that stutters at ten thousand rows is a
 * log viewer nobody uses twice. Rows expand inline rather than into a side
 * panel — you lose your place in a panel — which is why the virtualizer has to
 * handle variable heights.
 *
 * Keyboard-first: j/k move, Enter expands, and following the tail is automatic
 * until you scroll away from it.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { useVirtual } from '../lib/virtual.js'
import type { Line } from '../lib/types.js'
import { LogRow, type FilterAction } from './LogRow.js'

interface Props {
  lines: Line[]
  /** Streaming, as opposed to a finished historical page. */
  live: boolean
  loading: boolean
  /** Fields the current query mentions. */
  active: string[]
  /** Substrings to mark in each message. */
  needles: string[]
  /** Fields promoted into columns of their own. */
  columns: string[]
  onDropColumn: (field: string) => void
  /** More history exists beyond what is loaded. */
  hasMore: boolean
  /** The feed is held; new records are counted rather than shown. */
  paused: boolean
  /** How many arrived while it was held. */
  pending: number
  emptyHint?: string
  onFilter: (action: FilterAction) => void
  onLoadMore: () => void
  onContext: (line: Line) => void
  /** Hold the feed — opening a row does this. */
  onPause: () => void
  onResume: () => void
}

/** One collapsed row, matching --line-h. */
const ROW_ESTIMATE = 20

export function Results({
  lines,
  live,
  loading,
  active,
  needles,
  columns,
  hasMore,
  paused,
  pending,
  emptyHint,
  onFilter,
  onLoadMore,
  onContext,
  onPause,
  onResume,
  onDropColumn
}: Props) {
  const [expanded, setExpanded] = useState<Set<number>>(() => new Set())
  const [selected, setSelected] = useState(-1)
  const [following, setFollowing] = useState(true)

  const virtual = useVirtual(lines.length, ROW_ESTIMATE)
  const previous = useRef(lines.length)

  /*!
   * Opening a row holds the feed.
   *
   * Expanding something is a statement that you want to read it, and a live
   * tail will have pushed it off the screen before you have. Closing it does
   * not resume on its own — you may have opened a row to compare against one
   * further up — so resuming stays an explicit act.
   */
  const toggle = useCallback((key: number) => {
    setExpanded((current) => {
      const next = new Set(current)

      if (next.has(key)) {
        next.delete(key)
      } else {
        next.add(key)
        onPause()
      }

      return next
    })
  }, [onPause])

  /*!
   * Follow the tail while streaming, but only while the user is already at the
   * bottom. Yanking the viewport back down while someone is reading history is
   * the single most irritating thing a live log view can do.
   */
  useLayoutEffect(() => {
    if (!live || !following || paused) return
    if (lines.length === previous.current) return

    previous.current = lines.length
    virtual.scrollToEnd()
  }, [lines.length, live, following, virtual])

  useEffect(() => {
    if (!live) return
    setFollowing(virtual.atEnd)
  }, [live, virtual.atEnd])

  // Historical results are read from the top; streaming ones from the bottom.
  useEffect(() => {
    if (live) setFollowing(true)
  }, [live])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      if (target && /^(INPUT|TEXTAREA)$/.test(target.tagName)) return

      switch (event.key) {
        case 'j':
        case 'ArrowDown':
          event.preventDefault()
          setSelected((n) => {
            const next = Math.min(lines.length - 1, n + 1)
            virtual.scrollTo(next)
            return next
          })
          break

        case 'k':
        case 'ArrowUp':
          event.preventDefault()
          setSelected((n) => {
            const next = Math.max(0, n - 1)
            virtual.scrollTo(next)
            return next
          })
          break

        case 'Enter':
          if (selected >= 0 && lines[selected]) {
            event.preventDefault()
            toggle(lines[selected].key)
          }
          break

        case 'Escape':
          setExpanded(new Set())
          setSelected(-1)
          break
      }
    }

    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [lines, selected, toggle, virtual])

  /*!
   * The grid template lives on the panel, not on the header and the rows.
   *
   * Both read the same custom property, so adding a column widens the header
   * and the rows in one step and they cannot drift — which is exactly the bug
   * that existed before the two shared a template at all. Added columns are
   * given a sensible band rather than a fixed width: wide enough for a service
   * name, capped so one long value cannot squeeze the summary out.
   */
  const template = [
    'var(--caret-w)',
    'var(--sev-w)',
    'var(--gutter-w)',
    ...columns.map(() => 'minmax(5rem, 10rem)'),
    '1fr'
  ].join(' ')

  if (0 === lines.length) {
    return (
      <div class="results" style={{ '--row-template': template } as never}>
        <div class="results-empty">
          {loading ? (
            <p>Searching…</p>
          ) : (
            <>
              <div class="results-empty-mark" />
              <p>{emptyHint ?? 'No matching events'}</p>
            </>
          )}
        </div>
      </div>
    )
  }

  return (
    <div class="results" style={{ '--row-template': template } as never}>
      {/*
        A sticky column header, as Cloud Logging has. It is not decoration: a
        dense table of timestamps and text needs its columns named once, and
        the header is also what makes the caret column read as a control rather
        than as stray punctuation.
      */}
      <div class="results-header">
        <span class="results-col-caret" />
        {/* Abbreviated: the column is one chip wide, and "Severity" is not. */}
        <span class="results-col-level" title="Severity">Sev</span>
        <span class="results-col-time">Timestamp</span>

        {columns.map((field) => (
          <span key={field} class="results-col-added" title={field}>
            <span class="results-col-name">{field}</span>
            <button
              class="results-col-drop"
              aria-label={`Remove the ${field} column`}
              onClick={() => onDropColumn(field)}
            >
              ×
            </button>
          </span>
        ))}

        <span class="results-col-summary">Summary</span>
      </div>

      <div class="results-scroll" ref={virtual.scrollRef}>
        <div class="results-spacer" style={{ height: `${virtual.total}px` }}>
          {lines.slice(virtual.start, virtual.end).map((line, index) => (
            <div
              key={line.key}
              class="results-row"
              ref={virtual.measure(virtual.start + index)}
              style={{ transform: `translateY(${virtual.offsets[index]}px)` }}
            >
              <LogRow
                line={line}
                expanded={expanded.has(line.key)}
                selected={virtual.start + index === selected}
                active={active}
                needles={needles}
                columns={columns}
                onToggle={() => {
                  setSelected(virtual.start + index)
                  toggle(line.key)
                }}
                onFilter={onFilter}
                onContext={() => onContext(line)}
              />
            </div>
          ))}
        </div>

        {hasMore && !live && (
          <button class="results-more" disabled={loading} onClick={onLoadMore}>
            {loading ? 'Loading…' : 'Load older events'}
          </button>
        )}
      </div>

      {live && (paused || !following) && (
        <button
          class="results-follow"
          onClick={() => {
            setFollowing(true)
            onResume()
            virtual.scrollToEnd()
          }}
        >
          {paused ? 'Resume' : 'Jump to latest'}
          {pending > 0 && <span class="results-pending">{pending.toLocaleString()}</span>}
        </button>
      )}
    </div>
  )
}
