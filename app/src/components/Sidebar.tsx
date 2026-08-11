import { useCallback, useEffect, useState } from 'preact/hooks'

const MIN_WIDTH = 180
const MAX_WIDTH = 600

interface Props {
  streams: string[]
  favorites: string[]
  activeStream: string | null
  width: number
  onSelect: (stream: string) => void
  onResize: (width: number) => void
}

export function Sidebar({ streams, favorites, activeStream, width, onSelect, onResize }: Props) {
  const [filter, setFilter] = useState('')
  const [dragging, setDragging] = useState(false)

  const needle = filter.trim().toLowerCase()
  const matches = (stream: string) => !needle || stream.toLowerCase().includes(needle)

  const favouriteSet = new Set(favorites)
  const shownFavorites = favorites.filter(matches).sort()
  const shownStreams = streams.filter((s) => !favouriteSet.has(s)).filter(matches).sort()

  // Drag-to-resize. The old jQuery directive listened on window for the whole
  // session; pointer capture scopes it to the drag and survives the cursor
  // leaving the handle.
  const onPointerDown = useCallback((event: PointerEvent) => {
    event.preventDefault()
    ;(event.target as HTMLElement).setPointerCapture(event.pointerId)
    setDragging(true)
  }, [])

  const onPointerMove = useCallback(
    (event: PointerEvent) => {
      if (!dragging) return
      onResize(Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, event.clientX)))
    },
    [dragging, onResize]
  )

  const stopDragging = useCallback(() => setDragging(false), [])

  useEffect(() => {
    document.body.classList.toggle('resizing', dragging)
    return () => document.body.classList.remove('resizing')
  }, [dragging])

  return (
    <div class="sidebar" style={{ width: `${width}px` }}>
      <div class="search-box">
        <input
          type="text"
          placeholder="Search streams"
          aria-label="Search streams"
          value={filter}
          onInput={(event) => setFilter(event.currentTarget.value)}
        />
      </div>

      <div class="stream-sections">
        {shownFavorites.length > 0 && (
          <StreamSection
            title="Favorites"
            streams={shownFavorites}
            activeStream={activeStream}
            onSelect={onSelect}
          />
        )}

        <StreamSection
          title="Streams"
          streams={shownStreams}
          activeStream={activeStream}
          onSelect={onSelect}
        />
      </div>

      <div
        class="resize-handler"
        role="separator"
        aria-orientation="vertical"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={stopDragging}
        onPointerCancel={stopDragging}
      />
      <div class="fade" />
    </div>
  )
}

function StreamSection({
  title,
  streams,
  activeStream,
  onSelect
}: {
  title: string
  streams: string[]
  activeStream: string | null
  onSelect: (stream: string) => void
}) {
  return (
    <div class="stream-section">
      <h4>{title}</h4>
      {streams.map((stream) => (
        <a
          key={stream}
          href={`#/streams/${encodeURIComponent(stream)}`}
          class={stream === activeStream ? 'selected' : ''}
          aria-current={stream === activeStream ? 'true' : undefined}
          onClick={(event) => {
            event.preventDefault()
            onSelect(stream)
          }}
        >
          <span>{stream}</span>
        </a>
      ))}
    </div>
  )
}
