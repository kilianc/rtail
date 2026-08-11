import test, { describe } from 'node:test'
import assert from 'node:assert/strict'
import { Readable, Writable } from 'node:stream'
import { createClient, parseLine, type WirePayload } from '../../cli/lib/client.ts'
import { listen } from '../helpers/udp.ts'

const FIXED_NOW = 1_700_000_000_000
const now = () => FIXED_NOW

/** parseLine with the boring options filled in. */
function parse(raw: string, overrides: Partial<Parameters<typeof parseLine>[1]> = {}): WirePayload {
  return parseLine(raw, { id: 'stream', parseDate: true, now, ...overrides })
}

describe('parseLine', () => {
  test('passes a plain line through as text', () => {
    const payload = parse('just a log line')

    assert.equal(payload.content, 'just a log line')
    assert.equal(payload.id, 'stream')
    assert.equal(payload.timestamp, FIXED_NOW)
  })

  test('parses a JSON line into an object', () => {
    const payload = parse('{"level":"warn","msg":"disk full"}')

    assert.deepEqual(payload.content, { level: 'warn', msg: 'disk full' })
  })

  test('parses a JSON5 line', () => {
    // Unquoted keys and a trailing comma — invalid JSON, valid JSON5.
    const payload = parse("{level: 'warn', msg: 'disk full',}")

    assert.deepEqual(payload.content, { level: 'warn', msg: 'disk full' })
  })

  test('parses a bare numeric line as a number', () => {
    const payload = parse('42')

    assert.equal(payload.content, 42)
    assert.equal(typeof payload.content, 'number')
  })

  test('keeps a blank line as an empty string', () => {
    const payload = parse('')

    assert.equal(payload.content, '')
    assert.equal(payload.timestamp, FIXED_NOW)
  })

  test('promotes a leading date to the timestamp and strips it', () => {
    const payload = parse('2024-06-05T10:00:00Z something happened')

    assert.equal(payload.content, 'something happened')
    assert.equal(payload.timestamp, Date.parse('2024-06-05T10:00:00Z'))
  })

  test('leaves the line alone with parseDate off', () => {
    const raw = '2024-06-05T10:00:00Z something happened'
    const payload = parse(raw, { parseDate: false })

    assert.equal(payload.content, raw)
    assert.equal(payload.timestamp, FIXED_NOW)
  })

  test('escapes regexp metacharacters in the matched date text', () => {
    // The stripping step builds a RegExp out of the matched text. Brackets in
    // the surrounding line must not turn into a character class.
    const payload = parse('[2024-06-05 10:00:00] boom (retrying)')

    assert.equal(typeof payload.content, 'string')
    assert.match(payload.content as string, /boom \(retrying\)/)
    assert.doesNotMatch(payload.content as string, /2024-06-05/)
  })

  test('does not look for a date inside a structured line', () => {
    const payload = parse('{"at":"2024-06-05T10:00:00Z"}')

    assert.deepEqual(payload.content, { at: '2024-06-05T10:00:00Z' })
    assert.equal(payload.timestamp, FIXED_NOW, 'structured lines keep the arrival time')
  })

  test('falls back to the real clock when none is injected', () => {
    const before = Date.now()
    const payload = parseLine('x', { id: 'stream', parseDate: false })

    assert.ok(payload.timestamp >= before)
  })
})

describe('createClient', () => {
  /** Collects everything the client echoes. */
  function sink() {
    const chunks: string[] = []
    const stream = new Writable({
      write(chunk, _enc, cb) {
        chunks.push(String(chunk))
        cb()
      }
    })
    return { stream, text: () => chunks.join('') }
  }

  const base = {
    host: '127.0.0.1',
    id: 'stream',
    mute: false,
    tty: false,
    parseDate: true,
    isTTY: false,
    now
  }

  test('broadcasts one datagram per line', async () => {
    const udp = await listen()
    const out = sink()

    const client = createClient({
      ...base,
      port: udp.port,
      input: Readable.from(['first\nsecond\nthird\n']),
      output: out.stream
    })

    await client.finished
    await udp.waitFor(3)
    await udp.close()

    assert.equal(udp.messages.length, 3)
    assert.deepEqual(
      udp.messages.map((m) => (m as WirePayload).content),
      ['first', 'second', 'third']
    )
  })

  test('echoes stdin to the output by default', async () => {
    const udp = await listen()
    const out = sink()

    await createClient({
      ...base,
      port: udp.port,
      input: Readable.from(['hello\n']),
      output: out.stream
    }).finished

    await udp.close()

    assert.equal(out.text(), 'hello\n')
  })

  test('writes nothing when muted', async () => {
    const udp = await listen()
    const out = sink()

    await createClient({
      ...base,
      port: udp.port,
      mute: true,
      input: Readable.from(['hello\n']),
      output: out.stream
    }).finished

    await udp.waitFor(1)
    await udp.close()

    assert.equal(out.text(), '')
    assert.equal(udp.messages.length, 1, 'muting affects stdout only, not the wire')
  })

  test('strips ansi colours when the output is not a terminal', async () => {
    const udp = await listen()
    const out = sink()

    await createClient({
      ...base,
      port: udp.port,
      tty: true,
      isTTY: false,
      input: Readable.from(['\u001b[32mgreen\u001b[0m\n']),
      output: out.stream
    }).finished

    await udp.close()

    assert.equal(out.text(), 'green\n')
  })

  test('keeps ansi colours on a terminal with --tty', async () => {
    const udp = await listen()
    const out = sink()

    await createClient({
      ...base,
      port: udp.port,
      tty: true,
      isTTY: true,
      input: Readable.from(['\u001b[32mgreen\u001b[0m\n']),
      output: out.stream
    }).finished

    await udp.close()

    assert.equal(out.text(), '\u001b[32mgreen\u001b[0m\n')
  })

  test('strips ansi colours on a terminal with --no-tty', async () => {
    const udp = await listen()
    const out = sink()

    await createClient({
      ...base,
      port: udp.port,
      tty: false,
      isTTY: true,
      input: Readable.from(['\u001b[32mgreen\u001b[0m\n']),
      output: out.stream
    }).finished

    await udp.close()

    assert.equal(out.text(), 'green\n')
  })

  test('tags every datagram with the stream id', async () => {
    const udp = await listen()
    const out = sink()

    await createClient({
      ...base,
      id: 'api-gateway',
      port: udp.port,
      input: Readable.from(['x\n']),
      output: out.stream
    }).finished

    await udp.waitFor(1)
    await udp.close()

    assert.equal((udp.messages[0] as WirePayload).id, 'api-gateway')
  })

  test('resolves once an empty stdin closes', async () => {
    const udp = await listen()
    const out = sink()

    await createClient({
      ...base,
      port: udp.port,
      input: Readable.from([]),
      output: out.stream
    }).finished

    await udp.close()

    assert.equal(udp.messages.length, 0)
  })
})
