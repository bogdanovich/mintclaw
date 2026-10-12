'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { chromium } = require('playwright');
const { prepareCredentialLogin, performCredentialLogin } = require('./credential-login.cjs');

const origin = 'https://credential-fixture.example.org';
const username = 'private-fixture-account@example.org';
const password = 'private-browser-fixture-A9&secret';
const request = refs => ({ origin, ...refs,
  username, password, success_url: `${origin}/home`, success_marker_id: 'signed-in', timeout_ms: 1000 });

async function fixture(t, extra = '') {
  const browser = await chromium.launch({ headless: true,
    executablePath: process.env.MINTCLAW_BROWSER_TEST_EXECUTABLE || undefined });
  t.after(() => browser.close());
  const context = await browser.newContext();
  const page = await context.newPage();
  let submissions = 0;
  await context.route('**/*', async route => {
    const incoming = route.request(), url = new URL(incoming.url());
    assert.equal(url.origin, origin, 'unexpected fixture destination');
    if (url.pathname === '/login') {
      await route.fulfill({ contentType: 'text/html', body: `<form method="post" action="/session">
        <input type="email" name="username" aria-label="Account">
        <input type="password" name="password" aria-label="Password">
        <button type="submit">Sign in</button>
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
  const tree = await page.locator(':root').ariaSnapshotJSON({ mode: 'ai' });
  const nodes = tree.flatMap(function flatten(node) {
    return typeof node === 'string' ? [] : [node, ...(node.children || []).flatMap(flatten)];
  });
  const refs = Object.fromEntries([['username_ref', 'Account'], ['password_ref', 'Password'], ['submit_ref', 'Sign in']]
    .map(([key, name]) => [key, nodes.find(node => node.name === name).ref]));
  return { page, context, refs, submissions: () => submissions };
}

test('private login submits once and returns only fixed authentication evidence', { timeout: 30000 }, async t => {
  const f = await fixture(t), values = request(f.refs);
  const capsule = await prepareCredentialLogin(f.page, values);
  assert.ok(capsule, 'private readiness unavailable');
  const result = await performCredentialLogin(f.page, capsule, values);
  assert.equal(result, 'logged_in');
  assert.equal(f.submissions(), 1);
  assert.equal(values.username, ''); assert.equal(values.password, '');
});

test('origin changes before injection do not submit credentials', { timeout: 30000 }, async t => {
  const f = await fixture(t), values = request(f.refs);
  const capsule = await prepareCredentialLogin(f.page, values);
  await f.page.setContent('<p>Different document</p>');
  const result = await performCredentialLogin(f.page, capsule, values);
  assert.equal(['denied', 'outcome_unknown'].includes(result), true);
  assert.equal(f.submissions(), 0);
});

test('unproven authentication closes the affected context', { timeout: 30000 }, async t => {
  const f = await fixture(t), values = request(f.refs);
  values.success_marker_id = 'not-present';
  const capsule = await prepareCredentialLogin(f.page, values);
  assert.equal(await performCredentialLogin(f.page, capsule, values), 'outcome_unknown');
  assert.equal(f.page.isClosed(), true);
  assert.equal(f.submissions(), 1);
});

test('GET login forms are rejected before any private fill', { timeout: 30000 }, async t => {
  const f = await fixture(t), values = request(f.refs);
  await f.page.locator('form').evaluate(form => { form.method = 'get'; });
  const capsule = await prepareCredentialLogin(f.page, values);
  assert.equal(capsule, null);
  assert.equal(f.submissions(), 0);
});

test('invalid values dispose the prepared handle and clear the private request', { timeout: 30000 }, async t => {
  const f = await fixture(t), values = request(f.refs);
  const capsule = await prepareCredentialLogin(f.page, values);
  values.password += '\n';
  assert.equal(await performCredentialLogin(f.page, capsule, values), 'denied');
  assert.equal(values.username, ''); assert.equal(values.password, '');
  assert.equal(f.submissions(), 0);
  assert.equal(f.page.isClosed(), false);
  await assert.rejects(capsule.evaluate(() => true), undefined, 'prepared handle was not disposed');
});

test('submitter destination overrides are checked before vault retrieval', { timeout: 30000 }, async t => {
  const f = await fixture(t), values = request(f.refs);
  for (const [attribute, value] of [
    ['formmethod', 'get'], ['formaction', 'https://other.example.org/session'], ['formtarget', '_blank'],
  ]) {
    await f.page.locator('button').evaluate((button, pair) => button.setAttribute(...pair), [attribute, value]);
    assert.equal(await prepareCredentialLogin(f.page, values), null, 'unsafe submitter override was admitted');
    await f.page.locator('button').evaluate((button, name) => button.removeAttribute(name), attribute);
  }
  assert.equal(f.submissions(), 0);
});

test('a matching replacement form cannot inherit prepared native credential targets', { timeout: 30000 }, async t => {
  const f = await fixture(t), values = request(f.refs);
  const capsule = await prepareCredentialLogin(f.page, values);
  assert.ok(capsule);
  await f.page.locator('form').evaluate(form => form.replaceWith(form.cloneNode(true)));
  assert.equal(await performCredentialLogin(f.page, capsule, values), 'denied');
  assert.equal(f.submissions(), 0);
});

test('frame refs cannot move private credential injection into a child document', { timeout: 30000 }, async t => {
  const f = await fixture(t);
  await f.page.locator('form').evaluate(form => {
    const frame = document.createElement('iframe');
    frame.srcdoc = form.outerHTML;
    form.replaceWith(frame);
  });
  const frame = f.page.frames()[1];
  await frame.locator('button').waitFor();
  const tree = await f.page.locator(':root').ariaSnapshotJSON({ mode: 'ai' });
  const nodes = tree.flatMap(function flatten(node) {
    return typeof node === 'string' ? [] : [node, ...(node.children || []).flatMap(flatten)];
  });
  const refs = Object.fromEntries([['username_ref', 'Account'], ['password_ref', 'Password'], ['submit_ref', 'Sign in']]
    .map(([key, name]) => [key, nodes.find(node => node.name === name).ref]));
  assert.match(refs.username_ref, /^f\d+e\d+$/);
  assert.equal(await prepareCredentialLogin(f.page, request(refs)), null);
  assert.equal(f.submissions(), 0);
});
