import assert from 'node:assert/strict'
import dns from 'node:dns/promises'
import os from 'node:os'
import { after, before, describe, it } from 'node:test'
import { listen, runClient, waitFor } from './util.js'

const PORT = 9991

describe('rtail-client', () => {
  let udp

  before(async () => {
    udp = await listen(PORT)
  })

  after(async () => {
    await udp.close()
  })

  /** Runs the client against the shared listener and returns just its messages. */
  async function send(args, input) {
    const before = udp.messages.length
    const result = await runClient(['--port', String(PORT), ...args], input)
    return { result, messages: udp.messages.slice(before) }
  }

  it('splits stdin by newline', async () => {
    const { messages } = await send([], 'alpha\nbeta\ngamma\n')

    assert.equal(messages.length, 3)
    assert.deepEqual(messages.map((m) => m.content), ['alpha', 'beta', 'gamma'])
    assert.ok(messages[0].id)
    assert.equal(typeof messages[0].timestamp, 'number')
  })

  // Every line is offered to JSON5 first, so a line that happens to be a bare
  // number arrives as a number rather than a string. Long-standing behaviour,
  // pinned here because it is surprising.
  it('parses a bare numeric line as a number', async () => {
    const { messages } = await send([], '0\n1\n2\n')

    assert.deepEqual(messages.map((m) => m.content), [0, 1, 2])
  })

  it('uses a custom stream name', async () => {
    const { messages } = await send(['--name', 'test'], 'a\nb\nc\n')

    assert.equal(messages.length, 3)
    assert.ok(messages.every((m) => 'test' === m.id))
  })

  it('echoes stdin to stdout by default', async () => {
    const { result } = await send([], 'hello\n')

    assert.equal(result.stdout, 'hello\n')
  })

  it('respects --mute', async () => {
    const { result } = await send(['--mute'], '0\n1\n2\n')

    assert.equal(result.stdout, '')
  })

  it('strips ansi colours when stdout is not a tty', async () => {
    const esc = String.fromCharCode(27)
    const { result } = await send([], `hello ${esc}[32mworld${esc}[0m\n`)

    assert.equal(result.stdout, 'hello world\n')
  })

  it('parses JSON lines', async () => {
    const { messages } = await send([], '{ "foo": "bar" }\n')

    assert.equal(messages.length, 1)
    assert.equal(messages[0].content.foo, 'bar')
  })

  it('parses JSON5 lines', async () => {
    const { messages } = await send([], '{ foo: "bar" }\n')

    assert.equal(messages.length, 1)
    assert.equal(messages[0].content.foo, 'bar')
  })

  it('emits a line for a blank input line', async () => {
    const { messages } = await send([], 'a\n\nb\n')

    assert.deepEqual(messages.map((m) => m.content), ['a', '', 'b'])
  })

  it('extracts a leading date and strips it from the line', async () => {
    const { messages } = await send([], '2015-07-08T10:00:00Z something happened\n')

    assert.equal(messages.length, 1)
    assert.equal(messages[0].content, 'something happened')
    assert.equal(messages[0].timestamp, Date.parse('2015-07-08T10:00:00Z'))
  })

  it('leaves the line alone with --no-parse-date', async () => {
    const line = '2015-07-08T10:00:00Z something happened'
    const { messages } = await send(['--no-parse-date'], `${line}\n`)

    assert.equal(messages[0].content, line)
  })

  it('supports a custom host and port', async () => {
    const { address } = await dns.lookup(os.hostname())
    const other = await listen(9992, address)

    try {
      await runClient(['--port', '9992', '--host', address, '--mute'], '{ foo: "bar" }\n')
      await waitFor(() => other.messages.length >= 1, 'a message on the custom port')

      assert.equal(other.messages[0].content.foo, 'bar')
    } finally {
      await other.close()
    }
  })
})
