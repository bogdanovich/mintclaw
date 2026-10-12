"use strict";
const assert = require("node:assert/strict");
const https = require("node:https");
const fs = require("node:fs/promises");
const path = require("node:path");
const {chromium} = require("../../../runtime/browser/playwright-library/node_modules/playwright");
const origin = "https://approval.example.org";
let stage = "fixture-input";

(async () => {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  const config = JSON.parse(Buffer.concat(chunks).toString("utf8"));
  const pagePath = config.page_path;
  assert.ok(pagePath === "/" || pagePath === "/credentials/");
  const backend = new URL(config.backend);
  assert.equal(backend.hostname, "127.0.0.1");
  const request = (pathname, method, headers, body) => new Promise((resolve, reject) => {
    const connection = https.request({hostname: backend.hostname, port: backend.port, path: pathname,
      servername: "approval.example.org", ca: config.ca, minVersion: "TLSv1.3", method,
      headers: {...headers, host: "approval.example.org"}}, response => {
      const output = [];
      let count = 0;
      response.on("data", chunk => { count += chunk.length; if (count > 70000) connection.destroy(); else output.push(chunk); });
      response.on("end", () => resolve({status: response.statusCode, headers: response.headers, body: Buffer.concat(output)}));
      response.on("error", reject);
    });
    connection.setTimeout(15000, () => connection.destroy());
    connection.on("error", reject);
    connection.end(body);
  });
  const fixture = async (pathname, body = {}) => {
    const result = await request(pathname, "POST", {"content-type": "application/json"}, JSON.stringify(body));
    assert.equal(result.status, 200);
    return JSON.parse(result.body.toString("utf8"));
  };
  stage = "browser-launch";
  const browser = await chromium.launch({headless: true});
  try {
    const context = await browser.newContext({viewport: {width: 393, height: 852}, isMobile: true, hasTouch: true,
      deviceScaleFactor: 1, serviceWorkers: "block"});
    const apiPaths = [];
    const errors = [];
    await context.route("**/*", async route => {
      const source = route.request(), target = new URL(source.url());
      if (target.origin !== origin || target.hash || target.search) { await route.abort(); return; }
      if (target.pathname.startsWith(`${pagePath}api/`)) apiPaths.push(target.pathname);
      const response = await request(target.pathname, source.method(), await source.allHeaders(), source.postDataBuffer());
      const headers = {...response.headers};
      delete headers["transfer-encoding"];
      await route.fulfill({status: response.status, headers, body: response.body});
    });
    const page = await context.newPage();
    page.on("pageerror", () => errors.push("page error"));
    const cdp = await context.newCDPSession(page);
    await cdp.send("WebAuthn.enable");
    await cdp.send("WebAuthn.addVirtualAuthenticator", {options: {protocol: "ctap2", transport: "internal",
      hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true}});
    stage = "enrollment-page";
    await page.goto(config.invite);
    await page.getByRole("button", {name: "Create approval passkey"}).waitFor();
    assert.equal(await page.evaluate(() => location.hash), "");
    assert.equal((await page.locator("body").innerText()).includes(config.invite.split("=")[1]), false);
    stage = "enrollment-passkey";
    await page.getByRole("button", {name: "Create approval passkey"}).click();
    await page.getByRole("status").filter({hasText: "Passkey enrolled."}).waitFor();
    stage = "approval-page";
    const pending = await fixture("/_fixture/request");
    const link = `${origin}${pagePath}#request=${pending.id}`;
    await page.goto(link);
    await page.getByRole("button", {name: "Approve with passkey"}).waitFor();
    assert.equal(await page.locator("#accounts").innerText(), "facebook");
    assert.equal(await page.locator("#agent").innerText(), "agent");
    assert.equal(await page.locator("#host").innerText(), "host");
    assert.equal(await page.locator("#duration").innerText(), "20 hours");
    assert.equal(await page.evaluate(() => location.hash), "");
    await page.goto(link);
    await page.getByRole("button", {name: "Approve with passkey"}).waitFor();
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    if (config.artifact_directory) {
      await fs.mkdir(config.artifact_directory, {recursive: true});
      await page.screenshot({path: path.join(config.artifact_directory, "phone-approval.png"), fullPage: true});
    }
    let grantID = "";
    page.on("response", async response => {
      if (new URL(response.url()).pathname === `${pagePath}api/approve/finish` && response.ok()) grantID = (await response.json()).grant_id;
    });
    stage = "approval-passkey";
    await page.getByRole("button", {name: "Approve with passkey"}).click();
    await page.getByRole("status").filter({hasText: "Approved until"}).waitFor();
    assert.match(grantID, /^[a-f0-9]{64}$/);
    assert.equal((await fixture("/_fixture/check", {id: grantID})).status, "active");
    stage = "revocation-passkey";
    await page.getByRole("button", {name: "Revoke access", exact: true}).click();
    await page.getByRole("button", {name: "Revoke with passkey"}).click();
    await page.getByRole("status").filter({hasText: "Access revoked."}).waitFor();
    assert.equal((await fixture("/_fixture/check", {id: grantID})).status, "denied");
    stage = "replay-denial";
    await page.goto(link);
    await page.getByRole("status").filter({hasText: "unavailable or expired"}).waitFor();
    assert.equal(await page.getByRole("button", {name: "Approve with passkey"}).isVisible(), false);
    assert.deepEqual(errors, []);
    for (const pathname of ["enroll/begin", "enroll/finish", "approve/begin", "approve/finish", "revoke/begin", "revoke/finish"])
      assert.equal(apiPaths.includes(`${pagePath}api/${pathname}`), true);
    await context.close();
  } finally { await browser.close(); }
  process.stdout.write("phone browser qualification: OK\n");
})().catch(() => { process.stderr.write(`phone browser qualification: failed at ${stage}\n`); process.exitCode = 1; });
