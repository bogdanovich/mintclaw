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
let stage = 'start';

async function driver() {
  const instance = new Driver({ browser: 'chromium', headless: true });
  await instance.start();
  await instance.context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== origin) { await route.abort(); return; }
    if (url.pathname === '/session') {
      const fields = new URLSearchParams(request.postData());
      assert.equal(fields.get('username') === username && fields.get('password') === password, true);
      await route.fulfill({ contentType: 'text/html',
        headers: { 'set-cookie': `session=${cookie}; Secure; HttpOnly; Path=/` },
        body: '<script>location.replace("/home")</script>' });
    } else if (url.pathname === '/home') {
      await route.fulfill({ contentType: 'text/html',
        body: `<title>${cookie}</title><div id="authenticated">Ready</div><p>${username} ${password} ${cookie}</p>` });
    } else {
      await route.fulfill({ contentType: 'text/html', body: '<form method="post" action="/session">' +
        '<input name="username" type="email" aria-label="Account">' +
        '<input name="password" type="password" aria-label="Password">' +
        '<button type="submit">Sign in</button></form>' });
    }
  });
  await instance.page.goto(origin + '/login');
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
    assert.equal(await page.locator('#accounts').innerText(), 'fixture-account');
    assert.equal(await page.locator('#duration').innerText(), '20 hours');
    await page.getByRole('button', { name: 'Approve with passkey' }).click();
    await page.getByRole('status').filter({ hasText: 'Approved until' }).waitFor();
    stage = 'login-result';
    assert.equal((await fixture('/_fixture/result')).outcome, 'logged_in');
    stage = 'reuse';
    assert.equal((await fixture('/_fixture/reuse')).outcome, 'logged_in');
    stage = 'revoke';
    await page.getByRole('button', { name: 'Revoke access' }).click();
    await page.getByRole('button', { name: 'Revoke with passkey' }).click();
    await page.getByRole('status').filter({ hasText: 'Access revoked.' }).waitFor();
    assert.equal((await fixture('/_fixture/revoked')).retired, true);
  } finally {
    await browser.close();
  }
}

(process.argv[2] === 'driver' ? driver() : phone()).catch(() => {
  process.stderr.write(`credential gateway fixture failed at ${stage}\n`);
  process.exitCode = 1;
});
