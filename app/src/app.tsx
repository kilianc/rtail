import { useCallback, useEffect, useRef, useState } from 'preact/hooks'
import { Header } from './components/Header.js'
import { Sidebar } from './components/Sidebar.js'
import { StreamView } from './components/StreamView.js'
import { connect, type Connection } from './lib/connection.js'
import { formatLine } from './lib/format.js'
import { loadActiveStream, loadPrefs, savePrefs, saveActiveStream } from './lib/prefs.js'
import { onRouteChange, readStream, writeStream } from './lib/router.js'
import type { Line, Prefs } from './lib/types.js'

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

  return (
    <>
      <Header prefs={prefs} onChange={updatePrefs} />

      <div class="split-pane">
        <Sidebar
          streams={streams}
          favorites={prefs.favorites}
          activeStream={activeStream}
          width={prefs.sidebarWidth}
          onSelect={setActiveStream}
          onResize={(sidebarWidth) => updatePrefs({ sidebarWidth })}
        />

        <StreamView
          activeStream={activeStream}
          lines={lines}
          ascending={prefs.ascending}
          isFavorite={!!activeStream && prefs.favorites.includes(activeStream)}
          timestampsHidden={!!activeStream && prefs.hiddenTimestamps.includes(activeStream)}
          paused={paused}
          onToggleFavorite={() => activeStream && toggleIn('favorites', activeStream)}
          onToggleTimestamps={() => activeStream && toggleIn('hiddenTimestamps', activeStream)}
          onPause={pause}
          onResume={resume}
        />
      </div>
    </>
  )
}
