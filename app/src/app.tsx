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
import { api, PAGE, QueryError, type Bucket, type Field, type Params, type Scanned, type Store } from './lib/api.js'
import { connect, type Connection } from './lib/connection.js'
import { formatLine } from './lib/format.js'
import { loadPrefs, savePrefs } from './lib/prefs.js'
import { fieldsUsed, highlightTerms, toggleTerm, type Scalar } from './lib/query.js'
import { absolute, isRelative, resolve, type Range } from './lib/timerange.js'
import type { Line, Prefs } from './lib/types.js'
import { onChange, read, write, type ViewState } from './lib/urlstate.js'

/** How many live lines to keep before dropping the oldest. */
const LIVE_BUFFER = 2000

/*!
 * streaming is the view, live.
 *
 * Resuming the tail from a pinned window has to unpin it, or the tail would
 * stream into a range that has already ended. And it has to be rQL: SQL forces
 * history (see setLang), and the tail compiles nothing else, so a SQL filter
 * sent to it would be read as rQL. Relabelling the expression did exactly
 * that, so coming from SQL the tail starts unfiltered — the caller clears the
 * draft to match.
 */
function streaming(current: ViewState): ViewState {
  return {
    ...current,
    live: true,
    lang: 'rql',
    query: 'sql' === current.lang ? '' : current.query,
    range: isRelative(current.range) ? current.range : { from: '-1h', to: '' }
  }
}

/** One page of history. */

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
  const [scanned, setScanned] = useState<Scanned | null>(null)
  const [store, setStore] = useState<Store | null>(null)

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

  /** Lines the server skipped because this tab fell behind, this subscription. */
  const [dropped, setDropped] = useState(0)

  /*!
   * How many times the query has been run.
   *
   * Not part of the view and not in the URL: it exists so that Run with the
   * text unchanged still runs. Nothing else changes then, so no effect fired,
   * and a relative window could not be brought up to date nor a transient
   * failure retried without editing the query to something else and back.
   */
  const [runs, setRuns] = useState(0)

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
    document.title = view.stream ? `rtail : ${view.stream}` : 'rtail'
  }, [view.stream])

  /*!
   * The live connection.
   *
   * Reopened whenever the stream or the committed filter changes, because both
   * are part of the subscription URL. The draft is deliberately not — narrowing
   * a live tail on every keystroke would reconnect on every keystroke.
   *
   * Nor is `runs`. Run on an unchanged live query refreshes the histogram, but
   * the tail is already the latest there is; re-opening it would only replay
   * the backlog and collapse whatever row was open.
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

    /*!
     * A backlog while held is ignored.
     *
     * One arrives on every re-open, and not only the one resume asks for: the
     * server draining on a restart and the retry after a 502 re-open the feed
     * too. Taken while held, it swapped the list from under a reader and
     * collapsed the row they had open. Resuming re-opens the feed, and the
     * backlog that brings is the one that lands.
     */
    connection.on('backlog', (backlog) => {
      if (heldRef.current) return
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

    connection.on('dropped', (count) => setDropped((n) => n + count))

    connection.on('error', (message, position) => setError({ message, position }))

    heldRef.current = false
    setHeld(false)
    setPendingCount(0)
    setDropped(0)

    // Whatever the last search said is about a query that is no longer
    // running: a history 500 stayed red over a tail that was working fine.
    setError(null)

    connection.subscribe(view.stream, view.query)

    return () => {
      connection.close()
      socket.current = null
    }
  }, [view.live, view.stream, view.query])

  /*!
   * How much log there is, on a slow timer.
   *
   * It is the one number that is about the install rather than the query, so
   * it sits in the top bar and refreshes on its own — a minute is often
   * enough, because the answer changes by compaction and ingest, not by
   * anything the person is doing.
   */
  useEffect(() => {
    const controller = new AbortController()

    const load = () =>
      api.health(controller.signal).then((response) => setStore(response.store ?? null)).catch(() => {})

    load()
    const timer = window.setInterval(load, 60_000)

    return () => {
      controller.abort()
      window.clearInterval(timer)
    }
  }, [])

  // The stream list still has to arrive when the tail is not running.
  useEffect(() => {
    if (view.live) return

    const controller = new AbortController()
    api.streams(controller.signal).then((response) => setStreams(response.streams)).catch(() => {})

    return () => controller.abort()
  }, [view.live])

  /*!
   * Autocomplete needs the key inventory, which is per stream — and the
   * explorer needs each key's count *within the current query*, which is why
   * this re-runs on the query and the window too and not only on the stream.
   */
  useEffect(() => {
    const controller = new AbortController()

    api
      .schema(params, controller.signal)
      .then((response) => setFields(response.fields))
      .catch(() => setFields([]))

    return () => controller.abort()
  }, [params.q, params.lang, params.stream, params.from, params.to])

  /*!
   * Which pinned columns hold numbers.
   *
   * Read from the catalog's own kinds rather than guessed from the values on
   * screen: a column that happens to be all digits on this page and words on
   * the next would change alignment as you scroll, which is worse than being
   * left-aligned throughout.
   */
  const numeric = useMemo(
    () =>
      fields
        .filter((field) => ['int', 'float', 'number', 'bigint', 'double'].includes(field.kind))
        .map((field) => field.name),
    [fields]
  )

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
        setScanned(response.scanned)
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
  }, [params.q, params.lang, params.stream, params.from, params.to, runs])

  // In history mode the query runs whenever the committed state changes, and
  // whenever it is run again.
  useEffect(() => {
    /*!
     * Going live cancels a search still in flight.
     *
     * Left running it lands after the backlog has, and replaces the live list
     * with a page of history that the tail then appends to. Its own finally
     * leaves the spinner alone once aborted, so that is cleared here.
     */
    if (view.live) {
      pending.current?.abort()
      pending.current = null
      setLoading(false)
      return
    }

    setCursor(undefined)
    runSearch(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view.live, params.q, params.lang, params.stream, params.from, params.to, runs])

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
   *
   * The text can be passed in, for a caller that changed the draft in the
   * same handler: this closure still holds the draft from before it.
   */
  const commit = useCallback((query: string = draft) => {
    setView((current) => ({ ...current, query }))
    setRuns((n) => n + 1)
  }, [draft])

  /*!
   * Editing clears the parse error.
   *
   * It describes a character at an offset in text that no longer exists the
   * moment anything is typed, and it was outliving it — a query retyped from
   * scratch still carried the last one's red border, its message, and the
   * underline under whatever run now happened to sit at that offset. It also
   * took a line, which pushed the completion menu off the pane and onto the
   * chart.
   */
  const edit = useCallback((next: string) => {
    setDraft(next)
    setError(null)
  }, [])

  const setRange = useCallback((range: Range) => {
    setView((current) => ({
      ...current,
      range,
      // Dragging on the histogram means "look at then", which is not a thing
      // a live tail can do.
      live: current.live && isRelative(range)
    }))
  }, [])

  /*!
   * Clicking a value writes rQL.
   *
   * Appended to a SQL expression it was a 400 from DuckDB, so from SQL the
   * click starts over in rQL with just the clicked term — the same switch the
   * language toggle makes, and like it, one that leaves a search a search.
   */
  const applyFilter = useCallback(
    (field: string, op: string, value: Scalar, negated: boolean) => {
      setView((current) => {
        const base = 'sql' === current.lang ? '' : current.query
        const query = toggleTerm(base, field, op, value, negated)
        setDraft(query)
        return { ...current, lang: 'rql', query }
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
    setDropped(0)
    socket.current?.reconnect()
  }, [])

  // Going live from SQL drops the expression (see streaming), and the command
  // bar has to stop showing it too.
  const toggleLive = useCallback(() => {
    setView((current) => {
      if (current.live) return { ...current, live: false }
      if ('sql' === current.lang) setDraft('')
      return streaming(current)
    })
  }, [])

  // "Back to live" in the time range, which is going live rather than picking
  // a range: setRange keeps a history view in history, so a preset alone left
  // the view relative and still not streaming.
  const goLive = useCallback(() => {
    setView((current) => {
      if ('sql' === current.lang) setDraft('')
      return streaming(current)
    })
  }, [])

  return (
    <>
      <TopBar
        prefs={prefs}
        live={view.live}
        timeline={view.timeline}
        store={store}
        onChangePrefs={(patch) => setPrefs((current) => ({ ...current, ...patch }))}
        onToggleLive={toggleLive}
        onToggleTimeline={() => setView((current) => ({ ...current, timeline: !current.timeline }))}
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
          onChange={edit}
          onSubmit={commit}
        >
          <TimeRange range={view.range} onChange={setRange} onLive={goLive} />
        </CommandBar>
      </div>

      <Histogram
        collapsed={!view.timeline}
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
          numeric={numeric}
          live={view.live}
          loading={loading}
          scanned={scanned}
          store={store}
          active={active}
          needles={needles}
          columns={view.columns}
          hasMore={!!cursor}
          paused={held}
          pending={pendingCount}
          dropped={dropped}
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
