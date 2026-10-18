'use strict';

// Generic, runtime-private website steps. No selectors, values or page errors
// are returned to the model. Existing snapshot refs identify the live elements.
const REF = /^(?:f[1-9][0-9]{0,9})?e[1-9][0-9]{0,9}$/;
const STEPS = new Set(['credentials', 'username', 'password', 'totp', 'verify']);

function stepRefs(request) {
  if (!request || !STEPS.has(request.step)) return null;
  const keys = request.step === 'credentials' ? ['username_ref', 'password_ref', 'submit_ref'] :
    request.step === 'verify' ? ['authenticated_ref'] :
    [request.step === 'totp' ? 'otp_ref' : `${request.step}_ref`, 'submit_ref'];
  const refs = keys.map(key => request[key]);
  return refs.every(ref => typeof ref === 'string' && REF.test(ref)) && new Set(refs).size === refs.length ? keys : null;
}

function validOrigin(origin) {
  try {
    const url = new URL(origin);
    return url.protocol === 'https:' && url.origin === origin && !url.username && !url.password;
  } catch (_) { return false; }
}

function sameWebsite(url, request) {
  try {
    const entry = new URL(request.origin);
    const domain = request.website_domain || entry.hostname;
    const matches = hostname => hostname === domain || hostname.endsWith(`.${domain}`);
    return typeof domain === 'string' && domain.length <= 253 && /^[a-z0-9.-]+$/.test(domain) &&
      domain.includes('.') && matches(entry.hostname) && url.protocol === 'https:' &&
      !url.username && !url.password && matches(url.hostname);
  } catch (_) { return false; }
}

async function prepareWebsiteStep(page, request) {
  const keys = stepRefs(request);
  if (!keys || !validOrigin(request.origin) || !sameWebsite(new URL(page.url()), request) ||
      new URL(page.url()).origin !== request.origin) return null;
  const handles = [];
  let capsule;
  try {
    for (const key of keys) {
      const handle = await page.locator(`aria-ref=${request[key]}`).elementHandle({timeout: 500});
      if (!handle) return null;
      handles.push(handle);
    }
    capsule = await page.evaluateHandle(({origin, step, keys, handles}) => {
      const elements = Object.fromEntries(keys.map((key, index) => [key, handles[index]]));
      const originalDocument = document;
      const live = element => element instanceof HTMLElement && element.ownerDocument === originalDocument &&
        element.isConnected && element.getClientRects().length > 0 && !element.matches(':disabled') &&
        element.checkVisibility({checkOpacity: true, checkVisibilityCSS: true}) &&
        element.getAttribute('aria-disabled') !== 'true';
      const input = (element, types) => live(element) && element instanceof HTMLInputElement &&
        !element.readOnly && types.includes(element.type);
      const submit = elements.submit_ref;
      const form = submit?.form;
      const logout = () => {
        const element = elements.authenticated_ref;
        if (!live(element) || !(element instanceof HTMLAnchorElement || element instanceof HTMLButtonElement ||
            element.getAttribute('role') === 'button' || element.getAttribute('role') === 'menuitem')) return false;
        const label = (element.getAttribute('aria-label') || element.innerText || '').trim().replace(/\s+/g, ' ');
        return /^(?:log\s*out|sign\s*out|выйти|выход|cerrar sesión|déconnexion|abmelden)(?:$|\s)/iu.test(label);
      };
      const ready = () => {
        if (location.origin !== origin || document !== originalDocument) return false;
        if (step === 'verify') return logout();
        if (!live(submit) || !(submit instanceof HTMLButtonElement || submit instanceof HTMLInputElement ||
            submit.getAttribute('role') === 'button')) return false;
        if (['credentials', 'username'].includes(step) && !input(elements.username_ref, ['text', 'email'])) return false;
        if (['credentials', 'password'].includes(step) && !input(elements.password_ref, ['password'])) return false;
        if (step === 'totp' && !input(elements.otp_ref, ['text', 'tel', 'number', 'password'])) return false;
        if (submit.type === 'submit') {
          if (!(form instanceof HTMLFormElement) || !live(form)) return false;
          const method = submit.hasAttribute('formmethod') ? submit.formMethod : form.method;
          const target = submit.hasAttribute('formtarget') ? submit.formTarget : form.target;
          const action = new URL(submit.hasAttribute('formaction') ? submit.formAction : form.action);
          if (method.toLowerCase() !== 'post' || target !== '' || action.origin !== origin) return false;
          if (keys.some(key => key !== 'submit_ref' && elements[key].form !== form)) return false;
        } else if (submit.type !== 'button' && submit.getAttribute('role') !== 'button') return false;
        return true;
      };
      return ready() ? {elements, step, form, submit, ready} : null;
    }, {origin: request.origin, step: request.step, keys, handles});
    if (!(await capsule.evaluate(value => value !== null))) {
      await capsule.dispose();
      return null;
    }
    return capsule;
  } catch (_) {
    if (capsule) await capsule.dispose().catch(() => {});
    return null;
  } finally {
    await Promise.all(handles.map(handle => handle.dispose().catch(() => {})));
  }
}

// Both login paths use the same private field/event/recheck/submit operation.
async function submitCredentialForm(capsule, values) {
  return capsule.evaluate((state, values) => {
    if (!state?.ready()) return false;
    const fill = [];
    if (['credentials', 'username'].includes(state.step)) fill.push([state.elements.username_ref, values.username]);
    if (['credentials', 'password'].includes(state.step)) fill.push([state.elements.password_ref, values.password]);
    if (state.step === 'totp') fill.push([state.elements.otp_ref, values.password]);
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    for (const [element, value] of fill) {
      // Sites may clear dependent fields when the username changes. Finish
      // one field's events before filling the next, rechecking native refs
      // and the destination before exposing another value to the page.
      set.call(element, value);
      element.dispatchEvent(new Event('input', {bubbles: true}));
      element.dispatchEvent(new Event('change', {bubbles: true}));
      if (!state.ready() || element.value !== value) {
        for (const [target] of fill) set.call(target, '');
        return false;
      }
    }
    if (!state.ready() || fill.some(([element, value]) => element.value !== value)) {
      for (const [element] of fill) set.call(element, '');
      return false;
    }
    if (state.submit.type === 'submit') HTMLFormElement.prototype.requestSubmit.call(state.form, state.submit);
    else HTMLElement.prototype.click.call(state.submit);
    return true;
  }, values);
}

async function performWebsiteStep(page, capsule, request) {
  let attempted = false;
  try {
    if (!capsule || !stepRefs(request) || !validOrigin(request.origin)) return 'denied';
    if (request.step === 'verify') return await capsule.evaluate(state => state?.ready()) ? 'logged_in' : 'unverified';
    const bounded = (value, maximum) => typeof value === 'string' && value.length > 0 &&
      Buffer.byteLength(value) <= maximum && !/[\x00\r\n]/.test(value);
    if (['credentials', 'username'].includes(request.step) && !bounded(request.username, 1024)) return 'denied';
    if (['credentials', 'password'].includes(request.step) && !bounded(request.password, 4096)) return 'denied';
    if (request.step === 'totp' && !/^[0-9]{6,10}$/.test(request.password)) return 'denied';
    const timeout = Number.isInteger(request.timeout_ms) && request.timeout_ms >= 100 &&
      request.timeout_ms <= 10000 ? request.timeout_ms : 5000;
    const entryURL = page.url();
    const navigation = page.waitForURL(url => url.href !== entryURL,
      {timeout, waitUntil: 'domcontentloaded'}).catch(() => null);
    attempted = true;
    const dispatched = await submitCredentialForm(capsule, {username: request.username, password: request.password});
    if (!dispatched) throw new Error();
    await navigation;
    if (!sameWebsite(new URL(page.url()), request)) throw new Error();
    return 'submitted'; // A submit or redirect alone never certifies authentication.
  } catch (_) {
    if (attempted) await page.context().close().catch(() => {});
    return attempted ? 'outcome_unknown' : 'failed';
  } finally {
    if (request && typeof request === 'object') { request.username = ''; request.password = ''; }
    if (capsule) await capsule.dispose().catch(() => {});
  }
}

module.exports = {prepareWebsiteStep, performWebsiteStep, submitCredentialForm};
