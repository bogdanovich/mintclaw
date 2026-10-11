'use strict';

// Disposable, synthetic qualification only. No deployed launcher/role claim.
const readline = require('node:readline');
const { chromium } = require('../playwright-library/node_modules/playwright');
const { createSession } = require('./session.cjs');
const { qualifySandbox } = require('./sandbox.cjs');
const { privateArguments } = require('./launch-arguments.cjs');
let stage = 'configuration';

async function main() {
  if (process.env.MINTCLAW_SYNTHETIC_CREDENTIAL_BROWSER_FIXTURE !== '1') throw new Error();
  const lines = readline.createInterface({ input: process.stdin, crlfDelay: Infinity })[Symbol.asyncIterator]();
  const config = JSON.parse((await lines.next()).value);
  stage = 'launch';
  const browser = await chromium.launch({ channel: 'chromium', headless: true,
    chromiumSandbox: process.platform === 'linux',
    proxy: { server: config.endpoint, bypass: '<-loopback>' },
    args: privateArguments(config.pin) });
  let context, session;
  try {
    if (process.platform === 'linux') {
      const witness = await browser.newContext();
      try { await qualifySandbox(await witness.newPage()); }
      finally { await witness.close(); }
    }
    context = await browser.newContext({ acceptDownloads: false, serviceWorkers: 'block' });
    stage = 'session';
    const abort = new AbortController();
    session = await createSession(context, { signal: abort.signal, remainingMS: 60000,
      check: async metadata => { if (metadata.origin !== config.flow.entry_origin) throw new Error(); }
    }, config.flow, [config.flow.entry_origin]);
    stage = 'prepare';
    const prepared = await session.prepare();
    if (config.mode === 'bad-pin') {
      if (prepared.code === 'prepared') throw new Error();
      process.stdout.write('denied\n'); return;
    }
    if (prepared.code !== 'prepared' || (await session.ready()).code !== 'prepared') throw new Error();
    process.stdout.write('prepared\n');
    stage = 'injection';
    if ((await lines.next()).value !== 'inject') throw new Error();
    const user = Buffer.from('synthetic@example.org'), password = Buffer.from('SYNTHETIC_PRIVATE_PROXY_PASSWORD');
    try { if ((await session.inject(user, password)).code !== 'filled') throw new Error(); }
    finally { user.fill(0); password.fill(0); }
    process.stdout.write('filled\n');
    stage = 'submission';
    if ((await lines.next()).value !== 'submit') throw new Error();
    const result = await session.submit();
    if (!['logged_in', 'handoff_required'].includes(result.code)) throw new Error();
    process.stdout.write(`${result.code}\n`);
  } finally {
    if (session) await session.close();
    if (context) await context.close();
    await browser.close();
  }
}

main().catch(() => { process.stdout.write(`failed_${stage}\n`); process.stdin.destroy(); process.exitCode = 1; });
