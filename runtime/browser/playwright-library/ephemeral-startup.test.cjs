'use strict';

const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

// Startup policy runs in a dedicated process, as in production. No browser is
// launched: the existing native real-library test covers actual transport.
const bootstrap = `
  const fs = require('node:fs');
  const path = require('node:path');
  const { chromium, firefox } = require('playwright');
  const mode = process.argv[1];
  const root = process.env.TMPDIR;
  const initial = process.cwd();
  const page = { on() {} };
  const context = { route() {}, routeWebSocket() {}, on() {}, pages: () => [page], close() {} };
  let launches = 0;
  let observed = {};
  const launch = async options => {
    launches++;
    observed = {
      child_tmpdir: options.env?.TMPDIR, child_tmp: options.env?.TMP, child_temp: options.env?.TEMP,
      executable: options.executablePath, private_cwd: process.cwd() === root,
      original_cwd: process.cwd() === initial, parent_tmpdir_unchanged: process.env.TMPDIR === root,
    };
    return { newContext: () => context, close() {} };
  };
  chromium.launch = firefox.launch = launch;
  if (mode === 'different-real-uid') {
    const original = process.getuid;
    process.getuid = () => original() + 1;
  }
  if (mode === 'different-effective-uid') {
    const original = process.geteuid;
    process.geteuid = () => original() + 1;
  }
  if (mode === 'foreign-owner') {
    const original = fs.fstatSync;
    fs.fstatSync = fd => { const info = original(fd); info.uid++; return info; };
  }
  if (mode === 'replaced-directory') {
    const original = fs.openSync;
    fs.openSync = (...args) => {
      const fd = original(...args);
      if (args[0] === root) {
        fs.renameSync(root, root + '-retired');
        fs.mkdirSync(root, { mode: 0o700 });
      }
      return fd;
    };
  }
  const { Driver } = require('./sidecar.cjs');
  (async () => {
    let started = false;
    const driver = new Driver({ browser: mode === 'firefox' ? 'firefox' : 'chromium',
      isolated: mode !== 'managed', headless: true, executablePath: './synthetic-browser' });
    try { await driver.start(); started = true; } catch (_) {}
    await driver.closeBrowser();
    process.stdout.write(JSON.stringify({ started, launches, ...observed }));
  })().catch(() => process.exitCode = 1);
`;

function probe(t, mode) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'mintclaw-private-startup-')));
  t.after(() => fs.rmSync(parent, { recursive: true, force: true }));
  const root = path.join(parent, 'private-session-'.repeat(12));
  fs.mkdirSync(root, { mode: 0o700 });
  let temporary = root;
  if (mode === 'public') fs.chmodSync(root, 0o755);
  if (mode === 'symlink') {
    temporary = path.join(parent, 'alias');
    fs.symlinkSync(root, temporary);
  }
  if (mode === 'parent-symlink') {
    const alias = path.join(parent, 'parent-alias');
    fs.symlinkSync(parent, alias);
    temporary = path.join(alias, path.basename(root));
  }
  if (mode === 'file') {
    temporary = path.join(parent, 'not-directory');
    fs.writeFileSync(temporary, '', { mode: 0o600 });
  }
  if (mode === 'missing') temporary = path.join(root, 'missing');
  if (mode === 'relative') temporary = '.';
  if (mode === 'dot-alias') temporary = root + '/.';
  const env = { PATH: process.env.PATH, HOME: parent, TMPDIR: temporary };
  if (mode !== 'unmarked') env.PWTEST_SOCKETS_DIR = mode === 'mismatched' ? parent : temporary;
  const result = spawnSync(process.execPath, ['-e', bootstrap, mode], {
    cwd: __dirname, env, encoding: 'utf8', timeout: 10000,
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stderr, '');
  return JSON.parse(result.stdout);
}

test('Linux ephemeral Chromium uses the anchored private cwd without moving parent scratch',
  { skip: process.platform !== 'linux' }, t => {
    const result = probe(t, 'valid');
    assert.equal(result.started, true);
    assert.equal(result.launches, 1);
    assert.equal(result.private_cwd, true);
    assert.equal(result.parent_tmpdir_unchanged, true);
    assert.equal(result.child_tmpdir, '.');
    assert.equal(result.child_tmp, '.');
    assert.equal(result.child_temp, '.');
    assert.equal(result.executable, path.join(__dirname, 'synthetic-browser'));
  });

test('Linux native owner contract follows effective UID rather than real UID',
  { skip: process.platform !== 'linux' }, t => {
    const result = probe(t, 'different-real-uid');
    assert.equal(result.started, true);
    assert.equal(result.private_cwd, true);
    assert.equal(result.child_tmpdir, '.');
  });

for (const mode of ['public', 'symlink', 'parent-symlink', 'file', 'missing', 'relative', 'dot-alias',
  'mismatched', 'foreign-owner', 'different-effective-uid', 'replaced-directory']) {
  test('Linux private startup refuses ' + mode + ' before launching a browser',
    { skip: process.platform !== 'linux' }, t => {
      const result = probe(t, mode);
      assert.equal(result.started, false);
      assert.equal(result.launches, 0);
    });
}

for (const mode of ['managed', 'unmarked', 'firefox']) {
  test('private-cwd handling leaves ' + mode + ' startup unchanged', t => {
    const result = probe(t, mode);
    assert.equal(result.started, true);
    assert.equal(result.original_cwd, true);
    assert.equal(result.child_tmpdir, undefined);
  });
}

test('non-Linux ephemeral startup is unchanged', { skip: process.platform === 'linux' }, t => {
  const result = probe(t, 'valid');
  assert.equal(result.started, true);
  assert.equal(result.original_cwd, true);
  assert.equal(result.child_tmpdir, undefined);
});
