const os = require('os');
const dns = require('dns');
const dgram = require('dgram');
const { assert } = require('chai');

const { s, spawnClient } = require('./util');

describe('rtail-client.js', () => {
  it('should split stdin by \\n', (done) => {
    spawnClient({
      args: [],
      done,
      test(messages) {
        assert.equal(3, messages.length, s(messages));

        assert.equal(messages[0].content, '0');
        assert.equal(messages[1].content, '1');
        assert.equal(messages[2].content, '2');

        assert.isDefined(messages[0].id);
        assert.isNumber(messages[0].timestamp);
      },
    }).stdin.end(['0', '1', '2', ''].join('\n'));
  });

  it('should use custom name', (done) => {
    spawnClient({
      args: ['--name', 'test'],
      done,
      test(messages) {
        assert.equal(3, messages.length, s(messages));
        assert.equal(messages[0].id, 'test');
      },
    }).stdin.end(['0', '1', '2', ''].join('\n'));
  });

  it('should respect --mute', (done) => {
    const client = spawnClient({ args: ['--mute'], done });
    client.stdout.on('data', (data) => {
      done(new Error(`Expected no output instead got: "${data.toString()}"`));
    });
    client.stdin.end(['0', '1', '2', ''].join('\n'));
  });

  it('should parse JSON lines', (done) => {
    spawnClient({
      args: [],
      done,
      test(messages) {
        assert.equal(1, messages.length, s(messages));
        assert.equal(messages[0].content.foo, 'bar');
      },
    }).stdin.end(['{ "foo": "bar" }', ''].join('\n'));
  });

  it('should parse JSON5 lines', (done) => {
    spawnClient({
      args: [],
      done,
      test(messages) {
        assert.equal(1, messages.length, s(messages));
        assert.equal(messages[0].content.foo, 'bar');
      },
    }).stdin.end(['{ foo: "bar" }', ''].join('\n'));
  });

  it('should support custom port / host', (done) => {
    dns.lookup(os.hostname(), (err, address) => {
      const socket = dgram.createSocket('udp4');
      socket.bind(9998, address);

      spawnClient({
        done,
        socket,
        args: ['-p', '9998', '-h', address],
        test(messages) {
          assert.equal(1, messages.length, s(messages));
          assert.equal(messages[0].content.foo, 'bar');
        },
      }).stdin.end(['{ foo: "bar" }', ''].join('\n'));
    });
  });

  it('should strip colors with --no-tty', (done) => {
    const client = spawnClient({
      args: ['--no-tty'],
      done,
    });

    client.stdout.on('data', (data) => {
      assert.equal(data.toString(), 'Hello world\n');
    });

    client.stdin.end(['\u001b[31mHello world\u001b[0m', ''].join('\n'));
  });

  it('should parse date if --parse-date', (done) => {
    const date = 'Wed Jul 08 2010 01:01:03 GMT-0700 (PDT)';
    const client = spawnClient({
      done,
      test(messages) {
        assert.equal(messages[0].timestamp, Date.parse(date));
        assert.equal(messages[0].content, 'hello');
      },
    });

    client.stdin.end([`[${date}]  hello`, ''].join('\n'));
  });

  it('should not parse date if --no-parse-date', (done) => {
    const date = 'Wed Jul 08 2010 01:01:03 GMT-0700 (PDT)';
    const client = spawnClient({
      args: ['--no-parse-date'],
      done,
      test(messages) {
        assert.notEqual(messages[0].timestamp, Date.parse(date));
        assert.equal(messages[0].content, `[${date}]  hello`);
      },
    });

    client.stdin.end([`[${date}]  hello`, ''].join('\n'));
  });
});
