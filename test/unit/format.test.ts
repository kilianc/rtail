import test, { describe } from 'node:test'
import assert from 'node:assert/strict'
import {
  defaultExpanded,
  formatLine,
  formatTimestamp,
  renderFields
} from '../../app/src/lib/format.ts'
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

describe('formatLine: the compact and expanded payloads', () => {
  test('gives an object line both a pretty and a one-line rendering', () => {
    const line = formatLine(wire({ level: 'warn' }, 'object'))

    assert.match(line.html, /\n/, 'the expanded form is pretty-printed')
    assert.ok(line.htmlCompact)
    assert.doesNotMatch(line.htmlCompact, /\n/, 'the compact form is one line')
  })

  test('leaves a text line with no compact form', () => {
    // Nothing to collapse, so there is no caret to offer either.
    assert.equal(formatLine(wire('plain')).htmlCompact, null)
    assert.equal(formatLine(wire('')).htmlCompact, null)
  })

  test('flags a payload that is taller than the auto-expand cutoff', () => {
    const small = formatLine(wire({ a: 1 }, 'object'))
    const large = formatLine(
      wire(Object.fromEntries(Array.from({ length: 40 }, (_, i) => [`k${i}`, i])), 'object')
    )

    assert.equal(small.bulky, false)
    assert.equal(large.bulky, true)
  })
})

describe('defaultExpanded', () => {
  const line = (bulky: boolean) => ({ bulky }) as Line

  test('expands everything when asked to', () => {
    assert.equal(defaultExpanded('expanded', line(true)), true)
    assert.equal(defaultExpanded('expanded', line(false)), true)
  })

  test('collapses everything when asked to', () => {
    assert.equal(defaultExpanded('collapsed', line(false)), false)
    assert.equal(defaultExpanded('collapsed', line(true)), false)
  })

  test('collapses only the bulky ones on auto', () => {
    assert.equal(defaultExpanded('auto', line(false)), true)
    assert.equal(defaultExpanded('auto', line(true)), false)
  })
})

describe('renderFields', () => {
  const payload = { level: 'error', duration: 250, ok: false, user: { id: 42 } }

  test('renders the requested fields as key=value pairs', () => {
    const html = renderFields(payload, ['level', 'duration'])

    assert.ok(html)
    assert.match(html, /level/)
    assert.match(html, /error/)
    assert.match(html, /duration/)
    assert.match(html, /250/)
  })

  test('keeps the requested order', () => {
    const html = renderFields(payload, ['duration', 'level'])!

    assert.ok(html.indexOf('duration') < html.indexOf('level'))
  })

  test('walks a dotted path', () => {
    assert.match(renderFields(payload, ['user.id'])!, /42/)
  })

  test('drops fields the line does not carry', () => {
    // A stream is rarely uniform, and a row of placeholders reads as data.
    const html = renderFields(payload, ['level', 'missing'])!

    assert.match(html, /error/)
    assert.doesNotMatch(html, /missing/)
  })

  test('returns null when the line carries none of them', () => {
    // So the caller can fall back to the payload rather than an empty row.
    assert.equal(renderFields(payload, ['nope']), null)
  })

  test('returns null for a line that is not an object', () => {
    assert.equal(renderFields('a string', ['level']), null)
    assert.equal(renderFields(null, ['level']), null)
    assert.equal(renderFields([1, 2], ['level']), null)
  })

  test('ignores a malformed path', () => {
    assert.equal(renderFields(payload, ['...']), null)
  })

  test('quotes a value only where the quotes earn their place', () => {
    const plain = renderFields({ msg: 'hello' }, ['msg'])!
    const spaced = renderFields({ msg: 'hello there' }, ['msg'])!

    assert.doesNotMatch(plain, /&quot;|"hello"/)
    assert.match(spaced, /&quot;hello there&quot;/)
  })

  test('renders booleans and nulls as literals', () => {
    assert.match(renderFields(payload, ['ok'])!, /hljs-literal/)
    assert.match(renderFields({ n: null }, ['n'])!, /null/)
  })

  test('keeps nested objects highlighted as json', () => {
    assert.match(renderFields(payload, ['user'])!, /hljs/)
  })

  test('escapes a field name and a value', () => {
    const html = renderFields({ '<k>': '<script>alert(1)</script>' }, ['<k>'])

    // The path regexp rejects `<k>`, so nothing is rendered — but if that ever
    // changes, the value must still not come through as live markup.
    if (html) assert.doesNotMatch(html, /<script>/)
  })
})
