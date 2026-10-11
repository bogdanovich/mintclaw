'use strict';

// Test-only synthetic endpoint. Production worker.cjs always calls launch.cjs;
// it has no fixture mode or configurable module/command loader.
const net = require('node:net');
const { chromium } = require('../playwright-library/node_modules/playwright');
const { qualifySandbox } = require('./sandbox.cjs');
const { createSession } = require('./session.cjs');
const { startChannel } = require('./channel.cjs');
const origin = 'https://login.example.org';
const username = 'synthetic-channel-user@example.org', password = 'SYNTHETIC_CHANNEL_PASSWORD_v1';
const cookie = 'SYNTHETIC_CHANNEL_COOKIE_v1';
const stream = new net.Socket({ fd: 3, readable: true, writable: true });
let browser;
startChannel(stream, async config => {
  browser = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true, timeout: 30000 });
  if (process.platform === 'linux') {
    const context = await browser.newContext({ acceptDownloads: false, serviceWorkers: 'block' });
    try { await qualifySandbox(await context.newPage()); } finally { await context.close(); }
  }
  return {
    session: async (lease, flow, origins) => {
      const context = await browser.newContext({ acceptDownloads: false, serviceWorkers: 'block' });
      await context.route('**/*', async route => {
        try {
          const url = new URL(route.request().url());
          if (url.origin !== origin) throw new Error();
          if (url.pathname === '/login') {
            await route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html>' +
              '<form id="login" method="post" action="/session"><input id="username" name="username" type="email">' +
              '<input id="password" name="password" type="password"></form>' });
          } else if (url.pathname === '/session') {
            const values = new URLSearchParams(route.request().postData());
            if (values.get('username') !== username || values.get('password') !== password) throw new Error();
            const destination = config.executablePath === '/synthetic/mfa' ? flow.handoff_url : flow.success_url;
            await route.fulfill({ status: 200, contentType: 'text/html',
              headers: { 'set-cookie': `auth=${cookie}; Secure; HttpOnly; SameSite=Strict` },
              body: `<script>location.replace(${JSON.stringify(destination)})</script>` });
          } else {
            const marker = url.pathname === '/mfa' ? 'mfa-required' : 'authenticated';
            await route.fulfill({ status: 200, contentType: 'text/html', body:
              `<div id="${marker}">State</div><p>${password} ${Buffer.from(password).toString('base64')} ${cookie}</p>` });
          }
        } catch (_) { await route.abort().catch(() => {}); await context.close().catch(() => {}); }
      });
      const session = createSession(context, lease, flow, origins);
      let checks = 0;
      return Object.freeze({ ...session, ready: async () => {
        checks++;
        if ((config.executablePath === '/synthetic/stale-before' && checks === 1) ||
            (config.executablePath === '/synthetic/stale-after' && checks === 2)) {
          await context.pages()[0].evaluate(() => document.getElementById('password').remove());
        }
        return session.ready();
      } });
    },
    close: () => browser.close(),
  };
});
// No diagnostic text, browser artifacts, retained profile or account material
// is emitted. The Go fixture verifies and terminates this exact child process.
