'use strict';
const assert = require('node:assert/strict');
const {test, before, after} = require('node:test');
const {chromium} = require('playwright');
const {prepareCredentialLogin, performCredentialLogin} = require('./credential-login.cjs');
const origin = 'https://generic-login.example.org';
const username = 'synthetic-private-website-user@example.org';
const password = 'synthetic-private-website-password-A47';
const code = '004921';
let browser;
before(async () => { browser = await chromium.launch({headless: true,
  executablePath: process.env.MINTCLAW_BROWSER_TEST_EXECUTABLE || undefined}); });
after(async () => { await browser?.close(); });

async function fixture(t, combined = false, extra = '') {
  const context = await browser.newContext();
  t.after(() => context.close());
  const page = await context.newPage();
  const posts = [];
  await context.route('**/*', async route => {
    const incoming = route.request(), url = new URL(incoming.url());
    assert.equal(url.origin === origin, true, 'private step used an unexpected destination');
    const input = new URLSearchParams(incoming.postData() || '');
    let body;
    if (url.pathname === '/login') body = combined ?
      '<form method="post" action="/combined"><input name="username" aria-label="Account">' +
      '<input name="password" type="password" aria-label="Password"><button>Sign in</button></form>' :
      '<form method="post" action="/username"><input name="username" aria-label="Account"><button>Next</button></form>';
    else {
      assert.equal(incoming.method(), 'POST');
      posts.push(url.pathname);
      if (['/username', '/combined'].includes(url.pathname)) assert.equal(input.get('username') === username, true);
      if (['/password', '/combined'].includes(url.pathname)) assert.equal(input.get('password') === password, true);
      if (url.pathname === '/username') body = '<form method="post" action="/password">' +
        '<input name="password" type="password" aria-label="Password"><button>Sign in</button></form>';
      else if (url.pathname === '/password') body = '<form method="post" action="/code">' +
        '<input name="code" autocomplete="one-time-code" aria-label="Authenticator code"><button>Verify</button></form>';
      else {
        if (url.pathname === '/code') assert.equal(input.get('code') === code, true);
        body = '<button type="button">Log out</button>';
      }
    }
    await route.fulfill({contentType: 'text/html', body: body + extra});
  });
  await page.goto(`${origin}/login`);
  return {page, posts};
}

async function values(page, step, labels) {
  const tree = await page.locator(':root').ariaSnapshotJSON({mode: 'ai'});
  const nodes = tree.flatMap(function flatten(node) {
    return typeof node === 'string' ? [] : [node, ...(node.children || []).flatMap(flatten)];
  });
  const refs = Object.fromEntries(Object.entries(labels).map(([key, name]) => [key, nodes.find(node => node.name === name)?.ref]));
  return {origin, step, ...refs, username, password: step === 'totp' ? code : password, timeout_ms: 500};
}

async function perform(page, request) {
  const capsule = await prepareCredentialLogin(page, request);
  assert.ok(capsule, 'generic step readiness unavailable');
  const result = await performCredentialLogin(page, capsule, request);
  assert.equal(request.username, ''); assert.equal(request.password, '');
  return result;
}

test('combined generic form reports submission and verifies a fresh logout control', async t => {
  const f = await fixture(t, true);
  const request = await values(f.page, 'credentials', {username_ref: 'Account', password_ref: 'Password', submit_ref: 'Sign in'});
  assert.equal(await perform(f.page, request), 'submitted');
  assert.deepEqual(f.posts, ['/combined']);
  assert.equal(await perform(f.page, await values(f.page, 'verify', {authenticated_ref: 'Log out'})), 'logged_in');
  assert.deepEqual(f.posts, ['/combined'], 'verification must not submit again');
});

test('separate username, password and authenticator steps each submit once', async t => {
  const f = await fixture(t);
  assert.equal(await perform(f.page, await values(f.page, 'username', {username_ref: 'Account', submit_ref: 'Next'})), 'submitted');
  assert.equal(await perform(f.page, await values(f.page, 'password', {password_ref: 'Password', submit_ref: 'Sign in'})), 'submitted');
  assert.equal(await perform(f.page, await values(f.page, 'totp', {otp_ref: 'Authenticator code', submit_ref: 'Verify'})), 'submitted');
  assert.equal(await perform(f.page, await values(f.page, 'verify', {authenticated_ref: 'Log out'})), 'logged_in');
  assert.deepEqual(f.posts, ['/username', '/password', '/code']);
});

test('generic GET forms and wrong verification controls are rejected before retrieval', async t => {
  const f = await fixture(t, true);
  const request = await values(f.page, 'credentials', {username_ref: 'Account', password_ref: 'Password', submit_ref: 'Sign in'});
  await f.page.locator('form').evaluate(form => { form.method = 'get'; });
  assert.equal(await prepareCredentialLogin(f.page, request), null);
  const wrong = await values(f.page, 'verify', {authenticated_ref: 'Sign in'});
  assert.equal(await prepareCredentialLogin(f.page, wrong), null);
  assert.equal(f.posts.length, 0);
});

test('changed elements and post-fill destination changes do not submit', async t => {
  for (const mode of ['replace', 'destination']) {
    const f = await fixture(t, true);
    const request = await values(f.page, 'credentials', {username_ref: 'Account', password_ref: 'Password', submit_ref: 'Sign in'});
    const capsule = await prepareCredentialLogin(f.page, request);
    assert.ok(capsule);
    if (mode === 'replace') await f.page.locator('input[type=password]').evaluate(input => input.replaceWith(input.cloneNode()));
    else await f.page.locator('input[type=password]').evaluate(input => {
      input.addEventListener('input', () => { input.form.action = 'https://other.example.org/session'; });
    });
    assert.equal(await performCredentialLogin(f.page, capsule, request), 'outcome_unknown');
    assert.equal(f.posts.length, 0);
    assert.equal(f.page.isClosed(), true);
  }
});

test('TOTP step rejects a seed string before filling or submitting', async t => {
  const f = await fixture(t);
  await f.page.setContent('<form method="post"><input aria-label="Authenticator code"><button>Verify</button></form>');
  const request = await values(f.page, 'totp', {otp_ref: 'Authenticator code', submit_ref: 'Verify'});
  request.password = 'otpauth://totp/private?secret=SYNTHETIC_SEED';
  assert.equal(await perform(f.page, request), 'denied');
  assert.equal(await f.page.locator('input').inputValue(), '');
  assert.equal(f.posts.length, 0);
});

test('JavaScript button flow supports a code step without a native form', async t => {
  const f = await fixture(t);
  await f.page.setContent('<input aria-label="Authenticator code" autocomplete="one-time-code">' +
    '<button type="button" onclick="document.body.innerHTML = document.querySelector(\'input\').value === \'004921\' ? ' +
    '\'<button type=button>Log out</button>\' : \'Failed\'">Continue</button>');
  const request = await values(f.page, 'totp', {otp_ref: 'Authenticator code', submit_ref: 'Continue'});
  assert.equal(await perform(f.page, request), 'submitted');
  assert.equal(await perform(f.page, await values(f.page, 'verify', {authenticated_ref: 'Log out'})), 'logged_in');
});

test('post-login redirects admit sibling hosts only within the trusted registrable domain', async t => {
  for (const destination of ['https://accounts.example.org/home', 'https://other.example.net/home']) {
    const context = await browser.newContext();
    t.after(() => context.close());
    const page = await context.newPage();
    let submissions = 0;
    await context.route('**/*', async route => {
      const url = new URL(route.request().url());
      if (url.origin === origin && url.pathname === '/login') {
        await route.fulfill({contentType: 'text/html', body: '<form method="post" action="/session">' +
          '<input aria-label="Account"><input type="password" aria-label="Password"><button>Sign in</button></form>'});
      } else if (url.origin === origin && url.pathname === '/session') {
        submissions++;
        await route.fulfill({contentType: 'text/html', body: `<script>location.replace("${destination}")</script>`});
      } else if (url.href === destination) {
        await route.fulfill({contentType: 'text/html', body: '<button type="button">Log out</button>'});
      } else await route.abort();
    });
    await page.goto(`${origin}/login`);
    const request = await values(page, 'credentials', {username_ref: 'Account', password_ref: 'Password', submit_ref: 'Sign in'});
    request.website_domain = 'example.org'; // Derived by the Go broker from its live, PSL-checked snapshot.
    assert.equal(await perform(page, request), destination.includes('accounts.example.org') ? 'submitted' : 'outcome_unknown');
    assert.equal(submissions, 1);
    assert.equal(page.isClosed(), destination.includes('example.net'));
  }
});

test('generic steps reject controls hidden after their snapshot', async t => {
  for (const style of ['visibility:hidden', 'opacity:0', 'display:none', 'content-visibility:hidden']) {
    const f = await fixture(t, true);
    const request = await values(f.page, 'credentials', {
      username_ref: 'Account', password_ref: 'Password', submit_ref: 'Sign in',
    });
    await f.page.locator('form').evaluate((form, css) => { form.style.cssText = css; }, style);
    assert.equal(await prepareCredentialLogin(f.page, request) === null, true, style);
    assert.equal(await f.page.locator('input[type=password]').inputValue(), '');
    assert.equal(f.posts.length, 0);
  }
});

test('verification rejects logout controls hidden before preparation or completion', async t => {
  for (const style of ['visibility:hidden', 'opacity:0', 'display:none', 'content-visibility:hidden']) {
    for (const phase of ['prepare', 'complete']) {
      const f = await fixture(t, true);
      await f.page.setContent('<div><button type="button" aria-label="Log out">Log out</button></div>');
      const request = await values(f.page, 'verify', {authenticated_ref: 'Log out'});
      const capsule = phase === 'complete' ? await prepareCredentialLogin(f.page, request) : null;
      if (phase === 'complete') assert.ok(capsule);
      await f.page.locator('div').evaluate((parent, css) => { parent.style.cssText = css; }, style);
      if (phase === 'prepare') assert.equal(await prepareCredentialLogin(f.page, request) === null, true, style);
      else assert.equal(await performCredentialLogin(f.page, capsule, request), 'unverified', style);
      assert.equal(f.posts.length, 0);
    }
  }
});
