'use strict';

// Pin the current Chromium WebUI's exact row names and values. This witness is
// obtained only from a fresh pre-credential context, never a visited website.
async function qualifySandbox(page) {
  await page.goto('chrome://sandbox/', { waitUntil: 'domcontentloaded', timeout: 5000 });
  await page.waitForFunction(() => Boolean(document.querySelector('#evaluation')?.textContent.trim()),
    undefined, { timeout: 5000 });
  if (page.url() !== 'chrome://sandbox/') throw new Error('credential browser unavailable');
  const qualified = await page.evaluate(() => {
    const required = [['Layer 1 Sandbox', 'Namespace'], ['PID namespaces', 'Yes'],
      ['Network namespaces', 'Yes'], ['Seccomp-BPF sandbox', 'Yes'],
      ['Seccomp-BPF sandbox supports TSYNC', 'Yes']];
    const rows = [...document.querySelectorAll('#sandbox-status tr')].map(row =>
      [...row.querySelectorAll('td')].map(cell => cell.textContent.trim()));
    return required.every(([name, value]) => {
      const matches = rows.filter(row => row[0] === name);
      return matches.length === 1 && matches[0].length === 2 && matches[0][1] === value;
    }) && document.querySelector('#evaluation')?.textContent.trim() === 'You are adequately sandboxed.';
  });
  if (qualified !== true || page.url() !== 'chrome://sandbox/') throw new Error('credential browser unavailable');
}

module.exports = { qualifySandbox };
