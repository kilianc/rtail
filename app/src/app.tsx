/*!
 * The explorer.
 *
 * One filter drives everything. Typed into the command bar it narrows the live
 * tail as you type — the server compiles the same rQL to a predicate over the
 * ingest stream — and pressing Enter with a time range that is not "now" runs
 * it as a historical search instead. Same query, same semantics, two sources,
 * because rql has two compilers behind one AST.
 *
 * Everything that changes what you are looking at is in the URL, so any view
 * is a link.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { CommandBar } from './components/CommandBar.js'
import { FieldExplorer } from './components/FieldExplorer.js'
import { Histogram } from './components/Histogram.js'
import { Results } from './components/Results.js'
import { StreamPicker } from './components/StreamPicker.js'
import { TimeRange } from './components/TimeRange.js'
import { TopBar } from './components/TopBar.js'
import { api, QueryError, type Bucket, type Field, type Params } from './lib/api.js'
import { connect, type Connection } from './lib/connection.js'
import { formatLine } from './lib/format.js'
import { loadPrefs, savePrefs } from './lib/prefs.js'
import { fieldsUsed, highlightTerms, toggleTerm } from './lib/query.js'
import { absolute, isRelative, resolve, type Range } from './lib/timerange.js'
import type { Line, Prefs } from './lib/types.js'
import { onChange, read, write, type ViewState } from './lib/urlstate.js'

/** How many live lines to keep before dropping the oldest. */
const LIVE_BUFFER = 2000

/** One page of history. */
const PAGE = 200

export function App() {
  const [prefs, setPrefs] = useState<Prefs>(loadPrefs)
  const [view, setView] = useState<ViewState>(read)
  const [draft, setDraft] = useState(view.query)

  const [streams, setStreams] = useState<string[]>([])
  const [fields, setFields] = useState<Field[]>([])

  const [lines, setLines] = useState<Line[]>([])
  const [buckets, setBuckets] = useState<Bucket[]>([])
  const [interval, setInterval] = useState(60_000)

  const [cursor, setCursor] = useState<string | undefined>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<{ message: string; position?: number } | null>(null)
  const [scanned, setScanned] = useState<string>('')

  /*!
   * Holding the feed.
   *
   * Records keep arriving over the socket while held — dropping the
   * subscription would leave a gap nothing could fill afterwards — but they
   * are counted instead of rendered, so the list under a reader's eyes stops
   * moving. The ref shadows the state because the socket handler is created
   * once and would otherwise close over the initial value forever.
   */
  const [held, setHeld] = useState(false)
  const [pendingCount, setPendingCount] = useState(0)
  const heldRef = useRef(false)

  const socket = useRef<Connection | null>(null)
  const pending = useRef<AbortController | null>(null)

  const params: Params = useMemo(
    () => ({
      q: view.query,
      lang: view.lang,
      stream: view.stream,
      from: view.range.from,
      to: view.range.to,
      limit: PAGE
    }),
    [view.query, view.lang, view.stream, view.range.from, view.range.to]
  )

  // Both read rQL. Against a SQL expression they would be guessing, so they
  // stand down rather than highlight the wrong thing.
  const active = useMemo(
    () => ('sql' === view.lang ? [] : fieldsUsed(view.query)),
    [view.query, view.lang]
  )
  const needles = useMemo(
    () => ('sql' === view.lang ? [] : highlightTerms(view.query)),
    [view.query, view.lang]
  )

  /*!
   * Promoting a field to a column.
   *
   * Toggling rather than adding: the same control that put a column there
   * takes it away, so there is no hunting for a second affordance. Order is
   * insertion order, which is the only order the user chose.
   */
  const toggleColumn = useCallback((field: string) => {
    setView((current) => ({
      ...current,
      columns: current.columns.includes(field)
        ? current.columns.filter((name) => name !== field)
        : [...current.columns, field]
    }))
  }, [])
  const resolved = useMemo(() => resolve(view.range), [view.range])

  useEffect(() => write(view), [view])

  /*!
   * Back, forward, and hand-edited URLs.
   *
   * The draft has to follow, or navigating to a shared link leaves the command
   * bar showing whatever was typed last while the results show the link's
   * query. Our own writes use replaceState, which fires no hashchange, so this
   * cannot fight the user mid-keystroke.
   */
  useEffect(
    () =>
      onChange((next) => {
        setView(next)
        setDraft(next.query)
      }),
    []
  )
  useEffect(() => savePrefs(prefs), [prefs])

  useEffect(() => {
    document.body.className =
      `${prefs.theme} font-family-${prefs.fontFamily} font-size-${prefs.fontSize}`
  }, [prefs.theme, prefs.fontFamily, prefs.fontSize])

  useEffect(() => {
    document.title = view.stream ? `rTail : ${view.stream}` : 'rTail'
  }, [view.stream])

  /*!
   * The live connection.
   *
   * Reopened whenever the stream or the committed filter changes, because both
   * are part of the subscription URL. The draft is deliberately not — narrowing
   * a live tail on every keystroke would reconnect on every keystroke.
   */
  useEffect(() => {
    if (!view.live) {
      socket.current?.close()
      socket.current = null
      return
    }

    const connection = connect()
    socket.current = connection

    connection.on('streams', setStreams)

    connection.on('backlog', (backlog) => {
      setLines((backlog ?? []).map(formatLine))
    })

    connection.on('line', (wire) => {
      if (heldRef.current) {
        setPendingCount((n) => n + 1)
        return
      }

      setLines((current) => {
        const next = current.length >= LIVE_BUFFER ? current.slice(1) : current.slice()
        next.push(formatLine(wire))
        return next
      })
    })

    connection.on('error', (message, position) => setError({ message, position }))

    heldRef.current = false
    setHeld(false)
    setPendingCount(0)

    connection.subscribe(view.stream, view.query)

    return () => {
      connection.close()
      socket.current = null
    }
  }, [view.live, view.stream, view.query])

  // The stream list still has to arrive when the tail is not running.
  useEffect(() => {
    if (view.live) return

    const controller = new AbortController()
    api.streams(controller.signal).then((response) => setStreams(response.streams)).catch(() => {})

    return () => controller.abort()
  }, [view.live])

  // Autocomplete needs the key inventory, which is per stream.
  useEffect(() => {
    const controller = new AbortController()

    api
      .schema(view.stream, controller.signal)
      .then((response) => setFields(response.fields))
      .catch(() => setFields([]))

    return () => controller.abort()
  }, [view.stream])

  /*!
   * runSearch replaces the result list with a page of history.
   */
  const runSearch = useCallback(
    async (append = false) => {
      pending.current?.abort()

      const controller = new AbortController()
      pending.current = controller

      setLoading(true)
      setError(null)

      try {
        const response = await api.search(
          { ...params, cursor: append ? cursor : undefined },
          controller.signal
        )

        const page = response.records.map(formatLine)

        setLines((current) => (append ? [...current, ...page] : page))
        setCursor(response.cursor)
        setScanned(
          `${response.scanned.files} ${1 === response.scanned.files ? 'file' : 'files'} · ${response.scanned.millis}ms`
        )
      } catch (err) {
        if (controller.signal.aborted) return

        if (err instanceof QueryError) {
          setError({ message: err.message, position: err.position })
        } else {
          setError({ message: String(err) })
        }
      } finally {
        if (!controller.signal.aborted) setLoading(false)
      }
    },
    [params, cursor]
  )

  // The histogram follows the committed query, in live mode too — it is the
  // only view of what is outside the buffer.
  useEffect(() => {
    const controller = new AbortController()

    api
      .histogram({ ...params, buckets: 80 }, controller.signal)
      .then((response) => {
        setBuckets(response.buckets)
        setInterval(response.interval_ms)
      })
      .catch(() => setBuckets([]))

    return () => controller.abort()
  }, [params.q, params.lang, params.stream, params.from, params.to])

  // In history mode the query runs whenever the committed state changes.
  useEffect(() => {
    if (view.live) return
    setCursor(undefined)
    runSearch(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view.live, params.q, params.lang, params.stream, params.from, params.to])

  /*!
   * Switching language.
   *
   * SQL forces history: the live path compiles the query to a Go predicate and
   * evaluates it per record, and there is no in-process SQL engine to do that
   * with. Streaming a SQL filter would mean either ignoring it or shipping
   * every record to DuckDB one at a time, and both are worse than saying so.
   */
  const setLang = useCallback((lang: 'rql' | 'sql') => {
    setView((current) => ({
      ...current,
      lang,
      live: 'sql' === lang ? false : current.live
    }))
  }, [])

  /*!
   * commit promotes the draft to the committed query.
   *
   * Pressing Enter on a relative range keeps streaming with the new filter;
   * on a pinned range there is nothing to stream, so it searches.
   */
  const commit = useCallback(() => {
    setView((current) => ({ ...current, query: draft }))
  }, [draft])

  const setRange = useCallback((range: Range) => {
    setView((current) => ({
      ...current,
      range,
      // Dragging on the histogram means "look at then", which is not a thing
      // a live tail can do.
      live: current.live && isRelative(range)
    }))
  }, [])

  const applyFilter = useCallback(
    (field: string, op: string, value: string, negated: boolean) => {
      setView((current) => {
        const query = toggleTerm(current.query, field, op, value, negated)
        setDraft(query)
        return { ...current, query }
      })
    },
    []
  )

  /*!
   * showContext drops the filter and pins a window around one line.
   *
   * The feature people actually debug with: what happened *around* this, not
   * what else matched. Most tools bury it.
   */
  const showContext = useCallback((line: Line) => {
    const at = new Date(line.timestamp)

    setDraft('')
    setView((current) => ({
      ...current,
      query: '',
      live: false,
      range: absolute(new Date(at.getTime() - 30_000), new Date(at.getTime() + 30_000))
    }))
  }, [])

  const hold = useCallback(() => {
    heldRef.current = true
    setHeld(true)
  }, [])

  /*!
   * Resuming reconnects rather than draining a buffer.
   *
   * Whatever arrived while held was counted, not kept — keeping it would mean
   * an unbounded buffer for a tab somebody left open over lunch. Re-opening
   * the feed replays the server's backlog, which is the same bounded window a
   * fresh tab would get.
   */
  const resume = useCallback(() => {
    heldRef.current = false
    setHeld(false)
    setPendingCount(0)
    socket.current?.reconnect()
  }, [])

  const toggleLive = useCallback(() => {
    setView((current) => {
      const live = !current.live

      return {
        ...current,
        live,
        // Resuming the tail from a pinned window has to unpin it, or the tail
        // would stream into a range that has already ended.
        range: live && !isRelative(current.range) ? { from: '-1h', to: '' } : current.range
      }
    })
  }, [])

  return (
    <>
      <TopBar
        prefs={prefs}
        live={view.live}
        scanned={scanned}
        onChangePrefs={(patch) => setPrefs((current) => ({ ...current, ...patch }))}
        onToggleLive={toggleLive}
      >
        <StreamPicker
          streams={streams}
          active={view.stream}
          favorites={prefs.favorites}
          onSelect={(stream) => setView((current) => ({ ...current, stream }))}
          onToggleFavorite={(stream) =>
            setPrefs((current) => ({
              ...current,
              favorites: current.favorites.includes(stream)
                ? current.favorites.filter((name) => name !== stream)
                : [...current.favorites, stream]
            }))
          }
        />
      </TopBar>

      {/*
        The time range is passed *into* the editor rather than placed beside
        it. It is part of the question being asked — it is the partition
        pruner, and it is always applied — so it belongs with the query text
        rather than in a separate row of controls.
      */}
      <div class="query-row">
        <CommandBar
          value={draft}
          lang={view.lang}
          onChangeLang={setLang}
          fields={fields}
          error={error}
          busy={loading}
          onChange={setDraft}
          onSubmit={commit}
        >
          <TimeRange range={view.range} onChange={setRange} />
        </CommandBar>
      </div>

      <Histogram
        collapsed={!view.timeline}
        onToggle={() => setView((current) => ({ ...current, timeline: !current.timeline }))}
        buckets={buckets}
        intervalMs={interval}
        range={resolved}
        loading={loading}
        onSelect={setRange}
      />

      <div class="explorer-split">
        <FieldExplorer
          fields={fields}
          params={params}
          active={active}
          columns={view.columns}
          onFilter={(field, value, negated) => applyFilter(field, '=', value, negated)}
          onToggleColumn={toggleColumn}
        />

        <Results
          lines={lines}
          live={view.live}
          loading={loading}
          active={active}
          needles={needles}
          columns={view.columns}
          hasMore={!!cursor}
          paused={held}
          pending={pendingCount}
          emptyHint={
            view.live
              ? 'Waiting for events — pipe something into rtail'
              : 'No events matched in this time range'
          }
          onFilter={(action) => applyFilter(action.field, action.op, action.value, action.negated)}
          onLoadMore={() => runSearch(true)}
          onContext={showContext}
          onPause={hold}
          onResume={resume}
          onDropColumn={toggleColumn}
        />
      </div>
    </>
  )
}
