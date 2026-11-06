const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { spawn } = require('node:child_process');
const { createHash } = require('node:crypto');
const path = require('node:path');

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function browserSession(t) {
  const profile = fs.mkdtempSync('C:\\Temp\\opencode\\studio-e2e-');
  const child = spawn('C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe', [
    '--headless=new', '--disable-gpu', '--no-first-run',
    '--remote-debugging-address=127.0.0.1', '--remote-debugging-port=0',
    '--remote-allow-origins=*', `--user-data-dir=${profile}`, 'about:blank',
  ], { stdio: 'ignore' });
  t.after(() => child.kill());
  const marker = path.join(profile, 'DevToolsActivePort');
  let port;
  for (let attempt = 0; attempt < 60; attempt++) {
    if (fs.existsSync(marker)) {
      port = fs.readFileSync(marker, 'utf8').split('\n')[0];
      break;
    }
    await sleep(150);
  }
  assert.ok(port, 'Chrome DevTools endpoint started');
  const pages = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const page = pages.find((candidate) => candidate.type === 'page');
  assert.ok(page, 'Chrome page available');
  const socket = new WebSocket(page.webSocketDebuggerUrl);
  t.after(() => socket.close());
  await new Promise((resolve, reject) => {
    socket.addEventListener('open', resolve, { once: true });
    socket.addEventListener('error', reject, { once: true });
  });
  let id = 0;
  const pending = new Map();
  socket.addEventListener('message', ({ data }) => {
    const message = JSON.parse(data);
    const entry = pending.get(message.id);
    if (!entry) return;
    pending.delete(message.id);
    if (message.error) entry.reject(Error(JSON.stringify(message.error)));
    else entry.resolve(message.result);
  });
  const call = (method, params = {}) => new Promise((resolve, reject) => {
    const current = ++id;
    pending.set(current, { resolve, reject });
    socket.send(JSON.stringify({ id: current, method, params }));
  });
  await call('Page.enable');
  await call('Runtime.enable');
  await call('Page.navigate', { url: 'http://127.0.0.1:4184/studio.html' });
  const evaluate = async (expression) => {
    const response = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (response.exceptionDetails) throw Error(response.exceptionDetails.text);
    return response.result.value;
  };
  const until = async (expression) => {
    for (let attempt = 0; attempt < 50; attempt++) {
      if (await evaluate(expression)) return;
      await sleep(100);
    }
    throw Error(`Timed out waiting for: ${expression}`);
  };
  return { call, evaluate, until };
}

test('Studio starts a new defense and preserves numbered SHA-256 test history', { timeout: 30000 }, async (t) => {
  const browser = await browserSession(t);
  await browser.until('document.querySelector("#new-defense") !== null');
  const initialHash = await browser.evaluate('document.querySelector("#defense-hash").textContent.trim()');
  assert.match(initialHash, /^[a-f0-9]{64}$/);
  await browser.call('Emulation.setDeviceMetricsOverride', { width: 375, height: 812, deviceScaleFactor: 1, mobile: true });
  const layout = await browser.evaluate('({width: innerWidth, scroll: document.documentElement.scrollWidth})');
  assert.equal(layout.width, 375);
  assert.equal(layout.scroll, 375, 'no horizontal overflow on a small screen');

  await browser.evaluate('document.querySelector("#start").click()');
  await browser.evaluate('document.querySelector("#run").click()');
  await browser.until('Number(document.querySelector("#history-count").textContent) === 1');
  const history = await browser.evaluate('JSON.parse(localStorage.getItem("live-code-audit-studio-history-v1"))');
  assert.equal(history.defenses.length, 1);
  assert.equal(history.defenses[0].runs.length, 1);
  assert.equal(history.defenses[0].runs[0].cases.length, 3);
  const code = await browser.evaluate('document.querySelector("#source").value');
  assert.equal(history.defenses[0].runs[0].codeHash, createHash('sha256').update(code).digest('hex'));
  assert.match(history.defenses[0].hash, /^[a-f0-9]{64}$/);
  assert.match(history.defenses[0].runs[0].hash, /^[a-f0-9]{64}$/);
  assert.equal(new Set(history.defenses[0].runs[0].cases.map((entry) => entry.hash)).size, 3);
  for (const entry of history.defenses[0].runs[0].cases) {
    assert.match(entry.hash, /^[a-f0-9]{64}$/);
  }

  await browser.call('Page.reload', { ignoreCache: true });
  await browser.until('document.querySelector("#history-count") && Number(document.querySelector("#history-count").textContent) === 1');
  assert.equal(await browser.evaluate('document.querySelector("#defense-hash").textContent.trim()'), initialHash, 'refresh resumes the same defense');

  await browser.evaluate('document.querySelector("#new-defense").click()');
  await browser.until('document.querySelector("#defense-number").textContent.trim() === "2"');
  const nextHash = await browser.evaluate('document.querySelector("#defense-hash").textContent.trim()');
  assert.notEqual(nextHash, initialHash);
  assert.equal(await browser.evaluate('Number(document.querySelector("#history-count").textContent)'), 1);
  await browser.evaluate('document.querySelector("#history-toggle").click()');
  await browser.until('document.querySelector("#history-list").textContent.includes("TestBoundary")');

  await browser.evaluate('document.querySelector("#start").click()');
  await browser.evaluate('document.querySelector("#run").click()');
  await browser.until('Number(document.querySelector("#history-count").textContent) === 2');
  const secondHistory = await browser.evaluate('JSON.parse(localStorage.getItem("live-code-audit-studio-history-v1"))');
  assert.equal(secondHistory.defenses[1].runs[0].number, 2);
  assert.deepEqual(secondHistory.defenses[1].runs[0].cases.map((entry) => entry.number), [4, 5, 6]);
  await browser.evaluate('document.querySelector("#finish").click()');
  await browser.until('Number(document.querySelector("#history-count").textContent) === 3 && !document.querySelector("#report").hidden');
  const completed = await browser.evaluate('JSON.parse(localStorage.getItem("live-code-audit-studio-history-v1"))');
  assert.equal(completed.defenses[1].runs[1].number, 3, 'final test run has its own number');
  assert.deepEqual(completed.defenses[1].runs[1].cases.map((entry) => entry.number), [7, 8, 9]);

  await browser.call('Page.reload', { ignoreCache: true });
  await browser.until('document.querySelector("#history-count") && Number(document.querySelector("#history-count").textContent) === 3');
  assert.equal(await browser.evaluate('document.querySelector("#defense-hash").textContent.trim()'), nextHash, 'completed defense is restored');
  assert.equal(await browser.evaluate('document.querySelector("#report").hidden'), false, 'report remains available after reload');
  await browser.evaluate('document.querySelector("#new-defense").click()');
  await browser.until('document.querySelector("#defense-number").textContent.trim() === "3"');
  assert.equal(await browser.evaluate('Number(document.querySelector("#history-count").textContent)'), 3);
});
