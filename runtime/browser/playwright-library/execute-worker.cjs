'use strict';

// This worker evaluates operator-enabled browser_execute source without
// inheriting the sidecar process globals. Every browser operation is an RPC
// to the parent, where action, network, artifact, and time budgets are
// enforced independently of the evaluated code.

const vm = require('node:vm');
const { parentPort, workerData } = require('node:worker_threads');
const ts = require('typescript');

let sequence = 0;
const pending = new Map();

function rpc(method, args = {}) {
  const id = ++sequence;
  parentPort.postMessage({ type: 'rpc', id, method, args });
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
}

parentPort.on('message', message => {
  if (!message || message.type !== 'rpc_result' || !pending.has(message.id)) return;
  const waiter = pending.get(message.id);
  pending.delete(message.id);
  if (message.error) waiter.reject(new Error(String(message.error)));
  else waiter.resolve(message.value);
});

async function main() {
  let executable = workerData.source;
  if (workerData.language === 'typescript') {
    const transpiled = ts.transpileModule(`const __mintclaw_source = (${workerData.source});`, {
      compilerOptions: {
        target: ts.ScriptTarget.ES2022,
        module: ts.ModuleKind.CommonJS,
        isolatedModules: true,
        ignoreDeprecations: '6.0',
      },
      reportDiagnostics: true,
    });
    const errors = (transpiled.diagnostics || []).filter(item =>
      item.category === ts.DiagnosticCategory.Error);
    if (errors.length) throw new Error('typescript source is invalid');
    const marker = 'const __mintclaw_source =';
    const markerIndex = transpiled.outputText.indexOf(marker);
    if (markerIndex < 0) throw new Error('typescript source is invalid');
    executable = transpiled.outputText.slice(markerIndex + marker.length).trim().replace(/;\s*$/, '');
  }
  const sandbox = Object.create(null);
  sandbox.__mintclaw = undefined;
  sandbox.__mintclaw_bridge = (method, args) => rpc(method, args);
  sandbox.__mintclaw_facade = undefined;
  sandbox.__mintclaw_result = undefined;
  sandbox.__mintclaw_encoded = undefined;
  const contextified = vm.createContext(sandbox, {
    name: 'mintclaw-browser-execute',
    codeGeneration: { strings: false, wasm: false },
  });
  const bootstrap = new vm.Script(`
    __mintclaw_facade = (() => {
      const bridge = __mintclaw_bridge;
      const clean = value => JSON.parse(JSON.stringify(value === undefined ? null : value));
      const call = async (method, args = {}) => {
        try {
          return clean(await bridge(method, args));
        } catch (error) {
          throw new Error(String(error && error.message || error).slice(0, 2048));
        }
      };
      const locator = selector => {
        if (typeof selector !== 'string' || !selector || selector.length > 4096) {
          throw new Error('invalid locator');
        }
        const invoke = (operation, args = {}) => call('locator.' + operation, {selector, ...args});
        return Object.freeze({
          count: () => invoke('count'),
          click: options => invoke('click', {options}),
          fill: value => invoke('fill', {value}),
          press: key => invoke('press', {key}),
          check: () => invoke('check'),
          uncheck: () => invoke('uncheck'),
          hover: () => invoke('hover'),
          textContent: () => invoke('textContent'),
          innerText: () => invoke('innerText'),
          getAttribute: name => invoke('getAttribute', {name}),
          isVisible: () => invoke('isVisible'),
          selectOption: value => invoke('selectOption', {value}),
          evaluate: expression => invoke('evaluate', {expression}),
        });
      };
      const page = Object.freeze({
        url: () => call('page.url'),
        title: () => call('page.title'),
        content: () => call('page.content'),
        locator,
        getByRole: (role, options = {}) => locator(
          'role=' + String(role) + '[name=' + JSON.stringify(String(options.name || '')) + ']'
        ),
        getByText: text => locator('text=' + JSON.stringify(String(text))),
        goto: (url, options = {}) => call('page.goto', {url, options}),
        reload: options => call('page.reload', {options}),
        goBack: options => call('page.goBack', {options}),
        goForward: options => call('page.goForward', {options}),
        waitForLoadState: (state, options = {}) => call('page.waitForLoadState', {state, options}),
        waitForTimeout: milliseconds => call('page.waitForTimeout', {milliseconds}),
        evaluate: expression => call('page.evaluate', {expression}),
        keyboard: Object.freeze({
          press: key => call('keyboard.press', {key}),
          type: text => call('keyboard.type', {text}),
        }),
      });
      const browserContext = Object.freeze({pages: () => call('context.pages')});
      const artifacts = Object.freeze({
        screenshot: options => call('artifact.screenshot', {options}),
      });
      return Object.freeze({page, context: browserContext, artifacts});
    })();
    delete globalThis.__mintclaw_bridge;
  `, { filename: 'browser-execute-bootstrap.js' });
  bootstrap.runInContext(contextified, { timeout: workerData.syncTimeoutMilliseconds });
  const script = new vm.Script(`__mintclaw = (${executable})`, {
    filename: 'browser-execute.js',
  });
  script.runInContext(contextified, { timeout: workerData.syncTimeoutMilliseconds });
  if (typeof sandbox.__mintclaw !== 'function') throw new Error('source must evaluate to a function');
  sandbox.__mintclaw_result = await sandbox.__mintclaw(sandbox.__mintclaw_facade);
  const encode = new vm.Script(
    '__mintclaw_encoded = JSON.stringify(__mintclaw_result === undefined ? null : __mintclaw_result)',
    { filename: 'browser-execute-result.js' },
  );
  encode.runInContext(contextified, { timeout: workerData.syncTimeoutMilliseconds });
  const encoded = sandbox.__mintclaw_encoded;
  if (encoded === undefined) throw new Error('result is not JSON serializable');
  parentPort.postMessage({ type: 'result', encoded });
}

main().catch(error => {
  parentPort.postMessage({ type: 'error', error: String(error && error.message || error).slice(0, 2048) });
});
