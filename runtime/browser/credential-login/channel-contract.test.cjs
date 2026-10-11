'use strict';

const { test } = require('node:test');
const assert = require('node:assert/strict');
const { PassThrough, Duplex } = require('node:stream');
const { startChannel } = require('./channel.cjs');
const origin = 'https://login.example.org';
function configuration() {
  const fields = ['/synthetic/normal', 'http://127.0.0.1:18080', `${origin}/login`, origin,
    'login', 'username', 'password', 'email', `${origin}/session`, `${origin}/home`, 'authenticated', '', ''];
  const parts = [Buffer.from([0, 0, 0xea, 0x60])];
  for (const value of fields) {
    const bytes = Buffer.from(value), size = Buffer.alloc(2); size.writeUInt16BE(bytes.length); parts.push(size, bytes);
  }
  const count = Buffer.alloc(4); count.writeUInt32BE(1);
  const size = Buffer.alloc(2); size.writeUInt16BE(origin.length);
  return Buffer.concat([...parts, count, size, Buffer.from(origin)]);
}
function fixture(t, factory) {
  const input = new PassThrough(), output = new PassThrough();
  const worker = Duplex.from({ readable: input, writable: output });
  const frames = [], waiters = [];
  let pending = Buffer.alloc(0);
  output.on('data', chunk => {
    pending = Buffer.concat([pending, chunk]);
    while (pending.length >= 4 && pending.length >= pending.readUInt32BE() + 4) {
      const size = pending.readUInt32BE(), frame = Buffer.from(pending.subarray(4, size + 4));
      pending = pending.subarray(size + 4);
      if (waiters.length) waiters.shift()(frame); else frames.push(frame);
    }
  });
  const handle = startChannel(worker, factory);
  t.after(async () => { await handle.close(); input.destroy(); output.destroy(); worker.destroy(); });
  return {
    send: (type, id, body = Buffer.alloc(0)) => {
      const frame = Buffer.alloc(body.length + 9);
      frame.writeUInt32BE(body.length + 5); frame[4] = type; frame.writeUInt32BE(id, 5); body.copy(frame, 9);
      input.write(frame);
    },
    next: async () => {
      let deadline;
      try { return await Promise.race([frames.length ? Promise.resolve(frames.shift()) :
        new Promise(resolve => waiters.push(resolve)), new Promise((_, reject) => {
          deadline = setTimeout(() => reject(new Error('fixed channel fixture timeout')), 6000);
        })]); } finally { clearTimeout(deadline); }
    },
  };
}
const code = frame => { assert.equal(frame[0], 0x10); assert.equal(frame.length, 6); return frame[5]; };

test('private channel closes a late factory before acknowledging retirement', async t => {
  let closes = 0, allocations = 0;
  const f = fixture(t, async () => {
    await new Promise(resolve => setTimeout(resolve, 30));
    return { close: async () => { closes++; }, session: async () => { allocations++; } };
  });
  f.send(1, 1, configuration()); f.send(6, 0);
  assert.equal(code(await f.next()), 7);
  assert.equal(closes, 1); assert.equal(allocations, 0);
});

test('private injection frame is cleared and raw exceptions become fixed results', async t => {
  let borrowed, closes = 0;
  const f = fixture(t, async () => ({ close: async () => { closes++; }, session: async () => ({
    prepare: async () => ({ code: 'prepared' }), ready: async () => ({ code: 'prepared' }),
    inject: async (username, password) => { borrowed = [username, password]; throw new Error('SYNTHETIC_PRIVATE_CHANNEL_DIAGNOSTIC'); },
    close: async () => ({ code: 'closed' }),
  }) }));
  f.send(1, 1, configuration()); assert.equal(code(await f.next()), 1);
  f.send(2, 2); assert.equal(code(await f.next()), 1);
  const user = Buffer.from('synthetic-user'), pass = Buffer.from('synthetic-password');
  const body = Buffer.alloc(4 + user.length + pass.length);
  body.writeUInt16BE(user.length); user.copy(body, 2);
  body.writeUInt16BE(pass.length, 2 + user.length); pass.copy(body, 4 + user.length);
  f.send(4, 3, body); assert.equal(code(await f.next()), 6);
  assert.equal(code(await f.next()), 7); assert.equal(closes, 1);
  for (const value of borrowed) assert.equal(value.every(byte => byte === 0), true);
});

test('unknown or repeated private commands cannot create another worker', async t => {
  let allocations = 0;
  const f = fixture(t, async () => { allocations++; return { close: async () => {}, session: async () => ({ close: async () => ({ code: 'closed' }) }) }; });
  f.send(1, 1, configuration()); assert.equal(code(await f.next()), 1);
  f.send(1, 1, configuration()); assert.equal(code(await f.next()), 7);
  assert.equal(allocations, 1);
});
