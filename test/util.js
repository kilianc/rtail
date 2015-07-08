/*!
 * util.js
 * Created by Kilian Ciuffolo on Jul 7, 2015
 * (c) 2015
 */

'use strict'

const spawn = require('child_process').spawn
const dgram = require('dgram')

/**
 *
 */
module.exports.spawnClient = function spawnClient(opts) {
  opts = opts || {}

  if (!opts.socket) {
    opts.socket = dgram.createSocket('udp4')
    opts.socket.bind(9999)
  }

  let client = spawn('cli/rtail-client.js', opts.args)
  let messages = []

  client.stderr.pipe(process.stderr)

  opts.socket.on('message', function (data) {
    messages.push(JSON.parse(data))
  })

  client.on('exit', function (code) {
    let err = code ? new Error('rtail exited with code: ' + code) : null
    opts.test && opts.test(messages)
    opts.socket.close()
    opts.done && opts.done(err)
  })

  return client
}

/**
 *
 */
module.exports.spawnServer = function spawnServer(opts) {
  opts = opts || {}

  let server = spawn('cli/rtail-server.js', opts.args)
  server.stderr.pipe(process.stderr)
  server.stdout.pipe(process.stdout)

  server.on('exit', function (code) {
    let err = code ? new Error('rtail exited with code: ' + code) : null
    opts.done && opts.done(err)
  })

  return server
}

/**
 *
 */
module.exports.s = function s(obj) {
  return JSON.stringify(obj, null, '  ')
}
