#!/usr/bin/env node

var dgram = require('dgram')
  , app = require('express')()
  , http = require('http').Server(app)
  , io = require('socket.io')(http)
  , yargs = require('yargs')
  , pkg = require('./package')

/**
 * Parsing argv
 */

var argv = yargs
  .usage('Usage: rtail-server [--udp-host [string] --udp-port [num] --web-host [string] --web-port [num]]')
  .example('rtail-server --web-port 8080', 'custom http port')
  .example('rtail-server --udp-port 8080', 'custom udp port')
  .string('udp-host')
  .alias('udp-host', 'uh')
  .default('udp-host', 'localhost')
  .describe('udp-host', 'the listening udp hostname')
  .string('udp-port')
  .alias('udp-port', 'up')
  .default('udp-port', 9999)
  .describe('udp-port', 'the listening udp port')
  .string('web-host')
  .alias('web-host', 'wh')
  .default('web-host', 'localhost')
  .describe('web-host', 'the listening http hostname')
  .string('web-port')
  .alias('web-port', 'wp')
  .default('web-port', 8888)
  .describe('web-port', 'the listening http port')
  .help('help')
  .alias('help', 'h')
  .version(pkg.version, 'version')
  .alias('version', 'v')
  .strict()
  .argv

app.get('/', function (req, res) {
  res.sendFile(__dirname + '/public/index.html')
})

/**
 * UDP sockets setup
 */

var streams = {}
var socket = dgram.createSocket('udp4')

socket.on('message', function (line, remote) {
  // try to decode JSON
  try { var data = JSON.parse(line) }
  catch (e) { var data = line.toString() }

  if (!streams[data.id]) {
    streams[data.id] = []
    io.sockets.emit('streams', Object.keys(streams))
  }

  var message = {
    streamid: data.id,
    host: remote.address,
    port: remote.port,
    line: data.line,
    type: typeof data.line,
    source: data.source
  }

  streams[data.id].length >= 100 && streams[data.id].shift()
  streams[data.id].push(message)

  console.log(JSON.stringify(message))
  io.sockets.to(data.id).emit('line', message)
})

/**
 * socket.io
 */

io.on('connection', function (socket) {
  socket.emit('streams', Object.keys(streams))
  socket.on('select stream', function (stream) {
    socket.leave(socket.rooms[0])
    socket.join(stream)
    socket.emit('backlog', streams[stream])
  })
})

/**
 * Listen!
 */

socket.bind(argv.udpPort, argv.udpHost)
http.listen(argv.webPort, argv.webHost)