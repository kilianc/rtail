#!/bin/sh
":" //# comment; exec /usr/bin/env node --harmony "$0" "$@"

/*!
 * rtail-client.js
 * Created by Kilian Ciuffolo on Oct 26, 2014
 * (c) 2014-2015
 */

'use strict'

const fs = require('fs')
const dgram = require('dgram')
const split = require('split')
const chrono = require('chrono-node')
const JSON5 = require('json5')
const yargs = require('yargs')
const map = require('through2-map')
const stripAnsi = require('strip-ansi')
const moniker_ = require('moniker').choose
const updateNotifier = require('update-notifier')
const pkg = require('../package')

/*!
 * inform the user of updates
 */
updateNotifier({
  packageName: pkg.name,
  packageVersion: pkg.version
}).notify()

/*!
 * parsing argv
 */
let argv = yargs
  .usage('Usage: cmd | rtail [OPTIONS]')
  .example('server | rtail > server.log', 'localhost + file')
  .example('server | rtail --id api.domain.com', 'Name the log stream')
  .example('server | rtail --host example.com', 'Sends to example.com')
  .example('server | rtail --port 43567', 'Uses custom port')
  .example('server | rtail --mute', 'No stdout')
  .example('server | rtail --no-tty', 'Strips ansi colors')
  .example('server | rtail --no-date-parse', 'Disable date parsing/stripping')
  .option('host', {
    alias: 'h',
    type: 'string',
    default: '127.0.0.1',
    describe: 'The server host'
  })
  .option('port', {
    alias: 'p',
    type: 'string',
    default: 9999,
    describe: 'The server port'
  })
  .option('id', {
    alias: 'name',
    type: 'string',
    default: function moniker() { return moniker_() } ,
    describe: 'The log stream id'
  })
  .option('mute', {
    alias: 'm',
    type: 'boolean',
    describe: 'Don\'t pipe stdin with stdout'
  })
  .option('tty', {
    type: 'boolean',
    default: true,
    describe: 'Keeps ansi colors'
  })
  .option('parse-date', {
    type: 'boolean',
    default: true,
    describe: 'Looks for dates to use as timestamp'
  })
  .help('help')
  .version(pkg.version, 'version')
  .alias('version', 'v')
  .strict()
  .config('config-file', 'Configuration file path', function (configFile) {
    return JSON.parse(fs.readFileSync(configFile, 'utf-8')).client
  })
  .argv

/*!
 * setup pipes
 */
if (!argv.mute) {
  if (!process.stdout.isTTY || !argv.tty) {
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
    let timestamp = null

    try {
      // try to JSON parse
      line = JSON5.parse(line)
    } catch (err) {
      // look for timestamps if not an object
      timestamp = argv.parseDate ? chrono.parse(line)[0] : null
    }

    if (timestamp) {
      // escape for regexp and remove from line
      timestamp.text = timestamp.text.replace(/[-[\]{}()*+?.,\\^$|#\s]/g, '\\$&')
      line = line.replace(new RegExp(' *[^ ]?' + timestamp.text + '[^ ]? *'), '')
      // use timestamp as line timestamp
      baseMessage.timestamp = Date.parse(timestamp.start.date())
    } else {
      baseMessage.timestamp = Date.now()
    }

    // update default message
    baseMessage.content = line

    // prepare binary message
    let buffer = new Buffer(JSON.stringify(baseMessage))

    // set semaphore
    isSending ++

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
