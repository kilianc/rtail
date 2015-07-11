/*!
 * cli.test.js
 * Created by Kilian Ciuffolo on Jul 7, 2015
 * (c) 2015
 */

'use strict'

const assert = require('chai').assert
const dgram = require('dgram')
const dns = require('dns')
const os = require('os')
const s = require('./util').s
const spawnClient = require('./util').spawnClient

describe('rtail-client.js', function () {
  it('should split stdin by \\n', function (done) {
    spawnClient({
      args: [],
      done: done,
      test: function (messages) {
        assert.equal(3, messages.length, s(messages))

        assert.equal(messages[0].content, '0')
        assert.equal(messages[1].content, '1')
        assert.equal(messages[2].content, '2')

        assert.isDefined(messages[0].id)
        assert.isNumber(messages[0].timestamp)
      }
    }).stdin.end(['0', '1', '2', ''].join('\n'))
  })

  it('should use custom name', function (done) {
    spawnClient({
      args: ['--name', 'test'],
      done: done,
      test: function (messages) {
        assert.equal(3, messages.length, s(messages))
        assert.equal(messages[0].id, 'test')
      }
    }).stdin.end(['0', '1', '2', ''].join('\n'))
  })

  it('should respect --mute', function (done) {
    let client = spawnClient({ args: ['--mute'], done: done })
    client.stdout.on('data', function (data) {
      done(new Error('Expected no output instead got: "' + data.toString() + '"'))
    })
    client.stdin.end(['0', '1', '2', ''].join('\n'))
  })

  it('should parse JSON lines', function (done) {
    spawnClient({
      args: [],
      done: done,
      test: function (messages) {
        assert.equal(1, messages.length, s(messages))
        assert.equal(messages[0].content.foo, 'bar')
      }
    }).stdin.end(['{ "foo": "bar" }', ''].join('\n'))
  })

  it('should parse JSON5 lines', function (done) {
    spawnClient({
      args: [],
      done: done,
      test: function (messages) {
        assert.equal(1, messages.length, s(messages))
        assert.equal(messages[0].content.foo, 'bar')
      }
    }).stdin.end(['{ foo: "bar" }', ''].join('\n'))
  })

  it('should support custom port / host', function (done) {
    dns.lookup(os.hostname(), function (err, address) {
      let socket = dgram.createSocket('udp4')
      socket.bind(9998, address)

      spawnClient({
        done: done,
        socket: socket,
        args: ['-p', '9998', '-h', address],
        test: function (messages) {
          assert.equal(1, messages.length, s(messages))
          assert.equal(messages[0].content.foo, 'bar')
        }
      }).stdin.end(['{ foo: "bar" }', ''].join('\n'))
    })
  })

  it('should strip colors with --no-tty', function (done) {
    let client = spawnClient({
      args: ['--no-tty'],
      done: done
    })

    client.stdout.on('data', function (data) {
      assert.equal(data.toString(), 'Hello world\n')
    })

    client.stdin.end(['\u001b[31mHello world\u001b[0m', ''].join('\n'))
  })

  it('should parse date if --parse-date', function (done) {
    let date = 'Wed Jul 08 2010 01:01:03 GMT-0700 (PDT)'
    let client = spawnClient({
      done: done,
      test: function (messages) {
        assert.equal(messages[0].timestamp, Date.parse(date))
        assert.equal(messages[0].content, 'hello')
      }
    })

    client.stdin.end(['[' + date + ']  hello', ''].join('\n'))
  })

  it('should not parse date if --no-parse-date', function (done) {
    let date = 'Wed Jul 08 2010 01:01:03 GMT-0700 (PDT)'
    let client = spawnClient({
      args: ['--no-parse-date'],
      done: done,
      test: function (messages) {
        assert.notEqual(messages[0].timestamp, Date.parse(date))
        assert.equal(messages[0].content, '[' + date + ']  hello')
      }
    })

    client.stdin.end(['[' + date + ']  hello', ''].join('\n'))
  })
})
