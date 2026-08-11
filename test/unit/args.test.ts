import test, { describe } from 'node:test'
import assert from 'node:assert/strict'
import { isValidBacklog, parseClientArgv, parseServerArgv } from '../../cli/lib/args.ts'

describe('parseClientArgv', () => {
  test('applies the documented defaults', () => {
    const args = parseClientArgv([], 'fixed-name')

    assert.deepEqual(args, {
      host: '127.0.0.1',
      port: 9999,
      id: 'fixed-name',
      mute: false,
      tty: true,
      parseDate: true
    })
  })

  test('reads a custom host and port', () => {
    const args = parseClientArgv(['--host', 'example.com', '--port', '43567'], 'x')

    assert.equal(args.host, 'example.com')
    assert.equal(args.port, 43567)
  })

  test('supports the short aliases', () => {
    const args = parseClientArgv(['-h', 'example.com', '-p', '1234'], 'x')

    assert.equal(args.host, 'example.com')
    assert.equal(args.port, 1234)
  })

  test('names the stream with --id', () => {
    assert.equal(parseClientArgv(['--id', 'api.domain.com'], 'x').id, 'api.domain.com')
  })

  test('names the stream with the --name alias', () => {
    assert.equal(parseClientArgv(['--name', 'api'], 'x').id, 'api')
  })

  test('honours --mute', () => {
    assert.equal(parseClientArgv(['--mute'], 'x').mute, true)
    assert.equal(parseClientArgv(['-m'], 'x').mute, true)
  })

  test('honours --no-tty', () => {
    assert.equal(parseClientArgv(['--no-tty'], 'x').tty, false)
  })

  test('honours --no-parse-date', () => {
    assert.equal(parseClientArgv(['--no-parse-date'], 'x').parseDate, false)
  })

  test('falls back to a generated name when none is given', () => {
    // The default is a moniker, so only its shape is predictable.
    assert.match(parseClientArgv([]).id, /^[a-z]+-[a-z]+$/)
  })
})

describe('parseServerArgv', () => {
  test('applies the documented defaults', () => {
    const args = parseServerArgv([])

    assert.equal(args.udpHost, '127.0.0.1')
    assert.equal(args.udpPort, 9999)
    assert.equal(args.webHost, '127.0.0.1')
    assert.equal(args.webPort, 8888)
    assert.equal(args.backlog, 100)
    assert.equal(args.webVersion, undefined)
  })

  test('reads the web host and port', () => {
    const args = parseServerArgv(['--web-host', '0.0.0.0', '--web-port', '8080'])

    assert.equal(args.webHost, '0.0.0.0')
    assert.equal(args.webPort, 8080)
  })

  test('reads the udp host and port', () => {
    const args = parseServerArgv(['--udp-host', '0.0.0.0', '--udp-port', '7777'])

    assert.equal(args.udpHost, '0.0.0.0')
    assert.equal(args.udpPort, 7777)
  })

  test('supports the two-letter aliases', () => {
    const args = parseServerArgv(['--wp', '8080', '--up', '7777', '--wh', '::1', '--uh', '::1'])

    assert.equal(args.webPort, 8080)
    assert.equal(args.udpPort, 7777)
    assert.equal(args.webHost, '::1')
    assert.equal(args.udpHost, '::1')
  })

  test('reads --web-version', () => {
    assert.equal(parseServerArgv(['--web-version', '0.1.3']).webVersion, '0.1.3')
    assert.equal(parseServerArgv(['--web-version', 'development']).webVersion, 'development')
  })

  test('reads --backlog', () => {
    assert.equal(parseServerArgv(['--backlog', '5']).backlog, 5)
    assert.equal(parseServerArgv(['-b', '5']).backlog, 5)
  })
})

describe('isValidBacklog', () => {
  test('accepts positive integers', () => {
    assert.equal(isValidBacklog(1), true)
    assert.equal(isValidBacklog(100), true)
  })

  test('rejects zero and negatives', () => {
    // A backlog of zero would keep no history, which is never what was meant.
    assert.equal(isValidBacklog(0), false)
    assert.equal(isValidBacklog(-1), false)
  })

  test('rejects fractions and non-numbers', () => {
    assert.equal(isValidBacklog(1.5), false)
    assert.equal(isValidBacklog(NaN), false)
    assert.equal(isValidBacklog('10'), false)
    assert.equal(isValidBacklog(undefined), false)
  })
})
