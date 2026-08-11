import { useCallback, useEffect, useRef, useState } from 'preact/hooks'
import { Sidebar } from './components/Sidebar.tsx'
import { StreamView } from './components/StreamView.tsx'
import { TopBar } from './components/TopBar.tsx'
import { connect, type Connection } from './lib/connection.ts'
import { formatLine } from './lib/format.ts'
import { loadActiveStream, loadPrefs, savePrefs, saveActiveStream } from './lib/prefs.ts'
import { onRouteChange, readStream, writeStream } from './lib/router.ts'
import type { Line, Prefs } from './lib/types.ts'

/** Matches the server-side backlog cap. */
const BUFFER_SIZE = 100

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

  const isFavorite = !!activeStream && prefs.favorites.includes(activeStream)

  return (
    <>
      <TopBar
        prefs={prefs}
        activeStream={activeStream}
        isFavorite={isFavorite}
        paused={paused}
        filter={filter}
        onChangePrefs={updatePrefs}
        onToggleFavorite={() => activeStream && toggleIn('favorites', activeStream)}
        onFilter={setFilter}
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
          lines={lines}
          filter={filter}
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
