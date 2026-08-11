#!/usr/bin/env node

/*!
 * rtail-client.js
 * Created by Kilian Ciuffolo on Oct 26, 2014
 */

import dgram from 'node:dgram'
import { createInterface } from 'node:readline'
import * as chrono from 'chrono-node'
import JSON5 from 'json5'
import stripAnsi from 'strip-ansi'
import updateNotifier from 'update-notifier'
import yargs from 'yargs'
import { hideBin } from 'yargs/helpers'
import { choose as moniker } from './lib/moniker.js'
import { pkg } from './lib/pkg.js'

/*!
 * inform the user of updates
 */
updateNotifier({ pkg }).notify()

/*!
 * parsing argv
 */
const argv = yargs(hideBin(process.argv))
  .usage('Usage: cmd | rtail [OPTIONS]')
  .example('server | rtail > server.log', 'localhost + file')
  .example('server | rtail --id api.domain.com', 'Name the log stream')
  .example('server | rtail --host example.com', 'Sends to example.com')
  .example('server | rtail --port 43567', 'Uses custom port')
  .example('server | rtail --mute', 'No stdout')
  .example('server | rtail --no-tty', 'Strips ansi colors')
  .example('server | rtail --no-parse-date', 'Disable date parsing/stripping')
  .option('host', {
    alias: 'h',
    type: 'string',
    default: '127.0.0.1',
    describe: 'The server host'
  })
  .option('port', {
    alias: 'p',
    type: 'number',
    default: 9999,
    describe: 'The server port'
  })
  .option('id', {
    alias: 'name',
    type: 'string',
    default: moniker(),
    defaultDescription: 'a random name',
    describe: 'The log stream id'
  })
  .option('mute', {
    alias: 'm',
    type: 'boolean',
    default: false,
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
  .version(pkg.version)
  .alias('version', 'v')
  .strict()
  .parseSync()

/*!
 * initialize socket
 */
let isClosed = false
let isSending = 0
const socket = dgram.createSocket('udp4')

socket.bind(() => socket.setBroadcast(true))

// Colours are kept only when stdout is a real terminal and --tty is on.
const stripColors = !process.stdout.isTTY || !argv.tty

/*!
 * read stdin line by line, echoing to stdout and broadcasting to the server
 *
 * The old implementation piped stdin twice — once to stdout and once through a
 * line splitter — using `split` and `through2-map`. One pass over readline does
 * both and drops two dependencies.
 */
const lines = createInterface({ input: process.stdin, crlfDelay: Infinity })

lines.on('line', (raw) => {
  if (!argv.mute) {
    process.stdout.write((stripColors ? stripAnsi(raw) : raw) + '\n')
  }

  let content = raw
  let timestamp = null

  try {
    // try to JSON parse
    content = JSON5.parse(raw)
  } catch {
    // look for timestamps if not an object
    timestamp = argv.parseDate ? chrono.parse(raw)[0] : null
  }

  if (timestamp) {
    // escape for regexp and remove from line
    const text = timestamp.text.replace(/[-[\]{}()*+?.,\\^$|#\s]/g, '\\$&')
    content = raw.replace(new RegExp(' *[^ ]?' + text + '[^ ]? *'), '')
  }

  const buffer = Buffer.from(JSON.stringify({
    id: argv.id,
    timestamp: timestamp ? Date.parse(timestamp.start.date()) : Date.now(),
    content
  }))

  isSending++

  socket.send(buffer, 0, buffer.length, argv.port, argv.host, () => {
    isSending--
    if (isClosed && !isSending) socket.close()
  })
})

/*!
 * drain pipe and exit
 */
lines.on('close', () => {
  isClosed = true
  if (!isSending) socket.close()
})
