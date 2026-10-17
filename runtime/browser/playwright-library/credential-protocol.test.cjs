'use strict';

const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const path = require('node:path');
const readline = require('node:readline');
const test = require('node:test');
const { Driver } = require('./sidecar.cjs');

const origin = 'https://credential-protocol.example.org';
const username = 'protocol-private-account@example.org';
const password = 'protocol-private-password-M83&secret';
const cookie = 'protocol-private-cookie-F47';
const rotatedCookie = 'protocol-rotated-cookie-K49';

// The production Driver and JSON-lines loop run in a separate process. Only
// navigation is replaced with deterministic HTTPS routes; no real vault/site
// credentials or network requests are used by this fixture.
const bootstrap = `
  const { Driver, runProtocol } = require('./sidecar.cjs');
  (async () => {
    const driver = new Driver({ browser: 'chromium', headless: true,
      executablePath: process.env.MINTCLAW_BROWSER_TEST_EXECUTABLE || '' });
    await driver.start();
    let submissions = 0;
    await driver.context.route('https://credential-protocol.example.org/**', async route => {
      const incoming = route.request(), url = new URL(incoming.url());
      if (url.pathname === '/login') {
        await route.fulfill({ contentType: 'text/html', body: '<form method="post" action="/session">' +
          '<input type="email" name="username" aria-label="Account">' +
          '<input type="password" name="password" aria-label="Password" oninput="alert(this.value)">' +
          '<button type="submit">Sign in</button></form>' });
      } else if (url.pathname === '/session') {
        submissions++;
        const fields = new URLSearchParams(incoming.postData());
        if (submissions !== 1 || fields.get('username') !== ${JSON.stringify(username)} ||
            fields.get('password') !== ${JSON.stringify(password)}) throw new Error('fixture submission mismatch');
        await route.fulfill({ contentType: 'text/html',
          headers: { 'set-cookie': 'session=${cookie}; Secure; HttpOnly; Path=/' },
          body: '<script>location.replace("/home")</script>' });
      } else if (url.pathname === '/rotate') {
        await route.fulfill({ contentType: 'text/html',
          headers: { 'set-cookie': 'session=${rotatedCookie}; Secure; HttpOnly; Path=/' },
          body: '<div>Ready</div><input aria-label="Verification code" autocomplete="one-time-code" value="741852">' +
            '<textarea aria-label="Recovery value">private-textarea-N58</textarea>' +
            '<select aria-label="Recovery option"><option selected>private-option-R26</option></select>' +
            '<div role="textbox" contenteditable="true" aria-label="Recovery input">private-editable-J62</div>' });
      } else {
        await route.fulfill({ contentType: 'text/html', body: '<div id="signed-in">Ready</div>' });
      }
    });
    await runProtocol(driver, process.stdin, process.stdout);
  })().catch(() => { process.stderr.write('fixture failed\\n'); process.exitCode = 1; });
`;

async function wireFixture(t) {
  const child = spawn(process.execPath, ['-e', bootstrap], {
    cwd: path.resolve(__dirname), stdio: ['pipe', 'pipe', 'pipe'],
  });
  const pending = new Map();
  let stderr = '', id = 0;
  child.stderr.on('data', chunk => { stderr += chunk; });
  const lines = readline.createInterface({ input: child.stdout });
  lines.on('line', line => {
    let response;
    try { response = JSON.parse(line); } catch (_) {
      for (const waiter of pending.values()) waiter.reject(new Error('invalid private wire response'));
      return;
    }
    const waiter = pending.get(response.id);
    if (!waiter) return;
    pending.delete(response.id);
    waiter.resolve(response);
  });
  const exited = new Promise(resolve => child.on('exit', code => {
    for (const waiter of pending.values()) waiter.reject(new Error('private fixture exited'));
    resolve(code);
  }));
  t.after(() => { lines.close(); child.kill('SIGKILL'); });
  const raw = (requestID, line) => new Promise((resolve, reject) => {
    pending.set(requestID, { resolve, reject });
    child.stdin.write(line + '\n');
  });
  const call = async (method, params) => {
    const requestID = ++id;
    const response = await raw(requestID, JSON.stringify({ id: requestID, method, params }));
    assert.equal(typeof response.error, 'undefined', 'private operation unexpectedly failed');
    return response.result;
  };
  await call('mintclaw_initialize', { protocol: 'mintclaw.playwright_library.v1' });
  return { call, raw, stderr: () => stderr, shutdown: async () => {
    await call('shutdown', {}); child.stdin.end();
    assert.equal(await exited, 0); assert.equal(stderr, '');
  } };
}

const prepare = snapshot => {
  const refs = Object.fromEntries([['username_ref', 'Account'], ['password_ref', 'Password'], ['submit_ref', 'Sign in']]
    .map(([key, name]) => {
      const match = snapshot.content[0].text.match(new RegExp(`- (?:textbox|button) "${name}"[^\\n]*\\[ref=((?:f\\d+)?e\\d+)\\]`));
      assert.ok(match, 'native credential target unavailable');
      return [key, match[1]];
    }));
  return { origin, ...refs, success_url: `${origin}/home`, success_marker_id: 'signed-in' };
};
const envelope = result => {
  assert.equal(result.is_error, false, 'private response unexpectedly denied');
  assert.equal(result.content.length, 1);
  return JSON.parse(result.content[0].text);
};

test('private JSON-lines transaction suppresses dialogs and returns only fixed outcome plus private cookies',
  { timeout: 30000 }, async t => {
    const wire = await wireFixture(t);
    await wire.call('browser_navigate', { url: `${origin}/home` });
    const snapshot = await wire.call('browser_navigate', { url: `${origin}/login` });
    const fields = prepare(snapshot);
    assert.match(fields.username_ref, /^f\d+e\d+$/);
    const ready = envelope(await wire.call('mintclaw_credential_prepare', fields));
    assert.match(ready.id, /^[a-f0-9]{64}$/); assert.equal(ready.origin, origin);
    const pending = await wire.call('browser_snapshot', {});
    assert.equal(pending.is_error, true);
    const result = await wire.call('mintclaw_credential_submit', { id: ready.id, username, password });
    const response = envelope(result);
    assert.equal(response.code, 'logged_in');
    assert.equal(response.cookies.length, 1);
    assert.equal(response.cookies[0] === cookie, true, 'private cookie handoff mismatch');
    assert.equal(JSON.stringify(result).includes(username) || JSON.stringify(result).includes(password), false,
      'credential value escaped private submission');
    const bypass = await wire.call('browser_snapshot', {});
    assert.equal(bypass.is_error, true, 'authenticated observation bypassed private cookie harvesting');
    const observation = envelope(await wire.call('mintclaw_credential_call', { tool: 'browser_snapshot', args: {} }));
    assert.equal(observation.result.is_error, false);
    assert.equal(JSON.stringify(observation.result).includes(password), false, 'private dialog was retained');
    const rotated = envelope(await wire.call('mintclaw_credential_call', {
      tool: 'browser_navigate', args: { url: `${origin}/rotate` },
    }));
    assert.equal(rotated.cookies.includes(cookie), true, 'old cookie protection was evicted');
    assert.equal(rotated.cookies.includes(rotatedCookie), true, 'rotated cookie was not privately harvested');
    const observed = JSON.stringify(rotated.result);
    assert.equal(['741852', 'private-textarea-N58', 'private-option-R26', 'private-editable-J62']
      .some(value => observed.includes(value)), false, 'authentication input was observed');
    assert.equal(observed.includes('Verification code') && observed.includes('Recovery input'), true,
      'authenticated controls lost their labels');
    await wire.shutdown();
  });

test('discarded capsules cannot submit and malformed JSON never echoes private input',
  { timeout: 30000 }, async t => {
    const wire = await wireFixture(t);
    const snapshot = await wire.call('browser_navigate', { url: `${origin}/login` });
    const ready = envelope(await wire.call('mintclaw_credential_prepare', prepare(snapshot)));
    const discarded = await wire.call('mintclaw_credential_discard', { id: ready.id });
    assert.equal(discarded.content[0].text, 'discarded');
    const denied = envelope(await wire.call('mintclaw_credential_submit', { id: ready.id, username, password }));
    assert.deepEqual(denied, { code: 'denied', cookies: [] });
    const malformed = await wire.raw(null, '{"password":' + password);
    assert.deepEqual(malformed, { id: null, error: 'private driver request failed' });
    assert.equal(wire.stderr(), '');
    await wire.shutdown();
  });

test('expired prepared requests dispose their handle before denying submission', async () => {
  const driver = new Driver({});
  let disposed = 0;
  driver.credentialPending = { id: 'expired', deadline: Date.now() - 1,
    capsule: { dispose: async () => { disposed++; } } };
  const values = { id: 'expired', username, password };
  assert.deepEqual(envelope(await driver.submitCredential(values)), { code: 'denied', cookies: [] });
  assert.equal(disposed, 1); assert.equal(driver.credentialPending, null);
  assert.equal(values.username, ''); assert.equal(values.password, '');
});
