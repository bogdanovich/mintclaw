#!/usr/bin/env node
'use strict';

// This process is a private MintClaw driver. It intentionally implements a
// small JSON-lines protocol instead of MCP and never discovers or publishes
// tools to an agent.

const readline = require('node:readline');
const path = require('node:path');
const { Worker } = require('node:worker_threads');
const { chromium, firefox, webkit } = require('playwright');

const MAX_REQUEST_BYTES = 8 * 1024 * 1024;
const MAX_ERROR_BYTES = 2048;
const REF_ATTRIBUTE = 'data-mintclaw-playwright-ref';

function parseArguments(argv) {
  const options = {
    browser: 'chromium', executablePath: '', userDataDir: '', proxyServer: '',
    proxyBypass: '', outputDir: '', headless: false, isolated: false,
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
    if (name === 'headless' || name === 'isolated') {
      if (equals >= 0) throw new Error('boolean argument has a value');
      options[name] = true;
      continue;
    }
    if (!values.has(name)) throw new Error('unsupported driver argument');
    if (equals < 0) {
      if (++index >= argv.length || argv[index].startsWith('--')) {
        throw new Error('driver argument is missing a value');
      }
      value = argv[index];
    }
    const property = name.replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
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
    const contextOptions = { acceptDownloads: false };
    if (this.options.userDataDir) {
      this.context = await browserType.launchPersistentContext(this.options.userDataDir, {
        ...launch, ...contextOptions,
      });
    } else {
      this.browser = await browserType.launch(launch);
      this.context = await this.browser.newContext(contextOptions);
    }
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
      'locator.selectOption', 'locator.evaluate', 'page.evaluate', 'keyboard.press', 'keyboard.type',
    ]);
    permission.external_commit = new Set([...permission.local_edit, 'locator.click']);
    permission.unknown = permission.external_commit;

    this.executionActive = true;
    let actions = 0;
    let networkRequests = 0;
    let rpcOutputBytes = 0;
    const artifacts = [];
    const page = this.selectedPage();
    const seenRequests = new WeakSet();
    let rejectNetworkViolation;
    const networkViolation = new Promise((_, reject) => { rejectNetworkViolation = reject; });
    const countNetwork = request => {
      if (request && seenRequests.has(request)) return networkRequests <= networkLimit;
      if (request) seenRequests.add(request);
      networkRequests++;
      if (networkRequests > networkLimit) {
        rejectNetworkViolation(new Error('privileged execution network budget exceeded'));
        return false;
      }
      return true;
    };
    const requestHandler = request => { countNetwork(request); };
    const websocketHandler = () => { countNetwork(null); };
    const routeHandler = async route => {
      if (!countNetwork(route.request())) await route.abort('blockedbyclient').catch(() => {});
      else await route.continue().catch(() => {});
    };
    this.context.on('request', requestHandler);
    page.on('websocket', websocketHandler);
    await this.context.route('**/*', routeHandler);
    const worker = new Worker(path.join(__dirname, 'execute-worker.cjs'), {
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
        switch (method.slice('locator.'.length)) {
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
          case 'evaluate': return await locator.evaluate(safeString(args.expression, 64 * 1024));
          default: throw new Error('unsupported privileged execution operation');
        }
      }
      switch (method) {
        case 'page.url': return page.url();
        case 'page.title': return await page.title();
        case 'page.content': return await page.content();
        case 'page.goto': {
          const response = await page.goto(safeString(args.url, 16 * 1024), navigationOptions(args.options));
          return response ? { url: response.url(), status: response.status() } : null;
        }
        case 'page.reload': await page.reload(navigationOptions(args.options)); return null;
        case 'page.goBack': await page.goBack(navigationOptions(args.options)); return null;
        case 'page.goForward': await page.goForward(navigationOptions(args.options)); return null;
        case 'page.waitForLoadState': await page.waitForLoadState(safeString(args.state, 64), waitOptions(args.options)); return null;
        case 'page.waitForTimeout': {
          const milliseconds = Number(args.milliseconds);
          if (!Number.isSafeInteger(milliseconds) || milliseconds < 0 || milliseconds > runtimeSeconds * 1000) {
            throw new Error('invalid wait duration');
          }
          await page.waitForTimeout(milliseconds); return null;
        }
        case 'page.evaluate': return await page.evaluate(safeString(args.expression, 64 * 1024));
        case 'keyboard.press': await page.keyboard.press(safeString(args.key, 128)); return null;
        case 'keyboard.type': await page.keyboard.type(safeString(args.text, 64 * 1024)); return null;
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

    try {
      const workerOutcome = new Promise((resolve, reject) => {
        const timeout = setTimeout(() => reject(new Error('privileged execution timed out')), runtimeSeconds * 1000);
        worker.on('message', async message => {
          if (!message || typeof message !== 'object') return;
          if (message.type === 'rpc') {
            try {
              const value = boundedRPCResult(await perform(String(message.method), message.args));
              worker.postMessage({ type: 'rpc_result', id: message.id, value });
            } catch (error) {
              worker.postMessage({ type: 'rpc_result', id: message.id, error: boundedError(error) });
            }
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
    } finally {
      await worker.terminate().catch(() => {});
      await this.context.unroute('**/*', routeHandler).catch(() => {});
      this.context.off('request', requestHandler);
      page.off('websocket', websocketHandler);
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
    if (this.pendingDialog) {
      await this.pendingDialog.handle.dismiss().catch(() => {});
      this.pendingDialog = null;
    }
    if (this.context) await this.context.close();
    if (this.browser) await this.browser.close();
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
