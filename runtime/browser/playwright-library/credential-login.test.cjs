'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { chromium } = require('playwright');
const { prepareCredentialLogin, performCredentialLogin } = require('./credential-login.cjs');

const origin = 'https://credential-fixture.example.org';
const username = 'private-fixture-account@example.org';
const password = 'private-browser-fixture-A9&secret';
const request = () => ({ origin, username_ref: 'e1', password_ref: 'e2', submit_ref: 'e3',
  username, password, success_url: `${origin}/home`, success_marker_id: 'signed-in', timeout_ms: 1000 });

async function fixture(t, extra = '') {
  const browser = await chromium.launch({ headless: true });
  t.after(() => browser.close());
  const context = await browser.newContext();
  const page = await context.newPage();
  let submissions = 0;
  await context.route('**/*', async route => {
    const incoming = route.request(), url = new URL(incoming.url());
    assert.equal(url.origin, origin, 'unexpected fixture destination');
    if (url.pathname === '/login') {
      await route.fulfill({ contentType: 'text/html', body: `<form method="post" action="/session">
        <input type="email" name="username" data-mintclaw-playwright-ref="e1">
        <input type="password" name="password" data-mintclaw-playwright-ref="e2">
        <button type="submit" data-mintclaw-playwright-ref="e3">Sign in</button>
        </form>${extra}` });
    } else if (url.pathname === '/session') {
      submissions++;
      const fields = new URLSearchParams(incoming.postData());
      assert.equal(fields.get('username') === username, true, 'username injection mismatch');
      assert.equal(fields.get('password') === password, true, 'password injection mismatch');
      await route.fulfill({ contentType: 'text/html', body: '<script>location.replace("/home")</script>' });
    } else {
      await route.fulfill({ contentType: 'text/html', body: '<div id="signed-in">Ready</div>' });
    }
  });
  await page.goto(`${origin}/login`);
  return { page, context, submissions: () => submissions };
}

test('private login submits once and returns only fixed authentication evidence', { timeout: 30000 }, async t => {
  const f = await fixture(t), values = request();
  const capsule = await prepareCredentialLogin(f.page, values);
  assert.ok(capsule, 'private readiness unavailable');
  const result = await performCredentialLogin(f.page, capsule, values);
  assert.equal(result, 'logged_in');
  assert.equal(f.submissions(), 1);
  assert.equal(values.username, ''); assert.equal(values.password, '');
});

test('origin changes before injection do not submit credentials', { timeout: 30000 }, async t => {
  const f = await fixture(t), values = request();
  const capsule = await prepareCredentialLogin(f.page, values);
  await f.page.setContent('<p>Different document</p>');
  const result = await performCredentialLogin(f.page, capsule, values);
  assert.equal(['denied', 'outcome_unknown'].includes(result), true);
  assert.equal(f.submissions(), 0);
});

test('unproven authentication closes the affected context', { timeout: 30000 }, async t => {
  const f = await fixture(t), values = request();
  values.success_marker_id = 'not-present';
  const capsule = await prepareCredentialLogin(f.page, values);
  assert.equal(await performCredentialLogin(f.page, capsule, values), 'outcome_unknown');
  assert.equal(f.page.isClosed(), true);
  assert.equal(f.submissions(), 1);
});

test('GET login forms are rejected before any private fill', { timeout: 30000 }, async t => {
  const f = await fixture(t), values = request();
  await f.page.locator('form').evaluate(form => { form.method = 'get'; });
  const capsule = await prepareCredentialLogin(f.page, values);
  assert.equal(capsule, null);
  assert.equal(f.submissions(), 0);
});
