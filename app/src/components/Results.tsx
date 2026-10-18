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
import { formatBytes } from '../lib/format.js'
import { PAGE, type Scanned, type Store } from '../lib/api.js'
import type { Line } from '../lib/types.js'
import { Button } from './Button.js'
import { Popover } from './Popover.js'
import { LogRow, type FilterAction } from './LogRow.js'

interface Props {
  lines: Line[]
  /** Streaming, as opposed to a finished historical page. */
  live: boolean
  loading: boolean
  /** What the last historical query touched, for the cost readout. */
  scanned: Scanned | null
  /** The whole store, so the files touched can be given as a fraction. */
  store: Store | null
  /** Fields the current query mentions. */
  active: string[]
  /** Substrings to mark in each message. */
  needles: string[]
  /** Fields promoted into columns of their own. */
  columns: string[]
  /** Of those, the ones holding numbers, which align to the right. */
  numeric: string[]
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
  scanned,
  store,
  active,
  needles,
  columns,
  numeric,
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
  const [showColumns, setShowColumns] = useState(false)
  const columnsRef = useRef<HTMLButtonElement>(null)
  const [selected, setSelected] = useState(-1)
  const [following, setFollowing] = useState(true)

  const virtual = useVirtual(lines.length, ROW_ESTIMATE)
  const previous = useRef(lines.length)

  /*!
   * Whether the view has reached the tail yet.
   *
   * Starts false: a live view opens scrolled to the top of a list that is
   * already taller than it, and that is not somebody choosing to read
   * history. It becomes true once the tail has actually been reached, and
   * only then does leaving it mean anything.
   */
  const wasAtEnd = useRef(false)

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

  /*!
   * Scrolling away from the tail holds it, as opening a row does.
   *
   * It used to only stop *following* — the feed kept arriving, and because
   * the live buffer drops from the front once it is full, every new line slid
   * the one being read up by a row. Scrolling back to read something is the
   * clearest statement there is that the screen should stop moving, and the
   * button that appears says "Jump to latest", which is a promise that you
   * are no longer being shown it. Held, the count of what has arrived since
   * is what the pending badge is for.
   */
  useEffect(() => {
    if (!live) return

    setFollowing(virtual.atEnd)

    // Only a move *away* from the tail holds it. Before the first
    // scroll-to-end has run the list is already taller than the viewport and
    // sitting at the top, and reading that frame as "the reader scrolled
    // away" would open every live view already paused.
    if (wasAtEnd.current && !virtual.atEnd) onPause()
    wasAtEnd.current = virtual.atEnd
  }, [live, virtual.atEnd, onPause])

  // Historical results are read from the top; streaming ones from the bottom.
  useEffect(() => {
    if (live) {
      setFollowing(true)
      wasAtEnd.current = false
    }
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
    // Timestamp before severity: time is what you scan a log by, so it sits
    // closest to the caret, and the severity column reads as an annotation on
    // the line rather than as its primary key.
    'var(--gutter-w)',
    'var(--sev-w)',
    // Fixed, not elastic: the summary takes every pixel the pinned columns
    // do not, and a column sized to its content moves the line you are
    // reading every time a longer value scrolls past.
    ...columns.map(() => 'var(--col-w)'),
    '1fr'
  ].join(' ')

  const follow = () => {
    setFollowing(true)
    onResume()
    virtual.scrollToEnd()
  }

  return (
    <div class="results" style={{ '--row-template': template } as never}>
      {/*
        What the last query cost, on one line above the columns.
        
        A search interface that hides how much work it just did teaches nobody
        why one query is instant and another is not — and the count of what
        came back is the first thing anyone looks for, so it leads.
      */}
      <div class="results-stats">
        <span class="results-matched">
          <strong>{lines.length.toLocaleString()}</strong> {live ? 'lines' : 'matched'}
          {hasMore && !live ? '+' : ''}
        </span>

        {!live && scanned && (
          <>
            <span class="results-dot" aria-hidden="true">·</span>
            {/*
              The files read, out of the files there are. A bare "3 files"
              says nothing about whether the planner did well; "3 of 184"
              is the whole case for keeping a catalog.
            */}
            <span>
              {scanned.files.toLocaleString()}
              {store ? ` of ${store.files.toLocaleString()}` : ''}
              {1 === scanned.files && !store ? ' file' : ' files'}
            </span>
            <span class="results-dot" aria-hidden="true">·</span>
            <span>{formatBytes(scanned.bytes)} in {scanned.millis.toLocaleString()} ms</span>
          </>
        )}

        <span class="results-gap" />

        {/*
          How many fields are pinned out of the summary into columns of their
          own. It is a property of this view, like following, so it reads
          from the same strip — and it is the only place the set can be seen
          as a set rather than one × at a time in the header below.
        */}
        <Button
          variant="outline"
          size="sm"
          // On when its own list is open, not when columns exist. The accent
          // on this row means "the feed is following"; a second permanent
          // accent beside it left neither reading as a state.
          on={showColumns}
          aria-expanded={showColumns}
          elementRef={columnsRef}
          onClick={() => setShowColumns((open) => !open)}
        >
          columns {columns.length}
        </Button>

        {showColumns && (
          <Popover anchor={columnsRef.current} class="popover-columns" onClose={() => setShowColumns(false)}>
            <h4>Pinned columns</h4>

            {0 === columns.length ? (
              <p class="popover-note">None pinned. Pin a field with ⊞ in the explorer.</p>
            ) : (
              <ul class="popover-list">
                {columns.map((field) => (
                  <li key={field}>
                    <span>{field}</span>
                    <Button
                      variant="icon"
                      size="sm"
                      aria-label={`Remove the ${field} column`}
                      onClick={() => onDropColumn(field)}
                    >
                      ×
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </Popover>
        )}

        {/*
          Following is a state of this pane, so its switch lives in this
          pane's own header rather than floating over the rows. The pending
          count rides on it: while the feed is held, the number is the reason
          you would press it.
        */}
        {live && (
          <Button
            variant="outline"
            size="sm"
            on={following && !paused}
            title={paused ? 'Resume the feed' : 'Follow the tail'}
            onClick={follow}
          >
            following
            {pending > 0 && <span class="results-pending">{pending.toLocaleString()}</span>}
          </Button>
        )}
      </div>

      {/*
        A sticky column header, as Cloud Logging has. It is not decoration: a
        dense table of timestamps and text needs its columns named once, and
        the header is also what makes the caret column read as a control rather
        than as stray punctuation.
      */}
      <div class="results-header">
        <span class="results-col-caret" />
        <span class="results-col-time">Timestamp</span>
        {/* Abbreviated: the column is three characters wide, and "Severity" is not. */}
        <span class="results-col-level" title="Severity">Sev</span>

        {columns.map((field) => (
          <span
            key={field}
            class={`results-col-added ${numeric.includes(field) ? 'numeric' : ''}`}
            title={field}
          >
            <span class="results-col-name">{field}</span>
            <Button
              variant="icon"
              size="sm"
              class="results-col-drop"
              aria-label={`Remove the ${field} column`}
              onClick={() => onDropColumn(field)}
            >
              ×
            </Button>
          </span>
        ))}

        <span class="results-col-summary">Summary</span>
      </div>

      {0 === lines.length ? (
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
      ) : (
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
                  numeric={numeric}
                  onToggle={() => {
                    setSelected(virtual.start + index)
                    toggle(line.key)
                  }}
                  onFilter={onFilter}
                  onPin={onDropColumn}
                  onContext={() => onContext(line)}
                />
              </div>
            ))}
          </div>
        </div>
      )}

      {/*
        The footer says how the page was cut, because keyset pagination is why
        the next page is instant and why it cannot skip to page nine — and
        "older" is the only control that makes sense next to that sentence.
      */}
      {!live && lines.length > 0 && (
        <div class="results-footer">
          <span>keyset (ts, seq) · {lines.length.toLocaleString()} loaded</span>
          <span class="results-gap" />
          {hasMore && (
            <Button variant="outline" size="sm" disabled={loading} onClick={onLoadMore}>
              {loading ? 'loading…' : `older ${PAGE}`}
            </Button>
          )}
        </div>
      )}

      {/*
        One floating control, and only when the tail has run away from you:
        the header switch is where following is turned on, but when you are
        a thousand rows up it is off-screen, and the jump has to be where
        your eyes are.
      */}
      {live && (paused || !following) && (
        <Button variant="solid" class="results-follow" onClick={follow}>
          {paused ? 'Resume' : 'Jump to latest'}
          {pending > 0 && <span class="results-pending">{pending.toLocaleString()}</span>}
        </Button>
      )}
    </div>
  )
}
