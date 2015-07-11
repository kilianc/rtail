/*!
 * rtail-server.js
 * Created by Kilian Ciuffolo on Jul 7, 2015
 * (c) 2015
 */

'use strict'

const assert = require('chai').assert
const dgram = require('dgram')
const dns = require('dns')
const io = require('socket.io/node_modules/socket.io-client')
const spawnClient = require('./util').spawnClient
const spawnServer = require('./util').spawnServer
const get = require('request').get
const os = require('os')

describe('rtail-server.js', function () {
  this.timeout(5000)

  let server = null
  let streams = null
  let lines = []
  let backlogs = []
  let fakeSocket = { close: function () {}, on: function () {} }

  before(function (done) {
    server = spawnServer()

    setTimeout(function () {
      spawnClient({ socket: fakeSocket }).stdin.end(['1', '2', ''].join('\n'))
      spawnClient({ socket: fakeSocket }).stdin.end(['1', '2', ''].join('\n'))

      setTimeout(function () {
        let ws = io.connect('http://localhost:8888')

        ws.on('streams', function (data) {
          if (null === streams) {
            ws.emit('select stream', data[0])
          }
          streams = data
        })

        ws.on('backlog', function (data) {
          backlogs.push(data)
          spawnClient({ args: ['--name', streams[0]], socket: fakeSocket }).stdin.end(['A', 'B', ''].join('\n'))
        })

        ws.on('line', function (data) {
          lines.push(data)
          if (lines.length >= 2) done()
        })
      }, 500)
    }, 500)
  })

  after(function () {
    server.kill()
  })

  it('should send the streams list on connect (WS)', function () {
    assert.lengthOf(streams, 2)
  })

  it('should listen for messages', function () {
    assert.equal(lines[0].content, 'A')
    assert.equal(lines[1].content, 'B')
  })

  it('should skip non JSON messages', function () {
    let socket = dgram.createSocket('udp4')
    let buffer = new Buffer('foo')
    socket.send(buffer, 0, buffer.length, 9999, 'localhost')
  })

  it('should serve the webapp', function (done) {
    server.kill()
    server = spawnServer()

    setTimeout(function () {
      get('http://localhost:8888', function (err, res, body) {
        if (err) return done(err)
        assert.match(body, /ng-app="app"/)
        done(err)
      })
    }, 1000)
  })


  it('should serve the webapp from s3', function (done) {
    server.kill()
    server = spawnServer({
      args: ['--web-version', 'stable']
    })

    setTimeout(function () {
      get('http://localhost:8888/index.html', function (err, res, body) {
        if (err) return done(err)
        assert.match(body, /ng-app="app"/)
        assert.isDefined(res.headers['x-amz-request-id'])
        done(err)
      })
    }, 1000)
  })

  it('should support custom port / host', function (done) {
    server.kill()

    dns.lookup(os.hostname(), function (err, address) {
      server = spawnServer({
        args: [
          '--udp-host', address,
          '--udp-port', 9998,
          '--web-host', address,
          '--web-port', 8889,
        ]
      })

      // check websocket
      setTimeout(function () {
        spawnClient({
          socket: fakeSocket,
          args: ['--port', 9998, '--host', address, '--name', 'foobar']
        }).stdin.end(['1', '2', ''].join('\n'))

        setTimeout(function () {
          let ws = io.connect('http://' + address + ':8889')

          ws.on('streams', function (data) {
            assert.lengthOf(data, 1)
            assert.equal(data[0], 'foobar')
            done()
          })
        }, 500)
      }, 1000)
    })
  })
})
