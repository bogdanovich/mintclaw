'use strict';

// Runtime-private form handling. Return fixed outcomes only: never Playwright
// exceptions, page content, credential values, or authentication cookies.
const REF = /^(?:f[1-9][0-9]{0,9})?e[1-9][0-9]{0,9}$/;
const MARKER = /^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$/;
const {prepareWebsiteStep, performWebsiteStep} = require('./credential-steps.cjs');

function validOrigin(value) {
  try {
    const url = new URL(value);
    return url.protocol === 'https:' && !url.username && !url.password &&
      url.origin === value && url.pathname === '/' && !url.search && !url.hash;
  } catch (_) { return false; }
}

function validRequest(request) {
  return request && validOrigin(request.origin) &&
    ['username_ref', 'password_ref', 'submit_ref'].every(key => REF.test(request[key])) &&
    new Set([request.username_ref, request.password_ref, request.submit_ref]).size === 3;
}

// This check runs before vault retrieval, and again in the same document just
// before filling. No observation or snapshot is produced by either check.
async function prepareCredentialLogin(page, request) {
  if (request?.step) return prepareWebsiteStep(page, request);
  if (!validRequest(request) || new URL(page.url()).origin !== request.origin) return null;
  let capsule;
  const elements = [];
  try {
    // Resolve exact native refs once, before retrieving credentials. The
    // capsule retains these elements, never replacements with matching labels.
    for (const ref of [request.username_ref, request.password_ref, request.submit_ref]) {
      const element = await page.locator(`aria-ref=${ref}`).elementHandle({ timeout: 500 });
      if (!element) return null;
      elements.push(element);
    }
    capsule = await page.evaluateHandle(({ origin, elements }) => {
      if (location.origin !== origin) return null;
      const [username, password, submit] = elements;
      const form = username.form;
      const writable = input => input instanceof HTMLInputElement && input.isConnected &&
        !input.disabled && !input.matches(':disabled') && !input.readOnly &&
        !['true'].includes(input.getAttribute('aria-disabled'));
      const postDestination = () => {
        const method = submit.hasAttribute('formmethod') ? submit.formMethod : form.method;
        const target = submit.hasAttribute('formtarget') ? submit.formTarget : form.target;
        const action = new URL(submit.hasAttribute('formaction') ? submit.formAction : form.action);
        return method.toLowerCase() === 'post' && target === '' &&
          action.origin === origin && action.protocol === 'https:';
      };
      const ready = () => location.origin === origin && document === username.ownerDocument &&
        writable(username) && writable(password) && ['text', 'email'].includes(username.type) &&
        password.type === 'password' && form instanceof HTMLFormElement && form.isConnected &&
        password.form === form && submit.form === form && submit.isConnected &&
        !submit.disabled && !submit.matches(':disabled') && submit.type === 'submit' &&
        (submit instanceof HTMLButtonElement || submit instanceof HTMLInputElement) &&
        postDestination();
      return ready() ? { username, password, submit, form, ready } : null;
    }, { origin: request.origin, elements });
    if (!(await capsule.evaluate(value => value !== null))) {
      await capsule.dispose();
      return null;
    }
    return capsule;
  } catch (_) {
    if (capsule) await capsule.dispose().catch(() => {});
    return null;
  } finally {
    await Promise.all(elements.map(element => element.dispose().catch(() => {})));
  }
}

async function performCredentialLogin(page, capsule, request) {
  if (request?.step) return performWebsiteStep(page, capsule, request);
  let attempted = false;
  try {
    if (!capsule || !validRequest(request) || typeof request.username !== 'string' ||
        typeof request.password !== 'string' || !request.username || !request.password ||
        Buffer.byteLength(request.username) > 1024 || Buffer.byteLength(request.password) > 4096 ||
        /[\x00\r\n]/.test(request.username) || /[\x00\r\n]/.test(request.password)) return 'denied';
    const timeout = Number.isInteger(request.timeout_ms) && request.timeout_ms >= 100 &&
      request.timeout_ms <= 15000 ? request.timeout_ms : 10000;
    const entryURL = page.url();
    const navigation = page.waitForURL(url => url.origin === request.origin &&
      url.href !== entryURL, { timeout, waitUntil: 'domcontentloaded' }).catch(() => null);
    attempted = true;
    const dispatched = await capsule.evaluate((state, values) => {
      if (!state || !state.ready()) return false;
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      set.call(state.username, values.username);
      set.call(state.password, values.password);
      // Changes may update trusted site state. Recheck the live targets after
      // listeners run, before dispatching the single native submit operation.
      state.username.dispatchEvent(new Event('input', { bubbles: true }));
      state.password.dispatchEvent(new Event('input', { bubbles: true }));
      if (!state.ready() || state.username.value !== values.username || state.password.value !== values.password) {
        set.call(state.username, ''); set.call(state.password, '');
        return false;
      }
      HTMLFormElement.prototype.requestSubmit.call(state.form, state.submit);
      return true;
    }, { username: request.username, password: request.password });
    if (!dispatched) {
      await page.context().close().catch(() => {});
      return 'denied';
    }
    await navigation;
    if (new URL(page.url()).origin !== request.origin) throw new Error();
    // Successful submission is not proof of authentication. A configured
    // outcome marker is required to claim login or an MFA handoff.
    if (typeof request.success_url === 'string' && MARKER.test(request.success_marker_id) &&
        page.url() === request.success_url && await page.locator(`[id="${request.success_marker_id}"]`).isVisible()) {
      return 'logged_in';
    }
    if (typeof request.handoff_url === 'string' && MARKER.test(request.handoff_marker_id) &&
        page.url() === request.handoff_url && await page.locator(`[id="${request.handoff_marker_id}"]`).isVisible()) {
      return 'handoff_required';
    }
    throw new Error();
  } catch (_) {
    if (attempted) {
      await capsule.evaluate(state => {
        if (!state) return;
        const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
        set.call(state.username, ''); set.call(state.password, '');
      }).catch(() => {});
      await page.context().close().catch(() => {});
    }
    return attempted ? 'outcome_unknown' : 'failed';
  } finally {
    if (request && typeof request === 'object') {
      request.username = ''; request.password = '';
    }
    if (capsule) await capsule.dispose().catch(() => {});
  }
}

module.exports = { prepareCredentialLogin, performCredentialLogin };
