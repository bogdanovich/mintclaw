'use strict';

// Private inherited stream, never stdin/stdout, a public listener, or a model
// tool. Root startup must qualify both endpoints and their process lifetimes.
const { performance } = require('node:perf_hooks');
const MAX_FRAME = 32768;
const OPS = ['', 'navigate', 'network', 'prepare', 'retrieve', 'inject', 'submit', 'outcome', 'lease'];
const CODES = ['', 'prepared', 'filled', 'logged_in', 'handoff_required', 'denied', 'outcome_unknown', 'closed', 'quarantined'];
const FLOW = ['navigation_url', 'entry_origin', 'form_id', 'username_id', 'password_id', 'username_type',
  'submit_url', 'success_url', 'success_marker_id', 'handoff_url', 'handoff_marker_id'];

function unpack(data) {
  let offset = 0;
  const number = () => {
    if (offset + 4 > data.length) throw new Error();
    const value = data.readUInt32BE(offset); offset += 4; return value;
  };
  const bytes = max => {
    if (offset + 2 > data.length) throw new Error();
    const count = data.readUInt16BE(offset); offset += 2;
    if (count > max || offset + count > data.length) throw new Error();
    const value = data.subarray(offset, offset + count); offset += count; return value;
  };
  const string = max => {
    const value = new TextDecoder('utf-8', { fatal: true }).decode(bytes(max));
    if (/[\x00\r\n]/.test(value)) throw new Error();
    return value;
  };
  return { number, bytes, string, end: () => { if (offset !== data.length) throw new Error(); } };
}

function startChannel(stream, factory) {
  if (!stream || typeof factory !== 'function') throw new Error('credential browser unavailable');
  const abort = new AbortController(), pending = new Map();
  let session, owner, receiving = Buffer.alloc(0), nextCommand = 1, nextCheck = 0;
  let busy = false, retiring = false, closing, heartbeat, idleCheck = false, allocation;
  let initializedAt = 0, remaining = 0;

  function send(type, id, body = Buffer.alloc(0)) {
    if (stream.destroyed || body.length + 5 > MAX_FRAME || stream.writableLength > MAX_FRAME * 2) throw new Error();
    const frame = Buffer.alloc(9 + body.length);
    frame.writeUInt32BE(body.length + 5); frame[4] = type; frame.writeUInt32BE(id, 5); body.copy(frame, 9);
    stream.write(frame, () => frame.fill(0));
  }
  function finishChecks() {
    for (const entry of pending.values()) { clearTimeout(entry.timer); entry.reject(new Error()); }
    pending.clear();
  }
  async function close() {
    if (closing) return closing;
    retiring = true; abort.abort(); clearInterval(heartbeat); finishChecks();
    closing = Promise.resolve().then(async () => {
      let code = 'closed', deadline;
      try {
        await Promise.race([(async () => {
          if (allocation) await allocation;
          if (session && (await session.close()).code !== 'closed') throw new Error();
          if (owner) await owner.close();
        })(), new Promise((_, reject) => { deadline = setTimeout(() => reject(new Error()), 3500); })]);
      } catch (_) { code = 'quarantined'; }
      finally { clearTimeout(deadline); }
      try { send(0x10, 0, Buffer.from([CODES.indexOf(code)])); } catch (_) {}
      receiving.fill(0); receiving = Buffer.alloc(0);
      stream.end();
      return code;
    });
    return closing;
  }
  function check(metadata) {
    if (retiring || abort.signal.aborted || performance.now() - initializedAt >= remaining ||
        pending.size >= 32 || nextCheck === 0xffffffff) return Promise.reject(new Error());
    const operation = OPS.indexOf(metadata.operation), origin = Buffer.from(metadata.origin || '', 'utf8');
    if (operation < 1 || origin.length < 1 || origin.length > 2048) return Promise.reject(new Error());
    const id = ++nextCheck, body = Buffer.alloc(3 + origin.length);
    body[0] = operation; body.writeUInt16BE(origin.length, 1); origin.copy(body, 3);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(id); reject(new Error()); void close();
      }, 1000);
      pending.set(id, { resolve, reject, timer });
      try { send(0x20, id, body); }
      catch (_) { clearTimeout(timer); pending.delete(id); reject(new Error()); void close(); }
      finally { body.fill(0); origin.fill(0); }
    });
  }
  async function command(type, id, body) {
    if (busy || retiring || id !== nextCommand || id > 16 || type < 1 || type > 5 ||
        (id === 1 ? type !== 1 : type === 1)) throw new Error();
    nextCommand++; busy = true;
    try {
      let result;
      if (type === 1) {
        const fields = unpack(body), config = {};
        remaining = fields.number(); initializedAt = performance.now();
        if (!remaining || remaining > 20 * 60 * 60 * 1000) throw new Error();
        config.executablePath = fields.string(4096); config.proxyServer = fields.string(256);
        config.proxyPin = fields.string(44);
        const flow = {};
        for (const name of FLOW) flow[name] = fields.string(2048);
        const count = fields.number(), origins = [];
        if (!count || count > 16) throw new Error();
        for (let i = 0; i < count; i++) origins.push(fields.string(2048));
        fields.end();
        allocation = Promise.resolve().then(() => factory(Object.freeze(config))).then(value => { owner = value; });
        await allocation;
        if (retiring) throw new Error();
        session = await owner.session({ signal: abort.signal, check,
          remainingMS: remaining - Math.ceil(performance.now() - initializedAt) }, flow, origins);
        if (retiring) throw new Error();
        heartbeat = setInterval(() => {
          if (idleCheck || retiring) return;
          idleCheck = true;
          void check({ operation: 'lease', origin: flow.entry_origin }).catch(() => close())
            .finally(() => { idleCheck = false; });
        }, 1000);
        result = { code: 'prepared' }; // Channel configuration only; prepare still validates the live DOM.
      } else {
        if (!session) throw new Error();
        if (type === 4) {
          const fields = unpack(body), username = fields.bytes(1024), password = fields.bytes(4096);
          fields.end();
          result = await session.inject(username, password);
        } else {
          if (body.length) throw new Error();
          result = await session[type === 2 ? 'prepare' : type === 3 ? 'ready' : 'submit']();
        }
      }
      const allowed = type < 4 ? ['prepared'] : type === 4 ? ['filled'] : ['logged_in', 'handoff_required'];
      if (!result || Object.keys(result).join(',') !== 'code' ||
          ![...allowed, 'denied', 'outcome_unknown'].includes(result.code)) throw new Error();
      if (!retiring) send(0x10, id, Buffer.from([CODES.indexOf(result.code)]));
      if (!allowed.includes(result.code)) void close();
    } catch (_) {
      try { if (!retiring) send(0x10, id, Buffer.from([6])); } catch (_) {}
      void close();
    } finally { body.fill(0); busy = false; }
  }
  function receive(frame) {
    if (frame.length < 5) throw new Error();
    const type = frame[0], id = frame.readUInt32BE(1), body = frame.subarray(5);
    if (type === 0x21) {
      const entry = pending.get(id);
      if (!entry || body.length !== 1 || body[0] > 1) throw new Error();
      pending.delete(id); clearTimeout(entry.timer);
      if (body[0] === 1) entry.resolve();
      else { entry.reject(new Error()); void close(); }
      frame.fill(0);
    } else if (type === 6 && id === 0 && !body.length) {
      frame.fill(0); void close();
    } else {
      void command(type, id, body).catch(() => close()).finally(() => frame.fill(0));
    }
  }
  stream.on('data', chunk => {
    try {
      if (retiring || receiving.length + chunk.length > MAX_FRAME * 2 + 8) throw new Error();
      const combined = Buffer.concat([receiving, chunk]); receiving.fill(0); chunk.fill(0); receiving = combined;
      while (receiving.length >= 4) {
        const size = receiving.readUInt32BE();
        if (size < 5 || size > MAX_FRAME) throw new Error();
        if (receiving.length < size + 4) break;
        const frame = Buffer.from(receiving.subarray(4, size + 4)), rest = Buffer.from(receiving.subarray(size + 4));
        receiving.fill(0); receiving = rest; receive(frame);
      }
    } catch (_) { void close(); }
  });
  for (const event of ['end', 'error', 'close']) stream.on(event, () => { void close(); });
  // The Root supervisor must force termination if close cannot be acknowledged.
  return Object.freeze({ close });
}

module.exports = { startChannel };
