import test, { describe } from 'node:test'
import assert from 'node:assert/strict'
import { ADJECTIVES, choose, NOUNS } from '../../cli/lib/moniker.ts'
import { pkg } from '../../cli/lib/pkg.ts'

describe('choose', () => {
  test('joins an adjective and a noun with a dash', () => {
    const name = choose()
    const [adjective, noun, ...rest] = name.split('-')

    assert.equal(rest.length, 0, `expected exactly one dash in "${name}"`)
    assert.ok(ADJECTIVES.includes(adjective!), `"${adjective}" is not in the adjective list`)
    assert.ok(NOUNS.includes(noun!), `"${noun}" is not in the noun list`)
  })

  test('picks the first entry when the source returns 0', () => {
    assert.equal(choose(() => 0), `${ADJECTIVES[0]}-${NOUNS[0]}`)
  })

  test('stays in bounds when the source returns almost 1', () => {
    // Math.random() is exclusive of 1, and the index must not run off the end.
    assert.equal(
      choose(() => 0.999999),
      `${ADJECTIVES[ADJECTIVES.length - 1]}-${NOUNS[NOUNS.length - 1]}`
    )
  })

  test('varies across calls', () => {
    const names = new Set(Array.from({ length: 50 }, () => choose()))

    assert.ok(names.size > 1, 'expected more than one distinct name in 50 draws')
  })

  test('has no duplicates in either word list', () => {
    // A duplicate would silently skew the distribution.
    assert.equal(new Set(ADJECTIVES).size, ADJECTIVES.length)
    assert.equal(new Set(NOUNS).size, NOUNS.length)
  })
})

describe('pkg', () => {
  test('reads this package manifest', () => {
    assert.equal(pkg.name, 'rtail')
    assert.match(pkg.version, /^\d+\.\d+\.\d+/)
  })

  test('exposes the bin entries that npm installs', () => {
    // These point at .ts files on purpose; Node strips the types on load.
    assert.deepEqual(pkg.bin, {
      rtail: './cli/rtail-client.ts',
      'rtail-server': './cli/rtail-server.ts'
    })
  })
})
