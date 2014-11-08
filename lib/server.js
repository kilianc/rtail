#!/usr/bin/env node

var dgram = require('dgram')
  , app = require('express')()
  , static = require('express').static
  , http = require('http').Server(app)
  , io = require('socket.io')(http)
  , yargs = require('yargs')
  , debug = require('debug')('rtail:udp')
  , webapp = require('./webapp')
  , updateNotifier = require('update-notifier')
  , pkg = require('../package')
  , path = require('path')

/**
 * Inform the user of updates
 */

updateNotifier({
  packageName: pkg.name,
  packageVersion: pkg.version
}).notify()

/**
 * Parsing argv
 */

var argv = yargs
  .usage('Usage: rtail-server [--udp-host [string] --udp-port [num] --web-host [string] --web-port [num]]')
  .example('rtail-server --web-port 8080', 'custom http port')
  .example('rtail-server --udp-port 8080', 'custom udp port')
  .string('udp-host')
  .alias('udp-host', 'uh')
  .default('udp-host', '127.0.0.1')
  .describe('udp-host', 'the listening udp hostname')
  .string('udp-port')
  .alias('udp-port', 'up')
  .default('udp-port', 9999)
  .describe('udp-port', 'the listening udp port')
  .string('web-host')
  .alias('web-host', 'wh')
  .default('web-host', '127.0.0.1')
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
    type: typeof data.line
  }

  // limit backlog to 100 lines
  streams[data.id].length >= 100 && streams[data.id].shift()
  streams[data.id].push(message)

  debug(JSON.stringify(message))
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

app.use(static(path.join(__dirname, '../webapp/public')));

/**
 * Serve static webapp from s3
 */

app.use(webapp({
  s3: 'http://rtail.s3-website-us-east-1.amazonaws.com',
  ttl: 6000 * 60 * 60
}))

/*
app.get('/', function (req, res) {
  res.sendFile(path.join(__dirname, '../webapp/public/index.html'))
})
*/

/**
 * Listen!
 */

socket.bind(argv.udpPort, argv.udpHost)
http.listen(argv.webPort, argv.webHost)

