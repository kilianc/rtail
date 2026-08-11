import test, { describe } from 'node:test'
import assert from 'node:assert/strict'
import { collectPaths, getPath, isPlainObject, parsePath, stringify } from '../../app/src/lib/json.ts'

describe('parsePath', () => {
  test('parses a bare key', () => {
    assert.deepEqual(parsePath('level'), ['level'])
  })

  test('parses a dotted path', () => {
    assert.deepEqual(parsePath('user.profile.name'), ['user', 'profile', 'name'])
  })

  test('parses an array subscript', () => {
    assert.deepEqual(parsePath('items[0]'), ['items', 0])
  })

  test('parses a subscript followed by a key', () => {
    assert.deepEqual(parsePath('items[0].sku'), ['items', 0, 'sku'])
  })

  test('parses nested subscripts', () => {
    assert.deepEqual(parsePath('grid[1][2]'), ['grid', 1, 2])
  })

  test('rejects an empty input', () => {
    assert.equal(parsePath(''), null)
  })

  test('rejects a leading dot', () => {
    assert.equal(parsePath('.level'), null)
  })

  test('rejects an unclosed subscript', () => {
    assert.equal(parsePath('items[0'), null)
  })

  test('rejects a non-numeric subscript', () => {
    // Only numbers index; `items[sku]` is not a path this language addresses.
    assert.equal(parsePath('items[sku]'), null)
  })

  test('rejects text jammed against a closing bracket', () => {
    assert.equal(parsePath('items[0]sku'), null)
  })
})

describe('getPath', () => {
  const payload = {
    level: 'error',
    zero: 0,
    empty: '',
    nothing: null,
    user: { id: 42 },
    items: [{ sku: 'a' }, { sku: 'b' }]
  }

  test('reads a top-level key', () => {
    assert.equal(getPath(payload, ['level']), 'error')
  })

  test('reads a nested key', () => {
    assert.equal(getPath(payload, ['user', 'id']), 42)
  })

  test('indexes into an array', () => {
    assert.equal(getPath(payload, ['items', 1, 'sku']), 'b')
  })

  test('returns falsy values rather than treating them as missing', () => {
    assert.equal(getPath(payload, ['zero']), 0)
    assert.equal(getPath(payload, ['empty']), '')
    assert.equal(getPath(payload, ['nothing']), null)
  })

  test('returns undefined for a missing key', () => {
    assert.equal(getPath(payload, ['nope']), undefined)
  })

  test('returns undefined when a hop is not an object', () => {
    assert.equal(getPath(payload, ['level', 'length']), undefined)
  })

  test('returns undefined when descending through null', () => {
    assert.equal(getPath(payload, ['nothing', 'anything']), undefined)
  })

  test('returns undefined when indexing something that is not an array', () => {
    assert.equal(getPath(payload, ['user', 0]), undefined)
  })

  test('returns undefined past the end of an array', () => {
    assert.equal(getPath(payload, ['items', 9]), undefined)
  })

  test('does not walk the prototype chain', () => {
    // These are paths a user can type; answering them from the prototype would
    // report something that is not in the log line.
    assert.equal(getPath(payload, ['constructor']), undefined)
    assert.equal(getPath(payload, ['toString']), undefined)
    assert.equal(getPath(payload, ['__proto__']), undefined)
  })

  test('returns the payload itself for an empty path', () => {
    assert.equal(getPath(payload, []), payload)
  })

  test('returns undefined for a non-object payload', () => {
    assert.equal(getPath('a string', ['length']), undefined)
    assert.equal(getPath(null, ['x']), undefined)
  })
})

describe('collectPaths', () => {
  test('finds nothing in an empty buffer', () => {
    assert.deepEqual(collectPaths([]), [])
  })

  test('ignores lines that are not objects', () => {
    assert.deepEqual(collectPaths(['text', 42, null, ['a']]), [])
  })

  test('counts how many lines carry each path', () => {
    const paths = collectPaths([{ level: 'a' }, { level: 'b' }, { other: 'c' }])

    assert.deepEqual(paths, [
      { path: 'level', count: 2 },
      { path: 'other', count: 1 }
    ])
  })

  test('orders by frequency, then alphabetically', () => {
    const paths = collectPaths([{ b: 1, a: 1 }, { b: 1 }])

    assert.deepEqual(paths.map((p) => p.path), ['b', 'a'])
  })

  test('descends into nested objects', () => {
    const paths = collectPaths([{ user: { id: 1, name: 'x' } }])

    assert.deepEqual(paths.map((p) => p.path).sort(), ['user.id', 'user.name'])
  })

  test('treats an array as a leaf', () => {
    // Suggesting a path per element would bury the fields that actually
    // repeat across lines.
    const paths = collectPaths([{ items: [{ sku: 'a' }] }])

    assert.deepEqual(paths.map((p) => p.path), ['items'])
  })

  test('skips keys that cannot be typed back into the box', () => {
    const paths = collectPaths([{ 'has space': 1, 'has.dot': 1, ok: 1 }])

    assert.deepEqual(paths.map((p) => p.path), ['ok'])
  })

  test('stops descending past the depth cutoff', () => {
    const deep = { a: { b: { c: { d: { e: { f: 1 } } } } } }
    const paths = collectPaths([deep])

    assert.equal(paths.length, 1)
    assert.ok(paths[0]!.path.split('.').length <= 5, `too deep: ${paths[0]!.path}`)
  })

  test('honours the limit', () => {
    const wide = Object.fromEntries(Array.from({ length: 60 }, (_, i) => [`k${i}`, i]))

    assert.equal(collectPaths([wide]).length, 40)
    assert.equal(collectPaths([wide], 5).length, 5)
  })
})

describe('isPlainObject', () => {
  test('accepts an object', () => {
    assert.equal(isPlainObject({}), true)
  })

  test('rejects arrays, null and scalars', () => {
    assert.equal(isPlainObject([]), false)
    assert.equal(isPlainObject(null), false)
    assert.equal(isPlainObject('x'), false)
    assert.equal(isPlainObject(42), false)
    assert.equal(isPlainObject(undefined), false)
  })
})

describe('stringify', () => {
  test('passes a string through unquoted', () => {
    assert.equal(stringify('error'), 'error')
  })

  test('renders scalars the way they read in a query', () => {
    assert.equal(stringify(42), '42')
    assert.equal(stringify(true), 'true')
    assert.equal(stringify(null), 'null')
    assert.equal(stringify(undefined), 'undefined')
  })

  test('serialises objects and arrays as JSON', () => {
    assert.equal(stringify({ a: 1 }), '{"a":1}')
    assert.equal(stringify([1, 2]), '[1,2]')
  })

  test('survives a value that cannot be serialised', () => {
    // A getter that throws should not take the viewport down.
    const hostile = {
      get boom() {
        throw new Error('nope')
      }
    }

    assert.equal(stringify(hostile), '')
  })
})
