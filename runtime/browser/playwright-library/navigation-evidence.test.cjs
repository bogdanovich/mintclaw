'use strict';

const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const http = require('node:http');
const path = require('node:path');
const readline = require('node:readline');
const test = require('node:test');

test('navigation evidence distinguishes challenges from ordinary HTTP failures', { timeout: 60000 }, async t => {
  const fixtures = {
    '/normal': [200, '<title>Fixture</title><p>Ready</p>', ''],
    '/denied': [403, '<title>Access denied</title><p>Please log in</p>', ''],
    '/missing': [404, '<title>Not found</title><p>Missing inventory</p>', ''],
    '/limited': [429, '<title>Too many requests</title><p>Try later</p>', ''],
    '/cloudflare': [403, '<title>Just a moment...</title><form id="challenge-form">Verify you are human</form>', 'cloudflare'],
    '/header': [403, '<title>Checking</title><p>Waiting</p>', 'cloudflare'],
    '/datadome': [403, '<title>Verify access</title><iframe src="/captcha-delivery.com"></iframe>', 'datadome'],
    '/perimeterx': [403, '<title>Access to this page has been denied</title><div id="px-captcha">Press and hold</div>', 'perimeterx'],
    '/imperva': [403, '<title>Access denied</title><iframe src="/_Incapsula_Resource"></iframe>', 'imperva'],
    '/login': [200, '<title>Login</title><form><input type="password"><div id="px-captcha"></div></form>', ''],
  };
  const cookieGateRequests = [];
  const server = http.createServer((request, response) => {
    if (request.url === '/cookie-gate') {
      const admitted = /(?:^|; )navigation_ready=public-fixture(?:;|$)/.test(request.headers.cookie || '');
      cookieGateRequests.push({method: request.method, admitted});
      response.writeHead(admitted ? 200 : 403, {
        'content-type': 'text/html',
        'set-cookie': 'navigation_ready=public-fixture; Path=/; HttpOnly; SameSite=Lax',
      });
      response.end(admitted
        ? '<title>Public homepage</title><a href="/login">Sign in</a>'
        : '<title>Error Page</title><p>Something went wrong</p>');
      return;
    }
    const fixture = fixtures[request.url] || [200, '<p>Frame</p>', ''];
    response.writeHead(fixture[0], {
      'content-type': 'text/html', ...(request.url === '/header' ? { 'cf-mitigated': 'challenge' } : {}),
    });
    response.end(fixture[1]);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  const args = [path.join(__dirname, 'sidecar.cjs'), '--headless', '--browser=chromium'];
  if (process.env.MINTCLAW_BROWSER_TEST_EXECUTABLE) {
    args.push(`--executable-path=${process.env.MINTCLAW_BROWSER_TEST_EXECUTABLE}`);
  }
  const child = spawn(process.execPath, args, { stdio: ['pipe', 'pipe', 'pipe'] });
  t.after(() => child.kill('SIGKILL'));
  let stderr = '';
  child.stderr.on('data', chunk => { stderr += chunk; });
  const pending = new Map();
  const lines = readline.createInterface({ input: child.stdout });
  lines.on('line', line => {
    const result = JSON.parse(line);
    const waiter = pending.get(result.id);
    if (!waiter) return;
    pending.delete(result.id);
    if (result.error) waiter.reject(new Error(result.error));
    else waiter.resolve(result.result);
  });
  child.on('exit', code => {
    for (const waiter of pending.values()) waiter.reject(new Error(`sidecar exited ${code}: ${stderr}`));
  });
  let id = 0;
  const call = (method, params) => new Promise((resolve, reject) => {
    const requestID = ++id;
    pending.set(requestID, { resolve, reject });
    child.stdin.write(`${JSON.stringify({ id: requestID, method, params })}\n`);
  });
  const base = `http://127.0.0.1:${server.address().port}`;
  for (const [pathname, fixture] of Object.entries(fixtures)) {
    const result = await call('browser_navigate', { url: base + pathname });
    assert.equal(result.is_error, false, `${pathname}: ${JSON.stringify(result)}`);
    const content = result.content[0].text;
    const marker = content.split('\n').find(line => line.startsWith('- Navigation evidence: '));
    assert.ok(marker, `${pathname}: missing evidence`);
    const evidence = JSON.parse(marker.slice('- Navigation evidence: '.length));
    assert.deepEqual(evidence, { http_status: fixture[0], anti_bot_block: fixture[2] }, pathname);
  }
  // The production navigation driver uses CDP, not address-bar keystrokes.
  // A public error response must keep its cookies through capture and a repeat.
  const navigateCookieGate = async () => {
    const result = await call('browser_run_code_unsafe', {code: `async page => {
      const cdp = await page.context().newCDPSession(page);
      try {
        const loaded = page.waitForEvent('load', {timeout: 10000});
        await cdp.send('Page.navigate', {url: ${JSON.stringify(base + '/cookie-gate')}});
        await loaded;
      } finally { await cdp.detach(); }
      return 'navigated';
    }`});
    assert.equal(result.is_error, false, 'public navigation failed');
    const snapshot = await call('browser_snapshot', {});
    assert.equal(snapshot.is_error, false, 'public observation failed');
    return snapshot.content[0].text;
  };
  const denied = await navigateCookieGate();
  assert.match(denied, /"http_status":403/);
  const capture = await call('browser_take_screenshot', {});
  assert.equal(capture.content[0].type, 'image', 'capture must precede the repeat');
  assert.equal(capture.content[0].mime_type, 'image/png');
  const recovered = await navigateCookieGate();
  assert.match(recovered, /"http_status":200/);
  assert.match(recovered, /Public homepage/);
  assert.match(recovered, /Sign in/);
  // This test-only negative control clears only synthetic fixture cookies.
  await call('browser_run_code_unsafe', {code: 'async page => { await page.context().clearCookies(); }'});
  assert.match(await navigateCookieGate(), /"http_status":403/);
  assert.deepEqual(cookieGateRequests, [
    {method: 'GET', admitted: false}, {method: 'GET', admitted: true}, {method: 'GET', admitted: false},
  ]);
  await call('shutdown', {});
  child.stdin.end();
});
