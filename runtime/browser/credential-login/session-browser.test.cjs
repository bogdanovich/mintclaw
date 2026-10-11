'use strict';

// Synthetic DOM/control qualification; routed fixtures do not qualify network
// connectivity, provider access, startup identity, or production host isolation.
const assert = require('node:assert/strict');
const { test, before, after } = require('node:test');
const { chromium } = require('../playwright-library/node_modules/playwright');
const { createSession } = require('./session.cjs');
const { qualifySandbox } = require('./sandbox.cjs');
const origin = 'https://login.example.org';
const flow = { navigation_url: `${origin}/login`, entry_origin: origin, form_id: 'login',
  username_id: 'username', password_id: 'password', username_type: 'email', submit_url: `${origin}/session`,
  success_url: `${origin}/home`, success_marker_id: 'authenticated',
  handoff_url: `${origin}/mfa`, handoff_marker_id: 'mfa-required' };
const username = Buffer.from('synthetic-private-user@example.org');
const password = Buffer.from('SYNTHETIC_PRIVATE_PASSWORD_v1');
const cookie = 'SYNTHETIC_PRIVATE_COOKIE_v1';
let browser;
before(async () => {
  browser = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true, timeout: 30000 });
  if (process.platform === 'linux') {
    const context = await browser.newContext({ acceptDownloads: false, serviceWorkers: 'block' });
    try {
      const page = await context.newPage();
      await qualifySandbox(page);
    } finally { await context.close(); }
  }
});
after(async () => { if (browser) await browser.close(); });

async function fixture(t, options = {}) {
  const context = await browser.newContext({ acceptDownloads: false, serviceWorkers: 'block' });
  const abort = new AbortController(), calls = [], outcomes = [];
  let submissions = 0, requests = 0, denied = false;
  await context.route('**/*', async route => {
    requests++;
    const url = new URL(route.request().url());
    assert.equal(url.origin === origin, true, 'fixture escaped configured origin');
    if (url.pathname === '/login') {
      await route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html>' +
        '<form id="login" method="post" action="/session">' + (options.disabledFieldset ? '<fieldset disabled>' : '') +
        (options.legend ? '<legend>' : '') + '<input id="username" name="username" type="email">' +
        '<input id="password" name="password" type="password">' + (options.legend ? '</legend>' : '') +
        (options.disabledFieldset ? '</fieldset>' : '') + '</form>' + (options.extraHTML || '') });
    } else if (url.pathname === '/session') {
      submissions++;
      assert.equal(route.request().method() === 'POST', true, 'unexpected submission method');
      assert.equal(route.request().headers()['content-type'] === 'application/x-www-form-urlencoded', true,
        'unexpected submission encoding');
      const body = new URLSearchParams(route.request().postData());
      assert.equal(body.get('username') === username.toString(), true, 'username injection mismatch');
      assert.equal(body.get('password') === password.toString(), true, 'password injection mismatch');
      // Playwright's routed fixture cannot enforce a server redirect chain.
      // This client navigation exercises DOM/session behavior; the actual
      // enforcing proxy and real TLS/redirect transport remain separate gates.
      const destination = options.handoff ? flow.handoff_url : flow.success_url;
      await route.fulfill({ status: 200, contentType: 'text/html',
        headers: { 'set-cookie': `auth=${cookie}; Secure; HttpOnly; SameSite=Strict` },
        body: `<script>location.replace(${JSON.stringify(destination)})</script>` });
    } else {
      const marker = options.missingMarker ? '' : `<div id="${options.handoff ? 'mfa-required' : 'authenticated'}"` +
        `${options.hiddenMarker ? ' style="visibility:hidden"' : ''}>State</div>`;
      await route.fulfill({ status: 200, contentType: 'text/html', body:
        marker + `<p>Private echo ${password.toString()} ${password.toString('base64')} ${cookie}</p>` });
    }
  });
  const lease = { signal: abort.signal, remainingMS: options.remainingMS || 60000,
    check: async metadata => {
      assert.equal(Object.isFrozen(metadata), true);
      assert.deepEqual(Object.keys(metadata).sort(), ['operation', 'origin']);
      assert.equal(metadata.origin === origin, true);
      calls.push(metadata.operation);
      if (denied) throw new Error('SYNTHETIC_PRIVATE_DIAGNOSTIC');
      if (options.booleanApproval) return true;
    } };
  const session = createSession(context, lease, { ...flow }, [origin]);
  t.after(async () => { await session.close(); await context.close(); });
  const safe = result => {
    outcomes.push(result);
    const encoded = JSON.stringify(outcomes);
    for (const value of [username, password, Buffer.from(cookie)]) {
      assert.equal(encoded.includes(value.toString()), false, 'private value reached result');
      assert.equal(encoded.includes(value.toString('base64')), false, 'encoded private value reached result');
    }
    assert.deepEqual(Object.keys(result), ['code']);
    assert.equal(Object.isFrozen(result), true);
    return result.code;
  };
  return { context, session, abort, calls, lease, safe, submissions: () => submissions, requests: () => requests,
    deny: () => { denied = true; } };
}

test('private login returns only a fixed outcome and retires an idle authenticated context', async t => {
  const f = await fixture(t);
  assert.equal(f.safe(await f.session.prepare()), 'prepared');
  assert.equal(f.safe(await f.session.ready()), 'prepared');
  assert.equal(f.safe(await f.session.inject(username, password)), 'filled');
  const submitted = f.safe(await f.session.submit());
  assert.equal(submitted, 'logged_in', `submit phases: ${f.calls.join(',')}; submissions: ${f.submissions()}`);
  assert.equal(f.submissions(), 1);
  assert.equal(f.calls.includes('submit') && f.calls.includes('outcome'), true);
  assert.deepEqual(Object.keys(f.session).sort(), ['close', 'inject', 'prepare', 'ready', 'submit']);
  f.abort.abort();
  assert.equal(f.safe(await f.session.close()), 'closed');
  assert.equal(f.context.pages().every(page => page.isClosed()), true);
  assert.equal(f.safe(await f.session.inject(username, password)), 'denied');
  assert.equal(f.safe(await f.session.submit()), 'denied');
  assert.equal(f.safe(await f.session.ready()), 'denied');
  assert.equal(f.submissions(), 1);
});

test('configured MFA outcome uses the same private session boundary', async t => {
  const f = await fixture(t, { handoff: true });
  assert.equal(f.safe(await f.session.prepare()), 'prepared');
  assert.equal(f.safe(await f.session.inject(username, password)), 'filled');
  const submitted = f.safe(await f.session.submit());
  assert.equal(submitted, 'handoff_required', `submit phases: ${f.calls.join(',')}; submissions: ${f.submissions()}`);
});

test('success URL alone cannot claim authenticated state', async t => {
  const f = await fixture(t, { missingMarker: true });
  assert.equal(f.safe(await f.session.prepare()), 'prepared');
  assert.equal(f.safe(await f.session.inject(username, password)), 'filled');
  assert.equal(f.safe(await f.session.submit()), 'outcome_unknown');
  assert.equal(f.safe(await f.session.close()), 'closed');
});

for (const handoff of [false, true]) {
  test(`hidden ${handoff ? 'MFA' : 'success'} marker cannot confirm an outcome`, async t => {
    const f = await fixture(t, { handoff, hiddenMarker: true });
    assert.equal(f.safe(await f.session.prepare()), 'prepared');
    assert.equal(f.safe(await f.session.inject(username, password)), 'filled');
    assert.equal(f.safe(await f.session.submit()), 'outcome_unknown');
    assert.equal(f.safe(await f.session.close()), 'closed');
  });
  for (const navigation of ['reload', 'same-document']) {
    test(`authenticated ${handoff ? 'MFA' : 'login'} context retires on ${navigation}`, async t => {
      const f = await fixture(t, { handoff });
      assert.equal(f.safe(await f.session.prepare()), 'prepared');
      assert.equal(f.safe(await f.session.inject(username, password)), 'filled');
      assert.equal(f.safe(await f.session.submit()), handoff ? 'handoff_required' : 'logged_in');
      const page = f.context.pages()[0], closed = page.waitForEvent('close', { timeout: 3000 });
      if (navigation === 'reload') await page.reload().catch(() => {});
      else await page.evaluate(() => history.pushState({}, '', '/later')).catch(() => {});
      await closed;
      assert.equal(f.safe(await f.session.close()), 'closed');
      assert.equal(f.safe(await f.session.ready()), 'denied');
      assert.equal(f.submissions(), 1);
    });
  }
}

for (const mutation of ['action', 'method', 'enctype', 'bypass']) {
  test(`actual outgoing submission rejects submit-handler ${mutation} mutation`, async t => {
    const f = await fixture(t);
    assert.equal(f.safe(await f.session.prepare()), 'prepared');
    assert.equal(f.safe(await f.session.inject(username, password)), 'filled');
    await f.context.pages()[0].evaluate(mutation => {
      document.getElementById('login').addEventListener('submit', event => {
        if (mutation === 'action') event.target.action = '/changed-session';
        else if (mutation === 'method') event.target.method = 'get';
        else if (mutation === 'enctype') event.target.enctype = 'text/plain';
        else { event.preventDefault(); location.href = '/home'; }
      });
    }, mutation);
    assert.equal(f.safe(await f.session.submit()), 'outcome_unknown');
    assert.equal(f.safe(await f.session.close()), 'closed');
    assert.equal(f.requests(), 1, 'changed submission reached the destination');
    assert.equal(f.submissions(), 0);
  });
}

for (const phase of ['prepare', 'ready', 'inject', 'submit']) {
  test(`inherited fieldset disabling is rejected before ${phase}`, async t => {
    const f = await fixture(t, { disabledFieldset: phase === 'prepare' });
    if (phase !== 'prepare') {
      assert.equal(f.safe(await f.session.prepare()), 'prepared');
      if (phase === 'submit') assert.equal(f.safe(await f.session.inject(username, password)), 'filled');
      await f.context.pages()[0].evaluate(() => {
        const fieldset = document.createElement('fieldset'); fieldset.disabled = true;
        fieldset.append(document.getElementById('username'), document.getElementById('password'));
        document.getElementById('login').append(fieldset);
      });
    }
    const result = phase === 'inject' ? await f.session.inject(username, password) : await f.session[phase]();
    assert.equal(f.safe(result), 'outcome_unknown');
    assert.equal(f.safe(await f.session.close()), 'closed');
    assert.equal(f.submissions(), 0);
  });
}

test('first-legend inputs retain native enabled semantics in a disabled fieldset', async t => {
  const f = await fixture(t, { disabledFieldset: true, legend: true });
  assert.equal(f.safe(await f.session.prepare()), 'prepared');
  assert.equal(f.safe(await f.session.ready()), 'prepared');
  assert.equal(f.safe(await f.session.inject(username, password)), 'filled');
  assert.equal(f.safe(await f.session.submit()), 'logged_in');
  assert.equal(f.submissions(), 1);
});

for (const [name, change] of [
  ['detached input', page => page.evaluate(() => document.getElementById('password').remove())],
  ['replaced input', page => page.evaluate(() => {
    const previous = document.getElementById('password'), replacement = previous.cloneNode();
    previous.replaceWith(replacement);
  })],
  ['revealed password', page => page.evaluate(() => { document.getElementById('password').type = 'text'; })],
  ['disabled input', page => page.evaluate(() => { document.getElementById('password').disabled = true; })],
  ['hidden input', page => page.evaluate(() => { document.getElementById('password').style.display = 'none'; })],
  ['changed form encoding', page => page.evaluate(() => { document.getElementById('login').enctype = 'text/plain'; })],
  ['duplicate identity', page => page.evaluate(() => {
    document.getElementById('login').append(document.getElementById('password').cloneNode());
  })],
  ['changed action', page => page.evaluate(() => { document.getElementById('login').action = 'https://attacker.example/session'; })],
]) {
  test(`private injection rejects ${name}`, async t => {
    const f = await fixture(t);
    assert.equal(f.safe(await f.session.prepare()), 'prepared');
    await change(f.context.pages()[0]);
    assert.equal(f.safe(await f.session.inject(username, password)), 'outcome_unknown');
    assert.equal(f.safe(await f.session.close()), 'closed');
    assert.equal(f.submissions(), 0);
    assert.equal(f.context.pages().every(page => page.isClosed()), true);
  });
}

test('navigation after preparation retires document authority before retrieval', async t => {
  const f = await fixture(t);
  assert.equal(f.safe(await f.session.prepare()), 'prepared');
  await f.context.pages()[0].reload().catch(() => {});
  assert.equal(f.safe(await f.session.ready()), 'denied');
  assert.equal(f.safe(await f.session.inject(username, password)), 'denied');
  assert.equal(f.safe(await f.session.close()), 'closed');
  assert.equal(f.submissions(), 0);
});

test('stale input cannot pass pre-retrieval readiness', async t => {
  const f = await fixture(t);
  assert.equal(f.safe(await f.session.prepare()), 'prepared');
  await f.context.pages()[0].evaluate(() => document.getElementById('password').remove());
  assert.equal(f.safe(await f.session.ready()), 'outcome_unknown');
  assert.equal(f.safe(await f.session.close()), 'closed');
  assert.equal(f.submissions(), 0);
});

test('revocation between preparation and injection closes without submission', async t => {
  const f = await fixture(t);
  assert.equal(f.safe(await f.session.prepare()), 'prepared');
  f.abort.abort();
  assert.equal(f.safe(await f.session.inject(username, password)), 'denied');
  assert.equal(f.safe(await f.session.close()), 'closed');
  assert.equal(f.submissions(), 0);
});

test('current authorization denial never returns the private error', async t => {
  const f = await fixture(t);
  assert.equal(f.safe(await f.session.prepare()), 'prepared');
  f.deny();
  assert.equal(f.safe(await f.session.inject(username, password)), 'outcome_unknown');
  assert.equal(f.submissions(), 0);
});

test('model-shaped boolean cannot substitute for the trusted check', async t => {
  const f = await fixture(t, { booleanApproval: true });
  assert.equal(f.safe(await f.session.prepare()), 'outcome_unknown');
  assert.equal(f.submissions(), 0);
});

test('caller cannot replace the captured lease signal or checker', async t => {
  const f = await fixture(t);
  assert.equal(f.safe(await f.session.prepare()), 'prepared');
  f.lease.signal = new AbortController().signal;
  f.lease.check = async () => {};
  f.deny();
  assert.equal(f.safe(await f.session.inject(username, password)), 'outcome_unknown');
  assert.equal(f.safe(await f.session.close()), 'closed');
});

test('unexpected browser context loss retires the session permanently', async t => {
  const f = await fixture(t);
  assert.equal(f.safe(await f.session.prepare()), 'prepared');
  await f.context.close();
  assert.equal(f.safe(await f.session.inject(username, password)), 'denied');
  assert.equal(f.safe(await f.session.prepare()), 'denied');
  assert.equal(f.safe(await f.session.close()), 'closed');
});

test('original bounded lifetime closes without a subsequent operation', async t => {
  const f = await fixture(t, { remainingMS: 1500 });
  assert.equal(f.safe(await f.session.prepare()), 'prepared');
  const page = f.context.pages()[0];
  let deadline;
  try {
    await Promise.race([page.waitForEvent('close'), new Promise((_, reject) => {
      deadline = setTimeout(() => reject(new Error('idle expiry missed')), 5000);
    })]);
  } finally { clearTimeout(deadline); }
  assert.equal(f.safe(await f.session.inject(username, password)), 'denied');
  assert.equal(f.safe(await f.session.close()), 'closed');
});
