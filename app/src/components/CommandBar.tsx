/*!
 * The command bar.
 *
 * One monospace line, and the thing the whole interface is arranged around.
 * Completion is driven by the catalog's key inventory — every field that has
 * actually appeared on this stream, ranked by how often — so it suggests what
 * is really there rather than what a log line might plausibly contain.
 *
 * Two rules it lives by:
 *
 *   1. A half-written query is the normal state. The bar never blocks typing,
 *      never clears itself, and shows a parse error inline with the offending
 *      character underlined rather than turning red and refusing to explain.
 *   2. Enter is the only thing that runs a historical search. Streaming
 *      filters live as you type, because that costs nothing; scanning a week
 *      of Parquet on every keystroke does not.
 */

import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import type { Field } from '../lib/api.js'
import { complete, type Completion } from '../lib/query.js'

interface Props {
  value: string
  fields: Field[]
  /** Parse error from the last attempt, with the offset it happened at. */
  error?: { message: string; position?: number } | null
  busy: boolean
  onChange: (query: string) => void
  onSubmit: () => void
}

export function CommandBar({ value, fields, error, busy, onChange, onSubmit }: Props) {
  const input = useRef<HTMLInputElement>(null)
  const [cursor, setCursor] = useState(0)
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)

  const suggestions = useMemo(
    () => (open ? complete(value, cursor, fields) : { from: 0, to: 0, items: [] as Completion[] }),
    [open, value, cursor, fields]
  )

  useEffect(() => setActive(0), [suggestions.items.length, value])

  // `/` focuses the bar from anywhere, the way every tool built for keyboards
  // has done since less(1).
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      const typing = target && /^(INPUT|TEXTAREA)$/.test(target.tagName)

      if ('/' === event.key && !typing) {
        event.preventDefault()
        input.current?.focus()
        input.current?.select()
      }

      if ('k' === event.key && (event.metaKey || event.ctrlKey)) {
        event.preventDefault()
        input.current?.focus()
        input.current?.select()
      }
    }

    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  const apply = (item: Completion) => {
    const next = value.slice(0, suggestions.from) + item.value + value.slice(suggestions.to)
    const at = suggestions.from + item.value.length

    onChange(next)
    setOpen(false)

    requestAnimationFrame(() => {
      input.current?.focus()
      input.current?.setSelectionRange(at, at)
      setCursor(at)
    })
  }

  const onKeyDown = (event: KeyboardEvent) => {
    const items = suggestions.items

    if (open && items.length) {
      switch (event.key) {
        case 'ArrowDown':
          event.preventDefault()
          setActive((n) => (n + 1) % items.length)
          return
        case 'ArrowUp':
          event.preventDefault()
          setActive((n) => (n - 1 + items.length) % items.length)
          return
        case 'Tab':
          event.preventDefault()
          apply(items[active])
          return
        case 'Enter':
          // Enter takes the completion when the menu is open, and runs the
          // query when it is not — so it never does two things at once.
          event.preventDefault()
          apply(items[active])
          return
      }
    }

    if ('Escape' === event.key) {
      setOpen(false)
      return
    }

    if ('Enter' === event.key) {
      event.preventDefault()
      setOpen(false)
      onSubmit()
    }
  }

  const sync = (event: Event) => {
    const element = event.currentTarget as HTMLInputElement
    setCursor(element.selectionStart ?? 0)
  }

  return (
    <div class={`command ${error ? 'invalid' : ''}`}>
      <div class="command-input">
        <i class="command-icon" aria-hidden="true" />

        <input
          ref={input}
          type="text"
          spellcheck={false}
          autocomplete="off"
          autocorrect="off"
          autocapitalize="off"
          placeholder='level>=ERROR service=api "timeout"'
          aria-label="Filter query"
          value={value}
          onInput={(event) => {
            onChange(event.currentTarget.value)
            setCursor(event.currentTarget.selectionStart ?? 0)
            setOpen(true)
          }}
          onKeyDown={onKeyDown}
          onKeyUp={sync}
          onClick={sync}
          onFocus={() => setOpen(true)}
          // A click on a suggestion blurs the input first, so closing is
          // deferred by a frame or the click never lands.
          onBlur={() => setTimeout(() => setOpen(false), 120)}
        />

        {busy && <i class="command-busy" aria-label="Searching" />}

        {value && (
          <button
            class="command-clear"
            aria-label="Clear the filter"
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => {
              onChange('')
              onSubmit()
              input.current?.focus()
            }}
          />
        )}
      </div>

      {open && suggestions.items.length > 0 && (
        <ul class="command-menu" role="listbox">
          {suggestions.items.map((item, index) => (
            <li
              key={item.label}
              role="option"
              aria-selected={index === active}
              class={index === active ? 'active' : ''}
              onMouseEnter={() => setActive(index)}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => apply(item)}
            >
              <span class="command-menu-name">{item.label}</span>
              <span class={`command-menu-kind kind-${item.kind}`}>{item.kind}</span>
              {item.detail && <span class="command-menu-detail">{item.detail}</span>}
            </li>
          ))}
        </ul>
      )}

      {error && (
        <div class="command-error">
          {/*
            The caret sits under the character the parser stopped at. A search
            bar that says "syntax error" without saying where is a search bar
            that makes you delete the whole query and start again.
          */}
          {undefined !== error.position && (
            <span class="command-caret" style={{ '--at': error.position } as never}>
              ↑
            </span>
          )}
          {error.message}
        </div>
      )}
    </div>
  )
}
