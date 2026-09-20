import { chromium } from "./node-env/node_modules/playwright/index.mjs";

const [baseURL, input, output] = process.argv.slice(2);
if (!baseURL || !input || !output) {
  throw new Error("usage: node screenshot.mjs BASE_URL FIXTURE.pdf OUTPUT.png");
}
const allowedOrigin = new URL(baseURL).origin;
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 612, height: 792 } });
const requests = [];
const blockedRequests = [];
await page.route("**/*", async route => {
  const url = new URL(route.request().url());
  requests.push(url.href);
  if (url.origin === allowedOrigin) {
    await route.continue();
  } else {
    blockedRequests.push(url.href);
    await route.abort("blockedbyclient");
  }
});
await page.goto(`${baseURL}/viewer.html?file=${encodeURIComponent(input)}`);
await page.waitForFunction(() => window.__qualification?.done === true);
const result = await page.evaluate(() => window.__qualification);
if (result.error) {
  throw new Error(`${result.error}\n${result.stack ?? ""}`);
}
await page.locator("#page").screenshot({ path: output });
console.log(JSON.stringify({ ...result, requests, blockedRequests }, null, 2));
await browser.close();
