import test, { describe } from 'node:test'
import assert from 'node:assert/strict'
import { createStreamStore, decodePayload } from '../../cli/lib/streams.ts'

const REMOTE = { address: '10.0.0.7', port: 51234 }

function datagram(value: unknown): Buffer {
  return Buffer.from(JSON.stringify(value))
}

describe('decodePayload', () => {
  test('decodes a well-formed payload', () => {
    const payload = decodePayload(datagram({ id: 'api', timestamp: 42, content: 'hi' }))

    assert.deepEqual(payload, { id: 'api', timestamp: 42, content: 'hi' })
  })

  test('keeps a structured content value intact', () => {
    const payload = decodePayload(datagram({ id: 'api', timestamp: 1, content: { a: [1, 2] } }))

    assert.deepEqual(payload?.content, { a: [1, 2] })
  })

  test('rejects a datagram that is not JSON', () => {
    assert.equal(decodePayload(Buffer.from('not json at all')), null)
  })

  test('rejects a JSON scalar', () => {
    assert.equal(decodePayload(datagram('a string')), null)
    assert.equal(decodePayload(datagram(42)), null)
  })

  test('rejects JSON null', () => {
    assert.equal(decodePayload(datagram(null)), null)
  })

  test('rejects a payload with no id', () => {
    assert.equal(decodePayload(datagram({ content: 'orphan' })), null)
  })

  test('rejects a non-string id', () => {
    // The id is used as a socket.io room name.
    assert.equal(decodePayload(datagram({ id: 7, content: 'x' })), null)
  })

  test('rejects an empty id', () => {
    assert.equal(decodePayload(datagram({ id: '', content: 'x' })), null)
  })

  test('defaults a missing timestamp to now', () => {
    const before = Date.now()
    const payload = decodePayload(datagram({ id: 'api', content: 'x' }))

    assert.ok(payload)
    assert.ok(payload.timestamp >= before)
  })

  test('defaults a non-numeric timestamp to now', () => {
    const before = Date.now()
    const payload = decodePayload(datagram({ id: 'api', timestamp: 'yesterday', content: 'x' }))

    assert.ok(payload)
    assert.ok(payload.timestamp >= before)
  })
})

describe('createStreamStore', () => {
  test('reports a new stream on its first line only', () => {
    const store = createStreamStore(10)

    assert.equal(store.push({ id: 'api', timestamp: 1, content: 'a' }, REMOTE).isNew, true)
    assert.equal(store.push({ id: 'api', timestamp: 2, content: 'b' }, REMOTE).isNew, false)
  })

  test('enriches the line with the sender and the content type', () => {
    const store = createStreamStore(10)

    const { message } = store.push({ id: 'api', timestamp: 7, content: { a: 1 } }, REMOTE)

    assert.deepEqual(message, {
      timestamp: 7,
      streamid: 'api',
      host: '10.0.0.7',
      port: 51234,
      content: { a: 1 },
      type: 'object'
    })
  })

  test('records the type of a scalar line', () => {
    const store = createStreamStore(10)

    assert.equal(store.push({ id: 'a', timestamp: 1, content: 'text' }, REMOTE).message.type, 'string')
    assert.equal(store.push({ id: 'b', timestamp: 1, content: 12 }, REMOTE).message.type, 'number')
  })

  test('lists every stream it has seen', () => {
    const store = createStreamStore(10)

    store.push({ id: 'api', timestamp: 1, content: 'a' }, REMOTE)
    store.push({ id: 'worker', timestamp: 1, content: 'b' }, REMOTE)

    assert.deepEqual(store.names().sort(), ['api', 'worker'])
  })

  test('starts with no streams', () => {
    assert.deepEqual(createStreamStore(10).names(), [])
  })

  test('returns an empty backlog for an unknown stream', () => {
    assert.deepEqual(createStreamStore(10).backlog('nope'), [])
  })

  test('keeps the backlog at the configured size, dropping the oldest', () => {
    const store = createStreamStore(3)

    for (let i = 1; i <= 5; i++) {
      store.push({ id: 'api', timestamp: i, content: i }, REMOTE)
    }

    const backlog = store.backlog('api')

    assert.equal(backlog.length, 3)
    assert.deepEqual(backlog.map((line) => line.content), [3, 4, 5])
  })

  test('keeps each stream backlog independent', () => {
    const store = createStreamStore(2)

    store.push({ id: 'api', timestamp: 1, content: 'a1' }, REMOTE)
    store.push({ id: 'worker', timestamp: 1, content: 'w1' }, REMOTE)
    store.push({ id: 'api', timestamp: 2, content: 'a2' }, REMOTE)

    assert.deepEqual(store.backlog('api').map((l) => l.content), ['a1', 'a2'])
    assert.deepEqual(store.backlog('worker').map((l) => l.content), ['w1'])
  })

  test('holds a single line when the cap is one', () => {
    const store = createStreamStore(1)

    store.push({ id: 'api', timestamp: 1, content: 'old' }, REMOTE)
    store.push({ id: 'api', timestamp: 2, content: 'new' }, REMOTE)

    assert.deepEqual(store.backlog('api').map((l) => l.content), ['new'])
  })
})
