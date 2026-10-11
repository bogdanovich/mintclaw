'use strict';

// Fixed inherited FD 3 is established by qualified Root startup. No credential
// or configuration is accepted from argv, environment, stdin or a public port.
const net = require('node:net');
const { startChannel } = require('./channel.cjs');
const { launch } = require('./launch.cjs');
try {
  if (process.argv.length !== 2) throw new Error();
  const stream = new net.Socket({ fd: 3, readable: true, writable: true });
  const worker = startChannel(stream, launch);
  for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => { void worker.close(); });
} catch (_) { process.exitCode = 1; }
