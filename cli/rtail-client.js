#!/usr/bin/env node

const dgram = require('dgram');
const split = require('split');
const chrono = require('chrono-node');
const JSON5 = require('json5');
const yargs = require('yargs');
const map = require('through2-map');
const stripAnsi = require('strip-ansi');
const moniker = require('moniker');
const pkg = require('../package');

/*!
 * parsing argv
 */
const argv = yargs // eslint-disable-line prefer-destructuring
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
    describe: 'The server host',
  })
  .option('port', {
    alias: 'p',
    type: 'string',
    default: 9999,
    describe: 'The server port',
  })
  .option('id', {
    alias: 'name',
    type: 'string',
    default: () => moniker.choose(),
    describe: 'The log stream id',
  })
  .option('group', {
    alias: 'g',
    type: 'string',
    default: null,
    describe: 'The log stream group name',
  })
  .option('mute', {
    alias: 'm',
    type: 'boolean',
    describe: 'Don\'t pipe stdin with stdout',
  })
  .option('tty', {
    type: 'boolean',
    default: true,
    describe: 'Keeps ansi colors',
  })
  .option('parse-date', {
    type: 'boolean',
    default: true,
    describe: 'Looks for dates to use as timestamp',
  })
  .help('help')
  .alias('help', 'h')
  .version('version', pkg.version)
  .alias('version', 'v')
  .strict()
  .argv;

/*!
 * setup pipes
 */
if (!argv.mute) {
  if (!process.stdout.isTTY || !argv.tty) {
    process.stdin
      .pipe(map(chunk => stripAnsi(chunk.toString('utf8'))))
      .pipe(process.stdout);
  } else {
    process.stdin.pipe(process.stdout);
  }
}

/*!
 * initialize socket
 */
let isClosed = false;
let isSending = 0;
const socket = dgram.createSocket('udp4');
const baseMessage = { id: argv.id };
if (argv.group) baseMessage.group = argv.group;

socket.bind(() => {
  socket.setBroadcast(true);
});

/*!
 * broadcast lines to browser
 */
process.stdin
  .pipe(split(null, null, { trailing: false }))
  .on('data', (line) => {
    const message = Object.assign({}, baseMessage, { content: line });
    let timestamp = null;

    try {
      // try to JSON parse
      message.content = JSON5.parse(line);
    } catch (err) {
      // look for timestamps if not an object
      timestamp = argv.parseDate ? chrono.parse(line)[0] : null;
    }

    if (timestamp) {
      // escape for regexp and remove from line
      timestamp.text = timestamp.text.replace(/[-[\]{}()*+?.,\\^$|#\s]/g, '\\$&');
      message.content = message.content.replace(new RegExp(` *[^ ]?${timestamp.text}[^ ]? *`), '');
      // use timestamp as line timestamp
      message.timestamp = Date.parse(timestamp.start.date());
    } else {
      message.timestamp = Date.now();
    }

    // prepare binary message
    const buffer = Buffer.from(JSON.stringify(message));

    // set semaphore
    isSending++;

    socket.send(buffer, 0, buffer.length, argv.port, argv.host, () => {
      isSending--;
      if (isClosed && !isSending) socket.close();
    });
  });

/*!
 * drain pipe and exit
 */
process.stdin.on('end', () => {
  isClosed = true;
  if (!isSending) socket.close();
});
