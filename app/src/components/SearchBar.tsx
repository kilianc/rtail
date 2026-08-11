/*!
 * The filter box, its syntax card, and the field picker.
 *
 * The three are one feature — the query and the extracted fields are the two
 * halves of "show me this bit of the stream" — but they render in two places.
 * The controls sit in the bar; the panels are handed back to TopBar, which
 * mounts them outside it. `.topbar-main` is a query container, and layout
 * containment makes it the containing block for anything fixed inside it,
 * which would anchor the panels to the bar's box instead of the viewport.
 */

import { useEffect, useRef, useState } from 'preact/hooks'
import type { RefObject } from 'preact'
import { parsePath, type FieldPath } from '../lib/json.ts'
import { MAX_FIELDS } from '../lib/prefs.ts'
import { Popover } from './Popover.tsx'

export type SearchPanel = 'help' | 'fields'

interface Props {
  value: string
  /** Set when the query did not fully parse; shown on the box. */
  error: string | null
  matched: number
  total: number
  /** How many paths are extracted, for the badge on the button. */
  fieldCount: number
  open: SearchPanel | null
  helpRef: RefObject<HTMLButtonElement>
  fieldsRef: RefObject<HTMLButtonElement>
  onChange: (value: string) => void
  onToggle: (panel: SearchPanel) => void
}

const SYNTAX: Array<[string, string]> = [
  ['payment failed', 'both words, in any order'],
  ['"payment failed"', 'that exact phrase'],
  ['-healthcheck', 'lines without it'],
  ['/GET \\/1\\/users/', 'a regexp'],
  ['level:error', 'JSON field contains'],
  ['user.id=42', 'JSON field is exactly'],
  ['duration>250', 'numeric comparison'],
  ['trace_id:*', 'field is present']
]

export function SearchBar({
  value,
  error,
  matched,
  total,
  fieldCount,
  open,
  helpRef,
  fieldsRef,
  onChange,
  onToggle
}: Props) {
  const inputRef = useRef<HTMLInputElement>(null)

  // `f` jumps to the filter, the way `/` jumps to the stream search. Both stay
  // out of the way of a field the user is already typing into.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ('f' !== event.key || event.metaKey || event.ctrlKey || event.altKey) return

      const target = event.target as HTMLElement | null
      if (target && /^(INPUT|TEXTAREA)$/.test(target.tagName)) return

      event.preventDefault()
      inputRef.current?.focus()
      inputRef.current?.select()
    }

    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  return (
    <>
      <div class={`filter-box ${error ? 'invalid' : ''}`}>
        <input
          ref={inputRef}
          type="text"
          placeholder="Filter lines — text, /regexp/, level:error"
          aria-label="Filter lines"
          aria-invalid={error ? 'true' : undefined}
          title={error ?? undefined}
          value={value}
          onInput={(event) => onChange(event.currentTarget.value)}
          onKeyDown={(event) => {
            if ('Escape' !== event.key) return
            // Escape clears a query, or steps out of the field if it is empty.
            if (value) onChange('')
            else event.currentTarget.blur()
          }}
        />

        {value && (
          <span class="filter-count" title={`${matched} of ${total} lines match`}>
            {matched}/{total}
          </span>
        )}

        {value && (
          <button
            class="search-clear"
            aria-label="Clear filter"
            onClick={() => {
              onChange('')
              inputRef.current?.focus()
            }}
          />
        )}

        <button
          ref={helpRef}
          class="filter-help"
          aria-label="Filter syntax"
          aria-expanded={'help' === open}
          onClick={() => onToggle('help')}
        >
          ?
        </button>
      </div>

      <button
        ref={fieldsRef}
        class={`btn-fields ${fieldCount ? 'on' : ''}`}
        aria-expanded={'fields' === open}
        onClick={() => onToggle('fields')}
      >
        Fields
        {fieldCount > 0 && <span class="btn-fields-count">{fieldCount}</span>}
      </button>
    </>
  )
}

/** The syntax card behind the `?`. */
export function SearchHelp({
  anchor,
  onClose
}: {
  anchor: HTMLElement | null
  onClose: () => void
}) {
  return (
    <Popover anchor={anchor} class="popover-help" onClose={onClose}>
      <h4>Filter syntax</h4>

      <dl>
        {SYNTAX.map(([example, meaning]) => (
          <div key={example}>
            <dt>{example}</dt>
            <dd>{meaning}</dd>
          </div>
        ))}
      </dl>

      <p class="hint">
        Terms are ANDed. Case is ignored until a term contains a capital.
      </p>
    </Popover>
  )
}

/**
 * Picks the paths to extract.
 *
 * The list is the buffer's own field census rather than a fixed schema —
 * rtail has no idea what a stream logs until it logs it — with a free-text
 * path for the fields that have not come past yet.
 */
export function FieldsPicker({
  anchor,
  fields,
  available,
  onFields,
  onClose
}: {
  anchor: HTMLElement | null
  fields: string[]
  available: FieldPath[]
  onFields: (fields: string[]) => void
  onClose: () => void
}) {
  const [draft, setDraft] = useState('')

  const toggle = (path: string) => {
    if (fields.includes(path)) onFields(fields.filter((field) => field !== path))
    else if (fields.length < MAX_FIELDS) onFields([...fields, path])
  }

  const add = (event: Event) => {
    event.preventDefault()

    const path = draft.trim()
    if (!path || !parsePath(path) || fields.includes(path)) return

    onFields([...fields, path].slice(0, MAX_FIELDS))
    setDraft('')
  }

  // Selected first, so a field keeps its place as the counts shuffle beneath
  // it — and so a hand-typed path is still listed once the lines carrying it
  // have scrolled out of the buffer.
  const rows = [
    ...fields.map((path) => ({
      path,
      count: available.find((field) => field.path === path)?.count ?? 0
    })),
    ...available.filter((field) => !fields.includes(field.path))
  ]

  return (
    <Popover anchor={anchor} class="popover-fields" onClose={onClose}>
      <h4>Extract fields</h4>
      <p class="hint">Object lines collapse to the fields you pick.</p>

      <form onSubmit={add}>
        <input
          type="text"
          class="field-input"
          placeholder="Add a path, e.g. req.headers.host"
          aria-label="Add a field path"
          value={draft}
          onInput={(event) => setDraft(event.currentTarget.value)}
        />
      </form>

      <div class="field-list">
        {rows.map(({ path, count }) => (
          <button
            key={path}
            class={`field-row ${fields.includes(path) ? 'on' : ''}`}
            aria-pressed={fields.includes(path)}
            onClick={() => toggle(path)}
          >
            <span class="field-path">{path}</span>
            {count > 0 && <span class="field-count">{count}</span>}
          </button>
        ))}

        {0 === rows.length && <p class="hint">No JSON fields in the buffer yet.</p>}
      </div>

      {fields.length > 0 && (
        <button class="field-clear" onClick={() => onFields([])}>
          Show full payloads
        </button>
      )}
    </Popover>
  )
}
