/*!
 * The filter box's query language.
 *
 * Ported from the standalone test/query.test.js, which bundled the module into
 * a data: URL with esbuild because node:test could not import TypeScript. It
 * can now — and the direct import matters for more than tidiness: coverage
 * cannot attribute a data: URL back to app/src/lib/query.ts, so the bundled
 * version scored the module at zero however much it exercised.
 */

import test, { describe } from 'node:test'
import assert from 'node:assert/strict'
import { matchNeedle, matchesQuery, parseQuery } from '../../app/src/lib/query.ts'

/** A line as StreamView sees it: rendered text, plus the payload it came from. */
const line = (content: unknown) =>
  'string' === typeof content
    ? { text: content, content }
    : { text: JSON.stringify(content, null, '  '), content }

const matches = (query: string, content: unknown) => matchesQuery(parseQuery(query), line(content))

describe('query: text terms', () => {
  test('matches every line when empty', () => {
    assert.equal(parseQuery('').isEmpty, true)
    assert.equal(matches('', 'anything'), true)
    assert.equal(matches('   ', 'anything'), true)
  })

  test('ANDs the terms, in any order', () => {
    assert.equal(matches('payment failed', 'payment gateway failed'), true)
    assert.equal(matches('failed payment', 'payment gateway failed'), true)
    assert.equal(matches('payment refunded', 'payment gateway failed'), false)
  })

  test('keeps a quoted phrase together', () => {
    assert.equal(matches('"payment failed"', 'the payment failed'), true)
    assert.equal(matches('"payment failed"', 'payment gateway failed'), false)
  })

  test('keeps a single-quoted phrase together too', () => {
    assert.equal(matches("'payment failed'", 'the payment failed'), true)
  })

  test('unescapes a quote inside a quoted phrase', () => {
    assert.equal(matches('"say \\"hi\\""', 'they say "hi" back'), true)
  })

  test('negates with a leading dash', () => {
    assert.equal(matches('-healthcheck', 'GET /users'), true)
    assert.equal(matches('-healthcheck', 'GET /healthcheck'), false)
  })

  test('negates with a leading bang', () => {
    assert.equal(matches('!healthcheck', 'GET /users'), true)
    assert.equal(matches('!healthcheck', 'GET /healthcheck'), false)
  })

  test('ignores a bare negation with nothing after it', () => {
    assert.equal(parseQuery('-').isEmpty, true)
  })

  test('ignores case until a term has a capital', () => {
    assert.equal(matches('error', 'ERROR: nope'), true)
    assert.equal(matches('ERROR', 'error: nope'), false)
    assert.equal(matches('ERROR', 'ERROR: nope'), true)
  })

  test('takes a regexp between slashes', () => {
    assert.equal(matches('/GET \\/1\\/users/', 'GET /1/users 200'), true)
    assert.equal(matches('/GET \\/1\\/users/', 'POST /1/users 200'), false)
  })

  test('applies smart case to a regexp as well', () => {
    assert.equal(matches('/error/', 'ERROR'), true)
    assert.equal(matches('/Error/', 'ERROR'), false)
  })

  test('honours an explicit regexp flag', () => {
    assert.equal(matches('/ERROR/i', 'error'), true)
  })

  test('drops stateful regexp flags', () => {
    // `g` and `y` carry lastIndex between calls, which would make a line's
    // fate depend on the order the rows happened to be tested in.
    const query = parseQuery('/a/g')
    const needle = query.needles[0]

    assert.ok(needle && 'regexp' === needle.type)
    assert.doesNotMatch(needle.re.flags, /[gy]/)
  })

  test('searches a bare slash as text, not as a regexp', () => {
    assert.equal(matches('/1/users', 'GET /1/users'), true)
    assert.equal(matches('https://example.com', 'GET https://example.com'), true)
  })

  test('keeps a space inside a regexp', () => {
    assert.equal(matches('/payment failed/', 'the payment failed'), true)
  })

  test('keeps filtering on the terms that parsed when one does not', () => {
    const query = parseQuery('boom /unclosed(/')

    assert.notEqual(query.error, null)
    assert.equal(matchesQuery(query, line('boom')), true)
    assert.equal(matchesQuery(query, line('quiet')), false)
  })
})

describe('query: field terms', () => {
  const payload = {
    level: 'error',
    duration: 250,
    ok: false,
    user: { id: 42, name: 'Ada' },
    tags: ['a', 'b']
  }

  test('matches a field by substring', () => {
    assert.equal(matches('level:err', payload), true)
    assert.equal(matches('level:warn', payload), false)
  })

  test('walks a dotted path', () => {
    assert.equal(matches('user.name:ada', payload), true)
    assert.equal(matches('user.id=42', payload), true)
    assert.equal(matches('user.missing:x', payload), false)
  })

  test('indexes into arrays', () => {
    assert.equal(matches('tags[1]=b', payload), true)
    assert.equal(matches('tags[1]=a', payload), false)
  })

  test('compares numbers', () => {
    assert.equal(matches('duration>100', payload), true)
    assert.equal(matches('duration>250', payload), false)
    assert.equal(matches('duration>=250', payload), true)
    assert.equal(matches('duration<100', payload), false)
    assert.equal(matches('duration<=250', payload), true)
  })

  test('compares a numeric string', () => {
    assert.equal(matches('duration>100', { duration: '250' }), true)
  })

  test('refuses to compare something that is not a number', () => {
    assert.equal(matches('level>100', payload), false)
    assert.equal(matches('duration>abc', payload), false)
  })

  test('distinguishes exact from contains', () => {
    assert.equal(matches('level=error', payload), true)
    assert.equal(matches('level=err', payload), false)
    assert.equal(matches('level!=warn', payload), true)
  })

  test('matches a field against a regexp', () => {
    assert.equal(matches('level:/err(or)?/', payload), true)
    assert.equal(matches('level=/^error$/', payload), true)
  })

  test('asks whether a field is there at all', () => {
    assert.equal(matches('user.id:*', payload), true)
    assert.equal(matches('trace_id:*', payload), false)
  })

  test('never matches a line without the field, including with !=', () => {
    // `status!=200` means "has a status, and it is not 200". For "not 200,
    // whether or not there is a status", the negated form is the one to use.
    assert.equal(matches('level!=error', 'a plain text line'), false)
    assert.equal(matches('level:error', 'the level is error'), false)
    assert.equal(matches('-level:error', 'a plain text line'), true)
  })

  test('reads booleans, nulls and nested objects as text', () => {
    assert.equal(matches('ok=false', payload), true)
    assert.equal(matches('user:Ada', payload), true)
    assert.equal(matches('missing=null', { missing: null }), true)
  })

  test('does not walk into the prototype chain', () => {
    // A user can type `constructor:*`; answering it from the prototype would
    // report a field that is not in their log line.
    assert.equal(matches('constructor:*', payload), false)
    assert.equal(matches('__proto__:*', payload), false)
  })

  test('treats a malformed path as text', () => {
    assert.equal(matches('9lives:x', '9lives:x is the text'), true)
  })

  test('treats an operator at position zero as text', () => {
    assert.equal(matches(':colon', 'a :colon here'), true)
  })
})

describe('query: highlighting', () => {
  test('offers the positive terms as needles', () => {
    const query = parseQuery('boom -quiet level:error')

    assert.deepEqual(
      query.needles.map((needle) => ('literal' === needle.type ? needle.value : needle.re.source)),
      ['boom', 'error']
    )
  })

  test('leaves out the bound of a comparison, which no line contains', () => {
    assert.deepEqual(parseQuery('duration>250').needles, [])
    assert.deepEqual(parseQuery('trace_id:*').needles, [])
  })
})

describe('matchNeedle', () => {
  test('matches a case-sensitive literal exactly', () => {
    assert.equal(matchNeedle({ type: 'literal', value: 'Err', caseSensitive: true }, 'Error'), true)
    assert.equal(matchNeedle({ type: 'literal', value: 'Err', caseSensitive: true }, 'error'), false)
  })

  test('folds case for an insensitive literal', () => {
    assert.equal(matchNeedle({ type: 'literal', value: 'err', caseSensitive: false }, 'ERROR'), true)
  })

  test('applies a regexp needle', () => {
    assert.equal(matchNeedle({ type: 'regexp', re: /^err/ }, 'error'), true)
    assert.equal(matchNeedle({ type: 'regexp', re: /^err/ }, 'an error'), false)
  })
})
