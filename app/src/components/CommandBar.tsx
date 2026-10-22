/*!
 * The query editor.
 *
 * One pane, full width, a few lines tall, with every control that acts on the
 * query floating inside it: the language switch, the examples, the time range
 * and Run. Those were laid out beside the box before, which made the query
 * look like one input among several rather than the thing the whole screen is
 * arranged around — and it left the box itself too narrow to write a query in.
 *
 * Modelled on Cloud Logging's LQL pane, with one deliberate difference: there
 * it is behind a "Show query" toggle because the filter chips above can build a
 * query without it. rQL is the only way to ask rtail anything, so the editor is
 * always open — hiding the sole input behind a switch would be hiding the
 * product.
 *
 * Three rules it lives by:
 *
 *   1. A half-written query is the normal state. The pane never blocks typing,
 *      never clears itself, and shows a parse error inline with the offending
 *      character underlined rather than turning red and refusing to explain.
 *   2. Enter is a newline, ⌘⏎ runs. It is a code editor now, and a code editor
 *      that submits on Enter cannot be used to write two lines.
 *   3. Completion comes from the catalog's key inventory — every field that has
 *      actually appeared on this stream, ranked by how often — so it suggests
 *      what is really there rather than what a log line might contain.
 */

import type { ComponentChildren } from 'preact'
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import type { Field } from '../lib/api.js'
import { Button } from './Button.js'
import { complete, tokenize, type Completion, type Token } from '../lib/query.js'

/** Shown under an empty editor, and inserted on click. */
const EXAMPLES = ['level>=ERROR', 'service=api latency_ms>500']

/*!
 * The SQL examples name physical columns, because that is what the expression
 * is spliced into: promoted fields carry an `a_` prefix on disk, and the
 * envelope keeps its own names.
 */
const SQL_EXAMPLES = [
  "level = 'ERROR'",
  'a_latency_ms > 500',
  "a_service LIKE 'api%'"
]

/** How many lines the pane shows before it scrolls. */
const ROWS = 3

interface Props {
  value: string
  lang: 'rql' | 'sql'
  onChangeLang: (lang: 'rql' | 'sql') => void
  fields: Field[]
  /** Parse error from the last attempt, with the offset it happened at. */
  error?: { message: string; position?: number } | null
  busy: boolean
  onChange: (query: string) => void
  onSubmit: () => void
  /** The time range control, rendered inside the pane's footer. */
  children?: ComponentChildren
}

export function CommandBar({
  value,
  lang,
  onChangeLang,
  fields,
  error,
  busy,
  onChange,
  onSubmit,
  children
}: Props) {
  const input = useRef<HTMLTextAreaElement>(null)
  const [cursor, setCursor] = useState(0)
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)

  const suggestions = useMemo(
    () =>
      open && 'rql' === lang
        ? complete(value, cursor, fields)
        : { from: 0, to: 0, items: [] as Completion[] },
    [open, lang, value, cursor, fields]
  )

  useEffect(() => setActive(0), [suggestions.items.length, value])

  // The gutter numbers every line the query actually has, never fewer than the
  // pane is tall — so an empty editor still reads as an editor.
  const lines = useMemo(() => {
    const count = Math.max(ROWS, value.split('\n').length)
    return Array.from({ length: count }, (_, index) => index + 1)
  }, [value])

  // `/` focuses the pane from anywhere, the way every tool built for keyboards
  // has done since less(1).
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      const typing = target && /^(INPUT|TEXTAREA)$/.test(target.tagName)

      if (('/' === event.key && !typing) || ('k' === event.key && (event.metaKey || event.ctrlKey))) {
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
          // Enter takes the completion when the menu is open, and inserts a
          // newline when it is not — so it never does two things at once.
          event.preventDefault()
          apply(items[active])
          return
      }
    }

    if ('Escape' === event.key) {
      setOpen(false)
      return
    }

    /*!
     * ⌘⏎ runs; bare Enter is a newline.
     *
     * The single-line version ran on Enter, which is right for a search box and
     * wrong for an editor — there would be no way to type the second line of a
     * three-line query. This is the convention every query console uses, and
     * the Run button is there for anyone who does not know it.
     */
    if ('Enter' === event.key && (event.metaKey || event.ctrlKey)) {
      event.preventDefault()
      setOpen(false)
      onSubmit()
    }
  }

  /** True for the run holding the offset the parser stopped at. */
  const bad = (token: Token, at: number | undefined) =>
    undefined !== at && at >= token.at && at < token.at + token.text.length

  const sync = (event: Event) => {
    setCursor((event.currentTarget as HTMLTextAreaElement).selectionStart ?? 0)
  }

  const examples = 'sql' === lang ? SQL_EXAMPLES : EXAMPLES

  return (
    <div class={`command ${error ? 'invalid' : ''}`}>
      <div class="command-pane">
        <div class="command-editor">
          {/*
            A line-number gutter, as the reference has. It is what makes the
            pane read as somewhere code is written rather than as a search box.
          */}
          <div class="command-gutter" aria-hidden="true">
            {lines.map((line) => (
              <span key={line}>{line}</span>
            ))}
          </div>

          {/*
            The ink, under the text.

            A textarea cannot colour its own contents, so the pane draws the
            query twice: once here in colour, and once in the textarea above
            with transparent text and a visible caret. The two share font,
            size, leading and padding, which is what keeps them registered —
            any one of those drifting shows up immediately as ghosting.

            rQL only. The SQL escape hatch is DuckDB's grammar, not ours, and
            colouring it with an rQL tokeniser would be confidently wrong
            about half of what it highlighted.
          */}
          {/*
            The run the parser stopped on is underlined where it sits, rather
            than pointed at from below. The editor is a fixed three-line box,
            so a caret under the error line landed two lines away from the
            character it meant — and on top of the message, which shared its
            line. Marking the token needs no column arithmetic and survives a
            query that wraps.
          */}
          <pre class="command-ink" aria-hidden="true">
            {'rql' === lang
              ? tokenize(value).map((token, index) => (
                  <span
                    key={index}
                    class={`tok-${token.kind}${bad(token, error?.position) ? ' tok-bad' : ''}`}
                  >
                    {token.text}
                  </span>
                ))
              : value}
            {/* A trailing newline is collapsed by `pre`; this keeps the last
                line's height so the two layers end at the same place. */}
            {'\n' === value.slice(-1) ? ' ' : ''}
          </pre>

          <textarea
            ref={input}
            rows={ROWS}
            spellcheck={false}
            autocomplete="off"
            autocorrect="off"
            autocapitalize="off"
            placeholder={
              'sql' === lang
                ? "Start writing a SQL expression — level = 'ERROR' AND a_latency_ms > 500"
                : 'Start writing a query using rQL (rtail query language)'
            }
            aria-label="Query"
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
            // A click on a suggestion blurs the field first, so closing is
            // deferred by a frame or the click never lands.
            onBlur={() => setTimeout(() => setOpen(false), 120)}
          />

          {busy && <i class="command-busy" aria-label="Searching" />}

          {value && (
            <Button
              variant="icon"
              size="sm"
              icon="close"
              class="command-clear"
              aria-label="Clear the query"
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => {
                onChange('')
                onSubmit()
                input.current?.focus()
              }}
            />
          )}
        </div>

        {/*
          What the parser stopped on, inside the pane and directly under the
          line it stopped in. It sat outside the border, under the whole
          control, which put the red rule around the query and the sentence
          explaining it on opposite sides of an edge.
        */}
        {error && <p class="command-error">{error.message}</p>}

        {/*
          The footer, inside the pane. Everything that acts on the query lives
          here: which language it is written in, what one looks like, the window
          it runs over, and the button that runs it.
        */}
        <div class="command-footer">
          <div class="command-lang" role="group" aria-label="Query language">
            {(['rql', 'sql'] as const).map((option) => (
              <Button
                key={option}
                size="sm"
                on={option === lang}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => onChangeLang(option)}
              >
                {'rql' === option ? 'rQL' : 'SQL'}
              </Button>
            ))}
          </div>

          <div class="command-examples">
            {examples.map((example) => (
              <Button
                key={example}
                size="sm"
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => {
                  onChange(example)
                  requestAnimationFrame(() => input.current?.focus())
                }}
              >
                {example}
              </Button>
            ))}
          </div>

          <div class="command-actions">
            {children}

            <Button variant="solid" onClick={onSubmit}>
              Run query
              <kbd>⌘⏎</kbd>
            </Button>
          </div>
        </div>
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

    </div>
  )
}
