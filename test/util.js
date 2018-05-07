const { spawn } = require('child_process');
const dgram = require('dgram');

module.exports.spawnClient = (opts = {}) => {
  if (!opts.socket) {
    opts.socket = dgram.createSocket('udp4');
    opts.socket.bind(9999);
  }

  const client = spawn('cli/rtail-client.js', opts.args);
  const messages = [];

  client.stderr.pipe(process.stderr);

  opts.socket.on('message', (data) => {
    messages.push(JSON.parse(data));
  });

  client.on('exit', (code) => {
    const err = code ? new Error(`rtail exited with code: ${code}`) : null;
    if (opts.test) opts.test(messages);

    opts.socket.close();
    if (opts.done) opts.done(err);
  });

  return client;
};

module.exports.spawnServer = (opts = {}) => {
  const server = spawn('cli/rtail-server.js', opts.args);
  server.stderr.pipe(process.stderr);
  server.stdout.pipe(process.stdout);

  server.on('exit', (code) => {
    const err = code ? new Error(`rtail exited with code: ${code}`) : null;
    if (opts.done) opts.done(err);
  });

  return server;
};

module.exports.s = obj => JSON.stringify(obj, null, '  ');
