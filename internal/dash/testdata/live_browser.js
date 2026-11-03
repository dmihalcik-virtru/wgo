// Drives the live page in headless Chrome over the DevTools protocol (node's
// built-in WebSocket; no dependencies). Run by TestLivePageInBrowser when
// WGO_BROWSER_TESTS is set:
//
//	node live_browser.js <chrome> <page URL> [--perf=<URL>] [--shots=<dir>] [--no-checks]
//
// The page must serve the shared query vectors' snapshot with the probe
// addon (see page_browser_test.go). Prints "ok <checks> ..." or exits
// non-zero naming the failure. --perf times filter changes on a page that
// serves a 2,000-node snapshot; --shots saves light and dark screenshots;
// --no-checks skips the checks (for wgo dash --fixture, which has no probe).
"use strict";
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const [chrome, pageURL] = process.argv.slice(2);
const flag = (name) => { const a = process.argv.slice(4).find((x) => x === name || x.startsWith(name + "=")); return a == null ? null : a.includes("=") ? a.slice(a.indexOf("=") + 1) : true; };
const shotDir = flag("--shots"), perfURL = flag("--perf"), noChecks = flag("--no-checks");
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

  if (!noChecks) await runChecks();
  const perf = perfURL ? await timeRenders() : "";
  if (shotDir) await screenshots();
  finish(0, "ok " + checks + (perf ? " " + perf : ""));
})().catch((e) => finish(1, "FAIL " + e.message));

async function runChecks() {
  // 1. A URL restores view, query and selection; the addon view is a tab.
  await send("Page.navigate", { url: pageURL + "#view=table&q=prstate:open&sel=pr:acme%2Fapi%2341" });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap)", "the first snapshot");
  const st = await evaluate("(() => { const s = window.__wgoStore; return { view: s.view, q: s.query, sel: Array.from(s.selection), rows: s.qres && s.qres.rows } })()");
  check(st.view === "table" && st.q === "prstate:open" && st.sel.join() === "pr:acme/api#41", "hash not restored: " + JSON.stringify(st));
  const tabs = await evaluate("Array.from(document.querySelectorAll('.view-tabs button')).map(b => b.textContent + ':' + b.getAttribute('aria-pressed'))");
  check(tabs.join() === "Graph:false,Table:true,Probe:false", "tabs: " + tabs);
  check((await evaluate("Array.from(document.querySelectorAll('.mult')).map(m => m.textContent).includes('probe panel')")), "the addon panel renders");
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

  // 2b. A drag: live changes open one entry, the release commits it, and
  // Back returns to the state before the drag.
  // (Had each live change pushed, one Back would land on prstate:merged.)
  const q0 = await evaluate("window.__wgoStore.query");
  await evaluate("(() => { const s = window.__wgoStore; s.setQuery('prstate:open', {}); s.setQuery('prstate:merged', {}); s.setQuery('prstate:closed', {}); s.setQuery('prstate:closed', { commit: true }); })()");
  check((await evaluate("location.hash")).includes("q=prstate:closed"), "the drag's end is in the URL");
  await evaluate("history.back()"); await sleep(300);
  check((await evaluate("window.__wgoStore.query")) === q0, "Back undoes the whole drag");
  await evaluate("history.forward()"); await sleep(300);
  check((await evaluate("window.__wgoStore.query")) === "prstate:closed", "the drag is a single entry");

  // 2c. A click that would make an invalid query shows the error and a fix.
  await evaluate("window.__wgoStore.setQuery('kind:agent', { commit: true })");
  check((await clickBar("draft:")) === "kind:agent", "an invalid click is not applied");
  const clickErr = await evaluate("document.querySelector('.qmsg .qerr').textContent + ' | ' + document.querySelector('.qmsg button.linkish').textContent");
  check(clickErr.includes("leaves no rows") && clickErr.endsWith("Use prstate:draft"), "the click's error and fix: " + clickErr);
  check((await evaluate("document.querySelector('input.query').value")) === "kind:agent;prstate:draft", "the bar shows the attempted query");
  await evaluate("document.querySelector('.qmsg button.linkish').click()");
  check((await evaluate("window.__wgoStore.query")) === "prstate:draft", "the fix replaces the conflicting kind clause");

  // 2d. The query bar follows changes made elsewhere, even while focused.
  await evaluate("(() => { const i = document.querySelector('input.query'); i.focus(); i.value = 'kind:pr'; i.dispatchEvent(new Event('input', { bubbles: true })); i.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); })()");
  check((await evaluate("document.activeElement === document.querySelector('input.query')")), "the bar keeps focus after Enter");
  await clickBar("open:");
  check((await evaluate("document.querySelector('input.query').value")) === "kind:pr;prstate:open", "a chart click updates the focused bar");
  await evaluate("history.back()"); await sleep(300);
  check((await evaluate("document.querySelector('input.query').value")) === "kind:pr", "Back updates the bar");
  await evaluate("history.forward()"); await sleep(300);
  check((await evaluate("document.querySelector('.qmsg .qinfo').parentNode.getAttribute('role')")) === "status", "the summary is a status, not an alert");

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

  // 4. The addon view renders in its own container, is in the URL, keeps
  // its own key with the history model, and never sees the action token.
  await evaluate("Array.from(document.querySelectorAll('.view-tabs button')).find(b => b.textContent === 'Probe').click()");
  await sleep(200);
  check((await evaluate("Array.from(document.querySelectorAll('.viewwrap')).filter(d => !d.hidden).map(d => d.textContent).join()")) === "probe view: 2 rows, x=", "probe view");
  check((await evaluate("location.hash")).includes("view=probe"), "view in the hash");
  check((await evaluate("!!JSON.parse(document.getElementById('wgo-boot').textContent).token")), "the page itself has a token");
  const pboot = await evaluate("window.__probe.boot");
  check(!/(^|,)(token|action_api|ack_api)(,|$)/.test(pboot) && /(^|,)fields(,|$)/.test(pboot), "ctx.boot without the token: " + pboot);
  await evaluate("(() => { const c = window.__probe.ctx; c.setState({ x: '5' }); c.setState({ x: '6' }); c.setState({ x: '7' }, { commit: true }); })()");
  check((await evaluate("location.hash")).includes("x=7"), "ctx.setState writes its key");
  check((await evaluate("window.__probe.ctx.getState('x')")) === "7", "ctx.getState");
  await evaluate("history.back()"); await sleep(300);
  const back = await evaluate("({ x: window.__probe.ctx.getState('x'), reason: window.__probe.renders[window.__probe.renders.length - 1], text: document.querySelector('.viewwrap:not([hidden])').textContent })");
  check(back.x === "" && back.reason === "history" && back.text.endsWith("x="), "Back restores the addon key and renders with reason history: " + JSON.stringify(back));
  check((await evaluate("window.__probe.notes")) > 0, "ctx.subscribe hears changes");
  await evaluate("history.back()"); await sleep(300);
  check((await evaluate("window.__wgoStore.view")) === "table", "back to the table view");
  check((await evaluate("window.__probe.destroyed")) === 1, "leaving the view destroys it");
  const notes = await evaluate("window.__probe.notes");
  await evaluate("window.__wgoStore.select(['ws-0000000000000001'], false)");
  check((await evaluate("window.__probe.notes")) === notes, "a destroyed view's subscriptions end");
  await evaluate("WGOLive.registerPanel({ id: 'probe-panel', render: function (ctx) { ctx.container.textContent = 'probe panel 2'; } })");
  check((await evaluate("window.__probe.panelDestroyed")) === 1, "replacing a panel destroys the old one");
  check((await evaluate("Array.from(document.querySelectorAll('.mult')).map(m => m.textContent).includes('probe panel 2')")), "the replacement panel renders");

  // 4b. sort= is kept raw for the view that owns it (gh-101's multi-key sort).
  await send("Page.navigate", { url: "about:blank" });
  await send("Page.navigate", { url: pageURL + "#view=table&sort=-waiting,size&y=size" });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap)", "the snapshot");
  await sleep(300);
  const kept = await evaluate("location.hash");
  check(kept.includes("sort=-waiting,size") && kept.includes("y=size"), "sort and addon keys survive the first render: " + kept);
  check((await evaluate("window.__wgoStore.getState('sort')")) === "-waiting,size", "getState('sort') is raw");

  // 5. Copy as Markdown under the CSP: the clipboard, then the fallback.
  await evaluate("window.__wgoStore.setQuery('kind:pr;prstate:open', { commit: true }); window.__wgoStore.select(['pr:acme/api#41'], false)");
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

  // 5b. Sprints and tracked work (gh-97, gh-98): the sprint group's card
  // shows the sprint's dates and lists the ticket it tracks, with its
  // points, and sprints have a legend entry naming their shape.
  await send("Page.navigate", { url: "about:blank" });
  await send("Page.navigate", { url: pageURL + "#view=graph" });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap)", "the snapshot");
  const card = await evaluate("(() => { const s = Array.from(document.querySelectorAll('.efforts section.effort')).find(s => s.querySelector('header button.name').textContent === 'Sprint 12'); return s ? s.textContent : '' })()");
  check(card.includes("active sprint · 2026-10-12 to 2026-10-26") && card.includes("TEAM-99") && card.includes("unestimated") && !card.includes("No workspaces discovered"), "sprint group card: " + card);
  const legend = await evaluate("Array.from(document.querySelectorAll('.legend li')).map(li => li.textContent)");
  check(legend.includes("Sprint (a time box) (2)"), "sprint legend entry: " + legend.join(" | "));

  // 6. Nothing was refused by the CSP and nothing threw.
  // The browser's own favicon request is the only 404 the page may cause.
  const problems = events.filter((e) => (e.method === "Log.entryAdded" && e.params.entry.level === "error" && !/\/favicon\.ico$/.test(e.params.entry.url || "")) || e.method === "Runtime.exceptionThrown")
    .map((e) => e.method === "Runtime.exceptionThrown" ? e.params.exceptionDetails.text + " " + ((e.params.exceptionDetails.exception || {}).description || "") : e.params.entry.text + " " + (e.params.entry.url || ""));
  check(problems.length === 0, "console errors: " + problems.join(" | "));
}

// timeRenders times filter changes on the 2,000-node page: the synchronous
// re-render, and to the next frame (style, layout, paint), median of 7.
async function timeRenders() {
  await send("Page.navigate", { url: "about:blank" });
  await send("Page.navigate", { url: perfURL });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap)", "the 2,000-node snapshot");
  await sleep(1500);
  const out = [];
  for (const view of ["graph", "table"]) {
    await evaluate("window.__wgoStore.setState({ view: " + JSON.stringify(view) + " }, { commit: true })");
    await sleep(500);
    const r = await evaluate(`(async () => {
      const s = window.__wgoStore, frame = () => new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)));
      const med = (a) => a.slice().sort((x, y) => x - y)[a.length >> 1];
      const sync = { apply: [], clear: [] }, toFrame = { apply: [], clear: [] };
      for (let i = 0; i < 7; i++) {
        for (const [k, q] of [["apply", "prstate:open;repo:acme/repo-3"], ["clear", ""]]) {
          const t0 = performance.now(); s.setQuery(q, { commit: true }); const t1 = performance.now(); await frame(); const t2 = performance.now();
          sync[k].push(t1 - t0); toFrame[k].push(t2 - t0);
        }
      }
      return { n: s.idx.byId.size, apply: med(sync.apply), clear: med(sync.clear), applyFrame: med(toFrame.apply), clearFrame: med(toFrame.clear) };
    })()`);
    out.push(view + ": apply " + r.apply.toFixed(1) + " ms (" + r.applyFrame.toFixed(1) + " to frame), clear " + r.clear.toFixed(1) + " ms (" + r.clearFrame.toFixed(1) + " to frame)");
    if (view === "graph") out.unshift("perf " + r.n + " nodes");
  }
  return out.join("; ");
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
