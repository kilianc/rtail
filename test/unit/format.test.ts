import test, { describe } from 'node:test'
import assert from 'node:assert/strict'
import { buildFilter, formatLine, formatTimestamp } from '../../app/src/lib/format.ts'
import type { Line, WireLine } from '../../app/src/lib/types.ts'

function wire(content: unknown, type = typeof content): WireLine {
  return { timestamp: 1_700_000_000_000, streamid: 'api', host: '127.0.0.1', port: 1234, content, type }
}

describe('formatLine', () => {
  test('renders plain text as-is', () => {
    const line = formatLine(wire('hello world'))

    assert.equal(line.text, 'hello world')
    assert.equal(line.html, 'hello world')
  })

  test('converts ansi colours to classed spans', () => {
    const line = formatLine(wire('[32mgreen[0m'))

    // Classes rather than inline colours, so the palette follows the theme.
    assert.match(line.html, /class="[^"]*ansi-green-fg[^"]*"/)
    assert.match(line.html, />green</)
  })

  test('escapes html in a text line', () => {
    // Log lines are whatever a piped process printed, so they are untrusted.
    const line = formatLine(wire('<script>alert(1)</script>'))

    assert.doesNotMatch(line.html, /<script>/)
    assert.match(line.html, /&lt;script&gt;/)
  })

  test('keeps the raw text for filtering, not the escaped html', () => {
    const line = formatLine(wire('<b>bold</b>'))

    assert.equal(line.text, '<b>bold</b>')
  })

  test('pretty-prints and highlights an object line', () => {
    const line = formatLine(wire({ level: 'warn', n: 2 }, 'object'))

    assert.match(line.html, /^<pre>/)
    assert.match(line.html, /hljs/)
    assert.equal(line.text, JSON.stringify({ level: 'warn', n: 2 }, null, '  '))
  })

  test('renders an empty string as nothing', () => {
    const line = formatLine(wire(''))

    assert.equal(line.html, '')
    assert.equal(line.text, '')
  })

  test('renders null and undefined as nothing', () => {
    assert.equal(formatLine(wire(null)).html, '')
    assert.equal(formatLine(wire(undefined)).html, '')
  })

  test('stringifies a non-string scalar', () => {
    const line = formatLine(wire(42))

    assert.equal(line.text, '42')
    assert.equal(line.html, '42')
  })

  test('preserves the wire fields', () => {
    const line = formatLine(wire('x'))

    assert.equal(line.streamid, 'api')
    assert.equal(line.host, '127.0.0.1')
    assert.equal(line.port, 1234)
    assert.equal(line.timestamp, 1_700_000_000_000)
  })

  test('gives every line a distinct, increasing key', () => {
    const a = formatLine(wire('a'))
    const b = formatLine(wire('b'))

    assert.ok(b.key > a.key, 'keys must be monotonic so the list can key on them')
  })
})

describe('formatTimestamp', () => {
  test('drops the separator between the date and the time', () => {
    const formatted = formatTimestamp(Date.UTC(2024, 5, 5, 10, 30, 0))

    assert.doesNotMatch(formatted, /,/, 'a log gutter reads better without it')
  })

  test('renders two-digit, 24-hour parts', () => {
    const formatted = formatTimestamp(Date.UTC(2024, 5, 5, 22, 30, 5))

    // Locale ordering varies; what matters is that every part is zero-padded
    // and that the hour is not rendered as 10 PM.
    assert.match(formatted, /^[\d/.\-\s:]+$/)
    assert.doesNotMatch(formatted, /[AP]M/i)
    assert.equal(formatted.match(/\d{2}/g)?.length, 6)
  })
})

describe('buildFilter', () => {
  const lines = ['alpha', 'beta', 'gamma'].map((text) => ({ text }) as Line)

  test('matches everything when the pattern is empty', () => {
    assert.deepEqual(lines.filter(buildFilter('')), lines)
  })

  test('filters by regexp', () => {
    assert.deepEqual(lines.filter(buildFilter('^a')).map((l) => l.text), ['alpha'])
  })

  test('supports regexp syntax, not just substrings', () => {
    assert.deepEqual(lines.filter(buildFilter('a(lph|mm)a')).map((l) => l.text), ['alpha', 'gamma'])
  })

  test('matches everything when the pattern will not compile', () => {
    // Half-typed patterns are the common case; blanking the viewport while
    // someone is still typing would be worse than showing too much.
    assert.deepEqual(lines.filter(buildFilter('[unclosed')), lines)
  })

  test('is case sensitive', () => {
    assert.deepEqual(lines.filter(buildFilter('ALPHA')), [])
  })
})
