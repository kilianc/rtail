import test, { afterEach, beforeEach, describe } from 'node:test'
import assert from 'node:assert/strict'
import { onRouteChange, readStream, writeStream } from '../../app/src/lib/router.ts'
import { act, setupDom, type Dom } from '../helpers/dom.ts'

describe('router', () => {
  let dom: Dom

  beforeEach(() => {
    dom = setupDom('http://localhost/')
  })

  afterEach(() => dom.cleanup())

  /** Sets the hash without going through writeStream. */
  function setHash(hash: string) {
    dom.window.history.replaceState(null, '', hash)
  }

  describe('readStream', () => {
    test('returns null when there is no route', () => {
      assert.equal(readStream(), null)
    })

    test('reads the stream out of the hash', () => {
      setHash('#/streams/api-gateway')

      assert.equal(readStream(), 'api-gateway')
    })

    test('decodes a percent-encoded name', () => {
      setHash('#/streams/api%2Fv2')

      assert.equal(readStream(), 'api/v2')
    })

    test('ignores an unrelated hash', () => {
      setHash('#/settings')

      assert.equal(readStream(), null)
    })

    test('treats an empty name as no route', () => {
      setHash('#/streams/')

      assert.equal(readStream(), null)
    })
  })

  describe('writeStream', () => {
    test('writes the stream into the hash', () => {
      writeStream('api-gateway')

      assert.equal(dom.window.location.hash, '#/streams/api-gateway')
    })

    test('encodes a name with url-significant characters', () => {
      writeStream('api/v2')

      assert.equal(dom.window.location.hash, '#/streams/api%2Fv2')
      assert.equal(readStream(), 'api/v2', 'and reads back unchanged')
    })

    test('clears the route for a null stream', () => {
      writeStream('api')
      writeStream(null)

      assert.equal(dom.window.location.hash, '#/')
    })

    test('does not add a history entry per switch', () => {
      const before = dom.window.history.length

      writeStream('one')
      writeStream('two')
      writeStream('three')

      assert.equal(dom.window.history.length, before, 'expected replaceState, not pushState')
    })

    test('is a no-op when the hash already matches', () => {
      writeStream('api')

      let calls = 0
      const original = dom.window.history.replaceState.bind(dom.window.history)
      dom.window.history.replaceState = ((...args: unknown[]) => {
        calls++
        return original(...(args as Parameters<typeof original>))
      }) as typeof original

      try {
        writeStream('api')
        assert.equal(calls, 0)
      } finally {
        dom.window.history.replaceState = original
      }
    })
  })

  describe('onRouteChange', () => {
    test('reports the stream on a hash change', () => {
      const seen: (string | null)[] = []
      const stop = onRouteChange((stream) => seen.push(stream))

      setHash('#/streams/from-history')
      act(() => {
        dom.window.dispatchEvent(new dom.window.Event('hashchange'))
      })

      stop()

      assert.deepEqual(seen, ['from-history'])
    })

    test('reports null when the route is cleared', () => {
      const seen: (string | null)[] = []
      const stop = onRouteChange((stream) => seen.push(stream))

      setHash('#/')
      act(() => {
        dom.window.dispatchEvent(new dom.window.Event('hashchange'))
      })

      stop()

      assert.deepEqual(seen, [null])
    })

    test('stops reporting once unsubscribed', () => {
      const seen: (string | null)[] = []
      const stop = onRouteChange((stream) => seen.push(stream))

      stop()

      setHash('#/streams/ignored')
      act(() => {
        dom.window.dispatchEvent(new dom.window.Event('hashchange'))
      })

      assert.deepEqual(seen, [])
    })
  })
})
