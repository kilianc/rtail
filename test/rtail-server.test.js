const os = require('os');
const io = require('socket.io-client');
const dns = require('dns');
const dgram = require('dgram');
const { get } = require('request');
const { assert } = require('chai');

const { spawnClient, spawnServer } = require('./util');

describe('rtail-server.js', function () {
  this.timeout(5000);

  let ws = null;
  let server = null;
  let streams = null;
  const lines = [];
  const backlogs = [];
  const fakeSocket = { close() {}, on() {} };

  before((done) => {
    server = spawnServer();

    setTimeout(() => {
      spawnClient({ socket: fakeSocket }).stdin.end(['1', '2', ''].join('\n'));
      spawnClient({ socket: fakeSocket }).stdin.end(['1', '2', ''].join('\n'));

      setTimeout(() => {
        ws = io('http://localhost:8888');

        ws.on('streams', (data) => {
          if (streams === null) {
            ws.emit('streamSubscribe', data[0].id);
          }
          streams = data;
        });

        ws.on('backlog', (data) => {
          backlogs.push(data);
          spawnClient({ args: ['--name', streams[0].id], socket: fakeSocket }).stdin.end(['A', 'B', ''].join('\n'));
        });

        ws.on('line', (data) => {
          lines.push(data);
          if (lines.length >= 2) done();
        });
      }, 500);
    }, 500);
  });

  after(() => {
    if (ws) ws.close();
    server.kill();
  });

  it('should send the streams list on connect (WS)', () => {
    assert.lengthOf(streams, 2);
  });

  it('should listen for messages', () => {
    assert.equal(lines[0].content, 'A');
    assert.equal(lines[1].content, 'B');
  });

  it('should skip non JSON messages', () => {
    const socket = dgram.createSocket('udp4');
    const buffer = Buffer.from('foo');
    socket.send(buffer, 0, buffer.length, 9999, 'localhost');
    socket.close();
  });

  it('should serve the webapp', (done) => {
    server.kill();
    server = spawnServer();

    setTimeout(() => {
      get('http://localhost:8888', (err, res, body) => {
        if (err) {
          done(err);
          return;
        }

        assert.match(body, /ng-app="app"/);
        done(err);
      });
    }, 1000);
  });

  it('should support custom port / host', (done) => {
    server.kill();

    dns.lookup(os.hostname(), (err, address) => {
      server = spawnServer({
        args: [
          '--udp-host', address,
          '--udp-port', 9998,
          '--web-host', address,
          '--web-port', 8889,
        ],
      });

      // check websocket
      setTimeout(() => {
        spawnClient({
          socket: fakeSocket,
          args: ['--port', 9998, '--host', address, '--name', 'foobar'],
        }).stdin.end(['1', '2', ''].join('\n'));

        setTimeout(() => {
          const newConnection = io.connect(`http://${address}:8889`);

          newConnection.on('streams', (data) => {
            assert.lengthOf(data, 1);
            assert.equal(data[0].name, 'foobar');
            done();
            newConnection.close();
          });
        }, 500);
      }, 1000);
    });
  });
});
