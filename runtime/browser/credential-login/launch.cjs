'use strict';

const fs = require('node:fs/promises');
const path = require('node:path');
const { chromium } = require('../playwright-library/node_modules/playwright');
const { createSession } = require('./session.cjs');
const { qualifySandbox } = require('./sandbox.cjs');
const { privateArguments } = require('./launch-arguments.cjs');

async function protectedExecutable(filename) {
  if (typeof filename !== 'string' || filename.length > 4096 || !path.isAbsolute(filename) ||
      path.normalize(filename) !== filename || filename.split('/').length > 17 || /[^\x21-\x7e]/.test(filename)) throw new Error();
  const components = filename.split('/').filter(Boolean);
  let current = '/';
  for (let index = 0; index <= components.length; index++) {
    const stat = await fs.lstat(current);
    if (stat.uid !== 0 || (stat.mode & 0o022) !== 0 || stat.isSymbolicLink() ||
        (index < components.length ? !stat.isDirectory() : !stat.isFile() || stat.nlink !== 1 || !(stat.mode & 0o111))) throw new Error();
    if (index < components.length) current = path.join(current, components[index]);
  }
}

async function protectedProcess() {
  if (process.platform !== 'linux' || process.getuid() === 0 || process.getgid() === 0 ||
      process.getuid() !== process.geteuid() || process.getgid() !== process.getegid() ||
      process.getgroups().some(group => group !== process.getgid())) throw new Error();
  for (const name of ['OP_SERVICE_ACCOUNT_TOKEN', 'NODE_OPTIONS', 'NODE_PATH', 'LD_PRELOAD', 'LD_LIBRARY_PATH', 'DEBUG', 'PWDEBUG'])
    if (process.env[name]) throw new Error();
  const status = await fs.readFile('/proc/self/status', 'utf8');
  if (!/^NoNewPrivs:\s+1$/m.test(status)) throw new Error();
  for (const name of ['CapInh', 'CapPrm', 'CapEff', 'CapBnd', 'CapAmb'])
    if (!new RegExp(`^${name}:\\s+0+$`, 'm').test(status)) throw new Error();
}

// Startup supplies a root-protected pinned executable and the already-qualified
// enforcing proxy. These checks and the sandbox witness do not prove process
// role, complete filesystem/network isolation, or a trusted broker connection.
// No vault/provider operation may be enabled without those additional gates.
async function launch(config) {
  let browser, stopped = false, allocated = false, closing;
  async function close() {
    if (closing) return closing;
    stopped = true;
    closing = Promise.resolve().then(async () => {
      if (!browser) return;
      let deadline;
      try {
        await Promise.race([browser.close(), new Promise((_, reject) => {
          deadline = setTimeout(() => reject(new Error('credential browser unavailable')), 3000);
        })]);
      } finally { clearTimeout(deadline); }
    });
    return closing;
  }
  try {
    if (!config || Object.getPrototypeOf(config) !== Object.prototype ||
        Object.keys(config).sort().join(',') !== 'executablePath,proxyPin,proxyServer') throw new Error();
    const args = privateArguments(config.proxyPin);
    await protectedProcess();
    await protectedExecutable(config.executablePath);
    const proxy = new URL(config.proxyServer);
    if (proxy.protocol !== 'http:' || proxy.hostname !== '127.0.0.1' || !proxy.port ||
        Number(proxy.port) < 1024 || proxy.username || proxy.password || proxy.search || proxy.hash ||
        proxy.href !== `${config.proxyServer}/`) throw new Error();
    browser = await chromium.launch({ executablePath: config.executablePath, channel: 'chromium', chromiumSandbox: true, headless: true,
      timeout: 15000, env: { PATH: '/usr/bin:/bin', LANG: 'C.UTF-8', TZ: 'UTC' },
      proxy: { server: config.proxyServer, bypass: '<-loopback>' },
      args });
    const witness = await browser.newContext({ acceptDownloads: false, serviceWorkers: 'block' });
    try {
      const page = await witness.newPage();
      await qualifySandbox(page);
    } finally { await witness.close(); }
    browser.on('disconnected', () => { stopped = true; });
    return Object.freeze({
      session: async (lease, flow, origins) => {
        if (stopped || allocated || !browser.isConnected()) throw new Error('credential browser unavailable');
        allocated = true;
        let context;
        try {
          context = await browser.newContext({ acceptDownloads: false, serviceWorkers: 'block' });
          return createSession(context, lease, flow, origins);
        } catch (_) {
          if (context) await context.close().catch(() => {});
          await close().catch(() => {});
          throw new Error('credential browser unavailable');
        }
      },
      close: async () => {
        try { await close(); }
        catch (_) { throw new Error('credential browser unavailable'); }
      },
    });
  } catch (_) {
    await close().catch(() => {});
    throw new Error('credential browser unavailable');
  }
}

module.exports = { launch };
