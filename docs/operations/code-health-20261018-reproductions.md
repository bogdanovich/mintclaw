# Second Code Health Sweep Reproductions

These probes reproduce the two findings in the [October 18 assessment](../architecture/reliability-code-health-20261018-sweep.md)
on commit `60beb5641b499ee0b2c3c87c1f61d8392b4ad11c`. Both use disposable synthetic fixtures.
Their failing assertions express the proposed corrected behavior. Run them in an isolated checkout;
remove the temporary test files afterward. They are assessment evidence, not merged regression tests.

## Persistent Computer Cleanup Failure

Save the following as `pkg/nodes/computerhost/second_sweep_cleanup_probe_test.go`, then run:

```sh
go test -tags=goolm,stdjson ./pkg/nodes/computerhost \
  -run '^TestSecondSweepTerminalStatusRequiresConfirmedCleanup$' -count=1 -timeout=30s
```

Observed: the assertion fails with `control=released worker_closed=false retired=false`.
The fixture restores its real closer before teardown, so the test does not leave a worker behind.

```go
package computerhost

import (
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

type secondSweepFailClose struct{ captureWorker }

func (*secondSweepFailClose) Close() error { return errors.New("synthetic persistent cleanup failure") }

func TestSecondSweepTerminalStatusRequiresConfirmedCleanup(t *testing.T) {
	host, app := nativeHostFixture(t, nil)
	session := openFixture(t, host)
	entry := host.sessions[session.Binding.SessionID]
	original := entry.driver.worker
	entry.driver.worker = &secondSweepFailClose{captureWorker: original}
	t.Cleanup(func() { entry.driver.worker = original })
	_, err := host.Control(t.Context(), nodes.ComputerHostControlRequest{
		ComputerHostStatusRequest: nodes.ComputerHostStatusRequest{
			Principal: fixturePrincipal(), SessionID: session.Binding.SessionID,
		},
		Generation: session.Binding.Generation, Event: computer.EventRelease,
	})
	if err == nil {
		t.Fatal("release fixture did not retain its cleanup error")
	}
	status, err := host.Status(t.Context(), nodes.ComputerHostStatusRequest{
		Principal: fixturePrincipal(), SessionID: session.Binding.SessionID,
	})
	if err == nil {
		t.Fatalf("terminal status claimed success with cleanup still failing: control=%s worker_closed=%t retired=%t",
			status.Session.Control, app.workers[0].closed.Load(), entry.retired.Load())
	}
}
```

## Fixed-Alias And Generic Login Event Ordering

Install the frozen private-sidecar dependencies with `npm ci --ignore-scripts --no-audit --no-fund`
in `runtime/browser/playwright-library`. Use its pinned Playwright Chromium installation.
Save the following there as `second-sweep-login-probe.test.cjs`, then run:

```sh
node --test second-sweep-login-probe.test.cjs
```

Observed: the fixed-alias case fails with `denied` instead of `logged_in`; the generic case passes
with one submission. The fixture intercepts every browser request and uses synthetic values.
It does not contact or authenticate to a real website. This establishes a difference between the
existing supported paths; it does not establish when the fixed-alias defect was introduced.

```javascript
'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { chromium } = require('playwright');
const { prepareCredentialLogin, performCredentialLogin } = require('./credential-login.cjs');

const origin = 'https://credential-fixture.example.org';
const username = 'private-fixture-account@example.org';
const password = 'private-browser-fixture-A9&secret';
const request = refs => ({ origin, ...refs,
  username, password, success_url: `${origin}/home`, success_marker_id: 'signed-in', timeout_ms: 1000 });

async function fixture(t, extra = '') {
  const browser = await chromium.launch({ headless: true,
    executablePath: process.env.MINTCLAW_BROWSER_TEST_EXECUTABLE || undefined });
  t.after(() => browser.close());
  const context = await browser.newContext();
  const page = await context.newPage();
  let submissions = 0;
  await context.route('**/*', async route => {
    const incoming = route.request(), url = new URL(incoming.url());
    assert.equal(url.origin, origin, 'unexpected fixture destination');
    if (url.pathname === '/login') {
      await route.fulfill({ contentType: 'text/html', body: `<form method="post" action="/session">
        <input type="email" name="username" aria-label="Account">
        <input type="password" name="password" aria-label="Password">
        <button type="submit">Sign in</button>
        </form>${extra}` });
    } else if (url.pathname === '/session') {
      submissions++;
      const fields = new URLSearchParams(incoming.postData());
      assert.equal(fields.get('username') === username, true, 'username injection mismatch');
      assert.equal(fields.get('password') === password, true, 'password injection mismatch');
      await route.fulfill({ contentType: 'text/html', body: '<script>location.replace("/home")</script>' });
    } else {
      await route.fulfill({ contentType: 'text/html', body: '<div id="signed-in">Ready</div>' });
    }
  });
  await page.goto(`${origin}/login`);
  const tree = await page.locator(':root').ariaSnapshotJSON({ mode: 'ai' });
  const nodes = tree.flatMap(function flatten(node) {
    return typeof node === 'string' ? [] : [node, ...(node.children || []).flatMap(flatten)];
  });
  const refs = Object.fromEntries([['username_ref', 'Account'], ['password_ref', 'Password'], ['submit_ref', 'Sign in']]
    .map(([key, name]) => [key, nodes.find(node => node.name === name).ref]));
  return { page, context, refs, submissions: () => submissions };
}


for (const mode of ['fixed-alias', 'generic']) {
  test(`second sweep: ${mode} preserves password after username events`, {timeout: 30000}, async t => {
    const f = await fixture(t, `<script>
      document.querySelector('[name="username"]').addEventListener('input', () => {
        document.querySelector('[name="password"]').value = '';
      });
    </script>`);
    const values = request(f.refs);
    if (mode === 'generic') values.step = 'credentials';
    const capsule = await prepareCredentialLogin(f.page, values);
    assert.ok(capsule);
    const result = await performCredentialLogin(f.page, capsule, values);
    assert.equal(result, mode === 'generic' ? 'submitted' : 'logged_in');
    assert.equal(f.submissions(), 1);
  });
}
```
