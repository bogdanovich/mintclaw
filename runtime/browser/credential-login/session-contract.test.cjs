'use strict';

const assert = require('node:assert/strict');
const { test } = require('node:test');
const { createSession } = require('./session.cjs');
const { launch } = require('./launch.cjs');
const { qualifySandbox } = require('./sandbox.cjs');
const { privateArguments } = require('./launch-arguments.cjs');
const vm = require('node:vm');
const origin = 'https://login.example.org';
const flow = { navigation_url: `${origin}/login`, entry_origin: origin, form_id: 'login',
  username_id: 'username', password_id: 'password', username_type: 'email', submit_url: `${origin}/session`,
  success_url: `${origin}/home`, success_marker_id: 'authenticated' };
const lease = () => ({ signal: new AbortController().signal, remainingMS: 60000, check: async () => {} });
const context = () => ({ on() {}, close: async () => {} });

test('incomplete, ambiguous and model-shaped flows deny with one fixed error', () => {
  const changes = [
    value => { value.password_id = 'input[type=password]'; },
    value => { value.password_id = value.username_id; },
    value => { value.success_marker_id = ''; },
    value => { value.navigation_url += '?password=private'; },
    value => { value.navigation_url = `${origin}/a/../login`; },
    value => { value.navigation_url = `${origin}/%6cogin`; },
    value => { value.submit_url = 'https://attacker.example/session'; },
    value => { value.handoff_url = `${origin}/mfa`; },
    value => { value.handoff_marker_id = 'mfa'; },
    value => { value.handoff_url = 0; },
    value => { value.javascript = 'return password'; },
  ];
  for (const change of changes) {
    const changed = { ...flow };
    change(changed);
    assert.throws(() => createSession(context(), lease(), changed, [origin]),
      error => error.message === 'credential browser unavailable');
  }
  for (const remainingMS of [0, -1, 20 * 60 * 60 * 1000 + 1, NaN, Infinity, 1.5]) {
    assert.throws(() => createSession(context(), { ...lease(), remainingMS }, flow, [origin]),
      error => error.message === 'credential browser unavailable');
  }
});

test('unacknowledged close quarantines and cannot reopen a session', async () => {
  const session = createSession({ on() {}, close: async () => { throw new Error('SYNTHETIC_PRIVATE_DIAGNOSTIC'); } },
    lease(), flow, [origin]);
  assert.deepEqual(await session.close(), { code: 'quarantined' });
  assert.deepEqual(await session.prepare(), { code: 'denied' });
  assert.deepEqual(await session.inject(Buffer.from('synthetic-user'), Buffer.from('synthetic-password')), { code: 'denied' });
  assert.deepEqual(await session.close(), { code: 'quarantined' });
});

test('launch rejects arbitrary browser options and unsafe process startup', async () => {
  for (const config of [null, {}, { executablePath: '/tmp/browser', proxyServer: 'http://127.0.0.1:18080', args: ['--no-sandbox'] },
    { executablePath: '/tmp/browser', proxyServer: 'http://127.0.0.1:18080' }]) {
    await assert.rejects(launch(config), error => error.message === 'credential browser unavailable');
  }
});

test('private certificate exception accepts exactly one canonical SPKI pin', () => {
  const pin = Buffer.alloc(32).toString('base64');
  const args = privateArguments(pin);
  assert.deepEqual(args.filter(value => value.startsWith('--ignore-certificate-errors')), [
    `--ignore-certificate-errors-spki-list=${pin}`]);
  for (const invalid of [undefined, '', pin.slice(0, -1), `${pin},${pin}`, ` ${pin}`,
    Buffer.alloc(31).toString('base64'), Buffer.alloc(33).toString('base64'), `${pin.slice(0, -2)}B=`]) {
    assert.throws(() => privateArguments(invalid), error => error.message === 'credential browser unavailable');
  }
});

test('sandbox witness requires exact complete current Chromium rows and verdict', async () => {
  const baseline = [['Layer 1 Sandbox', 'Namespace'], ['PID namespaces', 'Yes'], ['Network namespaces', 'Yes'],
    ['Seccomp-BPF sandbox', 'Yes'], ['Seccomp-BPF sandbox supports TSYNC', 'Yes']];
  const page = (rows, verdict = 'You are adequately sandboxed.', url = 'chrome://sandbox/') => ({
    goto: async destination => { assert.equal(destination, 'chrome://sandbox/'); },
    waitForFunction: async () => {}, url: () => url,
    evaluate: async callback => vm.runInNewContext(`(${callback.toString()})()`, {
      document: { querySelector: () => ({ textContent: verdict }),
        querySelectorAll: () => rows.map(row => ({ querySelectorAll: () => row.map(textContent => ({ textContent })) })) },
    }),
  });
  await qualifySandbox(page(baseline));
  for (let i = 0; i < baseline.length; i++) {
    const changed = baseline.map(row => [...row]); changed[i][1] = 'No';
    await assert.rejects(qualifySandbox(page(changed)));
    await assert.rejects(qualifySandbox(page(baseline.filter((_, index) => index !== i))));
    await assert.rejects(qualifySandbox(page([...baseline, baseline[i]])));
  }
  await assert.rejects(qualifySandbox(page(baseline, 'You are NOT adequately sandboxed.')));
  await assert.rejects(qualifySandbox(page(baseline, 'You are adequately sandboxed.', 'https://attacker.example/')));
});
