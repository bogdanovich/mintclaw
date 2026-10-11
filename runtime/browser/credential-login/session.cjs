'use strict';

// Private driver component. Startup must own and qualify context and lease;
// neither value comes from an ordinary browser handle or a model argument.
const { performance } = require('node:perf_hooks');
const MAX_LEASE_MS = 20 * 60 * 60 * 1000;
const DENIED = Object.freeze({ code: 'denied' });
const UNKNOWN = Object.freeze({ code: 'outcome_unknown' });

function checkedFlow(input, origins) {
  const keys = ['navigation_url', 'entry_origin', 'form_id', 'username_id', 'password_id',
    'username_type', 'submit_url', 'success_url', 'success_marker_id'];
  if (!input || Object.getPrototypeOf(input) !== Object.prototype ||
      Object.keys(input).some(key => !keys.includes(key) && !['handoff_url', 'handoff_marker_id'].includes(key)) ||
      keys.some(key => typeof input[key] !== 'string' || !input[key])) throw new Error();
  for (const key of ['handoff_url', 'handoff_marker_id'])
    if (input[key] !== undefined && typeof input[key] !== 'string') throw new Error();
  const flow = { ...input, handoff_url: input.handoff_url || '', handoff_marker_id: input.handoff_marker_id || '' };
  if (!Array.isArray(origins) || !origins.length || origins.length > 16 ||
      new Set(origins).size !== origins.length || !origins.includes(flow.entry_origin) ||
      !['text', 'email'].includes(flow.username_type)) throw new Error();
  for (const id of [flow.form_id, flow.username_id, flow.password_id, flow.success_marker_id])
    if (!/^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$/.test(id)) throw new Error();
  if (!!flow.handoff_url !== !!flow.handoff_marker_id ||
      (flow.handoff_marker_id && !/^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$/.test(flow.handoff_marker_id))) throw new Error();
  if (new Set([flow.form_id, flow.username_id, flow.password_id]).size !== 3) throw new Error();
  for (const origin of origins) {
    if (typeof origin !== 'string') throw new Error();
    const url = new URL(origin);
    if (url.protocol !== 'https:' || url.origin !== origin || url.href !== `${origin}/` || url.hostname.endsWith('.')) throw new Error();
  }
  for (const key of ['navigation_url', 'submit_url', 'success_url', 'handoff_url']) {
    if (key === 'handoff_url' && !flow[key]) continue;
    const raw = flow[key], url = new URL(raw);
    if (raw.length > 2048 || url.href !== raw || url.protocol !== 'https:' || url.username || url.password ||
        /[?#\\%]/.test(raw) || !/^[A-Za-z0-9/_~.-]*$/.test(url.pathname) ||
        !origins.includes(url.origin) || ((key === 'navigation_url' || key === 'submit_url') && url.origin !== flow.entry_origin)) throw new Error();
  }
  if (flow.navigation_url === flow.success_url || flow.success_url === flow.handoff_url ||
      flow.navigation_url === flow.handoff_url) throw new Error();
  return Object.freeze(flow);
}

// check receives only operation/origin metadata. It must check the full current
// authenticated principal, account, policy, and original grant generation in
// the private broker. Resolving with undefined admits; booleans do not approve.
// The signal independently retires this context even when no calls arrive.
function createSession(context, lease, input, origins) {
  let flow;
  try {
    flow = checkedFlow(input, origins);
    if (!context || !(lease.signal instanceof AbortSignal) || typeof lease.check !== 'function' ||
        lease.signal.aborted || !Number.isSafeInteger(lease.remainingMS) ||
        lease.remainingMS <= 0 || lease.remainingMS > MAX_LEASE_MS) throw new Error();
  } catch (_) { throw new Error('credential browser unavailable'); }
  const permittedOrigins = new Set(origins), started = performance.now(), remaining = lease.remainingMS;
  const signal = lease.signal, check = lease.check;
  let page, authority, stage = 'new', busy = false, closing, submissionRequest = false, outcomeRequest = false;
  const retired = () => !!closing || signal.aborted || performance.now() - started >= remaining;
  const close = () => {
    if (closing) return closing;
    stage = 'closed';
    clearTimeout(timer);
    signal.removeEventListener('abort', onAbort);
    closing = Promise.resolve().then(async () => {
      let deadline;
      try {
        await Promise.race([context.close(), new Promise((_, reject) => {
          deadline = setTimeout(() => reject(new Error()), 3000);
        })]);
        return Object.freeze({ code: 'closed' });
      } catch (_) {
        stage = 'quarantined';
        return Object.freeze({ code: 'quarantined' });
      } finally { clearTimeout(deadline); authority = undefined; }
    });
    return closing;
  };
  const onAbort = () => { void close(); };
  const timer = setTimeout(onAbort, remaining);
  signal.addEventListener('abort', onAbort, { once: true });
  context.on('close', onAbort);
  if (signal.aborted) void close();
  async function admit(operation, origin) {
    if (retired() || !permittedOrigins.has(origin)) throw new Error();
    let deadline;
    try {
      const result = await Promise.race([check(Object.freeze({ operation, origin })),
        new Promise((_, reject) => { deadline = setTimeout(() => reject(new Error()), 1000); })]);
      if (result !== undefined || retired()) throw new Error();
    } finally { clearTimeout(deadline); }
  }
  async function run(expected, operation) {
    if (busy || retired() || stage !== expected) return DENIED;
    busy = true;
    try {
      const result = await operation();
      if (retired()) throw new Error();
      return result;
    }
    catch (_) { await close(); return UNKNOWN; }
    finally { busy = false; }
  }
  return Object.freeze({
    prepare: () => run('new', async () => {
      await admit('navigate', flow.entry_origin);
      await context.route('**/*', async route => {
        try {
          const request = route.request(), target = new URL(request.url());
          if (target.protocol !== 'https:' || target.username || target.password) throw new Error();
          // Once values enter the page, admit one native form POST and one
          // outcome navigation only. A submit listener can change DOM policy
          // after requestSubmit's precheck; enforce the actual request here.
          if (!['new', 'prepared'].includes(stage)) {
            if (stage !== 'submitting' || !request.isNavigationRequest() || request.frame() !== page.mainFrame()) throw new Error();
            if (!submissionRequest) {
              if (target.href !== flow.submit_url || request.method() !== 'POST' ||
                  request.headers()['content-type'] !== 'application/x-www-form-urlencoded') throw new Error();
              submissionRequest = true;
            } else {
              if (outcomeRequest || request.method() !== 'GET' || request.postData() !== null ||
                  (target.href !== flow.success_url && target.href !== flow.handoff_url)) throw new Error();
              outcomeRequest = true;
            }
          }
          await admit('network', target.origin);
          await route.fallback();
        } catch (_) { await route.abort('blockedbyclient').catch(() => {}); void close(); }
      });
      await context.routeWebSocket(/.*/, async route => {
        await route.close({ code: 1008, reason: 'denied' }).catch(() => {});
      });
      context.on('page', opened => {
        if (page && opened !== page) { void opened.close().catch(() => {}); void close(); }
        else page = opened;
      });
      page = await context.newPage();
      page.on('crash', onAbort);
      page.on('frameattached', onAbort);
      page.on('framenavigated', frame => {
        if (frame === page.mainFrame() && ['prepared', 'injecting', 'filled', 'active'].includes(stage)) onAbort();
      });
      page.on('download', onAbort);
      page.on('dialog', dialog => { void dialog.dismiss().catch(() => {}); });
      await page.goto(flow.navigation_url, { waitUntil: 'domcontentloaded', timeout: 10000 });
      await admit('prepare', new URL(page.url()).origin);
      authority = await page.evaluateHandle(flow => {
        const refs = { document, form: document.getElementById(flow.form_id),
          username: document.getElementById(flow.username_id), password: document.getElementById(flow.password_id) };
        refs.valid = () => {
          const matches = id => [...document.querySelectorAll('[id]')].filter(node => node.id === id);
          const form = refs.form, username = refs.username, password = refs.password;
          return refs.document === document && location.origin === flow.entry_origin &&
            location.href === flow.navigation_url && form instanceof HTMLFormElement &&
            username instanceof HTMLInputElement && password instanceof HTMLInputElement &&
            form.isConnected && username.isConnected && password.isConnected &&
            form.ownerDocument === document && username.ownerDocument === document && password.ownerDocument === document &&
            matches(flow.form_id).length === 1 && matches(flow.username_id).length === 1 && matches(flow.password_id).length === 1 &&
            matches(flow.form_id)[0] === form && matches(flow.username_id)[0] === username && matches(flow.password_id)[0] === password &&
            username.form === form && password.form === form && form.method.toLowerCase() === 'post' &&
            form.action === flow.submit_url && form.target === '' && form.enctype === 'application/x-www-form-urlencoded' &&
            username.type === flow.username_type && password.type === 'password' &&
            !username.matches(':disabled') && !password.matches(':disabled') && !username.readOnly && !password.readOnly &&
            !username.hidden && !password.hidden && username.getClientRects().length > 0 && password.getClientRects().length > 0 &&
            getComputedStyle(username).visibility !== 'hidden' && getComputedStyle(password).visibility !== 'hidden';
        };
        return refs;
      }, flow);
      // Evaluate in the current page realm. An old handle's own realm could
      // belong to an inactive document; it cannot establish current authority.
      if (!await page.evaluate(refs => refs.valid(), authority)) throw new Error();
      await admit('prepare', new URL(page.url()).origin);
      stage = 'prepared';
      return Object.freeze({ code: 'prepared' });
    }),
    ready: () => run('prepared', async () => {
      await admit('retrieve', new URL(page.url()).origin);
      if (!await page.evaluate(refs => refs.valid(), authority)) throw new Error();
      await admit('retrieve', new URL(page.url()).origin);
      return Object.freeze({ code: 'prepared' });
    }),
    inject: (username, password) => run('prepared', async () => {
      let user, pass;
      try {
        if (!Buffer.isBuffer(username) || !Buffer.isBuffer(password) || username.length < 1 || username.length > 1024 ||
            password.length < 1 || password.length > 4096) throw new Error();
        user = Buffer.from(username); pass = Buffer.from(password);
        const decoder = new TextDecoder('utf-8', { fatal: true });
        const credentials = { username: decoder.decode(user), password: decoder.decode(pass) };
        if (/[\x00\r\n]/.test(credentials.username + credentials.password)) throw new Error();
        await admit('inject', new URL(page.url()).origin);
        stage = 'injecting';
        const filled = await page.evaluate(({ refs, credentials }) => {
          if (!refs.valid()) return false;
          const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
          setter.call(refs.username, credentials.username);
          if (!refs.valid()) return false;
          setter.call(refs.password, credentials.password);
          refs.username.dispatchEvent(new Event('input', { bubbles: true }));
          if (!refs.valid()) return false;
          refs.password.dispatchEvent(new Event('input', { bubbles: true }));
          return refs.valid();
        }, { refs: authority, credentials });
        credentials.username = ''; credentials.password = '';
        if (!filled) throw new Error();
        await admit('inject', new URL(page.url()).origin);
        stage = 'filled';
        return Object.freeze({ code: 'filled' });
      } finally { if (user) user.fill(0); if (pass) pass.fill(0); }
    }),
    submit: () => run('filled', async () => {
      // check must independently admit browser login submission, not only the
      // credential grant. No boolean supplied by the model crosses this path.
      await admit('submit', new URL(page.url()).origin);
      stage = 'submitting';
      const submitted = await page.evaluate(refs => {
        if (!refs.valid()) return false;
        HTMLFormElement.prototype.requestSubmit.call(refs.form);
        return true;
      }, authority);
      if (!submitted) throw new Error();
      await page.waitForURL(url => url.href === flow.success_url ||
        (flow.handoff_url && url.href === flow.handoff_url), { waitUntil: 'domcontentloaded', timeout: 10000 });
      const current = page.url();
      await admit('outcome', new URL(current).origin);
      if (!submissionRequest || (current !== flow.success_url && current !== flow.handoff_url)) throw new Error();
      const confirmed = await page.evaluate(flow => {
        const id = location.href === flow.success_url ? flow.success_marker_id :
          location.href === flow.handoff_url ? flow.handoff_marker_id : '';
        const matches = [...document.querySelectorAll('[id]')].filter(node => node.id === id);
        return id !== '' && matches.length === 1 && matches[0] instanceof HTMLElement &&
          matches[0].isConnected && matches[0].getClientRects().length > 0 &&
          getComputedStyle(matches[0]).visibility === 'visible';
      }, flow);
      if (!confirmed || page.url() !== current) throw new Error();
      await admit('outcome', new URL(page.url()).origin);
      stage = 'active';
      await authority.dispose(); authority = undefined;
      return Object.freeze({ code: current === flow.success_url ? 'logged_in' : 'handoff_required' });
    }),
    close,
  });
}

module.exports = { createSession };
