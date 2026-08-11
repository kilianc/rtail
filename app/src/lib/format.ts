/*!
 * Turning wire lines into rendered HTML.
 *
 * Each line is formatted exactly once, on arrival, and the result is cached on
 * the line object — rendering a 100-line viewport should never re-run the ANSI
 * parser or the syntax highlighter.
 *
 * Object lines are rendered twice, both times up front: expanded, and on one
 * line. Which of the two a row shows is a display decision that changes with a
 * preference or a click, and re-highlighting a payload on every such change
 * would put the syntax highlighter back in the interaction path.
 */

import { AnsiUp } from 'ansi_up'
import hljs from 'highlight.js/lib/core'
import json from 'highlight.js/lib/languages/json'
import { getPath, isPlainObject, parsePath, stringify } from './json.js'
import type { JsonView, Line, WireLine } from './types.js'

hljs.registerLanguage('json', json)

const ansi = new AnsiUp()

// Emit `ansi-red-fg` style classes rather than inline colours, so the palette
// stays in the stylesheet and follows the active theme.
ansi.use_classes = true

// Belt and braces: ansi_up escapes by default, and log lines are attacker
// controlled (they are whatever a piped process printed).
ansi.escape_html = true

/**
 * Rows of expanded JSON above which `auto` collapses a payload.
 *
 * Small objects are the ones worth reading in full — a `{ level, msg, ms }`
 * costs three rows and saves a click. Past that a payload stops being a log
 * line and starts being a document, and it pushes everything else off screen.
 */
const AUTO_EXPAND_ROWS = 6

const timestampFormat = new Intl.DateTimeFormat(undefined, {
  year: '2-digit',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false
})

let nextKey = 0

/** Formats a line for display, computing its HTML and filter text once. */
export function formatLine(wire: WireLine): Line {
  const base = { ...wire, htmlCompact: null, bulky: false, key: nextKey++ }

  if (null === wire.content || undefined === wire.content || '' === wire.content) {
    return { ...base, html: '', text: '' }
  }

  if ('object' === wire.type) {
    const text = JSON.stringify(wire.content, null, '  ')

    return {
      ...base,
      text,
      html: `<pre>${highlightJson(text)}</pre>`,
      htmlCompact: `<pre>${highlightJson(JSON.stringify(wire.content))}</pre>`,
      bulky: rows(text) > AUTO_EXPAND_ROWS
    }
  }

  const text = String(wire.content)
  return { ...base, text, html: ansi.ansi_to_html(text) }
}

/** Whether an object line starts out expanded, before anyone clicks a caret. */
export function defaultExpanded(view: JsonView, line: Line): boolean {
  if ('expanded' === view) return true
  if ('collapsed' === view) return false

  return !line.bulky
}

/**
 * Renders a payload as just the fields that were asked for.
 *
 * `logfmt` shape rather than JSON: at one field per column the braces and
 * quotes are most of the row. Fields the line does not carry are dropped
 * instead of rendered empty — a stream is rarely uniform, and a row of
 * placeholders reads as data.
 *
 * Returns null when the line carries none of them, so the caller can fall back
 * to showing the payload rather than an empty row.
 */
export function renderFields(content: unknown, fields: string[]): string | null {
  if (!isPlainObject(content)) return null

  const pairs: string[] = []

  for (const field of fields) {
    const path = parsePath(field)
    if (!path) continue

    const value = getPath(content, path)
    if (undefined === value) continue

    pairs.push(
      `<span class="field-pair">` +
        `<span class="field-key">${escapeHtml(field)}</span>` +
        `<span class="field-eq">=</span>` +
        renderValue(value) +
        `</span>`
    )
  }

  return pairs.length ? `<pre>${pairs.join(' ')}</pre>` : null
}

function renderValue(value: unknown): string {
  if ('string' === typeof value) {
    // Quoted only where the quotes earn their place: without them a value with
    // spaces in it reads as the start of the next pair.
    const text = /[\s"]/.test(value) ? JSON.stringify(value) : value
    return `<span class="hljs-string">${escapeHtml(text)}</span>`
  }

  if ('number' === typeof value) {
    return `<span class="hljs-number">${escapeHtml(String(value))}</span>`
  }

  if ('boolean' === typeof value || null === value) {
    return `<span class="hljs-literal">${escapeHtml(stringify(value))}</span>`
  }

  // Objects and arrays keep their syntax highlighting; they are still JSON.
  return highlightJson(stringify(value))
}

export function formatTimestamp(timestamp: number): string {
  // Intl joins the date and time parts with a locale separator (", " in most
  // locales); a log gutter reads better without it.
  return timestampFormat.format(new Date(timestamp)).replace(',', '')
}

function highlightJson(text: string): string {
  return hljs.highlight(text, { language: 'json' }).value
}

function rows(text: string): number {
  let count = 1
  for (let index = text.indexOf('\n'); -1 !== index; index = text.indexOf('\n', index + 1)) {
    count++
  }

  return count
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}
