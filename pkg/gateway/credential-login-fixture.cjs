'use strict';

// Synthetic, opt-in gateway qualification. Neither mode accesses real sites,
// tokens or passkeys. All failures intentionally report only a fixed stage.
const assert = require('node:assert/strict');
const http = require('node:http');
const { Driver, runProtocol } = require('../../runtime/browser/playwright-library/sidecar.cjs');
const { chromium } = require('../../runtime/browser/playwright-library/node_modules/playwright');
const origin = 'https://93.184.216.34';
const username = 'gateway-private-account@example.org';
const password = 'gateway-private-password-Q68';
const cookie = 'gateway-private-cookie-K82';
const oneTimeCode = 'gateway-private-otp-R49';
let stage = 'start';

async function driver() {
  const outcome = process.argv[3] || 'logged_in';
  assert.equal(['logged_in', 'handoff_required', 'outcome_unknown', 'discovery'].includes(outcome), true);
  const discovery = outcome === 'discovery';
  const siteOrigin = discovery ? 'https://example.org' : origin;
  const computedCode = '19482673';
  const instance = new Driver({ browser: 'chromium', headless: true });
  await instance.start();
  if (discovery) await instance.context.addCookies([{name: 'ui_flag', value: '1', url: siteOrigin}]);
  let submissions = 0;
  await instance.context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== siteOrigin) { await route.abort(); return; }
    if (discovery && url.pathname === '/totp') {
      const fields = new URLSearchParams(request.postData());
      assert.equal(fields.get('code') === computedCode, true);
      await route.fulfill({contentType: 'text/html', body: `<script>alert("${computedCode}");location.replace("/home")</script>`,
        headers: {'set-cookie': `mfa=${computedCode}; Secure; HttpOnly; Path=/`}});
      return;
    }
    if (url.pathname === '/session') {
      assert.equal(++submissions, 1);
      const fields = new URLSearchParams(request.postData());
      assert.equal(fields.get('username') === username && fields.get('password') === password, true);
      const prefix = '--fixture-submission-checkpoint=';
      const checkpointArgument = process.argv.find(value => value.startsWith(prefix));
      if (checkpointArgument) {
        const backend = new URL(checkpointArgument.slice(prefix.length));
        assert.equal(backend.hostname, '127.0.0.1');
        const checkpoint = await fetch(`${backend.origin}/_fixture/submitted`, { method: 'POST' });
        assert.equal(checkpoint.status, 200);
        if ((await checkpoint.json()).hold) await new Promise(() => {});
      }
      const destination = outcome === 'logged_in' ? '/home' :
        outcome === 'handoff_required' || discovery ? '/challenge' : '/unconfirmed';
      await route.fulfill({ contentType: 'text/html',
        headers: { 'set-cookie': `session=${cookie}; Secure; HttpOnly; Path=/` },
        body: `<script>location.replace("${destination}")</script>` });
    } else if (['/home', '/challenge', '/unconfirmed'].includes(url.pathname)) {
      const marker = url.pathname === '/home' ? 'authenticated' : url.pathname === '/challenge' ? 'challenge' : 'unconfirmed';
      const challenge = url.pathname === '/challenge' ? discovery ?
        '<form method="post" action="/totp"><input name="code" aria-label="Verification code" autocomplete="one-time-code">' +
        '<button>Verify</button></form>' :
        `<input aria-label="Verification code" autocomplete="one-time-code" value="${oneTimeCode}">` : '';
      const logout = discovery && url.pathname === '/home' ? `<button type="button">Log out</button><p>${computedCode}</p>` : '';
      await route.fulfill({ contentType: 'text/html',
        body: `<title>${cookie}</title><div id="${marker}">Ready</div><p>${username} ${password} ${cookie}</p>${challenge}${logout}` });
    } else {
      const submit = discovery ?
        '<div role="button" tabindex="0" onclick="this.closest(\'form\').requestSubmit()">Sign in</div>' :
        '<button type="submit">Sign in</button>';
      const resetPassword = discovery ? '<script>const user = document.querySelector("input[name=username]");' +
        'for (const event of ["input", "change"]) user.addEventListener(event, () => {' +
        'document.querySelector("input[type=password]").value = "";});</script>' : '';
      await route.fulfill({ contentType: 'text/html', body: '<form method="post" action="/session">' +
        '<input name="username" type="email" aria-label="Account">' +
        '<input name="password" type="password" aria-label="Password">' +
        submit + '</form>' + resetPassword });
    }
  });
  await instance.page.goto(siteOrigin + '/login');
  process.stderr.write(`${username} ${password} ${cookie}\n`);
  await runProtocol(instance, process.stdin, process.stdout);
}

async function phone() {
  let input = '';
  for await (const chunk of process.stdin) input += chunk;
  const config = JSON.parse(input), backend = new URL(config.backend);
  assert.equal(backend.hostname, '127.0.0.1');
  const request = (pathname, method = 'POST', headers = {}, body = '{}') => new Promise((resolve, reject) => {
    const outgoing = http.request({ hostname: backend.hostname, port: backend.port, path: pathname, method,
      headers: { ...headers, host: 'approval.example.org', 'x-forwarded-proto': 'https' } }, response => {
      const chunks = [];
      response.on('data', chunk => chunks.push(chunk));
      response.on('end', () => resolve({ status: response.statusCode, headers: response.headers,
        body: Buffer.concat(chunks) }));
      response.on('error', reject);
    });
    outgoing.setTimeout(60000, () => outgoing.destroy());
    outgoing.on('error', reject);
    outgoing.end(body);
  });
  const fixture = async path => {
    const response = await request(path);
    assert.equal(response.status, 200);
    return JSON.parse(response.body.toString());
  };
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext({ serviceWorkers: 'block' });
    await context.route('**/*', async route => {
      const incoming = route.request(), url = new URL(incoming.url());
      assert.equal(url.origin, 'https://approval.example.org');
      const response = await request(url.pathname, incoming.method(), await incoming.allHeaders(), incoming.postDataBuffer());
      const headers = { ...response.headers };
      delete headers['transfer-encoding'];
      await route.fulfill({ status: response.status, headers, body: response.body });
    });
    const page = await context.newPage(), cdp = await context.newCDPSession(page);
    await cdp.send('WebAuthn.enable');
    await cdp.send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', transport: 'internal',
      hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });
    stage = 'enroll';
    await page.goto(config.invite);
    await page.getByRole('button', { name: 'Create approval passkey' }).click();
    await page.getByRole('status').filter({ hasText: 'Passkey enrolled.' }).waitFor();
    stage = 'login-request';
    const pending = await fixture('/_fixture/login');
    stage = 'approve';
    await page.goto(pending.link);
    await page.getByRole('button', { name: 'Approve with passkey' }).waitFor();
    if (config.discovery) assert.match(await page.locator('#accounts').innerText(), /All website-matching Login items/);
    else assert.equal(await page.locator('#accounts').innerText(), 'fixture-account');
    const duration = config.lifetime === 'expiry' ? `${10 / 60} minutes` : '20 hours';
    assert.equal(await page.locator('#duration').innerText(), duration);
    await page.getByRole('button', { name: 'Approve with passkey' }).click();
    await page.getByRole('status').filter({ hasText: 'Approved until' }).waitFor();
    stage = 'login-result';
    const firstOutcome = config.lifetime === 'cancel' ? 'caller_canceled' : config.outcome || 'logged_in';
    assert.equal((await fixture('/_fixture/result')).outcome, firstOutcome);
    if (config.restart || config.lifetime === 'expiry') {
      if (config.lifetime === 'expiry') {
        stage = 'expiry';
        assert.equal((await fixture('/_fixture/expired')).retired, true);
      }
      if (config.restart) {
        stage = 'restart';
        const restarted = await fixture('/_fixture/restart');
        assert.deepEqual(restarted, { passkey_retained: true, grant_denied: true, old_lease_denied: true, no_retrieval: true });
      }
      stage = config.restart ? 'restart-request' : 'expiry-request';
      const fresh = await fixture('/_fixture/login');
      assert.notEqual(fresh.link, pending.link);
      stage = config.restart ? 'restart-approve' : 'expiry-approve';
      await page.goto(fresh.link);
      await page.getByRole('button', { name: 'Approve with passkey' }).waitFor();
      assert.equal(await page.locator('#accounts').innerText(), 'fixture-account');
      assert.equal(await page.locator('#duration').innerText(), duration);
      await page.getByRole('button', { name: 'Approve with passkey' }).click();
      await page.getByRole('status').filter({ hasText: 'Approved until' }).waitFor();
      stage = config.restart ? 'restart-result' : 'expiry-result';
      assert.equal((await fixture('/_fixture/result')).outcome, config.outcome || 'logged_in');
    } else {
      stage = 'reuse';
      assert.equal((await fixture('/_fixture/reuse')).outcome, config.outcome || 'logged_in');
    }
    if (config.lifetime === 'expiry') {
      assert.equal((await fixture('/_fixture/expired')).retired, true);
    } else {
      stage = 'revoke';
      if (config.inflight) assert.equal((await fixture('/_fixture/inflight')).started, true);
      await page.getByRole('button', { name: 'Revoke access' }).click();
      await page.getByRole('button', { name: 'Revoke with passkey' }).click();
      await page.getByRole('status').filter({ hasText: 'Access revoked.' }).waitFor();
      assert.equal((await fixture('/_fixture/revoked')).retired, true);
    }
  } finally {
    await browser.close();
  }
}

(process.argv[2] === 'driver' ? driver() : phone()).catch(() => {
  process.stderr.write(`credential gateway fixture failed at ${stage}\n`);
  process.exitCode = 1;
});
