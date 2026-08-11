import { useCallback, useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { Sidebar } from './components/Sidebar.tsx'
import { StreamView } from './components/StreamView.tsx'
import { TopBar } from './components/TopBar.tsx'
import { connect, type Connection } from './lib/connection.ts'
import { formatLine } from './lib/format.ts'
import { collectPaths } from './lib/json.ts'
import { loadActiveStream, loadPrefs, savePrefs, saveActiveStream } from './lib/prefs.ts'
import { matchesQuery, parseQuery } from './lib/query.ts'
import { onRouteChange, readStream, writeStream } from './lib/router.ts'
import type { Line, Prefs } from './lib/types.ts'

/** Matches the server-side backlog cap. */
const BUFFER_SIZE = 100

/** Shared, so a stream with no extracted fields keeps the same array identity. */
const NO_FIELDS: string[] = []

export function App() {
  const [prefs, setPrefs] = useState<Prefs>(loadPrefs)
  const [streams, setStreams] = useState<string[]>([])
  const [activeStream, setActiveStream] = useState<string | null>(
    () => readStream() ?? loadActiveStream()
  )
  const [lines, setLines] = useState<Line[]>([])
  const [paused, setPaused] = useState(false)
  const [filter, setFilter] = useState('')

  const socketRef = useRef<Connection | null>(null)
  const activeStreamRef = useRef(activeStream)
  const pausedRef = useRef(false)

  activeStreamRef.current = activeStream

  /*!
   * connection — established once for the lifetime of the app
   */
  useEffect(() => {
    const socket = connect()
    socketRef.current = socket

    socket.on('connect', () => {
      // Re-subscribe after a reconnect, unless the user has paused.
      if (activeStreamRef.current && !pausedRef.current) {
        socket.emit('select stream', activeStreamRef.current)
      }
    })

    socket.on('streams', setStreams)

    socket.on('backlog', (backlog) => {
      setLines((backlog ?? []).map(formatLine))
    })

    socket.on('line', (wire) => {
      setLines((current) => {
        const next = current.length >= BUFFER_SIZE ? current.slice(1) : current.slice()
        next.push(formatLine(wire))
        return next
      })
    })

    return () => {
      socket.close()
      socketRef.current = null
    }
  }, [])

  /*!
   * stream selection
   */
  useEffect(() => {
    setLines([])
    setPaused(false)
    pausedRef.current = false

    socketRef.current?.emit('select stream', activeStream)
    saveActiveStream(activeStream)
    writeStream(activeStream)

    document.title = activeStream ? `rTail : ${activeStream}` : 'rTail'
  }, [activeStream])

  useEffect(() => onRouteChange((stream) => stream && setActiveStream(stream)), [])

  /*!
   * preferences
   */
  useEffect(() => savePrefs(prefs), [prefs])

  useEffect(() => {
    document.body.className =
      `${prefs.theme} font-family-${prefs.fontFamily} font-size-${prefs.fontSize}`
  }, [prefs.theme, prefs.fontFamily, prefs.fontSize])

  // The top bar's brand cell and the sidebar both read --sidebar-w, so the
  // vertical hairline between them stays a single unbroken line while dragging.
  useEffect(() => {
    document.documentElement.style.setProperty('--sidebar-w', `${prefs.sidebarWidth}px`)
  }, [prefs.sidebarWidth])

  const updatePrefs = useCallback((patch: Partial<Prefs>) => {
    setPrefs((current) => ({ ...current, ...patch }))
  }, [])

  const toggleIn = useCallback((key: 'favorites' | 'hiddenTimestamps', value: string) => {
    setPrefs((current) => {
      const list = current[key]
      return {
        ...current,
        [key]: list.includes(value) ? list.filter((item) => item !== value) : [...list, value]
      }
    })
  }, [])

  /*!
   * pause / resume
   *
   * Pausing unsubscribes server-side rather than merely hiding updates, so a
   * paused tab costs nothing. Resuming re-subscribes and pulls a fresh backlog.
   */
  const pause = useCallback(() => {
    if (pausedRef.current) return
    pausedRef.current = true
    setPaused(true)
    socketRef.current?.emit('select stream', null)
  }, [])

  const resume = useCallback(() => {
    if (!pausedRef.current) return
    pausedRef.current = false
    setPaused(false)
    socketRef.current?.emit('select stream', activeStreamRef.current)
  }, [])

  /*!
   * filtering
   *
   * Parsed once per keystroke rather than once per line, and applied here
   * rather than in StreamView: the bar shows how many lines survived, and the
   * viewport should not be the only place that knows.
   */
  const query = useMemo(() => parseQuery(filter), [filter])

  // Lines arrive in order, so insertion order is the source of truth. An older
  // implementation re-sorted by timestamp on every digest, which reordered
  // lines that shared a millisecond.
  const visible = useMemo(() => {
    const matching = query.isEmpty ? lines : lines.filter((line) => matchesQuery(query, line))
    return prefs.ascending ? matching : matching.slice().reverse()
  }, [lines, query, prefs.ascending])

  // The field census for the picker — rtail has no schema for a stream, so the
  // only source of field names is the lines already in the buffer.
  const availableFields = useMemo(
    () => collectPaths(lines.filter((line) => 'object' === line.type).map((line) => line.content)),
    [lines]
  )

  const fields = (activeStream && prefs.fields[activeStream]) || NO_FIELDS

  const setFields = useCallback(
    (next: string[]) => {
      if (!activeStream) return

      setPrefs((current) => {
        const all = { ...current.fields }
        if (next.length) all[activeStream] = next
        else delete all[activeStream]

        return { ...current, fields: all }
      })
    },
    [activeStream]
  )

  const isFavorite = !!activeStream && prefs.favorites.includes(activeStream)

  return (
    <>
      <TopBar
        prefs={prefs}
        activeStream={activeStream}
        isFavorite={isFavorite}
        paused={paused}
        filter={filter}
        filterError={query.error}
        matched={visible.length}
        total={lines.length}
        fields={fields}
        availableFields={availableFields}
        onChangePrefs={updatePrefs}
        onToggleFavorite={() => activeStream && toggleIn('favorites', activeStream)}
        onFilter={setFilter}
        onFields={setFields}
      />

      <div class="split-pane">
        <Sidebar
          streams={streams}
          favorites={prefs.favorites}
          activeStream={activeStream}
          onSelect={setActiveStream}
          onResize={(sidebarWidth) => updatePrefs({ sidebarWidth })}
        />

        <StreamView
          activeStream={activeStream}
          lines={visible}
          needles={query.needles}
          fields={fields}
          jsonView={prefs.jsonView}
          ascending={prefs.ascending}
          timestampsHidden={!!activeStream && prefs.hiddenTimestamps.includes(activeStream)}
          paused={paused}
          onToggleTimestamps={() => activeStream && toggleIn('hiddenTimestamps', activeStream)}
          onPause={pause}
          onResume={resume}
        />
      </div>
    </>
  )
}
