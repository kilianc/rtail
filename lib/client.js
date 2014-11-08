#!/usr/bin/env node

'use strict';

var dgram = require('dgram')
  , readline = require('readline')
  , crypto = require('crypto')
  , Writable = require('stream').Writable
  , yargs = require('yargs')
  , uuid = require('node-uuid')
  , updateNotifier = require('update-notifier')
  , pkg = require('../package')

/**
 * Inform the user of updates
 */

updateNotifier({
  packageName: pkg.name,
  packageVersion: pkg.version
}).notify()

// fix yargs help
uuid.v4.toString = function () { return 'uuid()' }

/**
 * Parsing argv
 */

var argv = yargs
  .usage('Usage: cmd | rtail --host [string] --port [num] [--mute] [--id [string]]')
  .example('server | rtail --host 127.0.0.1 > server.log',' broadcast to localhost + file')
  .example('server | rtail --port 43567', ' custom port')
  .example('server | rtail --mute', ' only remote', ' no stdout')
  .example('server | rtail --id api.domain.com', ' name the log stream')
  .boolean('mute')
  .alias('mute', 'm')
  .describe('mute', 'don\'t pipe stdin with stdout')
  .string('host')
  .default('host', '127.0.0.1')
  .describe('host', 'the recipient server host')
  .string('port')
  .alias('port', 'p')
  .default('port', 9999)
  .describe('port', 'the recipient server port')
  .string('id')
  .alias('id', ['name', 'n'])
  .default('id', uuid.v4)
  .describe('id', 'the log stream id')
  .help('help')
  .alias('help', 'h')
  .version(pkg.version, 'version')
  .alias('version', 'v')
  .strict()
  .argv

/**
 * Setup pipes
 */

var lines = readline.createInterface({
  input: process.stdin,
  output: !argv.mute ? process.stdout : new Writable(),
  terminal: false
})

/**
 * Initialize socket
 */

var isClosed = false
var isSending = false
var socket = dgram.createSocket('udp4')
var baseMessage = { id: argv.id }

socket.bind(function () {
  socket.setBroadcast(true)
})

/**
 * Broadcast lines to browser
 */

lines.on('line', function (line) {
  // set semaphore
  isSending = true

  // try to JSON parse
  try { line = JSON.parse(line) }
  catch (e) {}

  // update default message
  baseMessage.line = line

  // prepare binary message
  var buffer = new Buffer(JSON.stringify(baseMessage))

  socket.send(buffer, 0, buffer.length, argv.port, argv.host, function () {
    if (isClosed) socket.close()
    isSending = false
  })
})

/**
 * Drain pipe and exit
 */

lines.on('close', function () {
  isClosed = true
  if (!isSending) socket.close()
})