import test, { describe } from 'node:test'
import assert from 'node:assert/strict'
import { listen } from '../helpers/udp.ts'
import { runClient } from '../helpers/spawn.ts'

/**
 * The bins as installed: `node cli/rtail-client.ts`, types stripped at load,
 * no build step. If the shebang, the .ts import specifiers or the Node floor
 * ever regress, this is what notices.
 */
describe('rtail (client bin)', () => {
  test('sends one datagram per line', async () => {
    const udp = await listen()

    const { code } = await runClient(['--port', String(udp.port), '--mute'], 'one\ntwo\n')

    await udp.waitFor(2)
    await udp.close()

    assert.equal(code, 0)
    assert.deepEqual(
      udp.messages.map((m) => (m as { content: unknown }).content),
      ['one', 'two']
    )
  })

  test('echoes stdin to stdout by default', async () => {
    const udp = await listen()

    const { stdout } = await runClient(['--port', String(udp.port)], 'hello\n')

    await udp.close()

    assert.equal(stdout, 'hello\n')
  })

  test('respects --mute', async () => {
    const udp = await listen()

    const { stdout } = await runClient(['--port', String(udp.port), '--mute'], 'hello\n')

    await udp.waitFor(1)
    await udp.close()

    assert.equal(stdout, '')
    assert.equal(udp.messages.length, 1)
  })

  test('strips ansi colours when stdout is not a tty', async () => {
    const udp = await listen()

    // Written as an escape rather than a literal ESC byte: a bare control
    // character is invisible in a diff and trivially lost in an edit.
    const { stdout } = await runClient(['--port', String(udp.port)], '\u001b[32mgreen\u001b[0m\n')

    await udp.close()

    assert.equal(stdout, 'green\n')
  })

  test('names the stream with --id', async () => {
    const udp = await listen()

    await runClient(['--port', String(udp.port), '--id', 'api.domain.com', '--mute'], 'x\n')

    await udp.waitFor(1)
    await udp.close()

    assert.equal((udp.messages[0] as { id: string }).id, 'api.domain.com')
  })

  test('defaults to a generated stream name', async () => {
    const udp = await listen()

    await runClient(['--port', String(udp.port), '--mute'], 'x\n')

    await udp.waitFor(1)
    await udp.close()

    assert.match((udp.messages[0] as { id: string }).id, /^[a-z]+-[a-z]+$/)
  })

  test('parses JSON lines into objects', async () => {
    const udp = await listen()

    await runClient(['--port', String(udp.port), '--mute'], '{"level":"warn"}\n')

    await udp.waitFor(1)
    await udp.close()

    assert.deepEqual((udp.messages[0] as { content: unknown }).content, { level: 'warn' })
  })

  test('extracts a leading date and strips it from the line', async () => {
    const udp = await listen()

    await runClient(
      ['--port', String(udp.port), '--mute'],
      '2024-06-05T10:00:00Z something happened\n'
    )

    await udp.waitFor(1)
    await udp.close()

    const message = udp.messages[0] as { content: string; timestamp: number }

    assert.equal(message.content, 'something happened')
    assert.equal(message.timestamp, Date.parse('2024-06-05T10:00:00Z'))
  })

  test('leaves the line alone with --no-parse-date', async () => {
    const udp = await listen()

    const raw = '2024-06-05T10:00:00Z something happened'
    await runClient(['--port', String(udp.port), '--mute', '--no-parse-date'], `${raw}\n`)

    await udp.waitFor(1)
    await udp.close()

    assert.equal((udp.messages[0] as { content: string }).content, raw)
  })

  test('prints its version', async () => {
    const { stdout, code } = await runClient(['--version'], '')

    assert.equal(code, 0)
    assert.match(stdout.trim(), /^\d+\.\d+\.\d+/)
  })

  test('rejects an unknown flag', async () => {
    const { code, stderr } = await runClient(['--nope'], '')

    assert.notEqual(code, 0)
    assert.match(stderr, /Unknown argument/i)
  })
})
