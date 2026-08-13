/*!
 * The stream picker.
 *
 * v1 gave streams a permanent sidebar, which made sense when picking one was
 * the only thing the interface did. In v2 the sidebar belongs to the field
 * explorer and the stream is one term of a query, so it collapses into a
 * control in the top bar — chosen once, then out of the way.
 */

import { useEffect, useRef, useState } from 'preact/hooks'
import { Popover } from './Popover.js'

interface Props {
  streams: string[]
  active: string | null
  favorites: string[]
  onSelect: (stream: string | null) => void
  onToggleFavorite: (stream: string) => void
}

export function StreamPicker({ streams, active, favorites, onSelect, onToggleFavorite }: Props) {
  const anchor = useRef<HTMLButtonElement>(null)
  const search = useRef<HTMLInputElement>(null)
  const [open, setOpen] = useState(false)
  const [filter, setFilter] = useState('')

  useEffect(() => {
    if (open) requestAnimationFrame(() => search.current?.focus())
    else setFilter('')
  }, [open])

  const matching = streams.filter((stream) =>
    stream.toLowerCase().includes(filter.toLowerCase())
  )

  // Favourites first, then alphabetical — a list that reorders itself by
  // recency makes muscle memory useless.
  const ordered = [...matching].sort((a, b) => {
    const favour = Number(favorites.includes(b)) - Number(favorites.includes(a))
    return favour || a.localeCompare(b)
  })

  return (
    <>
      <button
        ref={anchor}
        class={`stream-picker ${active ? 'chosen' : ''}`}
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <i class="stream-picker-icon" aria-hidden="true" />
        <span class="stream-picker-name">{active ?? 'All streams'}</span>
        <span class="stream-picker-count">{streams.length}</span>
      </button>

      {open && (
        <Popover anchor={anchor.current} class="popover-streams" onClose={() => setOpen(false)}>
          <input
            ref={search}
            type="text"
            placeholder="Find a stream"
            aria-label="Find a stream"
            value={filter}
            onInput={(event) => setFilter(event.currentTarget.value)}
            onKeyDown={(event) => {
              if ('Enter' === event.key && ordered.length) {
                onSelect(ordered[0])
                setOpen(false)
              }
            }}
          />

          <ul class="stream-list">
            <li>
              <button
                class={`stream-item ${null === active ? 'selected' : ''}`}
                onClick={() => {
                  onSelect(null)
                  setOpen(false)
                }}
              >
                All streams
              </button>
            </li>

            {ordered.map((stream) => (
              <li key={stream}>
                <button
                  class={`stream-item ${stream === active ? 'selected' : ''}`}
                  onClick={() => {
                    onSelect(stream)
                    setOpen(false)
                  }}
                >
                  {stream}
                </button>

                <button
                  class={`stream-fav ${favorites.includes(stream) ? 'on' : ''}`}
                  aria-label={
                    favorites.includes(stream) ? 'Remove from favourites' : 'Add to favourites'
                  }
                  onClick={() => onToggleFavorite(stream)}
                />
              </li>
            ))}

            {0 === ordered.length && <li class="stream-empty">No streams yet</li>}
          </ul>
        </Popover>
      )}
    </>
  )
}
