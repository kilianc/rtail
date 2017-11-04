#!/usr/bin/env node

const dgram = require('dgram');
const express = require('express');
const path = require('path');
const http = require('http');
const socketIO = require('socket.io');
const yargs = require('yargs');
const debug = require('debug')('rtail:server');
const pkg = require('../package');

const serve = express.static;
const io = socketIO();
const app = express();
const server = http.Server(app);

/*!
 * parsing argv
 */
const argv = yargs // eslint-disable-line prefer-destructuring
  .usage('Usage: rtail-server [OPTIONS]')
  .example('rtail-server --web-port 8080', 'Use custom HTTP port')
  .example('rtail-server --udp-port 8080', 'Use custom UDP port')
  .option('udp-host', {
    alias: 'uh',
    default: '127.0.0.1',
    describe: 'The listening UDP hostname',
  })
  .option('udp-port', {
    alias: 'up',
    default: 9999,
    describe: 'The listening UDP port',
  })
  .option('web-host', {
    alias: 'wh',
    default: '127.0.0.1',
    describe: 'The listening HTTP hostname',
  })
  .option('web-port', {
    alias: 'wp',
    default: 8888,
    describe: 'The listening HTTP port',
  })
  .option('backlog-limit', {
    default: 500,
    describe: 'Define backlog limit for each log stream',
  })
  .help('help')
  .alias('help', 'h')
  .version(pkg.version, 'version')
  .alias('version', 'v')
  .strict()
  .argv;

/*!
 * UDP sockets setup
 */
const streams = {};
const socket = dgram.createSocket('udp4');

const genStreamList = () => Object.keys(streams)
  .map((streamId) => {
    const stream = streams[streamId];

    return { id: streamId, name: stream.name, group: stream.group };
  });

socket.on('message', (rawData, remote) => {
  let data = rawData;
  // try to decode JSON
  try {
    data = JSON.parse(data);
  } catch (err) {
    debug('invalid data sent');
    return;
  }

  const streamId = data.group ? `${data.group}/${data.id}` : `${data.id}`;
  if (!streams[streamId]) {
    streams[streamId] = {
      name: data.id,
      group: data.group,
      backlog: [],
    };
    io.sockets.emit('streams', genStreamList());
  }

  const currentStream = streams[streamId];
  const message = {
    host: remote.address,
    port: remote.port,
    name: data.id,
    group: data.group || null,
    type: typeof data.content,
    content: data.content,
    streamId,
    timestamp: data.timestamp,
  };

  if (currentStream.backlog.length >= argv.backlogLimit) {
    currentStream.backlog.shift();
  }

  currentStream.backlog.push(message);

  debug(JSON.stringify(message));
  io.sockets.to(streamId).emit('line', message);
});

/*!
 * socket.io
 */
io.on('connection', (clientSocket) => {
  clientSocket.emit('streams', genStreamList());

  clientSocket.on('streamUnsubscribe', (stream) => {
    if (!stream) return;
    clientSocket.leave(stream);
  });

  clientSocket.on('streamSubscribe', (stream) => {
    if (!stream) return;
    clientSocket.join(stream);
    if (streams[stream]) clientSocket.emit('backlog', streams[stream].backlog);
  });
});

app.use(serve(path.resolve(__dirname, '../dist'), {
  extensions: ['html', 'js', 'css'],
}));

/*!
 * listen!
 */
io.attach(server, { serveClient: false });
socket.bind(argv.udpPort, argv.udpHost);
server.listen(argv.webPort, argv.webHost);

console.log('UDP server listening: %s:%s', argv.udpHost, argv.udpPort); // eslint-disable-line no-console
console.log('HTTP server listening: http://%s:%s', argv.webHost, argv.webPort); // eslint-disable-line no-console
