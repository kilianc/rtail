import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { describe, it } from 'node:test'
import * as esbuild from 'esbuild'

/**
 * The filter box's query language.
 *
 * The webapp is TypeScript and the test runner is plain node, so the module
 * under test is bundled in memory first — the same esbuild the build uses,
 * with no build step to keep in sync and nothing written to disk.
 */
const entry = fileURLToPath(new URL('../app/src/lib/query.ts', import.meta.url))

const bundle = await esbuild.build({
  entryPoints: [entry],
  bundle: true,
  format: 'esm',
  write: false,
  logLevel: 'silent'
})

const { parseQuery, matchesQuery } = await import(
  `data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`
)

/** A line as StreamView sees it: rendered text, plus the payload it came from. */
const line = (content) =>
  'string' === typeof content
    ? { text: content, content }
    : { text: JSON.stringify(content, null, '  '), content }

const matches = (query, content) => matchesQuery(parseQuery(query), line(content))

describe('query: text terms', () => {
  it('matches every line when empty', () => {
    assert.equal(parseQuery('').isEmpty, true)
    assert.equal(matches('', 'anything'), true)
    assert.equal(matches('   ', 'anything'), true)
  })

  it('ANDs the terms, in any order', () => {
    assert.equal(matches('payment failed', 'payment gateway failed'), true)
    assert.equal(matches('failed payment', 'payment gateway failed'), true)
    assert.equal(matches('payment refunded', 'payment gateway failed'), false)
  })

  it('keeps a quoted phrase together', () => {
    assert.equal(matches('"payment failed"', 'the payment failed'), true)
    assert.equal(matches('"payment failed"', 'payment gateway failed'), false)
  })

  it('negates with a leading dash', () => {
    assert.equal(matches('-healthcheck', 'GET /users'), true)
    assert.equal(matches('-healthcheck', 'GET /healthcheck'), false)
  })

  it('ignores case until a term has a capital', () => {
    assert.equal(matches('error', 'ERROR: nope'), true)
    assert.equal(matches('ERROR', 'error: nope'), false)
    assert.equal(matches('ERROR', 'ERROR: nope'), true)
  })

  it('takes a regexp between slashes', () => {
    assert.equal(matches('/GET \\/1\\/users/', 'GET /1/users 200'), true)
    assert.equal(matches('/GET \\/1\\/users/', 'POST /1/users 200'), false)
  })

  it('searches a bare slash as text, not as a regexp', () => {
    assert.equal(matches('/1/users', 'GET /1/users'), true)
    assert.equal(matches('https://example.com', 'GET https://example.com'), true)
  })

  it('keeps filtering on the terms that parsed when one does not', () => {
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

  it('matches a field by substring', () => {
    assert.equal(matches('level:err', payload), true)
    assert.equal(matches('level:warn', payload), false)
  })

  it('walks a dotted path', () => {
    assert.equal(matches('user.name:ada', payload), true)
    assert.equal(matches('user.id=42', payload), true)
    assert.equal(matches('user.missing:x', payload), false)
  })

  it('indexes into arrays', () => {
    assert.equal(matches('tags[1]=b', payload), true)
    assert.equal(matches('tags[1]=a', payload), false)
  })

  it('compares numbers', () => {
    assert.equal(matches('duration>100', payload), true)
    assert.equal(matches('duration>250', payload), false)
    assert.equal(matches('duration>=250', payload), true)
    assert.equal(matches('duration<100', payload), false)
  })

  it('distinguishes exact from contains', () => {
    assert.equal(matches('level=error', payload), true)
    assert.equal(matches('level=err', payload), false)
    assert.equal(matches('level!=warn', payload), true)
  })

  it('asks whether a field is there at all', () => {
    assert.equal(matches('user.id:*', payload), true)
    assert.equal(matches('trace_id:*', payload), false)
  })

  it('never matches a line without the field, including with !=', () => {
    assert.equal(matches('level!=error', 'a plain text line'), false)
    assert.equal(matches('level:error', 'the level is error'), false)
  })

  it('reads booleans and nested objects as text', () => {
    assert.equal(matches('ok=false', payload), true)
    assert.equal(matches('user:Ada', payload), true)
  })

  it('does not walk into the prototype chain', () => {
    assert.equal(matches('constructor:*', payload), false)
  })
})

describe('query: highlighting', () => {
  it('offers the positive terms as needles', () => {
    const query = parseQuery('boom -quiet level:error')

    assert.deepEqual(
      query.needles.map((needle) => needle.value),
      ['boom', 'error']
    )
  })

  it('leaves out the bound of a comparison, which no line contains', () => {
    assert.deepEqual(parseQuery('duration>250').needles, [])
    assert.deepEqual(parseQuery('trace_id:*').needles, [])
  })
})
