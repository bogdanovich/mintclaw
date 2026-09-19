#!/usr/bin/env node
'use strict';

// This process is a private MintClaw driver. It intentionally implements a
// small JSON-lines protocol instead of MCP and never discovers or publishes
// tools to an agent.

const readline = require('node:readline');
const path = require('node:path');
const dns = require('node:dns').promises;
const net = require('node:net');
const { Worker } = require('node:worker_threads');
const { chromium, firefox, webkit } = require('playwright');

const MAX_REQUEST_BYTES = 8 * 1024 * 1024;
const MAX_ERROR_BYTES = 2048;
const REF_ATTRIBUTE = 'data-mintclaw-playwright-ref';

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
  return options;
}

function boundedError(error) {
  const text = error && typeof error.message === 'string' ? error.message : 'driver operation failed';
  return text.slice(0, MAX_ERROR_BYTES).replace(/[\r\n]+/g, ' ');
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

function snapshotFrameScript({ prefix, target, attribute }) {
  const prior = document.querySelectorAll(`[${attribute}]`);
  for (const element of prior) element.removeAttribute(attribute);
  const visible = element => {
    const style = getComputedStyle(element);
    const box = element.getBoundingClientRect();
    return element.isConnected && style.display !== 'none' && style.visibility !== 'hidden' &&
      box.width > 0 && box.height > 0;
  };
  const name = element => {
    const labelledBy = String(element.getAttribute('aria-labelledby') || '').trim();
    if (labelledBy) {
      const labels = labelledBy.split(/\s+/).map(id => document.getElementById(id))
        .filter(Boolean).map(label => label.textContent || '').join(' ').trim();
      if (labels) return labels;
    }
    const ariaLabel = element.getAttribute('aria-label');
    if (ariaLabel) return ariaLabel;
    if (element.labels && element.labels.length) {
      const labels = Array.from(element.labels).map(label => label.textContent || '').join(' ').trim();
      if (labels) return labels;
    }
    return element.getAttribute('alt') ||
      element.getAttribute('placeholder') || element.getAttribute('title') ||
      element.getAttribute('value') || element.textContent || '';
  };
  const implicitRole = element => {
    const explicit = String(element.getAttribute('role') || '').trim().toLowerCase();
    if (explicit) return explicit.split(/\s+/)[0];
    const tag = element.tagName.toLowerCase();
    if (tag === 'a' && element.hasAttribute('href')) return 'link';
    if (tag === 'button' || tag === 'summary') return 'button';
    if (tag === 'textarea') return 'textbox';
    if (tag === 'select') return element.multiple ? 'listbox' : 'combobox';
    if (tag === 'option') return 'option';
    if (tag === 'img') return 'img';
    if (/^h[1-6]$/.test(tag)) return 'heading';
    if (tag === 'li') return 'listitem';
    if (tag === 'input') {
      const type = String(element.type || 'text').toLowerCase();
      if (type === 'checkbox') return 'checkbox';
      if (type === 'radio') return 'radio';
      if (['button', 'submit', 'reset', 'image'].includes(type)) return 'button';
      if (type === 'range') return 'slider';
      if (type === 'number') return 'spinbutton';
      if (type === 'file') return 'button';
      if (type !== 'hidden') return 'textbox';
    }
    if (element.isContentEditable) return 'textbox';
    return '';
  };
  const clean = value => String(value || '').replace(/[\r\n\t]+/g, ' ').replace(/\s+/g, ' ')
    .trim().slice(0, 512).replace(/"/g, "'");
  const lines = [];
  const elements = Array.from(document.querySelectorAll('*')).slice(0, 12000);
  let next = 1;
  for (const element of elements) {
    if (!visible(element)) continue;
    const role = implicitRole(element);
    if (!role) continue;
    const ref = prefix + 'e' + next++;
    element.setAttribute(attribute, ref);
    if (target && target !== ref) continue;
    const label = clean(name(element));
    const state = [];
    if (element.disabled || element.matches(':disabled') || element.getAttribute('aria-disabled') === 'true') state.push('disabled');
    if (element.checked || element.getAttribute('aria-checked') === 'true') state.push('checked');
    let value = '';
    if (['input', 'textarea', 'select'].includes(element.tagName.toLowerCase()) &&
        String(element.type || '').toLowerCase() !== 'password') {
      value = clean(element.value);
    }
    lines.push(`- ${role}${label ? ` "${label}"` : ''}${state.length ? ` [${state.join(', ')}]` : ''} [ref=${ref}]${value ? `: ${value}` : ''}`);
  }
  if (!target) {
    const bodyText = clean(document.body && document.body.innerText || '');
    if (bodyText) lines.push(`- text: "${bodyText.slice(0, 4096)}"`);
  }
  return lines;
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
  }

  async start() {
    const browserType = { chromium, firefox, webkit }[this.options.browser] ||
      (this.options.browser === 'chrome' || this.options.browser === 'msedge' ? chromium : null);
    if (!browserType) throw new Error('unsupported browser');
    const launch = { headless: this.options.headless };
    if (this.options.executablePath) launch.executablePath = this.options.executablePath;
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
    if (this.options.userDataDir) {
      this.context = await browserType.launchPersistentContext(this.options.userDataDir, {
        ...launch, ...contextOptions,
      });
    } else {
      this.browser = await browserType.launch(launch);
      this.context = await this.browser.newContext(contextOptions);
    }
    await this.context.route('**/*', async route => {
      const request = route.request();
      for (const guard of this.executionNetworkGuards) {
        if (!(await guard(request.url(), false, request))) {
          await route.abort('blockedbyclient').catch(() => {});
          return;
        }
      }
      await route.continue().catch(() => {});
    });
    await this.context.routeWebSocket(/.*/, async websocket => {
      for (const guard of this.executionNetworkGuards) {
        if (!(await guard(websocket.url(), true, null))) return;
      }
      await websocket.connectToServer();
    });
    this.context.on('page', page => this.watchPage(page));
    for (const page of this.context.pages()) this.watchPage(page);
    this.page = this.context.pages()[0] || await this.context.newPage();
  }

  watchPage(page) {
    if (page.__mintclawSidecarWatched) return;
    Object.defineProperty(page, '__mintclawSidecarWatched', { value: true });
    page.on('dialog', dialog => {
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
    for (const frame of this.selectedPage().frames()) {
      const locator = frame.locator(`[${REF_ATTRIBUTE}="${ref}"]`);
      // Locator resolution is lazy, so returning every-frame candidates would
      // require a composite locator. Snapshot prefixes make the frame exact.
      if (!ref.startsWith('f') && frame === this.selectedPage().mainFrame()) return locator;
      if (ref.startsWith('f') && frame !== this.selectedPage().mainFrame()) {
        const index = this.selectedPage().frames().filter(item => item !== this.selectedPage().mainFrame()).indexOf(frame) + 1;
        if (ref.startsWith(`f${index}e`)) return locator;
      }
    }
    return this.selectedPage().locator(`[${REF_ATTRIBUTE}="${ref}"]`);
  }

  async runCode(fn) {
    const page = this.selectedPage();
    const driver = this;
    const ownLocator = Object.getOwnPropertyDescriptor(page, 'locator');
    const nativeLocator = page.locator.bind(page);
    Object.defineProperty(page, 'locator', {
      configurable: true,
      value(selector, options) {
        const match = /^aria-ref=(.+)$/.exec(String(selector));
        return match ? driver.locatorFor(match[1]) : nativeLocator(selector, options);
      },
    });
    try {
      return await fn(page);
    } finally {
      if (ownLocator) Object.defineProperty(page, 'locator', ownLocator);
      else delete page.locator;
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
    const frames = page.frames();
    const lines = [];
    let child = 0;
    for (const frame of frames) {
      const prefix = frame === page.mainFrame() ? '' : `f${++child}`;
      try {
        const frameLines = await frame.evaluate(snapshotFrameScript, {
          prefix, target, attribute: REF_ATTRIBUTE,
        });
        if (frame !== page.mainFrame() && frameLines.length) {
          lines.push(`- iframe "${quoteName(frame.name())}"`);
          lines.push(...frameLines.map(line => `  ${line}`));
        } else {
          lines.push(...frameLines);
        }
      } catch (_) {
        if (frame === page.mainFrame()) throw _;
      }
    }
    const title = quoteName(await page.title());
    return `- Page URL: ${page.url()}\n- Page Title: ${title}\n### Snapshot\n\`\`\`yaml\n${lines.join('\n')}\n\`\`\``;
  }

  async call(tool, args) {
    if (this.closed && tool !== 'browser_close') throw new Error('driver is closed');
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
    let timedOut = false;
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
      const result = {};
      timeoutOption(optionObject(raw).timeout, result);
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
          case 'textContent': return await locator.textContent();
          case 'innerText': return await locator.innerText();
          case 'getAttribute': return await locator.getAttribute(safeString(args.name, 256));
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
          if (!message || typeof message !== 'object') return;
          if (message.type === 'rpc') {
            const pending = (async () => {
              try {
                const value = boundedRPCResult(await perform(String(message.method), message.args));
                worker.postMessage({ type: 'rpc_result', id: message.id, value });
              } catch (error) {
                worker.postMessage({ type: 'rpc_result', id: message.id, error: boundedError(error) });
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
      if (timedOut) {
        this.executionQuarantined = true;
        if (worker) {
          await worker.terminate().catch(() => {});
          worker = null;
        }
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
    this.executionNetworkGuards.clear();
    let failure = null;
    if (this.pendingDialog) {
      await this.pendingDialog.handle.dismiss().catch(() => {});
      this.pendingDialog = null;
    }
    if (this.context) {
      try { await this.context.close(); } catch (error) { failure = error; }
    }
    if (this.browser) {
      try { await this.browser.close(); } catch (error) { failure ||= error; }
    }
    if (failure) throw failure;
  }
}

async function main() {
  const driver = new Driver(parseArguments(process.argv.slice(2)));
  await driver.start();
  const lines = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
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
        process.stdout.write(`${JSON.stringify({ id, result: textResult('closed') })}\n`);
        return;
      }
      const result = await driver.call(request.method, request.params);
      process.stdout.write(`${JSON.stringify({ id, result })}\n`);
    } catch (error) {
      process.stdout.write(`${JSON.stringify({ id, error: boundedError(error) })}\n`);
    }
  }
  await driver.closeBrowser();
}

main().catch(error => {
  process.stderr.write(`playwright library sidecar: ${boundedError(error)}\n`);
  process.exitCode = 1;
});
