#!/bin/sh
":" //# comment; exec /usr/bin/env node --harmony "$0" "$@"

/*!
 * client.js
 * Created by Kilian Ciuffolo on Oct 26, 2014
 * (c) 2014-2015
 */

'use strict'

const dgram = require('dgram')
const split = require('split')
const chrono = require('chrono-node')
const JSON5 = require('json5')
const yargs = require('yargs')
const map = require('through2-map')
const stripAnsi = require('strip-ansi')
const moniker = require('moniker').choose
const updateNotifier = require('update-notifier')
const pkg = require('../package')

/*!
 * inform the user of updates
 */
updateNotifier({
  packageName: pkg.name,
  packageVersion: pkg.version
}).notify()

// fix yargs help
moniker.toString = function () { return 'moniker()' }

/*!
 * parsing argv
 */
let argv = yargs
  .usage('Usage: cmd | rtail --host [string] --port [num] [--mute] [--id [string]]')
  .example('server | rtail --host 127.0.0.1 > server.log', 'Broadcast to localhost + file')
  .example('server | rtail --port 43567', 'Custom port')
  .example('server | rtail --mute', 'Only remote', 'No stdout')
  .example('server | rtail --id api.domain.com', 'Name the log stream')
  .example('server | rtail --not-tty', 'Strips ansi colors')
  .option('mute', {
    alias: 'm',
    type: 'boolean',
    describe: 'Don\'t pipe stdin with stdout'
  })
  .option('host', {
    alias: 'h',
    type: 'string',
    default: '127.0.0.1',
    describe: 'The recipient server host'
  })
  .option('port', {
    alias: 'p',
    type: 'string',
    default: 9999,
    describe: 'The recipient server port'
  })
  .option('id', {
    alias: 'name',
    type: 'string',
    default: moniker,
    describe: 'The log stream id'
  })
  .option('not-tty', {
    type: 'boolean',
    describe: 'Strips ansi colors'
  })
  .help('help')
  .version(pkg.version, 'version')
  .alias('version', 'v')
  .strict()
  .argv

/*!
 * setup pipes
 */
if (!argv.mute) {
  if (!process.stdout.isTTY || argv['not-tty']) {
    process.stdin
      .pipe(map(function (chunk) {
        return stripAnsi(chunk.toString('utf8'))
      }))
      .pipe(process.stdout)
  } else {
    process.stdin.pipe(process.stdout)
  }
}

/*!
 * initialize socket
 */
let isClosed = false
let isSending = 0
let socket = dgram.createSocket('udp4')
let baseMessage = { id: argv.id }

socket.bind(function () {
  socket.setBroadcast(true)
})

/*!
 * broadcast lines to browser
 */
process.stdin
  .pipe(split(null, null, { trailing: false }))
  .on('data', function (line) {
    // set semaphore
    isSending ++

    // try to JSON parse
    try { line = JSON5.parse(line) }
    catch (e) {}

    // look for timestamps
    let timestamp = chrono.parse(line)[0]

    if (timestamp) {
      line = line.replace(new RegExp(' ?[^ ]?' + timestamp.text + '[^ ]? ?'), '')
      baseMessage.timestamp = Date.parse(timestamp.ref)
    } else {
      baseMessage.timestamp = Date.now()
    }

    // update default message
    baseMessage.content = line

    // prepare binary message
    let buffer = new Buffer(JSON.stringify(baseMessage))

    socket.send(buffer, 0, buffer.length, argv.port, argv.host, function () {
      isSending --
      if (isClosed && !isSending) socket.close()
    })
  })

/*!
 * drain pipe and exit
 */
process.stdin.on('end', function () {
  isClosed = true
  if (!isSending) socket.close()
})
