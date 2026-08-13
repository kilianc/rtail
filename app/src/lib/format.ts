/*!
 * Turning wire lines into rendered HTML.
 *
 * Each line is formatted exactly once, on arrival, and the result is cached on
 * the line object — rendering a 100-line viewport should never re-run the ANSI
 * parser or the syntax highlighter.
 */

import { AnsiUp } from 'ansi_up'
import hljs from 'highlight.js/lib/core'
import json from 'highlight.js/lib/languages/json'
import type { Line, WireLine } from './types.js'

hljs.registerLanguage('json', json)

const ansi = new AnsiUp()

// Emit `ansi-red-fg` style classes rather than inline colours, so the palette
// stays in the stylesheet and follows the active theme.
ansi.use_classes = true

// Belt and braces: ansi_up escapes by default, and log lines are attacker
// controlled (they are whatever a piped process printed).
ansi.escape_html = true

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

/*!
 * Formats a line for display, computing its HTML and filter text once.
 *
 * A structured record renders as its extracted message rather than as pretty
 * JSON: the row is one line tall, the full payload is a click away in the
 * expanded view, and twenty lines of indented JSON per event makes a result
 * list unreadable. Plain-text lines keep their ANSI colours, which is the
 * whole reason anyone points rtail at a terminal.
 */
export function formatLine(wire: WireLine): Line {
  let html: string
  let text: string

  if (null === wire.content || undefined === wire.content || '' === wire.content) {
    html = ''
    text = ''
  } else if ('object' === wire.type) {
    text = wire.msg || JSON.stringify(wire.content)

    html = wire.msg
      ? escapeHtml(wire.msg)
      : '<span class="row-raw">' +
        hljs.highlight(JSON.stringify(wire.content), { language: 'json' }).value +
        '</span>'
  } else {
    text = String(wire.content)
    html = ansi.ansi_to_html(text)
  }

  return { ...wire, html, text, key: nextKey++ }
}

/*!
 * escapeHtml exists because a message is attacker-controlled text.
 *
 * The row body is injected as HTML so that ANSI colouring works, which means
 * everything on that path has to be escaped by whoever produced it. ansi_up
 * does its own; a plain message needs this.
 */
function escapeHtml(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

export function formatTimestamp(timestamp: number): string {
  // Intl joins the date and time parts with a locale separator (", " in most
  // locales); a log gutter reads better without it.
  return timestampFormat.format(new Date(timestamp)).replace(',', '')
}

/**
 * Builds a line predicate from the filter box.
 *
 * The input is a regexp; while it is being typed it is usually invalid, and a
 * half-written pattern should not blank the viewport — so anything that fails
 * to compile matches everything.
 */
export function buildFilter(pattern: string): (line: Line) => boolean {
  if (!pattern) return () => true

  try {
    const re = new RegExp(pattern)
    return (line) => re.test(line.text)
  } catch {
    return () => true
  }
}
