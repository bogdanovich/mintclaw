#!/usr/bin/env node
'use strict';

// This process is a private MintClaw driver. It intentionally implements a
// small JSON-lines protocol instead of MCP and never discovers or publishes
// tools to an agent.

const readline = require('node:readline');
const fs = require('node:fs');
const path = require('node:path');
const dns = require('node:dns').promises;
const net = require('node:net');
const { randomBytes } = require('node:crypto');
const { prepareCredentialLogin, performCredentialLogin } = require('./credential-login.cjs');
const { Worker } = require('node:worker_threads');
const { chromium, firefox, webkit } = require('playwright');

const MAX_REQUEST_BYTES = 8 * 1024 * 1024;
const MAX_ERROR_BYTES = 2048;
const MAX_IMAGE_EVIDENCE = 32;

const SPECIAL_IPV4_PREFIXES = [
  ['0.0.0.0', 8], ['10.0.0.0', 8], ['100.64.0.0', 10], ['127.0.0.0', 8],
  ['168.63.129.16', 32], ['169.254.0.0', 16], ['172.16.0.0', 12],
  ['192.0.0.0', 24], ['192.0.2.0', 24], ['192.31.196.0', 24],
  ['192.52.193.0', 24], ['192.88.99.0', 24], ['192.168.0.0', 16],
  ['192.175.48.0', 24], ['198.18.0.0', 15], ['198.51.100.0', 24],
  ['203.0.113.0', 24], ['224.0.0.0', 3],
];

const SPECIAL_IPV6_PREFIXES = [
  ['::', 128], ['::1', 128], ['64:ff9b::', 96], ['64:ff9b:1::', 48],
  ['100::', 64], ['100:0:0:1::', 64], ['2001::', 23], ['2001:db8::', 32],
  ['2002::', 16], ['2620:4f:8000::', 48], ['3fff::', 20], ['5f00::', 16],
  ['fc00::', 7], ['fe80::', 10], ['ff00::', 8],
];

function ipv4Number(value) {
  const parts = String(value).split('.');
  if (parts.length !== 4 || parts.some(part => !/^\d{1,3}$/.test(part))) return null;
  const octets = parts.map(Number);
  if (octets.some(part => part < 0 || part > 255)) return null;
  return octets.reduce((result, part) => ((result << 8) | part) >>> 0, 0) >>> 0;
}

function ipv6Number(value) {
  let input = String(value).toLowerCase().replace(/^\[|\]$/g, '');
  if (input.includes('.')) {
    const separator = input.lastIndexOf(':');
    const ipv4 = separator >= 0 ? ipv4Number(input.slice(separator + 1)) : null;
    if (ipv4 === null) return null;
    input = `${input.slice(0, separator)}:${(ipv4 >>> 16).toString(16)}:${(ipv4 & 0xffff).toString(16)}`;
  }
  if ((input.match(/::/g) || []).length > 1) return null;
  const halves = input.split('::');
  const left = halves[0] ? halves[0].split(':') : [];
  const right = halves.length === 2 && halves[1] ? halves[1].split(':') : [];
  const missing = 8 - left.length - right.length;
  if (missing < 0 || (halves.length === 1 && missing !== 0)) return null;
  const groups = [...left, ...Array(missing).fill('0'), ...right];
  if (groups.length !== 8 || groups.some(group => !/^[0-9a-f]{1,4}$/.test(group))) return null;
  return groups.reduce((result, group) => (result << 16n) | BigInt(parseInt(group, 16)), 0n);
}

function prefixContains(address, network, bits, width) {
  const shift = BigInt(width - bits);
  return (address >> shift) === (network >> shift);
}

function publicIPAddress(value) {
  const version = net.isIP(String(value).replace(/^\[|\]$/g, ''));
  if (version === 4) {
    const address = ipv4Number(value);
    return address !== null && !SPECIAL_IPV4_PREFIXES.some(([network, bits]) =>
      prefixContains(BigInt(address), BigInt(ipv4Number(network)), bits, 32));
  }
  if (version !== 6) return false;
  const address = ipv6Number(value);
  if (address === null) return false;
  const mappedPrefix = ipv6Number('::ffff:0:0');
  if (prefixContains(address, mappedPrefix, 96, 128)) {
    return publicIPAddress([
      Number((address >> 24n) & 255n), Number((address >> 16n) & 255n),
      Number((address >> 8n) & 255n), Number(address & 255n),
    ].join('.'));
  }
  return !SPECIAL_IPV6_PREFIXES.some(([network, bits]) =>
    prefixContains(address, ipv6Number(network), bits, 128));
}

function networkURL(raw, websocket = false) {
  let parsed;
  try { parsed = new URL(String(raw)); } catch (_) { return null; }
  const protocols = websocket ? new Set(['ws:', 'wss:']) : new Set(['http:', 'https:']);
  if (!protocols.has(parsed.protocol) || parsed.username || parsed.password || !parsed.hostname) return null;
  const originProtocol = parsed.protocol === 'ws:' ? 'http:' : parsed.protocol === 'wss:' ? 'https:' : parsed.protocol;
  const origin = `${originProtocol}//${parsed.host}`;
  return { parsed, origin, hostname: parsed.hostname.replace(/^\[|\]$/g, '') };
}

function parseArguments(argv) {
  const options = {
    browser: 'chromium', executablePath: '', userDataDir: '', proxyServer: '',
    proxyBypass: '', outputDir: '', headless: false, isolated: false,
    privilegedExecution: false,
    cdpEndpoint: '', networkMode: '', allowedOrigins: [], remoteRuntime: false,
  };
  const values = new Set([
    'browser', 'executable-path', 'user-data-dir', 'proxy-server',
    'proxy-bypass', 'output-dir', 'config', 'allowed-origins', 'caps',
    'output-mode',
  ]);
  for (let index = 0; index < argv.length; index++) {
    let raw = argv[index];
    if (!raw.startsWith('--')) throw new Error('unsupported positional argument');
    raw = raw.slice(2);
    const equals = raw.indexOf('=');
    const name = equals < 0 ? raw : raw.slice(0, equals);
    let value = equals < 0 ? '' : raw.slice(equals + 1);
    const property = name.replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
    if (name === 'headless' || name === 'isolated' || name === 'privileged-execution') {
      if (equals >= 0) throw new Error('boolean argument has a value');
      options[property] = true;
      continue;
    }
    if (!values.has(name)) throw new Error('unsupported driver argument');
    if (equals < 0) {
      if (++index >= argv.length || argv[index].startsWith('--')) {
        throw new Error('driver argument is missing a value');
      }
      value = argv[index];
    }
    options[property] = value;
  }
  if (options.isolated && options.userDataDir) {
    throw new Error('isolated and user-data-dir are mutually exclusive');
  }
  const privateEndpoint = String(process.env.MINTCLAW_PLAYWRIGHT_CDP_ENDPOINT || '');
  const privateMode = String(process.env.MINTCLAW_PLAYWRIGHT_NETWORK_MODE || '');
  const privateOrigins = String(process.env.MINTCLAW_PLAYWRIGHT_ALLOWED_ORIGINS || '');
  const remoteRuntime = String(process.env.MINTCLAW_PLAYWRIGHT_REMOTE_RUNTIME || '') === '1';
  if (remoteRuntime) {
    let parsedEndpoint;
    let parsedOrigins;
    try {
      parsedEndpoint = new URL(privateEndpoint);
      parsedOrigins = JSON.parse(privateOrigins);
    } catch (_) {
      throw new Error('invalid private remote runtime configuration');
    }
    if (parsedEndpoint.protocol !== 'wss:' || !parsedEndpoint.hostname ||
        parsedEndpoint.username || parsedEndpoint.password || parsedEndpoint.hash ||
        !['exact_origins', 'public_web', 'any_http'].includes(privateMode) ||
        !Array.isArray(parsedOrigins) || parsedOrigins.length > 64 ||
        (privateMode === 'exact_origins' && parsedOrigins.length === 0) ||
        (privateMode !== 'exact_origins' && parsedOrigins.length !== 0) ||
        options.userDataDir || options.isolated || options.executablePath || options.browser !== 'chromium') {
      throw new Error('invalid private remote runtime configuration');
    }
    const normalized = [];
    for (const raw of parsedOrigins) {
      const parsed = networkURL(raw);
      if (!parsed || parsed.origin !== raw || parsed.parsed.pathname !== '/' ||
          parsed.parsed.search || parsed.parsed.hash || normalized.includes(parsed.origin)) {
        throw new Error('invalid private remote runtime configuration');
      }
      normalized.push(parsed.origin);
    }
    options.cdpEndpoint = privateEndpoint;
    options.networkMode = privateMode;
    options.allowedOrigins = normalized;
    options.remoteRuntime = true;
  } else if (privateEndpoint || privateMode || privateOrigins) {
    throw new Error('incomplete private remote runtime configuration');
  }
  return options;
}

function boundedError(error) {
  const text = error && typeof error.message === 'string' ? error.message : 'driver operation failed';
  return text.replace(/(?:wss?|https?):\/\/[^\s"']+/gi, '[private-url]')
    .replace(/(apiKey|sessionId)=[^&\s"']+/gi, '$1=[private]')
    .slice(0, MAX_ERROR_BYTES).replace(/[\r\n]+/g, ' ');
}

function enterPrivateEphemeralDirectory(options) {
  if (process.platform !== 'linux' || !['chromium', 'chrome', 'msedge'].includes(options.browser) ||
      !options.isolated || options.remoteRuntime || options.userDataDir ||
      !process.env.PWTEST_SOCKETS_DIR) return false;
  const root = process.env.TMPDIR;
  const invalid = () => new Error('private ephemeral runtime directory is unavailable');
  if (!root || !path.isAbsolute(root) || root !== process.env.PWTEST_SOCKETS_DIR ||
      path.resolve(root) !== root || fs.realpathSync(root) !== root) throw invalid();
  const before = fs.lstatSync(root);
  const privateDirectory = info => info.isDirectory() && (info.mode & 0o777) === 0o700 &&
    info.uid === process.getuid();
  if (!privateDirectory(before)) throw invalid();
  const descriptor = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW);
  try {
    const opened = fs.fstatSync(descriptor);
    const same = info => privateDirectory(info) && info.dev === before.dev && info.ino === before.ino;
    if (!same(opened)) throw invalid();
    process.chdir(`/proc/self/fd/${descriptor}`);
    if (!same(fs.statSync('.')) || !same(fs.lstatSync(root))) throw invalid();
  } finally {
    fs.closeSync(descriptor);
  }
  return true;
}

function textResult(text, isError = false) {
  return { is_error: isError, content: [{ type: 'text', text: String(text) }] };
}

function modalText(dialog) {
  const type = String(dialog.type || 'alert');
  const message = String(dialog.message || '').replace(/[\r\n]+/g, ' ').slice(0, 4096);
  return `### Modal state\n- ["${type}" dialog with message "${message}"]: can be handled by browser_handle_dialog`;
}

function resultText(value) {
  let encoded;
  try {
    encoded = JSON.stringify(value);
  } catch (_) {
    encoded = JSON.stringify(String(value));
  }
  if (encoded === undefined) encoded = 'undefined';
  return `### Result\n${encoded}`;
}

function quoteName(value) {
  return String(value || '').replace(/[\r\n\t]+/g, ' ').replace(/\s+/g, ' ')
    .trim().slice(0, 512).replace(/"/g, "'");
}

async function writableSemanticState(locator) {
  return locator.evaluate(element => {
    const tag = String(element.tagName || '').toLowerCase();
    const type = String(element.getAttribute('type') || '').toLowerCase();
    const explicitRole = String(element.getAttribute('role') || '').trim().toLowerCase();
    const implicitRole = () => {
      if (explicitRole) return explicitRole.split(/\s+/)[0];
      if (tag === 'textarea') return 'textbox';
      if (tag === 'input') {
        if (type === 'checkbox') return 'checkbox';
        if (type === 'radio') return 'radio';
        if (['button', 'submit', 'reset', 'image', 'file'].includes(type)) return 'button';
        if (type === 'range') return 'slider';
        if (type === 'number') return 'spinbutton';
        if (type !== 'hidden') return 'textbox';
      }
      if (element.isContentEditable) return 'textbox';
      return '';
    };
    const accessibleName = () => {
      const labelledBy = String(element.getAttribute('aria-labelledby') || '').trim();
      if (labelledBy) {
        const labels = labelledBy.split(/\s+/).map(id => document.getElementById(id))
          .filter(Boolean).map(label => label.textContent || '').join(' ').trim();
        if (labels) return labels;
      }
      const ariaLabel = element.getAttribute('aria-label');
      if (ariaLabel) return ariaLabel;
      if (element.labels && element.labels.length) {
        return Array.from(element.labels).map(label => label.textContent || '').join(' ').trim();
      }
      return element.getAttribute('placeholder') || element.getAttribute('title') || '';
    };
    const nonFillTypes = new Set([
      'hidden', 'checkbox', 'radio', 'file', 'submit', 'button', 'reset', 'image', 'range', 'color',
    ]);
    const ariaDisabled = String(element.getAttribute('aria-disabled') || '').toLowerCase().trim();
    const ariaReadOnly = String(element.getAttribute('aria-readonly') || '').toLowerCase().trim();
    const writable = ((tag === 'input' && !nonFillTypes.has(type)) || tag === 'textarea' ||
      element.isContentEditable) && !element.disabled && !element.matches(':disabled') &&
      !element.readOnly && (ariaDisabled === '' || ariaDisabled === 'false') &&
      (ariaReadOnly === '' || ariaReadOnly === 'false');
    return { tag, type, role: implicitRole(), name: accessibleName(), writable };
  });
}

function sameWritableSemantics(before, after) {
  return before && after && before.writable && after.writable && before.tag === after.tag &&
    before.type === after.type && before.role === after.role && before.name === after.name;
}

function snapshotString(value) {
  // Page-authored text is never a reference token in the private wire format.
  return JSON.stringify(String(value)).replace(/\[ref=/g, '\\u005bref=');
}

function renderAriaNodes(nodes, indent = '') {
  const lines = [];
  for (const node of nodes) {
    if (typeof node === 'string') {
      lines.push(`${indent}- text: ${snapshotString(node)}`);
      continue;
    }
    // Only serialize semantics supplied by Playwright. Do not reconstruct
    // roles, accessible names, or element bindings from HTML tags or captions.
    let line = `${indent}- ${node.role}`;
    if (node.name) line += ` ${snapshotString(node.name)}`;
    for (const state of ['checked', 'disabled', 'expanded', 'active', 'invalid', 'level', 'pressed', 'selected']) {
      if (node[state] !== undefined) {
        line += node[state] === true ? ` [${state}]` : ` [${state}=${node[state]}]`;
      }
    }
    if (node.ref) line += ` [ref=${node.ref}]`;
    if (node.cursor) line += ` [cursor=${node.cursor}]`;
    if (node.text) line += `: ${snapshotString(node.text)}`;
    else if (node.children && node.children.length) line += ':';
    lines.push(line);
    if (node.url) lines.push(`${indent}  - /url: ${snapshotString(node.url)}`);
    if (node.placeholder) lines.push(`${indent}  - /placeholder: ${snapshotString(node.placeholder)}`);
    if (node.children) lines.push(...renderAriaNodes(node.children, indent + '  '));
  }
  return lines;
}

function imageEvidenceScript(root, maximum) {
  const clean = value => String(value || '').replace(/\s+/g, ' ').trim().slice(0, 160);
  const images = [...(root.matches('img') ? [root] : []), ...root.querySelectorAll('img')];
  const items = images.slice(0, maximum).map((image, index) => {
    const box = image.getBoundingClientRect();
    const rendered = image.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true }) &&
      box.width > 0 && box.height > 0;
    const inViewport = rendered && box.right > 0 && box.bottom > 0 &&
      box.left < innerWidth && box.top < innerHeight;
    const x = Math.max(0, Math.min(innerWidth - 1, box.left + box.width / 2));
    const y = Math.max(0, Math.min(innerHeight - 1, box.top + box.height / 2));
    const hit = inViewport ? document.elementFromPoint(x, y) : null;
    const context = [];
    for (let ancestor = image.parentElement; ancestor && context.length < 3; ancestor = ancestor.parentElement) {
      const label = ancestor.getAttribute('aria-label');
      const caption = ancestor.tagName === 'FIGURE' && ancestor.querySelector('figcaption');
      if (label) context.push(clean(label));
      else if (caption) context.push(clean(caption.textContent));
    }
    let sourceOrigin = '';
    try { sourceOrigin = new URL(image.currentSrc || image.src, document.baseURI).origin; } catch (_) {}
    return {
      index: index + 1, alt: clean(image.getAttribute('alt')), context,
      source_origin: sourceOrigin, loaded: image.complete && image.naturalWidth > 0,
      natural_width: image.naturalWidth, natural_height: image.naturalHeight,
      rendered, in_viewport: inViewport, receives_pointer: inViewport ? hit === image || image.contains(hit) : null,
    };
  });
  return { scope: 'frame_light_dom', dom_image_count: images.length, omitted: images.length - items.length, items };
}

class Driver {
  constructor(options) {
    this.options = options;
    this.browser = null;
    this.context = null;
    this.page = null;
    this.closed = false;
    this.pendingDialog = null;
    this.pendingFileChooser = null;
    this.blockedAction = null;
    this.dialogWaiters = new Set();
    this.executionActive = false;
    this.executionQuarantined = false;
    this.executionNetworkGuards = new Set();
    this.remoteDNSCache = new Map();
    this.navigationResponses = new WeakMap();
    this.credentialPending = null;
    this.credentialActive = false;
    this.credentialBound = false;
    this.credentialCookies = new Set();
    this.credentialCookieReads = new Set();
    this.credentialCookieFailure = false;
  }

  async start() {
    const browserType = { chromium, firefox, webkit }[this.options.browser] ||
      (this.options.browser === 'chrome' || this.options.browser === 'msedge' ? chromium : null);
    if (!browserType) throw new Error('unsupported browser');
    const launch = { headless: this.options.headless };
    if (this.options.executablePath) launch.executablePath = path.resolve(this.options.executablePath);
    if (enterPrivateEphemeralDirectory(this.options)) {
      // Chromium needs a short socket spelling; all bytes still live in the
      // existing private cwd. Playwright's own scratch keeps its absolute root.
      launch.env = { ...process.env, TMPDIR: '.', TMP: '.', TEMP: '.' };
    }
    if (this.options.browser === 'chrome' || this.options.browser === 'msedge') {
      launch.channel = this.options.browser;
    }
    if (this.options.proxyServer) {
      launch.proxy = { server: this.options.proxyServer };
      if (this.options.proxyBypass) launch.proxy.bypass = this.options.proxyBypass;
    }
    const contextOptions = {
      acceptDownloads: false,
      serviceWorkers: this.options.privilegedExecution ? 'block' : 'allow',
    };
    if (this.options.remoteRuntime) {
      this.browser = await chromium.connectOverCDP(this.options.cdpEndpoint);
      const contexts = this.browser.contexts();
      if (contexts.length !== 1) throw new Error('remote browser context is unavailable');
      this.context = contexts[0];
    } else if (this.options.userDataDir) {
      this.context = await browserType.launchPersistentContext(this.options.userDataDir, {
        ...launch, ...contextOptions,
      });
    } else {
      this.browser = await browserType.launch(launch);
      this.context = await this.browser.newContext(contextOptions);
    }
    await this.context.route('**/*', async route => {
      const request = route.request();
      if (this.options.remoteRuntime && !(await this.remoteDestinationAllowed(request.url(), false))) {
        await route.abort('blockedbyclient').catch(() => {});
        return;
      }
      for (const guard of this.executionNetworkGuards) {
        if (!(await guard(request.url(), false, request))) {
          await route.abort('blockedbyclient').catch(() => {});
          return;
        }
      }
      await route.continue().catch(() => {});
    });
    await this.context.routeWebSocket(/.*/, async websocket => {
      if (this.options.remoteRuntime && !(await this.remoteDestinationAllowed(websocket.url(), true))) return;
      for (const guard of this.executionNetworkGuards) {
        if (!(await guard(websocket.url(), true, null))) return;
      }
      await websocket.connectToServer();
    });
    this.context.on('page', page => this.watchPage(page));
    for (const page of this.context.pages()) this.watchPage(page);
    this.page = this.context.pages()[0] || await this.context.newPage();
  }

  async remoteDestinationAllowed(raw, websocket = false) {
    const destination = networkURL(raw, websocket);
    if (!destination) return false;
    if (this.options.networkMode === 'exact_origins') {
      return this.options.allowedOrigins.includes(destination.origin);
    }
    if (this.options.networkMode === 'any_http') return true;
    const lower = destination.hostname.toLowerCase();
    if (lower === 'localhost' || lower.endsWith('.localhost') ||
        lower === 'metadata.google.internal' || (!lower.includes('.') && net.isIP(lower) === 0)) return false;
    if (net.isIP(lower)) return publicIPAddress(lower);
    let lookup = this.remoteDNSCache.get(lower);
    if (!lookup) {
      lookup = dns.lookup(lower, { all: true, verbatim: true }).catch(() => []);
      this.remoteDNSCache.set(lower, lookup);
    }
    const addresses = await lookup;
    return addresses.length > 0 && addresses.length <= 32 &&
      addresses.every(item => item && publicIPAddress(item.address));
  }

  watchPage(page) {
    if (page.__mintclawSidecarWatched) return;
    Object.defineProperty(page, '__mintclawSidecarWatched', { value: true });
    page.on('response', response => {
      if (this.credentialBound) this.trackCredentialCookies(response);
      const request = response.request();
      if (!request.isNavigationRequest() || request.frame() !== page.mainFrame()) return;
      this.navigationResponses.set(page, {
        url: response.url(), http_status: response.status(),
        cloudflareChallenge: response.headers()['cf-mitigated'] === 'challenge',
      });
    });
    page.on('dialog', dialog => {
      if (this.credentialActive || this.credentialPending) {
        dialog.dismiss().catch(() => {});
        return;
      }
      if (!this.pendingDialog) {
        this.pendingDialog = { handle: dialog, type: dialog.type(), message: dialog.message() };
        for (const resolve of this.dialogWaiters) resolve({ kind: 'dialog' });
        this.dialogWaiters.clear();
      } else {
        dialog.dismiss().catch(() => {});
      }
    });
  }

  dialogWaiter() {
    let resolve;
    const promise = new Promise(accept => { resolve = accept; });
    this.dialogWaiters.add(resolve);
    return { promise, cancel: () => this.dialogWaiters.delete(resolve) };
  }

  async actionOrDialog(action) {
    const tagged = Promise.resolve().then(action).then(
      value => ({ kind: 'value', value }),
      error => ({ kind: 'error', error }),
    );
    if (this.pendingDialog) {
      this.blockedAction = tagged;
      return { dialog: true };
    }
    const waiter = this.dialogWaiter();
    let outcome;
    try {
      outcome = await Promise.race([tagged, waiter.promise]);
    } finally {
      waiter.cancel();
    }
    if (outcome.kind === 'dialog' || this.pendingDialog) {
      this.blockedAction = tagged;
      return { dialog: true };
    }
    if (outcome.kind === 'error') throw outcome.error;
    return { dialog: false, value: outcome.value };
  }

  async handleDialog(args) {
    if (!this.pendingDialog) throw new Error('dialog is unavailable');
    const pending = this.pendingDialog;
    const blocked = this.blockedAction;
    this.pendingDialog = null;
    const waiter = this.dialogWaiter();
    try {
      if (args.accept) {
        await pending.handle.accept(args.promptText === undefined ? undefined : String(args.promptText));
      } else {
        await pending.handle.dismiss();
      }
      if (blocked) {
        const outcome = await Promise.race([blocked, waiter.promise]);
        if (outcome.kind === 'dialog' || this.pendingDialog) return modalText(this.pendingDialog);
        this.blockedAction = null;
        if (outcome.kind === 'error') throw outcome.error;
      } else {
        await Promise.race([
          waiter.promise,
          this.selectedPage().waitForTimeout(25).then(() => ({ kind: 'settled' })),
        ]);
      }
    } finally {
      waiter.cancel();
    }
    if (this.pendingDialog) return modalText(this.pendingDialog);
    const followUp = await this.actionOrDialog(() => this.snapshot());
    return followUp.dialog ? modalText(this.pendingDialog) : followUp.value;
  }

  selectedPage() {
    if (!this.page || this.page.isClosed()) throw new Error('selected page is unavailable');
    return this.page;
  }

  locatorFor(ref) {
    if (!/^(?:f[1-9][0-9]{0,9})?e[1-9][0-9]{0,9}$/.test(ref)) {
      throw new Error('invalid element reference');
    }
    return this.selectedPage().locator(`aria-ref=${ref}`);
  }

  async runCode(fn) {
    const page = this.selectedPage();
    try {
      return await fn(page);
    } finally {
      if (page.isClosed()) {
        const remaining = this.context.pages();
        if (remaining.length) {
          this.page = remaining[0];
        } else {
          this.page = await this.context.newPage();
          this.watchPage(this.page);
        }
      }
    }
  }

  async snapshot(target = '') {
    const page = this.selectedPage();
    if (page.url() === 'about:blank') {
      return '- Page URL: about:blank\n- Page Title: \n### Snapshot\n```yaml\n\n```';
    }
    const scope = target ? this.locatorFor(target) : page.locator(':root');
    const tree = await scope.ariaSnapshotJSON({ mode: 'ai' });
    const redactControls = async nodes => {
      await Promise.all(nodes.map(async node => {
        if (typeof node === 'string') return;
        if (node.ref && (this.credentialBound || node.text)) {
          let source = null;
          try {
            // Inspect exact native refs; never rediscover an element by its
            // caption or wait an action-sized timeout for removed sources.
            source = await this.locatorFor(node.ref).evaluate((element, bound) => {
              if (!element.isConnected) return null;
              const control = bound && (element.matches('input,textarea,select') || element.isContentEditable);
              if (control) {
                const labelText = label => {
                  const controls = 'input,textarea,select,[contenteditable],script,style';
                  if (label.matches(controls)) return '';
                  const copy = label.cloneNode(true);
                  for (const child of copy.querySelectorAll(controls)) child.remove();
                  return copy.textContent || '';
                };
                const labelledBy = (element.getAttribute('aria-labelledby') || '').trim();
                const labels = labelledBy ? labelledBy.split(/\s+/).map(id => document.getElementById(id))
                  .filter(Boolean).map(labelText).join(' ').trim() : '';
                const name = labels || element.getAttribute('aria-label') ||
                  (element.labels && Array.from(element.labels).map(labelText).join(' ').trim()) ||
                  element.getAttribute('placeholder') || element.getAttribute('title') || '';
                return { control: true, name };
              }
              return { password: element.tagName === 'INPUT' && element.type === 'password' };
            }, this.credentialBound, { timeout: 500 });
          } catch (_) {}
          if (source && source.control) {
            node.name = source.name;
            delete node.text;
            delete node.children;
          } else if (source && source.password) delete node.text;
          else if (source === null) {
            if (this.credentialBound) {
              node.name = '[source unavailable]';
              delete node.text;
              delete node.children;
            } else if (node.text) node.text = '[text withheld: source changed during observation]';
          }
        }
        if (node.children) await redactControls(node.children);
      }));
    };
    await redactControls(tree);
    const lines = renderAriaNodes(tree);
    // Unlabelled images can be meaningful even when intentionally absent from
    // ARIA. Keep a bounded, non-actionable inventory without inventing img
    // roles, product-photo counts, gallery controls, or new reference authority.
    const inventories = [];
    if (target) {
      try {
        inventories.push(await scope.evaluate(imageEvidenceScript, MAX_IMAGE_EVIDENCE, { timeout: 500 }));
      } catch (_) {
        inventories.push({ unavailable: true });
      }
    } else {
      let remaining = MAX_IMAGE_EVIDENCE;
      const frames = page.frames();
      for (const frame of frames.slice(0, 8)) {
        try {
          const inventory = await frame.locator(':root').evaluate(imageEvidenceScript, remaining, { timeout: 500 });
          remaining -= inventory.items.length;
          inventories.push(inventory);
        } catch (_) {
          inventories.push({ unavailable: true });
        }
      }
      if (frames.length > 8) inventories.push({ omitted_frames: frames.length - 8 });
    }
    if (inventories.some(item => item.dom_image_count || item.unavailable || item.omitted_frames)) {
      lines.push('- text: "DOM image evidence is not a product-photo or gallery count. Verify context and visual content; pointer dispatch alone does not confirm the requested outcome."');
      for (const [frame, inventory] of inventories.entries()) {
        lines.push(`- text: ${snapshotString('DOM image evidence ' + JSON.stringify({ frame_index: frame, ...inventory }))}`);
      }
    }
    if (!lines.length) lines.push('- text: "No accessible content"');
    const title = quoteName(await page.title());
    const response = this.navigationResponses.get(page);
    const sameDocument = response && response.url.split('#')[0] === page.url().split('#')[0];
    const evidence = { http_status: sameDocument ? response.http_status : 0, anti_bot_block: '' };
    if (sameDocument && response.cloudflareChallenge) {
      evidence.anti_bot_block = 'cloudflare';
    } else {
      evidence.anti_bot_block = await page.evaluate(() => {
        const title = document.title.toLowerCase();
        if (/just a moment|attention required/.test(title) &&
            document.querySelector('#challenge-form, #cf-challenge-running, .cf-error-details')) return 'cloudflare';
        if (/captcha|verify|access denied/.test(title) &&
            document.querySelector('iframe[src*="captcha-delivery.com"]')) return 'datadome';
        if (/access to this page has been denied|verify|press.*hold/.test(title) &&
            document.querySelector('#px-captcha')) return 'perimeterx';
        if (/access denied|pardon our interruption/.test(title) &&
            document.querySelector('iframe[src*="_Incapsula_Resource"]')) return 'imperva';
        return '';
      });
    }
    return `- Page URL: ${page.url()}\n- Page Title: ${title}\n- Snapshot encoding: json-names-v1\n- Navigation evidence: ${JSON.stringify(evidence)}\n### Snapshot\n\`\`\`yaml\n${lines.join('\n')}\n\`\`\``;
  }

  async call(tool, args, credentialAccess = false) {
    if (this.closed && tool !== 'browser_close') throw new Error('driver is closed');
    if (tool === 'mintclaw_credential_call') return await this.callCredential(args);
    if (tool === 'mintclaw_credential_prepare') return await this.prepareCredential(args);
    if (tool === 'mintclaw_credential_submit') return await this.submitCredential(args);
    if (tool === 'mintclaw_credential_discard') return await this.discardCredential(args);
    if (this.credentialBound && !credentialAccess && tool !== 'browser_close' && tool !== 'browser_ping') {
      return textResult('private credential envelope required', true);
    }
    if (this.credentialPending && tool !== 'browser_close' && tool !== 'browser_ping') {
      return textResult('private credential operation pending', true);
    }
    if (this.pendingDialog && tool !== 'browser_handle_dialog') {
      return textResult(modalText(this.pendingDialog), true);
    }
    try {
      let response = '';
      switch (tool) {
        case 'mintclaw_initialize':
          if (args.protocol !== 'mintclaw.playwright_library.v1') {
            throw new Error('incompatible private protocol');
          }
          response = 'MINTCLAW_PLAYWRIGHT_LIBRARY_V1|ready';
          break;
        case 'browser_ping':
          this.selectedPage();
          response = 'pong';
          break;
        case 'browser_close':
          await this.closeBrowser();
          response = 'closed';
          break;
        case 'browser_navigate':
          response = await this.actionOrDialog(async () => {
            await this.selectedPage().goto(String(args.url), { waitUntil: 'load' });
            return this.snapshot();
          });
          response = response.dialog ? modalText(this.pendingDialog) : response.value;
          break;
        case 'browser_snapshot':
          response = await this.actionOrDialog(() =>
            this.snapshot(args.target ? String(args.target) : ''));
          response = response.dialog ? modalText(this.pendingDialog) : response.value;
          break;
        case 'browser_tabs':
          response = await this.actionOrDialog(() => this.tabs(args));
          response = response.dialog ? modalText(this.pendingDialog) : response.value;
          break;
        case 'browser_take_screenshot': {
          const screenshot = await this.actionOrDialog(() => this.screenshot(args));
          if (!screenshot.dialog) return screenshot.value;
          response = modalText(this.pendingDialog);
          break;
        }
        case 'browser_click':
          response = await this.actionOrDialog(() => this.click(args));
          response = response.dialog ? modalText(this.pendingDialog) : response.value;
          break;
        case 'browser_type': {
          const locator = this.locatorFor(String(args.target));
          response = await this.actionOrDialog(async () => {
            const before = await writableSemanticState(locator);
            await locator.focus();
            const after = await writableSemanticState(locator);
            if (!sameWritableSemantics(before, after)) throw new Error('target changed after focus');
            if (args.slowly) await locator.pressSequentially(String(args.text));
            else await locator.fill(String(args.text));
            if (args.submit) await locator.press('Enter');
            return this.snapshot();
          });
          response = response.dialog ? modalText(this.pendingDialog) : response.value;
          break;
        }
        case 'browser_select_option': {
          const locator = this.locatorFor(String(args.target));
          response = await this.actionOrDialog(async () => {
            await locator.selectOption(args.values.map(String));
            return this.snapshot();
          });
          response = response.dialog ? modalText(this.pendingDialog) : response.value;
          break;
        }
        case 'browser_press_key':
          response = await this.actionOrDialog(async () => {
            await this.selectedPage().keyboard.press(String(args.key));
            return this.snapshot();
          });
          response = response.dialog ? modalText(this.pendingDialog) : response.value;
          break;
        case 'browser_mouse_wheel':
          response = await this.actionOrDialog(async () => {
            await this.selectedPage().mouse.wheel(Number(args.deltaX), Number(args.deltaY));
            return this.snapshot();
          });
          response = response.dialog ? modalText(this.pendingDialog) : response.value;
          break;
        case 'browser_hover':
          response = await this.actionOrDialog(async () => {
            await this.locatorFor(String(args.target)).hover();
            return this.snapshot();
          });
          response = response.dialog ? modalText(this.pendingDialog) : response.value;
          break;
        case 'browser_drag':
          response = await this.actionOrDialog(async () => {
            await this.locatorFor(String(args.startTarget)).dragTo(this.locatorFor(String(args.endTarget)));
            return this.snapshot();
          });
          response = response.dialog ? modalText(this.pendingDialog) : response.value;
          break;
        case 'browser_file_upload':
          if (!this.pendingFileChooser) throw new Error('file chooser is unavailable');
          response = await this.actionOrDialog(async () => {
            await this.pendingFileChooser.setFiles((args.paths || []).map(String));
            this.pendingFileChooser = null;
            return this.snapshot();
          });
          response = response.dialog ? modalText(this.pendingDialog) : response.value;
          break;
        case 'browser_handle_dialog':
          response = await this.handleDialog(args);
          break;
        case 'browser_run_code_unsafe': {
          const fn = (0, eval)(`(${String(args.code)})`);
          if (typeof fn !== 'function') throw new Error('driver code is not callable');
          response = await this.actionOrDialog(() => this.runCode(fn));
          response = response.dialog ? modalText(this.pendingDialog) : resultText(response.value);
          break;
        }
        case 'mintclaw_browser_execute':
          return await this.executePrivileged(args);
        default:
          throw new Error('unsupported private driver operation');
      }
      if (this.pendingDialog && tool !== 'browser_handle_dialog') response = modalText(this.pendingDialog);
      return textResult(response);
    } catch (error) {
      const response = this.pendingDialog ? modalText(this.pendingDialog) : `### Error\n${boundedError(error)}`;
      return textResult(response, !this.pendingDialog);
    }
  }

  async prepareCredential(args) {
    if (this.credentialPending || this.pendingDialog || this.executionActive || this.closed) {
      return textResult('private credential operation denied', true);
    }
    const page = this.selectedPage();
    const capsule = await prepareCredentialLogin(page, args);
    if (!capsule) return textResult('private credential operation denied', true);
    const id = randomBytes(32).toString('hex');
    const pending = { id, page, capsule, request: { ...args }, deadline: Date.now() + 15000 };
    this.credentialPending = pending;
    pending.timer = setTimeout(() => {
      if (this.credentialPending === pending) {
        this.credentialPending = null;
        pending.capsule.dispose().catch(() => {});
      }
    }, 15000);
    pending.timer.unref();
    return textResult(JSON.stringify({ id, origin: args.origin }));
  }

  async discardCredential(args) {
    const pending = this.credentialPending;
    if (!pending || args.id !== pending.id) return textResult('private credential operation denied', true);
    this.credentialPending = null;
    clearTimeout(pending.timer);
    await pending.capsule.dispose().catch(() => {});
    return textResult('discarded');
  }

  async submitCredential(args) {
    const pending = this.credentialPending;
    let code = 'denied';
    if (!pending || args.id !== pending.id || Date.now() >= pending.deadline) {
      args.username = ''; args.password = '';
      if (pending && Date.now() >= pending.deadline) {
        this.credentialPending = null;
        clearTimeout(pending.timer);
        await pending.capsule.dispose().catch(() => {});
      }
      return textResult(JSON.stringify({ code, cookies: [] }));
    }
    this.credentialPending = null;
    clearTimeout(pending.timer);
    this.credentialActive = true;
    this.credentialBound = true;
    const request = { ...pending.request, username: args.username, password: args.password };
    try {
      code = await performCredentialLogin(pending.page, pending.capsule, request);
      const cookies = ['logged_in', 'handoff_required', 'submitted', 'unverified'].includes(code) ? await this.harvestCredentialCookies() : [];
      // This response goes only to the trusted runtime's private parser. Cookie
      // values must be registered before allowing any later observations.
      return textResult(JSON.stringify({ code, cookies }));
    } catch (_) {
      await pending.page.context().close().catch(() => {});
      return textResult(JSON.stringify({ code: 'outcome_unknown', cookies: [] }));
    } finally {
      args.username = ''; args.password = '';
      request.username = ''; request.password = '';
      this.credentialActive = false;
    }
  }

  rememberCredentialCookie(value) {
    if (!value) return;
    if (typeof value !== 'string' || Buffer.byteLength(value) > 4096 ||
        (!this.credentialCookies.has(value) && this.credentialCookies.size >= 128)) {
      this.credentialCookieFailure = true;
      return;
    }
    this.credentialCookies.add(value);
  }

  trackCredentialCookies(response) {
    const pending = (async () => {
      for (const header of await response.headerValues('set-cookie')) {
        const pair = header.split(';', 1)[0], separator = pair.indexOf('=');
        if (separator < 1) throw new Error();
        let value = pair.slice(separator + 1).trim();
        if (value.startsWith('"') && value.endsWith('"')) value = value.slice(1, -1);
        this.rememberCredentialCookie(value);
      }
    })().catch(() => { this.credentialCookieFailure = true; });
    this.credentialCookieReads.add(pending);
    pending.finally(() => this.credentialCookieReads.delete(pending));
  }

  async harvestCredentialCookies() {
    for (const cookie of await this.context.cookies()) this.rememberCredentialCookie(cookie.value);
    // Response events can overlap an action or observation. Drain the reads
    // already observed before releasing that operation's private envelope.
    while (this.credentialCookieReads.size) await Promise.all([...this.credentialCookieReads]);
    if (this.credentialCookieFailure) throw new Error();
    return [...this.credentialCookies];
  }

  async callCredential(args) {
    if (!this.credentialBound || !args || typeof args.tool !== 'string' ||
        args.tool.startsWith('mintclaw_credential_') || !args.args || typeof args.args !== 'object') {
      return textResult('private credential output unavailable', true);
    }
    try {
      if (args.tool === 'browser_take_screenshot' || args.tool === 'mintclaw_browser_execute') throw new Error();
      const result = await this.call(args.tool, args.args, true);
      const cookies = args.tool === 'browser_close' ? [] : await this.harvestCredentialCookies();
      if (result.content.some(content => content.type !== 'text')) throw new Error();
      return textResult(JSON.stringify({ result, cookies }));
    } catch (_) {
      await this.closeBrowser().catch(() => {});
      return textResult('private credential output unavailable', true);
    }
  }

  async executePrivileged(args) {
    if (this.executionActive) throw new Error('privileged execution is busy');
    if (this.executionQuarantined || this.closed) throw new Error('privileged execution is quarantined');
    const source = String(args.source || '');
    const language = String(args.language || 'javascript');
    const effect = String(args.effect || 'unknown');
    const limits = args.limits && typeof args.limits === 'object' ? args.limits : {};
    const integer = (name, maximum) => {
      const value = Number(limits[name]);
      if (!Number.isSafeInteger(value) || value < 1 || value > maximum) {
        throw new Error('invalid privileged execution limits');
      }
      return value;
    };
    if (!source || Buffer.byteLength(source) > 64 * 1024 ||
        !['javascript', 'typescript'].includes(language) ||
        !['read', 'navigation', 'local_edit', 'external_commit', 'unknown'].includes(effect)) {
      throw new Error('invalid privileged execution request');
    }
    const runtimeSeconds = integer('runtime_seconds', 60);
    const outputBytes = integer('output_bytes', 256 * 1024);
    const actionLimit = integer('actions', 256);
    const memoryMB = integer('memory_mb', 256);
    const networkLimit = integer('network_requests', 256);
    const artifactLimit = integer('artifacts', 8);
    const artifactBytes = integer('artifact_bytes', 8 * 1024 * 1024);
    if (integer('concurrent', 1) !== 1) throw new Error('invalid privileged execution concurrency');
    const network = args.network && typeof args.network === 'object' && !Array.isArray(args.network) ? args.network : {};
    const networkMode = String(network.mode || '');
    const rawAllowedOrigins = Array.isArray(network.allowed_origins) ? network.allowed_origins : [];
    if (!['exact_origins', 'public_web', 'any_http'].includes(networkMode) ||
        (networkMode === 'exact_origins' && (rawAllowedOrigins.length < 1 || rawAllowedOrigins.length > 64)) ||
        (networkMode !== 'exact_origins' && rawAllowedOrigins.length !== 0)) {
      throw new Error('invalid privileged execution network authority');
    }
    const allowedOrigins = new Set();
    for (const raw of rawAllowedOrigins) {
      const parsed = networkURL(raw);
      if (!parsed || parsed.origin !== raw || parsed.parsed.pathname !== '/' ||
          parsed.parsed.search || parsed.parsed.hash || allowedOrigins.has(parsed.origin)) {
        throw new Error('invalid privileged execution network authority');
      }
      allowedOrigins.add(parsed.origin);
    }
    const dnsCache = new Map();
    const publicHost = async hostname => {
      const lower = hostname.toLowerCase();
      if (lower === 'localhost' || lower.endsWith('.localhost') ||
          lower === 'metadata.google.internal' || (!lower.includes('.') && net.isIP(lower) === 0)) return false;
      if (net.isIP(lower)) return publicIPAddress(lower);
      let lookup = dnsCache.get(lower);
      if (!lookup) {
        lookup = dns.lookup(lower, { all: true, verbatim: true }).catch(() => []);
        dnsCache.set(lower, lookup);
      }
      const addresses = await lookup;
      return addresses.length > 0 && addresses.length <= 32 &&
        addresses.every(item => item && publicIPAddress(item.address));
    };
    const destinationAllowed = async (raw, websocket = false) => {
      const destination = networkURL(raw, websocket);
      if (!destination) return false;
      if (networkMode === 'exact_origins') return allowedOrigins.has(destination.origin);
      if (networkMode === 'any_http') return true;
      return await publicHost(destination.hostname);
    };

    const permission = {
      read: new Set([
        'page.url', 'page.title', 'page.content', 'locator.count', 'locator.textContent',
        'locator.innerText', 'locator.getAttribute', 'locator.isVisible', 'context.pages',
        'artifact.screenshot', 'page.waitForLoadState', 'page.waitForTimeout', 'locator.hover',
      ]),
      navigation: null,
      local_edit: null,
      external_commit: null,
      unknown: null,
    };
    permission.navigation = new Set([...permission.read, 'page.goto', 'page.reload', 'page.goBack', 'page.goForward']);
    permission.local_edit = new Set([
      ...permission.navigation, 'locator.fill', 'locator.press', 'locator.check', 'locator.uncheck',
      'locator.selectOption', 'keyboard.press', 'keyboard.type',
    ]);
    permission.external_commit = new Set([
      ...permission.local_edit, 'locator.click', 'locator.evaluate', 'page.evaluate',
    ]);
    permission.unknown = permission.external_commit;

    this.executionActive = true;
    let actions = 0;
    let networkRequests = 0;
    let rpcOutputBytes = 0;
    const artifacts = [];
    let page = null;
    let worker = null;
    let executionStopped = false;
    let timedOut = false;
    let readTimedOut = false;
    let retainNetworkGuard = false;
    let networkGuardInstalled = false;
    const pendingRPCs = new Set();
    const seenRequests = new WeakSet();
    let rejectNetworkViolation;
    let networkViolationSignaled = false;
    const networkViolation = new Promise((_, reject) => { rejectNetworkViolation = reject; });
    networkViolation.catch(() => {});
    const signalNetworkViolation = error => {
      if (networkViolationSignaled) return;
      networkViolationSignaled = true;
      rejectNetworkViolation(error);
    };
    const countNetwork = request => {
      if (request && seenRequests.has(request)) return networkRequests <= networkLimit;
      if (request) seenRequests.add(request);
      networkRequests++;
      if (networkRequests > networkLimit) {
        signalNetworkViolation(new Error('privileged execution network budget exceeded'));
        return false;
      }
      return true;
    };
    const networkGuard = async (raw, websocket, request) => {
      const withinBudget = countNetwork(request);
      const allowed = withinBudget && await destinationAllowed(raw, websocket);
      if (!allowed) {
        signalNetworkViolation(new Error(websocket
          ? 'privileged execution network authority denied websocket'
          : 'privileged execution network authority denied request'));
        return false;
      }
      return true;
    };
    try {
      page = this.selectedPage();
      this.executionNetworkGuards.add(networkGuard);
      networkGuardInstalled = true;
      worker = new Worker(path.join(__dirname, 'execute-worker.cjs'), {
        workerData: {
          source,
          language,
          syncTimeoutMilliseconds: Math.min(runtimeSeconds * 1000, 5000),
        },
        resourceLimits: {
          maxOldGenerationSizeMb: memoryMB,
          maxYoungGenerationSizeMb: Math.max(4, Math.min(16, Math.floor(memoryMB / 4))),
          stackSizeMb: 4,
        },
      });
      const executionDeadline = Date.now() + runtimeSeconds * 1000;
      // Leave time for the native read to reject and its worker RPC to settle
      // before the source budget expires. Do not change global page timeouts.
      const readOptions = () => ({ timeout: Math.max(1, executionDeadline - Date.now() - 25) });

      const safeString = (value, maximum = 4096) => {
      if (typeof value !== 'string' || !value || Buffer.byteLength(value) > maximum) {
        throw new Error('invalid privileged execution argument');
      }
      return value;
    };
    const optionObject = value => value && typeof value === 'object' && !Array.isArray(value) ? value : {};
    const timeoutOption = (value, result) => {
      if (value === undefined) return;
      const timeout = Number(value);
      if (!Number.isSafeInteger(timeout) || timeout < 0 || timeout > runtimeSeconds * 1000) {
        throw new Error('invalid privileged execution timeout');
      }
      result.timeout = timeout;
    };
    const navigationOptions = raw => {
      const input = optionObject(raw);
      const result = {};
      timeoutOption(input.timeout, result);
      if (input.waitUntil !== undefined) {
        const waitUntil = String(input.waitUntil);
        if (!['commit', 'domcontentloaded', 'load', 'networkidle'].includes(waitUntil)) {
          throw new Error('invalid privileged execution navigation option');
        }
        result.waitUntil = waitUntil;
      }
      return result;
    };
    const actionOptions = raw => {
      const input = optionObject(raw);
      const result = {};
      timeoutOption(input.timeout, result);
      if (input.force !== undefined) result.force = Boolean(input.force);
      if (input.noWaitAfter !== undefined) result.noWaitAfter = Boolean(input.noWaitAfter);
      if (input.trial !== undefined) result.trial = Boolean(input.trial);
      if (input.button !== undefined) {
        const button = String(input.button);
        if (!['left', 'middle', 'right'].includes(button)) throw new Error('invalid privileged execution action option');
        result.button = button;
      }
      if (input.clickCount !== undefined) {
        const clickCount = Number(input.clickCount);
        if (!Number.isSafeInteger(clickCount) || clickCount < 1 || clickCount > 3) {
          throw new Error('invalid privileged execution action option');
        }
        result.clickCount = clickCount;
      }
      return result;
    };
    const waitOptions = raw => {
      const result = readOptions();
      timeoutOption(optionObject(raw).timeout, result);
      result.timeout = Math.max(1, Math.min(result.timeout || runtimeSeconds * 1000, readOptions().timeout));
      return result;
    };
    const screenshotOptions = raw => {
      const input = optionObject(raw);
      const result = { type: 'png' };
      timeoutOption(input.timeout, result);
      if (input.fullPage !== undefined) result.fullPage = Boolean(input.fullPage);
      if (input.omitBackground !== undefined) result.omitBackground = Boolean(input.omitBackground);
      if (input.animations !== undefined) {
        const animations = String(input.animations);
        if (!['allow', 'disabled'].includes(animations)) throw new Error('invalid screenshot option');
        result.animations = animations;
      }
      if (input.caret !== undefined) {
        const caret = String(input.caret);
        if (!['hide', 'initial'].includes(caret)) throw new Error('invalid screenshot option');
        result.caret = caret;
      }
      if (input.scale !== undefined) {
        const scale = String(input.scale);
        if (!['css', 'device'].includes(scale)) throw new Error('invalid screenshot option');
        result.scale = scale;
      }
      return result;
    };
    const boundedRPCResult = value => {
      const encoded = JSON.stringify(value === undefined ? null : value);
      if (encoded === undefined) throw new Error('privileged execution RPC result is not serializable');
      rpcOutputBytes += Buffer.byteLength(encoded);
      if (rpcOutputBytes > outputBytes) throw new Error('privileged execution output budget exceeded');
      return JSON.parse(encoded);
    };
    const perform = async (method, raw) => {
      if (!permission[effect].has(method)) throw new Error('requested effect does not permit operation');
      if (++actions > actionLimit) throw new Error('privileged execution action budget exceeded');
      const args = raw && typeof raw === 'object' ? raw : {};
      if (method.startsWith('locator.')) {
        const locator = page.locator(safeString(args.selector));
        const operation = method.slice('locator.'.length);
        if (['click', 'fill', 'press', 'check', 'uncheck', 'hover', 'selectOption', 'evaluate'].includes(operation)) {
          retainNetworkGuard = true;
        }
        switch (operation) {
          case 'count': return await locator.count();
          case 'click': await locator.click(actionOptions(args.options)); return null;
          case 'fill': await locator.fill(safeString(args.value, 64 * 1024)); return null;
          case 'press': await locator.press(safeString(args.key, 128)); return null;
          case 'check': await locator.check(); return null;
          case 'uncheck': await locator.uncheck(); return null;
          case 'hover': await locator.hover(); return null;
          case 'textContent': return await locator.textContent(readOptions());
          case 'innerText': return await locator.innerText(readOptions());
          case 'getAttribute': return await locator.getAttribute(safeString(args.name, 256), readOptions());
          case 'isVisible': return await locator.isVisible();
          case 'selectOption': return await locator.selectOption(args.value);
          case 'evaluate': {
            const expression = safeString(args.expression, 64 * 1024);
            // Playwright serializes this fixed callback into the page realm. The
            // operator source is parsed and invoked there, never in the sidecar host.
            return await locator.evaluate((element, source) => {
              const callback = (0, eval)(`(${source})`);
              if (typeof callback !== 'function') throw new Error('locator evaluate requires a function');
              return callback(element);
            }, expression);
          }
          default: throw new Error('unsupported privileged execution operation');
        }
      }
      switch (method) {
        case 'page.url': return page.url();
        case 'page.title': return await page.title();
        case 'page.content': return await page.content();
        case 'page.goto': {
          retainNetworkGuard = true;
          const destination = safeString(args.url, 16 * 1024);
          if (!await destinationAllowed(destination)) {
            signalNetworkViolation(new Error('privileged execution network authority denied navigation'));
            throw new Error('privileged execution network authority denied navigation');
          }
          const response = await page.goto(destination, navigationOptions(args.options));
          return response ? { url: response.url(), status: response.status() } : null;
        }
        case 'page.reload': retainNetworkGuard = true; await page.reload(navigationOptions(args.options)); return null;
        case 'page.goBack': retainNetworkGuard = true; await page.goBack(navigationOptions(args.options)); return null;
        case 'page.goForward': retainNetworkGuard = true; await page.goForward(navigationOptions(args.options)); return null;
        case 'page.waitForLoadState': await page.waitForLoadState(safeString(args.state, 64), waitOptions(args.options)); return null;
        case 'page.waitForTimeout': {
          const milliseconds = Number(args.milliseconds);
          if (!Number.isSafeInteger(milliseconds) || milliseconds < 0 || milliseconds > runtimeSeconds * 1000) {
            throw new Error('invalid wait duration');
          }
          await page.waitForTimeout(milliseconds); return null;
        }
        case 'page.evaluate': retainNetworkGuard = true; return await page.evaluate(safeString(args.expression, 64 * 1024));
        case 'keyboard.press': retainNetworkGuard = true; await page.keyboard.press(safeString(args.key, 128)); return null;
        case 'keyboard.type': retainNetworkGuard = true; await page.keyboard.type(safeString(args.text, 64 * 1024)); return null;
        case 'context.pages': return this.context.pages().map(candidate => ({ url: candidate.url() }));
        case 'artifact.screenshot': {
          if (artifacts.length >= artifactLimit) throw new Error('privileged execution artifact budget exceeded');
          const data = await page.screenshot(screenshotOptions(args.options));
          const retainedBytes = artifacts.reduce((total, item) => total + item.data.length, 0);
          if (!data.length || retainedBytes + data.length > artifactBytes) {
            throw new Error('privileged execution artifact budget exceeded');
          }
          const id = `artifact_${artifacts.length + 1}`;
          artifacts.push({ id, data });
          return { id, content_type: 'image/png', bytes: data.length };
        }
        default: throw new Error('unsupported privileged execution operation');
      }
    };

      const workerOutcome = new Promise((resolve, reject) => {
        const timeout = setTimeout(() => {
          timedOut = true;
          reject(new Error('privileged execution timed out'));
        }, runtimeSeconds * 1000);
        worker.on('message', message => {
          if (executionStopped || !message || typeof message !== 'object') return;
          if (message.type === 'rpc') {
            const pending = (async () => {
              try {
                const value = boundedRPCResult(await perform(String(message.method), message.args));
                if (!executionStopped) worker.postMessage({ type: 'rpc_result', id: message.id, value });
              } catch (error) {
                if (error && error.name === 'TimeoutError' && !retainNetworkGuard) readTimedOut = true;
                if (!executionStopped) worker.postMessage({ type: 'rpc_result', id: message.id, error: boundedError(error) });
              }
            })();
            pendingRPCs.add(pending);
            pending.finally(() => pendingRPCs.delete(pending)).catch(() => {});
            return;
          }
          if (message.type === 'result') {
            clearTimeout(timeout);
            resolve(String(message.encoded));
          } else if (message.type === 'error') {
            clearTimeout(timeout);
            reject(new Error(String(message.error)));
          }
        });
        worker.once('error', error => { clearTimeout(timeout); reject(error); });
        worker.once('exit', code => {
          if (code !== 0) { clearTimeout(timeout); reject(new Error('privileged execution worker exited')); }
        });
      });
      const outcome = await Promise.race([workerOutcome, networkViolation]);
      if (Buffer.byteLength(outcome) > outputBytes) throw new Error('privileged execution output budget exceeded');
      return {
        is_error: false,
        content: [
          { type: 'text', text: JSON.stringify({ value: JSON.parse(outcome), actions, network_requests: networkRequests }) },
          ...artifacts.map(item => ({ type: 'image', mime_type: 'image/png', data: item.data.toString('base64') })),
        ],
      };
    } catch (error) {
      executionStopped = true;
      let workerStopped = !worker;
      if (worker) {
        try {
          await worker.terminate();
          worker = null;
          workerStopped = true;
        } catch (_) {}
      }
      if (workerStopped && !retainNetworkGuard && !networkViolationSignaled) {
        let settlementTimer;
        const settled = await Promise.race([
          Promise.allSettled([...pendingRPCs]).then(() => true),
          new Promise(resolve => { settlementTimer = setTimeout(() => resolve(false), 1000); }),
        ]);
        clearTimeout(settlementTimer);
        if (settled && !networkViolationSignaled && page && !page.isClosed() && !this.closed) {
          // This marker is private driver evidence. It is never derived from
          // page text, the submitted effect, or an exception's message.
          const code = timedOut || readTimedOut ? 'execution_timeout' : 'execution_rejected';
          return textResult(`MINTCLAW_EXECUTION_FAILED_V1|${code}`, true);
        }
      }
      if (timedOut) {
        this.executionQuarantined = true;
        await this.closeBrowser().catch(() => {});
        await Promise.allSettled([...pendingRPCs]);
      }
      throw error;
    } finally {
      if (worker) await worker.terminate().catch(() => {});
      if (networkGuardInstalled && !retainNetworkGuard) {
        this.executionNetworkGuards.delete(networkGuard);
      }
      this.executionActive = false;
    }
  }

  async click(args) {
    const locator = this.locatorFor(String(args.target));
    const type = await locator.getAttribute('type').catch(() => '');
    if (String(type).toLowerCase() === 'file') {
      this.pendingFileChooser = locator;
      return '- [File chooser]: can be handled by browser_file_upload';
    }
    const chooser = this.selectedPage().waitForEvent('filechooser', { timeout: 400 }).catch(() => null);
    const options = { button: args.button || 'left', noWaitAfter: true };
    if (args.doubleClick) await locator.dblclick(options);
    else await locator.click(options);
    this.pendingFileChooser = await chooser;
    if (this.pendingFileChooser) return '- [File chooser]: can be handled by browser_file_upload';
    return this.pendingDialog ? modalText(this.pendingDialog) : await this.snapshot();
  }

  async screenshot(args) {
    const options = { type: 'png', fullPage: Boolean(args.fullPage), scale: args.scale || 'css' };
    let data;
    if (args.target) data = await this.locatorFor(String(args.target)).screenshot(options);
    else data = await this.selectedPage().screenshot(options);
    return { is_error: false, content: [{ type: 'image', mime_type: 'image/png', data: data.toString('base64') }] };
  }

  async tabs(args) {
    const action = String(args.action);
    if (action === 'new') {
      const page = await this.context.newPage();
      this.watchPage(page);
      this.page = page;
      if (args.url) await page.goto(String(args.url), { waitUntil: 'load' });
    } else if (action === 'select') {
      const pages = this.context.pages();
      const selected = pages[Number(args.index)];
      if (!selected) throw new Error('tab is unavailable');
      this.page = selected;
      await selected.bringToFront();
    } else if (action === 'close') {
      const pages = this.context.pages();
      const selected = args.index === undefined ? this.selectedPage() : pages[Number(args.index)];
      if (!selected) throw new Error('tab is unavailable');
      await selected.close();
      const remaining = this.context.pages();
      if (!remaining.length) {
        const replacement = await this.context.newPage();
        this.watchPage(replacement);
        this.page = replacement;
      } else if (selected === this.page) {
        this.page = remaining[Math.min(Number(args.index) || 0, remaining.length - 1)];
      }
    } else if (action !== 'list') {
      throw new Error('unsupported tab action');
    }
    return this.snapshot();
  }

  async closeBrowser() {
    if (this.closed) return;
    this.closed = true;
    if (this.credentialPending) {
      clearTimeout(this.credentialPending.timer);
      await this.credentialPending.capsule.dispose().catch(() => {});
      this.credentialPending = null;
    }
    this.executionNetworkGuards.clear();
    let failure = null;
    if (this.pendingDialog) {
      await this.pendingDialog.handle.dismiss().catch(() => {});
      this.pendingDialog = null;
    }
    if (this.options.remoteRuntime) {
      this.context = null;
      this.browser = null;
    } else if (this.context) {
      try { await this.context.close(); } catch (error) { failure = error; }
    }
    if (this.browser) {
      try { await this.browser.close(); } catch (error) { failure ||= error; }
    }
    if (failure) throw failure;
  }
}

async function runProtocol(driver, input, output) {
  const lines = readline.createInterface({ input, crlfDelay: Infinity });
  for await (const line of lines) {
    let id = null;
    try {
      if (Buffer.byteLength(line) > MAX_REQUEST_BYTES) throw new Error('request is too large');
      const request = JSON.parse(line);
      id = request.id;
      if (!Number.isSafeInteger(id) || id < 1 || typeof request.method !== 'string' ||
          request.params === null || typeof request.params !== 'object' || Array.isArray(request.params)) {
        throw new Error('invalid request');
      }
      if (request.method === 'shutdown') {
        await driver.closeBrowser();
        output.write(`${JSON.stringify({ id, result: textResult('closed') })}\n`);
        return;
      }
      const result = await driver.call(request.method, request.params);
      output.write(`${JSON.stringify({ id, result })}\n`);
    } catch (_) {
      output.write(`${JSON.stringify({ id, error: 'private driver request failed' })}\n`);
    }
  }
  await driver.closeBrowser();
}

async function main() {
  const driver = new Driver(parseArguments(process.argv.slice(2)));
  await driver.start();
  await runProtocol(driver, process.stdin, process.stdout);
}

if (require.main === module) {
  main().catch(() => {
    process.stderr.write('playwright library sidecar unavailable\n');
    process.exitCode = 1;
  });
}

module.exports = { Driver, runProtocol };
