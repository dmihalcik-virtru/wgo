// Drives the live page in headless Chrome over the DevTools protocol (node's
// built-in WebSocket; no dependencies). Run by TestLivePageInBrowser when
// WGO_BROWSER_TESTS is set:
//
//	node live_browser.js <chrome> <page URL> [screenshot dir] [--no-checks]
//
// The page must serve the shared query vectors' snapshot with the probe
// addon view. Prints "ok <checks>" or exits non-zero naming the failure.
// With a screenshot dir it also saves light and dark screenshots; with
// --no-checks it only takes them (for wgo dash --fixture, which has no
// probe view).
"use strict";
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const [chrome, pageURL, shotDir, mode] = process.argv.slice(2);
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "wgo-chrome-"));
const proc = spawn(chrome, ["--headless=new", "--remote-debugging-port=0", "--user-data-dir=" + profile, "--no-first-run",
  "--no-default-browser-check", "--disable-gpu", "--window-size=1400,900", "about:blank"], { stdio: ["ignore", "ignore", "pipe"] });
function finish(code, msg) {
  if (msg) console.log(msg);
  try { proc.kill(); } catch (e) { /* gone */ }
  try { fs.rmSync(profile, { recursive: true, force: true }); } catch (e) { /* best effort */ }
  process.exit(code);
}
setTimeout(() => finish(1, "FAIL timed out"), 60000);

const wsURL = new Promise((resolve) => {
  let buf = "";
  proc.stderr.on("data", (d) => { buf += d; const m = /DevTools listening on (ws:\/\/\S+)/.exec(buf); if (m) resolve(m[1]); });
});

let ws, nextId = 1, session = null;
const pending = new Map(), events = [];
function send(method, params, sid) {
  const id = nextId++;
  const msg = { id, method, params: params || {} };
  if (sid !== undefined ? sid : session) msg.sessionId = sid !== undefined ? sid : session;
  ws.send(JSON.stringify(msg));
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject, method }));
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function evaluate(expr, opts) {
  const r = await send("Runtime.evaluate", Object.assign({ expression: expr, returnByValue: true, awaitPromise: true }, opts || {}));
  if (r.exceptionDetails) throw new Error("evaluate " + expr.slice(0, 80) + ": " + JSON.stringify(r.exceptionDetails.exception && r.exceptionDetails.exception.description || r.exceptionDetails.text));
  return r.result.value;
}
async function waitFor(expr, what) {
  for (let i = 0; i < 100; i++) { if (await evaluate(expr)) return; await sleep(100); }
  throw new Error("timed out waiting for " + what);
}
let checks = 0;
function check(cond, what) { if (!cond) throw new Error(what); checks++; }

(async () => {
  ws = new WebSocket(await wsURL);
  await new Promise((r) => ws.addEventListener("open", r));
  ws.addEventListener("message", (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) {
      const p = pending.get(m.id); pending.delete(m.id);
      if (m.error) p.reject(new Error(p.method + ": " + m.error.message)); else p.resolve(m.result);
    } else if (m.method) events.push(m);
  });
  const { targetId } = await send("Target.createTarget", { url: "about:blank" }, null);
  session = (await send("Target.attachToTarget", { targetId, flatten: true }, null)).sessionId;
  await send("Runtime.enable"); await send("Log.enable"); await send("Page.enable");
  await send("Emulation.setFocusEmulationEnabled", { enabled: true });
  await send("Emulation.setDeviceMetricsOverride", { width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
  const origin = new URL(pageURL).origin;
  await send("Browser.grantPermissions", { origin, permissions: ["clipboardReadWrite", "clipboardSanitizedWrite"] }, null);

  if (mode !== "--no-checks") await runChecks();
  if (shotDir) await screenshots();
  finish(0, "ok " + checks);
})().catch((e) => finish(1, "FAIL " + e.message));

async function runChecks() {
  // 1. A URL restores view, query and selection; the addon view is a tab.
  await send("Page.navigate", { url: pageURL + "#view=table&q=prstate:open&sel=pr:acme%2Fapi%2341" });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap)", "the first snapshot");
  const st = await evaluate("(() => { const s = window.__wgoStore; return { view: s.view, q: s.query, sel: Array.from(s.selection), rows: s.qres && s.qres.rows } })()");
  check(st.view === "table" && st.q === "prstate:open" && st.sel.join() === "pr:acme/api#41", "hash not restored: " + JSON.stringify(st));
  const tabs = await evaluate("Array.from(document.querySelectorAll('.view-tabs button')).map(b => b.textContent + ':' + b.getAttribute('aria-pressed'))");
  check(tabs.join() === "Graph:false,Table:true,Probe:false", "tabs: " + tabs);
  const table = await evaluate("({ rows: document.querySelectorAll('table.live tbody tr').length, ctx: document.querySelectorAll('table.live tbody tr.ctx').length, sel: document.querySelectorAll('table.live tbody tr.sel').length })");
  check(table.rows > 2 && table.ctx > 0 && table.sel === 1, "table: " + JSON.stringify(table));
  check((await evaluate("document.querySelector('input.query').value")) === "prstate:open", "query bar text");
  check((await evaluate("document.querySelector('.qmsg .qinfo').textContent")).startsWith("2 rows"), "query summary");

  // 2. Chart clicks become clauses, combine with AND, toggle off, and are history.
  const clickBar = (prefix) => evaluate("(() => { const g = Array.from(document.querySelectorAll('.mult .mbar')).find(g => g.getAttribute('aria-label').startsWith(" + JSON.stringify(prefix) + ")); g.dispatchEvent(new MouseEvent('click', { bubbles: true })); return window.__wgoStore.query })()");
  check((await clickBar("draft:")) === "prstate:draft", "PR-state bar");
  check((await clickBar("2026-10-14:")) === "prstate:draft;activity:2026-10-14", "day bar ANDs");
  check((await clickBar("API widgets:")) === "prstate:draft;activity:2026-10-14;effort:api-widgets", "effort bar ANDs");
  check((await clickBar("2026-10-14:")) === "prstate:draft;effort:api-widgets", "day bar toggles off");
  check((await evaluate("location.hash")).includes("q=prstate:draft;effort:api-widgets"), "hash follows the query");
  check((await evaluate("document.querySelector('.mult .mbar.on') !== null")), "the active bar is marked");
  await evaluate("history.back()"); await sleep(300);
  check((await evaluate("window.__wgoStore.query")) === "prstate:draft;activity:2026-10-14;effort:api-widgets", "back restores the previous query");
  await evaluate("history.back()"); await sleep(300);
  check((await evaluate("window.__wgoStore.query")) === "prstate:draft;activity:2026-10-14", "back again");
  await evaluate("history.forward()"); await sleep(300);
  check((await evaluate("window.__wgoStore.query")) === "prstate:draft;activity:2026-10-14;effort:api-widgets", "forward re-applies");

  // 3. The query bar: errors inline with a fix, Enter applies, Esc reverts.
  const type = (text, key) => evaluate("(() => { const i = document.querySelector('input.query'); i.focus(); i.value = " + JSON.stringify(text) + "; i.dispatchEvent(new Event('input', { bubbles: true })); i.dispatchEvent(new KeyboardEvent('keydown', { key: " + JSON.stringify(key) + ", bubbles: true })); return window.__wgoStore.query })()");
  const before = await evaluate("window.__wgoStore.query");
  check((await type("kind:pr;prstat:open", "Enter")) === before, "an invalid query is not applied");
  const err = await evaluate("document.querySelector('.qmsg .qerr').textContent");
  check(err.includes("did you mean prstate?") && err.includes("column 9"), "inline error: " + err);
  await evaluate("document.querySelector('.qmsg button.linkish').click()");
  check((await evaluate("window.__wgoStore.query")) === "kind:pr;prstate:open", "the fix applies");
  check((await type("kind:ticket", "Escape")) === "kind:pr;prstate:open", "Esc keeps the query");
  check((await evaluate("document.querySelector('input.query').value")) === "kind:pr;prstate:open", "Esc reverts the text");
  check((await evaluate("document.querySelectorAll('table.live tbody tr.ctx').length")) === 0, "a kind clause hides context rows");

  // 4. The addon view renders in its own container and is in the URL.
  await evaluate("Array.from(document.querySelectorAll('.view-tabs button')).find(b => b.textContent === 'Probe').click()");
  await sleep(200);
  check((await evaluate("Array.from(document.querySelectorAll('.viewwrap')).filter(d => !d.hidden).map(d => d.textContent).join()")) === "probe view: 2 rows", "probe view");
  check((await evaluate("location.hash")).includes("view=probe"), "view in the hash");
  await evaluate("history.back()"); await sleep(300);
  check((await evaluate("window.__wgoStore.view")) === "table", "back to the table view");

  // 5. Copy as Markdown under the CSP: the clipboard, then the fallback.
  await evaluate("window.__wgoStore.select(['pr:acme/api#41'], false)");
  await send("Runtime.evaluate", { expression: "Array.from(document.querySelectorAll('button')).find(b => b.textContent === 'Copy as Markdown').click()", userGesture: true });
  await waitFor("document.querySelector('.status').textContent.startsWith('Copied') || document.querySelector('.status').textContent.includes('failed') || !document.querySelector('.copyout').hidden", "the copy to finish");
  const status = await evaluate("document.querySelector('.status').textContent");
  check(status.startsWith("Copied the Markdown"), "copy status: " + status);
  const clip = await evaluate("navigator.clipboard.readText()", { userGesture: true });
  check(clip.startsWith("## kind:pr;prstate:open\n") && clip.includes("Query: `kind:pr;prstate:open`") && clip.includes("acme/api#41"), "clipboard: " + clip.slice(0, 200));
  await evaluate("Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })");
  await send("Runtime.evaluate", { expression: "Array.from(document.querySelectorAll('button')).find(b => b.textContent === 'Copy as Markdown').click()", userGesture: true });
  await waitFor("!document.querySelector('.copyout').hidden", "the fallback text area");
  const fb = await evaluate("(() => { const t = document.querySelector('.copyout textarea'); return { same: t.value, active: document.activeElement === t, selected: t.selectionStart === 0 && t.selectionEnd === t.value.length } })()");
  check(fb.same === clip && fb.active && fb.selected, "fallback text area: " + JSON.stringify({ active: fb.active, selected: fb.selected }));

  // 6. Nothing was refused by the CSP and nothing threw.
  // The browser's own favicon request is the only 404 the page may cause.
  const problems = events.filter((e) => (e.method === "Log.entryAdded" && e.params.entry.level === "error" && !/\/favicon\.ico$/.test(e.params.entry.url || "")) || e.method === "Runtime.exceptionThrown")
    .map((e) => e.method === "Runtime.exceptionThrown" ? e.params.exceptionDetails.text + " " + ((e.params.exceptionDetails.exception || {}).description || "") : e.params.entry.text + " " + (e.params.entry.url || ""));
  check(problems.length === 0, "console errors: " + problems.join(" | "));
}

async function screenshots() {
  fs.mkdirSync(shotDir, { recursive: true });
  const shots = [
    ["query-table-light.png", "light", "#q=activity:2026-10-14;prstate:open,draft&view=table"],
    ["query-table-dark.png", "dark", "#q=repo:acme/api;kind:pr,ticket&view=table&sel=ticket:jira:TEAM-7"],
    ["query-graph-light.png", "light", "#q=prstate:draft"],
    ["query-error-light.png", "light", "#q=kind:pr;prstat:open"],
  ];
  for (const [file, scheme, hash] of shots) {
    await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: scheme }] });
    await send("Page.navigate", { url: "about:blank" }); // a fresh document, not a same-page hash change
    await send("Page.navigate", { url: pageURL + hash });
    await waitFor("!!(window.__wgoStore && window.__wgoStore.snap)", "the snapshot");
    await sleep(1500);
    const shot = await send("Page.captureScreenshot", { format: "png" });
    fs.writeFileSync(path.join(shotDir, file), Buffer.from(shot.data, "base64"));
  }
}
