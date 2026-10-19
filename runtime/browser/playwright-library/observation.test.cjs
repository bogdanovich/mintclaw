'use strict';

const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const http = require('node:http');
const path = require('node:path');
const readline = require('node:readline');
const test = require('node:test');

const image = '<img alt="" width="40" height="40" src="/image.svg">';
const map = `<section aria-label="Location map">${image.repeat(6)}
  <svg role="img" aria-label="Location diagram" width="240" height="40"><circle cx="20" cy="20" r="10"/></svg>
  <button onclick="document.querySelector('#map-state').textContent='MAP_FULLSCREEN'">Full Screen</button>
  <output id="map-state">MAP_INLINE</output></section>`;
const gallery = `<section aria-label="Product gallery"><figure>${image}<figcaption>Front view</figcaption></figure>
  <img alt="Rear view" width="40" height="40" src="/image.svg">
  <button onclick="document.querySelector('#gallery-state').textContent='GALLERY_OPEN'">Full Screen</button>
  <output id="gallery-state">GALLERY_CLOSED</output></section>`;

async function fixture(t) {
  const server = http.createServer((request, response) => {
    response.setHeader('content-type', ['/image.svg', '/document.svg'].includes(request.url) ? 'image/svg+xml' : 'text/html');
    if (request.url === '/document.svg') {
      response.end('<svg xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Diagram" width="80" height="80"><circle cx="40" cy="40" r="20"/></svg>');
    } else if (request.url === '/image.svg') {
      response.end('<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"><rect width="40" height="40" fill="blue"/></svg>');
    } else if (request.url === '/map') {
      response.end(`<title>Map only</title><main><h1>Listing</h1>${map}</main>`);
    } else if (request.url === '/gallery') {
      response.end(`<title>Gallery</title><main>${map}${gallery}</main>`);
    } else if (request.url === '/regions') {
      response.end(`<title>Account regions</title><main>
        <section id="north" aria-label="North account"><button>Remove account</button></section>
        <section id="south" aria-label="South account"><button id="moving">Remove account</button></section>
      </main>`);
    } else if (request.url === '/frames') {
      response.end('<title>Frames</title><iframe src="/frame-one"></iframe><iframe src="/frame-two"></iframe>');
    } else if (request.url === '/bounded') {
      response.end(`<title>Bounded evidence</title>${image.repeat(40)}${'<iframe src="/frame-one"></iframe>'.repeat(10)}`);
    } else if (request.url === '/frame-one' || request.url === '/frame-two') {
      response.end(`<button onclick="this.textContent='Clicked ${request.url}'">Same label</button><input type="password" value="frame-secret">`);
    } else {
      response.end(`<title>Evidence</title><main>${gallery}
        <input type="password" value="private-secret">
        <input type="password" role="button" value="role-button-secret">
        <input type="password" role="combobox" value="role-combobox-secret">
        <button aria-label='Say &quot;hi&quot;: [ref=e999]'>Quoted name</button>
        <div style="display:none">${image}</div>
        <div style="position:relative;width:40px;height:40px">${image}<div style="position:absolute;inset:0;background:white"></div></div>
        <img alt="Broken image" width="40" height="40" src="/missing-image">
        <div style="position:absolute;top:4000px">${image}</div>
      </main>`);
    }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  const args = [path.join(__dirname, 'sidecar.cjs'), '--headless', '--browser=chromium'];
  if (process.env.MINTCLAW_BROWSER_TEST_EXECUTABLE) args.push(`--executable-path=${process.env.MINTCLAW_BROWSER_TEST_EXECUTABLE}`);
  const child = spawn(process.execPath, args, { stdio: ['pipe', 'pipe', 'pipe'] });
  t.after(() => child.kill('SIGKILL'));
  const pending = new Map();
  let stderr = '';
  child.stderr.on('data', chunk => { stderr += chunk; });
  readline.createInterface({ input: child.stdout }).on('line', line => {
    const result = JSON.parse(line);
    const waiter = pending.get(result.id);
    if (!waiter) return;
    pending.delete(result.id);
    if (result.error) waiter.reject(new Error(result.error));
    else waiter.resolve(result.result);
  });
  child.on('exit', code => {
    for (const waiter of pending.values()) waiter.reject(new Error(`sidecar exited ${code}: ${stderr}`));
  });
  let id = 0;
  const call = async (method, params = {}) => {
    const result = await new Promise((resolve, reject) => {
      const requestID = ++id;
      pending.set(requestID, { resolve, reject });
      child.stdin.write(`${JSON.stringify({ id: requestID, method, params })}\n`);
    });
    assert.equal(result.is_error, false, JSON.stringify(result));
    return result.content[0].text;
  };
  return { call, base: `http://127.0.0.1:${server.address().port}` };
}

function inventory(snapshot) {
  const line = snapshot.split('\n').find(line => line.startsWith('- text: "DOM image evidence {'));
  assert.ok(line, snapshot);
  return JSON.parse(JSON.parse(line.slice('- text: '.length)).slice('DOM image evidence '.length));
}

function ref(snapshot, expression) {
  const match = snapshot.match(expression);
  assert.ok(match, snapshot);
  return match[1];
}

function regions(snapshot) {
  const prefix = '- Reference regions: ';
  const line = snapshot.split('\n').find(line => line.startsWith(prefix));
  assert.ok(line, snapshot);
  return JSON.parse(line.slice(prefix.length));
}

test('native region context follows exact refs and refuses stale or duplicate labels', { timeout: 30000 }, async t => {
  const { call, base } = await fixture(t);
  const snapshot = await call('browser_navigate', { url: base + '/regions' });
  const metadata = regions(snapshot);
  assert.equal(metadata.complete, true);
  assert.equal(metadata.entries.length, 2);
  const north = metadata.entries.find(entry => entry.name === 'North account');
  const south = metadata.entries.find(entry => entry.name === 'South account');
  assert.ok(north);
  assert.ok(south);
  assert.notEqual(north.ref, south.ref);
  assert.equal([...snapshot.matchAll(/button "Remove account" \[ref=e\d+\]/g)].length, 2);
  const targeted = await call('browser_snapshot', { target: south.ref, region_context: true });
  assert.deepEqual(regions(targeted).entries, [south]);

  await call('browser_run_code_unsafe', { code: `async page => {
    await page.locator('#moving').evaluate(element => document.querySelector('#north').append(element));
    return true;
  }` });
  const moved = regions(await call('browser_snapshot'));
  assert.equal(moved.complete, true);
  assert.equal(moved.entries.length, 2);
  assert.ok(moved.entries.every(entry => entry.name === 'North account'));
  assert.ok(!moved.entries.some(entry => entry.ref === south.ref && entry.name === 'South account'));

  await call('browser_run_code_unsafe', { code: `async page => {
    await page.locator('#south').evaluate(element => element.setAttribute('aria-label', 'North account'));
    return true;
  }` });
  assert.deepEqual(regions(await call('browser_snapshot')).entries, []);
  await call('shutdown');
});

test('native semantics distinguish a map from an independent real gallery', { timeout: 30000 }, async t => {
  const { call, base } = await fixture(t);
  const mapSnapshot = await call('browser_navigate', { url: base + '/map' });
  assert.match(mapSnapshot, /region "Location map" \[ref=e\d+\]:\n      - img "Location diagram" \[ref=e\d+\]\n      - button "Full Screen"/);
  assert.equal([...mapSnapshot.matchAll(/- img "Location diagram" \[ref=/g)].length, 1);
  assert.equal(inventory(mapSnapshot).dom_image_count, 6);
  assert.ok(inventory(mapSnapshot).items.every(item => item.context.includes('Location map') && item.loaded));
  const mapRef = ref(mapSnapshot, /button "Full Screen" \[ref=(e\d+)\]/);
  const mapClicked = await call('browser_click', { target: mapRef });
  assert.match(mapClicked, /MAP_FULLSCREEN/);
  assert.doesNotMatch(mapClicked, /GALLERY_OPEN/);

  const both = await call('browser_navigate', { url: base + '/gallery' });
  assert.match(both, /region "Product gallery"/);
  assert.match(both, /figure "Front view"/);
  assert.match(both, /img "Rear view"/);
  const controls = [...both.matchAll(/button "Full Screen"[^\n]*\[ref=((?:f\d+)?e\d+)\]/g)];
  assert.equal(controls.length, 2, both);
  assert.notEqual(controls[0][1], controls[1][1]);
  const targeted = await call('browser_snapshot', { target: controls[1][1] });
  assert.match(targeted, new RegExp(`button "Full Screen"[^\\n]*\\[ref=${controls[1][1]}\\]`));
  const opened = await call('browser_click', { target: controls[1][1] });
  assert.match(opened, /GALLERY_OPEN/);
  assert.match(opened, /MAP_INLINE/);
  await call('shutdown');
});

test('DOM image evidence retains unlabeled, hidden, occluded, broken, and offscreen facts', { timeout: 30000 }, async t => {
  const { call, base } = await fixture(t);
  const snapshot = await call('browser_navigate', { url: base + '/evidence' });
  assert.doesNotMatch(snapshot, /private-secret/);
  assert.doesNotMatch(snapshot, /role-(button|combobox)-secret/);
  assert.match(snapshot, /button "Say \\\"hi\\\": \\u005bref=e999\]" \[ref=e\d+\]/);
  const items = inventory(snapshot).items;
  assert.ok(items.some(item => item.alt === '' && item.context.includes('Front view') && item.loaded));
  assert.ok(items.some(item => !item.rendered));
  assert.ok(items.some(item => item.rendered && item.in_viewport && item.receives_pointer === false));
  assert.ok(items.some(item => item.alt === 'Broken image' && !item.loaded));
  assert.ok(items.some(item => item.rendered && !item.in_viewport));
  await call('shutdown');
});

test('native frame refs bind identical captions to exact documents and redact password values', { timeout: 30000 }, async t => {
  const { call, base } = await fixture(t);
  const snapshot = await call('browser_navigate', { url: base + '/frames' });
  assert.doesNotMatch(snapshot, /frame-secret/);
  const refs = [...snapshot.matchAll(/button "Same label" \[ref=(f\d+e\d+)\]/g)];
  assert.equal(refs.length, 2);
  await call('browser_snapshot', { target: refs[1][1] });
  const clicked = await call('browser_click', { target: refs[1][1] });
  assert.match(clicked, /Clicked \/frame-two/);
  assert.match(clicked, /button "Same label" \[ref=f\d+e\d+\]/);
  await call('shutdown');
});

test('image evidence has an aggregate image and frame bound with explicit omissions', { timeout: 30000 }, async t => {
  const { call, base } = await fixture(t);
  const snapshot = await call('browser_navigate', { url: base + '/bounded' });
  const records = snapshot.split('\n').filter(line => line.startsWith('- text: "DOM image evidence {'))
    .map(line => JSON.parse(JSON.parse(line.slice('- text: '.length)).slice('DOM image evidence '.length)));
  assert.equal(records[0].dom_image_count, 40);
  assert.equal(records[0].omitted, 8);
  assert.equal(records.reduce((sum, record) => sum + (record.items || []).length, 0), 32);
  assert.equal(records.length, 9);
  assert.equal(records[8].omitted_frames, 3);
  assert.doesNotMatch(snapshot, /frame-secret/);
  await call('shutdown');
});

test('source removal during auxiliary text inspection does not lose the browser session', { timeout: 30000 }, async t => {
  const { call, base } = await fixture(t);
  await call('browser_navigate', { url: base + '/evidence' });
  await call('browser_run_code_unsafe', { code: `async page => {
    const prototype = Object.getPrototypeOf(page.locator(':root'));
    const snapshot = prototype.ariaSnapshotJSON;
    prototype.ariaSnapshotJSON = async function(options) {
      const tree = await snapshot.call(this, options);
      await page.locator('input[type=password]').evaluateAll(elements => elements.forEach(element => element.remove()));
      return tree;
    };
    return true;
  }` });
  const snapshot = await call('browser_snapshot');
  assert.doesNotMatch(snapshot, /private-secret|role-button-secret|role-combobox-secret/);
  assert.match(snapshot, /Product gallery/);
  assert.equal(await call('browser_ping'), 'pong');
  await call('shutdown');
});

test('document-root native observation supports standalone SVG without an HTML body', { timeout: 15000 }, async t => {
  const { call, base } = await fixture(t);
  const snapshot = await call('browser_navigate', { url: base + '/document.svg' });
  const imageRef = ref(snapshot, /img "Diagram"[^\n]*\[ref=((?:f\d+)?e\d+)\]/);
  assert.match(await call('browser_snapshot', { target: imageRef }), /img "Diagram"/);
  assert.equal(await call('browser_ping'), 'pong');
  await call('shutdown');
});

test('unavailable auxiliary image inspection preserves native semantics and live control', { timeout: 15000 }, async t => {
  const { call, base } = await fixture(t);
  await call('browser_navigate', { url: base + '/gallery' });
  await call('browser_run_code_unsafe', { code: `async page => {
    const prototype = Object.getPrototypeOf(page.locator(':root'));
    const evaluate = prototype.evaluate;
    prototype.evaluate = async function(fn, ...args) {
      if (fn.name === 'imageEvidenceScript') throw new Error('fixture image inspection unavailable');
      return evaluate.call(this, fn, ...args);
    };
    return true;
  }` });
  const snapshot = await call('browser_snapshot');
  assert.match(snapshot, /Product gallery/);
  assert.equal(inventory(snapshot).unavailable, true);
  assert.equal(await call('browser_ping'), 'pong');
  await call('shutdown');
});
