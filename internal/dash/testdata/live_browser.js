// Drives the live page in headless Chrome over the DevTools protocol (node's
// built-in WebSocket; no dependencies). Run by TestLivePageInBrowser when
// WGO_BROWSER_TESTS is set:
//
//	node live_browser.js <chrome> <page URL> [--perf=<URL>] [--perf-table=<URL>] [--<suite flag>=<URL>…] [--shots=<dir>] [--set=<shot set>] [--no-checks]
//
// The page must serve the shared query vectors' snapshot with the probe
// addon (see page_browser_test.go). Prints "ok <checks> ..." or exits
// non-zero naming the failure. --perf times filter changes on a page that
// serves a 2,000-node snapshot; --perf-table times the analysis table on a
// page that serves the analysis fixture scaled to 2,000 nodes; --shots saves
// light and dark screenshots of a SHOT_SETS entry (--set: query, the default,
// table, or an addon's); --no-checks skips the checks (for wgo dash
// --fixture, which has no probe).
//
// Suites. Every check runs as a suite in SUITES, in order. An addon adds
// its own in a block of its own at the end of this file, its names inside
// the block's { } so sibling blocks cannot collide:
//
//	SUITES.push({ name, flag, checks, perf, deadlineMs })
//
//   - name labels the suite's failures and timeouts.
//   - flag is the option, without "--", whose value is the URL of the page
//     the suite drives; page_browser_test.go serves that page from the
//     addon's browserSuites entry and passes --<flag>=<URL>. A suite whose
//     flag is absent is skipped. The core suites' flag "" is the page URL.
//   - checks(url) and perf(url), both async and optional: checks call
//     check(); perf returns a summary for the "ok" line, or "". Every
//     suite's checks run before any suite's perf; --no-checks skips checks.
//   - deadlineMs (60000 by default) bounds checks and perf each, instead of
//     one watchdog for the whole run.
//
// Screenshots are SHOT_SETS entries: [file, scheme, hash, extra] rows,
// where extra "columns" opens the table's chooser and "cvd:<type>" shoots
// under a colour-vision-deficiency simulation; an entry may carry
// .viewport = { width, height }.
"use strict";
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const [chrome, pageURL] = process.argv.slice(2);
const flag = (name) => { const a = process.argv.slice(4).find((x) => x === name || x.startsWith(name + "=")); return a == null ? null : a.includes("=") ? a.slice(a.indexOf("=") + 1) : true; };
const shotDir = flag("--shots"), noChecks = flag("--no-checks"), shotSet = flag("--set") || "query";
const SUITES = [];
// within runs fn, failing with label if it takes longer than ms.
function within(label, ms, fn) {
  let timer;
  const late = new Promise((resolve, reject) => { timer = setTimeout(() => reject(new Error(label + " timed out after " + ms / 1000 + " s")), ms); });
  return Promise.race([Promise.resolve().then(fn), late]).finally(() => clearTimeout(timer));
}
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "wgo-chrome-"));
const proc = spawn(chrome, ["--headless=new", "--remote-debugging-port=0", "--user-data-dir=" + profile, "--no-first-run",
  "--no-default-browser-check", "--disable-gpu", "--window-size=1400,900", "about:blank"], { stdio: ["ignore", "ignore", "pipe"] });
function finish(code, msg) {
  if (msg) console.log(msg);
  try { proc.kill(); } catch (e) { /* gone */ }
  try { fs.rmSync(profile, { recursive: true, force: true }); } catch (e) { /* best effort */ }
  process.exit(code);
}

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
// key presses a key as the keyboard does (Input.dispatchKeyEvent), with
// Shift when shift is set.
const KEYS = { Enter: [13, "Enter", "\r"], " ": [32, "Space", " "], Tab: [9, "Tab", ""], Escape: [27, "Escape", ""], ArrowDown: [40, "ArrowDown", ""], ArrowUp: [38, "ArrowUp", ""] };
async function key(k, shift) {
  const [vk, code, text] = KEYS[k], modifiers = shift ? 8 : 0;
  await send("Input.dispatchKeyEvent", Object.assign({ type: text ? "keyDown" : "rawKeyDown", key: k, code, windowsVirtualKeyCode: vk, modifiers }, text ? { text } : {}));
  await send("Input.dispatchKeyEvent", { type: "keyUp", key: k, code, windowsVirtualKeyCode: vk, modifiers });
  await sleep(60);
}

(async () => {
  ws = new WebSocket(await within("starting Chrome", 30000, () => wsURL));
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

  const urlOf = (s) => s.flag ? flag("--" + s.flag) : pageURL;
  for (const s of SUITES) {
    const url = urlOf(s);
    if (url && s.checks && !noChecks) await within(s.name + " checks", s.deadlineMs || 60000, () => s.checks(url));
  }
  const perf = [];
  for (const s of SUITES) {
    const url = urlOf(s);
    if (url && s.perf) perf.push(await within(s.name + " timing", s.deadlineMs || 60000, () => s.perf(url)));
  }
  if (shotDir) await within("screenshots", 300000, screenshots);
  finish(0, "ok " + checks + (perf.filter(Boolean).length ? " " + perf.filter(Boolean).join("; ") : ""));
})().catch((e) => finish(1, "FAIL " + e.message));

// The core suites: the query language, URL state and the analysis table on
// the page URL (gh-100, gh-101), and their latency.
SUITES.push({ name: "query and table", flag: "", checks: runChecks, deadlineMs: 90000 });
SUITES.push({ name: "filter latency", flag: "perf", perf: timeRenders });
SUITES.push({ name: "table latency", flag: "perf-table", perf: timeTable });

async function runChecks() {
  // 1. A URL restores view, query and selection; the addon view is a tab.
  await send("Page.navigate", { url: pageURL + "#view=table&q=prstate:open&sel=pr:acme%2Fapi%2341" });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap)", "the first snapshot");
  const st = await evaluate("(() => { const s = window.__wgoStore; return { view: s.view, q: s.query, sel: Array.from(s.selection), rows: s.qres && s.qres.rows } })()");
  check(st.view === "table" && st.q === "prstate:open" && st.sel.join() === "pr:acme/api#41", "hash not restored: " + JSON.stringify(st));
  const tabs = await evaluate("Array.from(document.querySelectorAll('.view-tabs button')).map(b => b.textContent + ':' + b.getAttribute('aria-pressed'))");
  // Graph and Table lead, the probe (inlined last) ends, and every view an
  // addon registered has its tab, whatever their order.
  const viewLabels = await evaluate("WGOLive.views.map(v => v.label || v.id)");
  check(tabs[0] === "Graph:false" && tabs[1] === "Table:true" && tabs[tabs.length - 1] === "Probe:false" && tabs.length === viewLabels.length &&
    viewLabels.every((l) => tabs.includes(l + (l === "Table" ? ":true" : ":false"))), "tabs: " + tabs + " for views " + viewLabels);
  check((await evaluate("Array.from(document.querySelectorAll('.mult')).map(m => m.textContent).includes('probe panel')")), "the addon panel renders");
  const table = await evaluate("({ rows: document.querySelectorAll('table.live tbody tr').length, ctx: document.querySelectorAll('table.live tbody tr.ctx').length, sel: document.querySelectorAll('table.live tbody tr.sel').length })");
  check(table.rows > 2 && table.ctx > 0 && table.sel === 1, "table: " + JSON.stringify(table));
  check((await evaluate("document.querySelector('input.query').value")) === "prstate:open", "query bar text");
  check((await evaluate("document.querySelector('.qmsg .qinfo').textContent")).startsWith("2 rows"), "query summary");

  // 2. Chart clicks become clauses, combine with AND, toggle off, and are history.
  // (gh-102 made the day chart the when facet: a day bar sets when:<day>.)
  const clickBar = (prefix) => evaluate("(() => { const g = Array.from(document.querySelectorAll('.mult .mbar')).find(g => g.getAttribute('aria-label').startsWith(" + JSON.stringify(prefix) + ")); g.dispatchEvent(new MouseEvent('click', { bubbles: true })); return window.__wgoStore.query })()");
  check((await clickBar("draft:")) === "prstate:draft", "PR-state bar");
  check((await clickBar("2026-10-14:")) === "prstate:draft;when:2026-10-14", "day bar ANDs");
  check((await clickBar("API widgets:")) === "prstate:draft;when:2026-10-14;effort:api-widgets", "effort bar ANDs");
  check((await clickBar("2026-10-14:")) === "prstate:draft;effort:api-widgets", "day bar toggles off");
  check((await evaluate("location.hash")).includes("q=prstate:draft;effort:api-widgets"), "hash follows the query");
  check((await evaluate("document.querySelector('.mult .mbar.on') !== null")), "the active bar is marked");
  await evaluate("history.back()"); await sleep(300);
  check((await evaluate("window.__wgoStore.query")) === "prstate:draft;when:2026-10-14;effort:api-widgets", "back restores the previous query");
  await evaluate("history.back()"); await sleep(300);
  check((await evaluate("window.__wgoStore.query")) === "prstate:draft;when:2026-10-14", "back again");
  await evaluate("history.forward()"); await sleep(300);
  check((await evaluate("window.__wgoStore.query")) === "prstate:draft;when:2026-10-14;effort:api-widgets", "forward re-applies");

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
  check(card.split("Ship the widget API").length === 2, "the sprint's goal, also its group's description, shows once: " + card);
  const legend = await evaluate("Array.from(document.querySelectorAll('.legend li')).map(li => li.textContent)");
  check(legend.includes("Sprint (a time box) (2)"), "sprint legend entry: " + legend.join(" | "));

  // 5c. Details on demand (gh-99): a PR's move, reasons with evidence and
  // wait, and a ticket's mismatch with its evidence, in the detail panel.
  await evaluate("window.__wgoStore.select(['pr:acme/web#12', 'ticket:jira:TEAM-99'], false)");
  await sleep(200);
  const detail = await evaluate("document.querySelector('.detail').textContent");
  check(detail.includes("Whose move: reviewer — team:web, alice (you)") && detail.includes("awaiting review: ready since Oct 14; review requested from team:web only; alice through a team") &&
    detail.includes("Waiting since:") && detail.includes("Mismatch: unassigned in sprint: in Sprint 12 with no assignee"), "facts in the detail panel: " + detail);
  // The effort list's CI badge follows the facts: #41's own failure keeps
  // the failure style.
  const badge = await evaluate("(() => { const b = Array.from(document.querySelectorAll('.efforts li')).find(li => li.querySelector('button') && li.querySelector('button').textContent === 'acme/api#41'); const s = b && Array.from(b.querySelectorAll('.state')).find(x => x.textContent.startsWith('CI') || x.textContent.includes('CI ')); return s ? s.className + '|' + s.textContent : '' })()");
  check(badge === "state s-bad|CI failure", "acme/api#41 CI badge: " + badge);

  // 7. The analysis table (gh-101): kind:pr rows and PR default columns.
  const heads = () => evaluate("Array.from(document.querySelectorAll('table.atable thead th > button > span:first-child')).map(s => s.textContent)");
  const rowNames = () => evaluate("Array.from(document.querySelectorAll('table.atable tbody tr[data-i] td.nm')).map(td => td.firstChild.textContent)");
  const active = () => evaluate("(() => { const a = document.activeElement; return a ? (a.dataset.act || '') + ':' + (a.dataset.col || '') + ':' + a.textContent : '' })()");
  await send("Page.navigate", { url: "about:blank" });
  await send("Page.navigate", { url: pageURL + "#view=table&q=kind:pr" });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap) && !!document.querySelector('table.atable tbody tr[data-i]')", "the table");
  check((await heads()).join() === "Name,Repository,Author,Move,Waiting,Age,Size,CI,Reviews,Tickets", "PR default columns: " + (await heads()));
  check((await evaluate("getComputedStyle(document.querySelector('thead th')).position + ' ' + getComputedStyle(document.querySelector('.atable-scroll')).overflowY")) === "sticky auto", "the addon's own stylesheet (live-table.css) applies under the CSP");
  const prRows = await rowNames();
  check(prRows.length === 5 && prRows.every((n) => /^acme\/(api|web)#\d+$/.test(n)), "kind:pr lists only the PRs: " + prRows);
  check((await evaluate("document.querySelectorAll('table.atable tbody tr.ctx').length")) === 0, "kind:pr shows no context rows");
  check((await evaluate("document.querySelector('table.atable tbody tr[data-i] td.unk') !== null")), "unknown values read unknown");
  const graphics = await evaluate("Array.from(document.querySelectorAll('table.atable .g')).map(g => [g.getAttribute('role'), g.getAttribute('aria-label') || '', g.textContent.trim()])");
  check(graphics.length >= 2 && graphics.every((g) => g[0] === "img" && g[1].length > 8 && g[2].length > 0), "every graphic is an img with a sentence and text beside it: " + JSON.stringify(graphics));

  // 7a. The column chooser by keyboard alone: move, remove, add, close.
  await evaluate("document.querySelector('.atable-bar button[aria-controls]').focus()");
  await key("Enter");
  check(!(await evaluate("document.querySelector('.atable-chooser').hidden")), "Enter opens the chooser");
  check((await active()).startsWith("right:repo:"), "focus starts on the first usable button: " + (await active()));
  await key("Enter");
  check((await evaluate("window.__wgoStore.getState('cols')")) === "author,repo,move,waiting,age,size,ci,reviewer,ticket", "→ moves Repository right: " + (await evaluate("location.hash")));
  check((await active()).startsWith("right:repo:"), "focus follows the moved column: " + (await active()));
  await key("Tab");
  check((await active()).startsWith("remove:repo:"), "Tab reaches Remove");
  await key("Enter");
  check((await evaluate("window.__wgoStore.getState('cols')")) === "author,move,waiting,age,size,ci,reviewer,ticket", "✕ removes Repository");
  for (let i = 0; i < 80 && !(await active()).startsWith("add:updated_ago:"); i++) await key("Tab");
  check((await active()).startsWith("add:updated_ago:"), "Tab reaches + Since update");
  await key("Enter");
  check((await evaluate("window.__wgoStore.getState('cols')")) === "author,move,waiting,age,size,ci,reviewer,ticket,updated_ago", "+ adds Since update at the end");
  await key("Escape");
  check((await evaluate("document.querySelector('.atable-chooser').hidden")) && (await active()).includes("Columns"), "Esc closes the chooser and returns to its button");
  // Reset keeps the keyboard in the chooser (its own button is then disabled).
  await key("Enter");
  for (let i = 0; i < 80 && !(await active()).startsWith("reset:"); i++) await key("Tab");
  await key("Enter");
  check((await evaluate("window.__wgoStore.getState('cols')")) === "" && !(await evaluate("location.hash")).includes("cols=") && (await evaluate("document.activeElement.tagName === 'BUTTON' && !document.activeElement.disabled && document.querySelector('.atable-chooser').contains(document.activeElement)")),
    "Reset to defaults clears cols= and keeps focus in the chooser: " + (await active()));
  await evaluate("history.back()"); await sleep(300);
  await key("Escape");
  check((await evaluate("window.__wgoStore.getState('cols')")) === "author,move,waiting,age,size,ci,reviewer,ticket,updated_ago", "Back restores the columns: " + (await evaluate("location.hash + ' ' + window.__wgoStore.getState('cols')")));
  check((await heads()).join() === "Name,Author,Move,Waiting,Age,Size,CI,Reviews,Tickets,Since update", "headers follow cols=: " + (await heads()));

  // 7b. Multi-key sort by keyboard: Enter sorts, Shift+Enter adds a key;
  // rank and direction show in the headers.
  await evaluate("document.querySelector('thead button[data-col=updated_ago]').focus()");
  await key("Enter");
  check((await evaluate("window.__wgoStore.getState('sort')")) === "-updated_ago", "Enter sorts, longest first");
  await evaluate("document.querySelector('thead button[data-col=author]').focus()");
  await key("Enter", true);
  check((await evaluate("window.__wgoStore.getState('sort')")) === "-updated_ago,author", "Shift+Enter adds a key: " + (await evaluate("window.__wgoStore.getState('sort')")));
  const sortedRows = ["acme/web#9", "acme/web#12", "acme/api#42", "acme/api#38", "acme/api#41"];
  const sortShown = () => evaluate("({ rows: Array.from(document.querySelectorAll('table.atable tbody tr[data-i] td.nm')).map(td => td.firstChild.textContent), sort: Array.from(document.querySelectorAll('table.atable thead th')).map(th => th.getAttribute('aria-sort') + (th.querySelector('.si') ? th.querySelector('.si').textContent : '')).join() })");
  let shown = await sortShown();
  check(shown.rows.join() === sortedRows.join(), "rows sorted by Since update, then Author: " + shown.rows);
  check(shown.sort === "none,none▲2,none,none,none,none,none,none,none,descending▼1", "rank and direction in the headers: " + shown.sort);
  const sortedURL = await evaluate("location.href");
  await send("Page.navigate", { url: "about:blank" });
  await send("Page.navigate", { url: sortedURL });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap) && !!document.querySelector('table.atable tbody tr[data-i]')", "the table after a reload");
  shown = await sortShown();
  check(shown.rows.join() === sortedRows.join() && shown.sort === "none,none▲2,none,none,none,none,none,none,none,descending▼1", "sort and columns survive a reload: " + JSON.stringify(shown));
  check((await heads()).join() === "Name,Author,Move,Waiting,Age,Size,CI,Reviews,Tickets,Since update", "columns survive a reload");

  // 7c. Unknown sorts last in both directions (only acme/api#41 has an age).
  const ageCol = () => evaluate("(() => { const i = Array.from(document.querySelectorAll('table.atable thead th')).findIndex(th => th.textContent.startsWith('Age')); return Array.from(document.querySelectorAll('table.atable tbody tr[data-i]')).map(tr => tr.children[i].textContent) })()");
  await evaluate("document.querySelector('thead button[data-col=age]').click()");
  const ageDesc = await ageCol();
  await evaluate("document.querySelector('thead button[data-col=age]').click()");
  const ageAsc = await ageCol();
  check((await evaluate("window.__wgoStore.getState('sort')")) === "age" && ageDesc[0] === ageAsc[0] && /^\d+d$/.test(ageAsc[0]) && ageDesc.slice(1).concat(ageAsc.slice(1)).every((t) => t === "unknown"),
    "unknown last both ways: " + ageDesc + " / " + ageAsc);

  // 7d. Copy as Markdown uses the table's columns and sort.
  await send("Runtime.evaluate", { expression: "Array.from(document.querySelectorAll('button')).find(b => b.textContent === 'Copy as Markdown').click()", userGesture: true });
  await waitFor("document.querySelector('.status').textContent.startsWith('Copied') || document.querySelector('.status').textContent.includes('failed') || !document.querySelector('.copyout').hidden", "the table's copy");
  const tclip = await evaluate("navigator.clipboard.readText()", { userGesture: true });
  // The copy is one list in the table's order: no effort sections, each row once.
  const clipRows = tclip.split("\n").filter((l) => l.startsWith("- ")).map((l) => (/^- \[([^\]]+)\]/.exec(l) || /^- (\S+)/.exec(l))[1]);
  const tableRows = await rowNames();
  check(clipRows.join() === tableRows.join() && !tclip.includes("\n### "), "the copy lists the table's rows in its order: " + clipRows + " vs " + tableRows);
  check(tclip.includes("Sort: `age` (unknown values last)") && tclip.includes(" · author: alice · move: ") && tclip.includes(" · age: 15d · size: ") && tclip.includes("updated_ago: 1d"), "the copy has the table's columns: " + tclip.slice(0, 400));
  // A sort on the table's own column cannot reach the Markdown: the status says so.
  await evaluate("window.__wgoStore.setState({ sort: '-last_activity,age' }, { commit: true })");
  await send("Runtime.evaluate", { expression: "Array.from(document.querySelectorAll('button')).find(b => b.textContent === 'Copy as Markdown').click()", userGesture: true });
  await waitFor("document.querySelector('.status').textContent.includes('Last activity') || document.querySelector('.status').textContent.includes('failed')", "the note");
  check((await evaluate("document.querySelector('.status').textContent")).includes("Markdown cannot sort by Last activity"), "the status names the sort key the copy left out: " + (await evaluate("document.querySelector('.status').textContent")));
  check((await evaluate("navigator.clipboard.readText()", { userGesture: true })).includes("Sort: `age` (unknown values last)"), "the copy keeps the other keys");
  await evaluate("window.__wgoStore.setState({ sort: 'age' }, { commit: true })");

  // 7e. Rows by keyboard: arrows move and select, Shift+arrows only move,
  // Shift+Enter adds, Enter selects; hovering a row marks it in the store.
  await evaluate("Array.from(document.querySelectorAll('.atable-bar button')).find(b => b.textContent === 'Default order').click()");
  check(!(await evaluate("location.hash")).includes("sort=") && (await evaluate("document.activeElement.dataset.col")) === "name", "Default order clears sort= and leaves focus on the first header");
  await evaluate("document.querySelector('table.atable tbody tr[data-i]').focus()");
  const rowIds = await evaluate("window.__wgoStore.qres.rows.slice().sort()");
  await key("ArrowDown");
  const sel1 = await evaluate("Array.from(window.__wgoStore.selection)");
  check(sel1.length === 1 && (await evaluate("document.activeElement.getAttribute('aria-rowindex')")) === "3", "ArrowDown moves and selects: " + sel1);
  await key("ArrowDown", true);
  await key("Enter", true);
  check((await evaluate("window.__wgoStore.selection.size")) === 2 && (await evaluate("document.querySelectorAll('table.atable tbody tr.sel').length")) === 2, "Shift+Enter adds the focused row");
  await key("Enter");
  check((await evaluate("window.__wgoStore.selection.size")) === 1 && rowIds.length === 5, "Enter selects the focused row alone");
  const hov = await evaluate("(() => { const tr = document.querySelectorAll('table.atable tbody tr[data-i]')[1]; tr.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })); const h = window.__wgoStore.hover; document.querySelector('table.atable').dispatchEvent(new MouseEvent('mouseleave')); return [h, window.__wgoStore.hover] })()");
  check(hov[0] && rowIds.includes(hov[0]) && hov[1] === null, "hover marks the row's node, and leaving clears it: " + hov);

  // 7f. Without kind:, context rows are dimmed and can be hidden (in the URL).
  await evaluate("window.__wgoStore.setQuery('repo:acme/api', { commit: true })");
  const ctxN = await evaluate("document.querySelectorAll('table.atable tbody tr.ctx').length");
  const ctxBtn = await evaluate("Array.from(document.querySelectorAll('.atable-bar button')).find(b => /context row/.test(b.textContent)).textContent");
  check(ctxN > 0 && ctxBtn === "Hide " + ctxN + " context row" + (ctxN === 1 ? "" : "s"), "context rows are listed and dimmed: " + ctxN + " " + ctxBtn);
  await evaluate("Array.from(document.querySelectorAll('.atable-bar button')).find(b => /context row/.test(b.textContent)).click()");
  check((await evaluate("document.querySelectorAll('table.atable tbody tr.ctx').length")) === 0 && (await evaluate("location.hash")).includes("ctx=hide"), "the toggle hides them");
  await evaluate("history.back()"); await sleep(300);
  check((await evaluate("document.querySelectorAll('table.atable tbody tr.ctx').length")) === ctxN, "Back shows them again");
  await evaluate("window.__wgoStore.setQuery('repo:acme/api;kind:pr', { commit: true })");
  check((await evaluate("document.querySelectorAll('table.atable tbody tr.ctx').length")) === 0, "a kind: clause hides context rows");

  // 7g. The table's own columns in cols= do not break Copy as Markdown from
  // a view without columns.
  await evaluate("window.__wgoStore.setState({ view: 'graph', cols: 'state,repo,last_activity' }, { commit: true })");
  await send("Runtime.evaluate", { expression: "Array.from(document.querySelectorAll('button')).find(b => b.textContent === 'Copy as Markdown').click()", userGesture: true });
  await waitFor("document.querySelector('.status').textContent.startsWith('Copied') || document.querySelector('.status').textContent.includes('failed') || !document.querySelector('.copyout').hidden", "the graph's copy");
  const gclip = await evaluate("navigator.clipboard.readText()", { userGesture: true });
  check((await evaluate("document.querySelector('.status').textContent")).startsWith("Copied") && gclip.includes("· repo: acme/api") && !gclip.includes("state:"), "graph copy keeps the query fields of cols=: " + gclip.slice(0, 200));

  // 6. Nothing was refused by the CSP and nothing threw.
  // The browser's own favicon request is the only 404 the page may cause.
  const problems = events.filter((e) => (e.method === "Log.entryAdded" && e.params.entry.level === "error" && !/\/favicon\.ico$/.test(e.params.entry.url || "")) || e.method === "Runtime.exceptionThrown")
    .map((e) => e.method === "Runtime.exceptionThrown" ? e.params.exceptionDetails.text + " " + ((e.params.exceptionDetails.exception || {}).description || "") : e.params.entry.text + " " + (e.params.entry.url || ""));
  check(problems.length === 0, "console errors: " + problems.join(" | "));
}

// timeRenders times filter changes on the 2,000-node page: the synchronous
// re-render, and to the next frame (style, layout, paint), median of 7.
async function timeRenders(perfURL) {
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

const SHOT_SETS = {
  query: [
    ["query-table-light.png", "light", "#q=activity:2026-10-14;prstate:open,draft&view=table"],
    ["query-table-dark.png", "dark", "#q=repo:acme/api;kind:pr,ticket&view=table&sel=ticket:jira:TEAM-7"],
    ["query-graph-light.png", "light", "#q=prstate:draft"],
    ["query-error-light.png", "light", "#q=kind:pr;prstat:open"],
  ],
  // The analysis table on internal/dash/testdata/analysis_fixture.json.
  table: [
    ["table-pr-light.png", "light", "#view=table&q=kind:pr;prstate:open,draft&sel=pr:acme/api%23101"],
    ["table-pr-dark.png", "dark", "#view=table&q=kind:pr;prstate:open,draft&sel=pr:acme/api%23101"],
    ["table-ticket-light.png", "light", "#view=table&q=kind:ticket"],
    ["table-ticket-dark.png", "dark", "#view=table&q=kind:ticket"],
    ["table-context-light.png", "light", "#view=table&q=repo:acme/api&sort=kind,-updated_ago"],
    ["table-columns-dark.png", "dark", "#view=table&q=kind:pr&cols=repo,author,updated_ago,additions,deletions,mergeable&sort=-updated_ago", "columns"],
  ],
};

// timeTable times the analysis table on the analysis fixture scaled to
// 2,000 nodes (median of 7, synchronous and to the next frame): switching
// to it, re-sorting every row, and a kind:pr query; and checks that only
// the rows in view are in the DOM, following the scroll.
// The table's PR columns need a wider window than the graph.
SHOT_SETS.table.viewport = { width: 2200, height: 1000 };

async function timeTable(tableURL) {
  await send("Page.navigate", { url: "about:blank" });
  await send("Page.navigate", { url: tableURL + "#view=graph" });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap)", "the scaled analysis fixture");
  await sleep(1500);
  const r = await evaluate(`(async () => {
    const s = window.__wgoStore, frame = () => new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)));
    const med = (a) => a.slice().sort((x, y) => x - y)[a.length >> 1], out = {};
    const time = async (name, a, b) => {
      const sync = [], to = [];
      for (let i = 0; i < 7; i++) {
        const t0 = performance.now(); a(); const t1 = performance.now(); await frame(); sync.push(t1 - t0); to.push(performance.now() - t0);
        b(); await frame();
      }
      out[name] = med(sync).toFixed(1) + " ms (" + med(to).toFixed(1) + " to frame)";
    };
    await time("show", () => s.setState({ view: "table" }, { commit: true }), () => s.setState({ view: "graph" }, { commit: true }));
    s.setState({ view: "table" }, { commit: true }); await frame();
    out.rows = document.querySelector("table.atable").getAttribute("aria-rowcount") - 1;
    out.dom = document.querySelectorAll("table.atable tbody tr[data-i]").length;
    await time("sort", () => s.setState({ sort: "-updated_ago,repo" }, { commit: true }), () => s.setState({ sort: "" }, { commit: true }));
    await time("kindpr", () => s.setQuery("kind:pr", { commit: true }), () => s.setQuery("", { commit: true }));
    s.setQuery("kind:pr", { commit: true }); await frame();
    out.prs = document.querySelector("table.atable").getAttribute("aria-rowcount") - 1;
    // The most rows drawn in full: a query just under the 500-row threshold.
    await time("openQ", () => s.setQuery("kind:pr;prstate:open", { commit: true }), () => s.setQuery("", { commit: true }));
    s.setQuery("kind:pr;prstate:open", { commit: true }); await frame();
    out.open = document.querySelectorAll("table.atable tbody tr[data-i]").length;
    await time("openSort", () => s.setState({ sort: "-age" }, { commit: true }), () => s.setState({ sort: "" }, { commit: true }));
    s.setQuery("", { commit: true }); await frame();
    const sc = document.querySelector(".atable-scroll"); sc.scrollTop = sc.scrollHeight / 2; await frame(); await frame();
    const vis = Array.from(document.querySelectorAll("table.atable tbody tr[data-i]")).map((tr) => +tr.getAttribute("aria-rowindex"));
    out.mid = vis.length ? vis[0] + "-" + vis[vis.length - 1] : "";
    out.midOK = vis.length > 0 && vis[0] > 100;
    sc.scrollTop = 0; await frame(); await frame();
    return out;
  })()`);
  // Holding ArrowDown walks far past the first window without losing the
  // keyboard, and a wheel scroll that redraws the window keeps it too.
  await evaluate("document.querySelector('table.atable tbody tr[data-i]').focus()");
  for (let i = 0; i < 60; i++) await key("ArrowDown");
  await sleep(200);
  const walk = await evaluate("({ tag: document.activeElement.tagName, at: document.activeElement.getAttribute('aria-rowindex'), sel: Array.from(window.__wgoStore.selection).length })");
  check(walk.tag === "TR" && walk.at === "62" && walk.sel === 1, "ArrowDown through the virtual window keeps focus: " + JSON.stringify(walk));
  await evaluate("document.querySelector('.atable-scroll').scrollTop += 30");
  await sleep(200);
  check((await evaluate("document.activeElement.tagName")) === "TR", "a scroll that redraws the window keeps focus on a row");
  // A long wheel scroll draws the focused row away: the keyboard parks on
  // the scroller, and the next arrow resumes from that row, not from
  // whatever is drawn.
  const focusedId = await evaluate("Array.from(window.__wgoStore.selection)[0]");
  await evaluate("document.querySelector('.atable-scroll').scrollTop += 4000");
  await sleep(300);
  check((await evaluate("document.activeElement === document.querySelector('.atable-scroll')")), "a far scroll parks the keyboard on the scroller");
  check((await evaluate("Array.from(window.__wgoStore.selection)[0]")) === focusedId, "parking selects nothing");
  await key("ArrowDown");
  const resumed = await evaluate("({ tag: document.activeElement.tagName, at: document.activeElement.getAttribute('aria-rowindex'), sel: document.activeElement.classList.contains('sel') })");
  check(resumed.tag === "TR" && resumed.at === "63" && resumed.sel, "ArrowDown resumes from the focused row: " + JSON.stringify(resumed));
  // A re-sort, and Back, keep the keyboard on the same node wherever it lands.
  const node = await evaluate("Array.from(window.__wgoStore.selection)[0]");
  const onNode = () => evaluate("({ tag: document.activeElement.tagName, sel: document.activeElement.classList.contains('sel'), at: document.activeElement.getAttribute('aria-rowindex'), id: Array.from(window.__wgoStore.selection)[0] })");
  await evaluate("window.__wgoStore.setState({ sort: '-updated_ago,repo' }, { commit: true })");
  await sleep(200);
  const afterSort = await onNode();
  check(afterSort.tag === "TR" && afterSort.sel && afterSort.id === node && afterSort.at !== "63", "a re-sort keeps focus on the same node: " + JSON.stringify(afterSort));
  await evaluate("history.back()"); await sleep(400);
  const afterBack = await onNode();
  // Back also undoes the arrow keys' selection (one history entry with the
  // sort); the node is back at its old position, and so is the keyboard.
  check(afterBack.tag === "TR" && afterBack.at === "63", "Back keeps focus on the same node: " + JSON.stringify(afterBack));
  check(r.rows >= 2000 && r.dom < 200, "the table draws only the rows in view: " + r.dom + " of " + r.rows);
  check(r.midOK, "the drawn rows follow the scroll: " + r.mid);
  return "table on the scaled analysis fixture (" + r.rows + " rows, " + r.dom + " in the DOM): show " + r.show + ", re-sort " + r.sort + ", kind:pr (" + r.prs + " rows) " + r.kindpr +
    ", rows " + r.mid + " drawn at mid-scroll; " + r.open + " rows drawn in full: query " + r.openQ + ", re-sort " + r.openSort;
}

async function screenshots() {
  fs.mkdirSync(shotDir, { recursive: true });
  const shots = SHOT_SETS[shotSet];
  if (!shots) throw new Error("unknown --set " + shotSet + "; use " + Object.keys(SHOT_SETS).join(" or "));
  if (shots.viewport) await send("Emulation.setDeviceMetricsOverride", Object.assign({ deviceScaleFactor: 1, mobile: false }, shots.viewport));
  for (const [file, scheme, hash, open] of shots) {
    await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: scheme }] });
    await send("Page.navigate", { url: "about:blank" }); // a fresh document, not a same-page hash change
    await send("Page.navigate", { url: pageURL + hash });
    await waitFor("!!(window.__wgoStore && window.__wgoStore.snap)", "the snapshot");
    await sleep(1500);
    if (open === "columns") { await evaluate("document.querySelector('.atable-bar button[aria-controls]').click()"); await sleep(300); }
    if (open && open.startsWith("cvd:")) await send("Emulation.setEmulatedVisionDeficiency", { type: open.slice(4) });
    const shot = await send("Page.captureScreenshot", { format: "png" });
    if (open && open.startsWith("cvd:")) await send("Emulation.setEmulatedVisionDeficiency", { type: "none" });
    fs.writeFileSync(path.join(shotDir, file), Buffer.from(shot.data, "base64"));
  }
}

// ---------- gh-102: linked filters ----------
// The facet panel, the attention summary, the brushable when chart and the
// saved filter chips on the analysis fixture (--facets), by pointer and by
// keyboard, with the history model; then their latency at 2,000 nodes on
// the --perf-table page. --set=facets and facets-cvd shoot them on the
// analysis fixture, the latter under each colour-vision-deficiency
// simulation.
{
const FACET_SHOTS = "#q=kind:pr;!repo:acme/infra;ci:green,systemic_only,pending;when:2026-10-20..2026-10-30&sel=pr:acme/api%23101";
SHOT_SETS.facets = [
  ["facets-light.png", "light", FACET_SHOTS],
  ["facets-dark.png", "dark", FACET_SHOTS],
  ["shipped-week-light.png", "light", "#view=table&q=kind:pr;prstate:merged;merged:2026-10-16..2026-10-22"],
  ["shipped-week-dark.png", "dark", "#view=table&q=kind:pr;prstate:merged;merged:2026-10-16..2026-10-22"],
  ["not-covered-light.png", "light", "#q=kind:pr;prstate:merged;merged:2026-10-03..2026-10-09"],
  ["ranges-dark.png", "dark", "#view=table&q=kind:pr;size:30..299;age:..13;!author:carol&facets=size_bucket,created"],
];
SHOT_SETS["facets-cvd"] = [];
for (const type of ["protanopia", "deuteranopia", "tritanopia", "achromatopsia"]) {
  for (const scheme of ["light", "dark"]) SHOT_SETS["facets-cvd"].push(["cvd-" + type + "-" + scheme + ".png", scheme, FACET_SHOTS, "cvd:" + type]);
}
SHOT_SETS.facets.viewport = SHOT_SETS["facets-cvd"].viewport = { width: 1600, height: 1500 }; // every facet in view
SUITES.push({ name: "linked filters", flag: "facets", checks: facetChecks, perf: () => flag("--perf-table") ? timeFacets(flag("--perf-table")) : "", deadlineMs: 90000 });

// keyMod presses k with modifiers (Alt 1, Ctrl 2, Meta 4, Shift 8).
const MOD_KEYS = Object.assign({ ArrowLeft: [37, "ArrowLeft", ""], ArrowRight: [39, "ArrowRight", ""], Shift: [16, "ShiftLeft", ""], Home: [36, "Home", ""] }, KEYS);
async function keyMod(k, modifiers, type) {
  const [vk, code, text] = MOD_KEYS[k];
  if (type !== "up") await send("Input.dispatchKeyEvent", Object.assign({ type: text && !(modifiers & 1) ? "keyDown" : "rawKeyDown", key: k, code, windowsVirtualKeyCode: vk, modifiers: modifiers || 0 }, text ? { text } : {}));
  if (type !== "down") await send("Input.dispatchKeyEvent", { type: "keyUp", key: k, code, windowsVirtualKeyCode: vk, modifiers: type === "up" ? 0 : modifiers || 0 });
  await sleep(60);
}
// center is the middle of the element expr finds, in page coordinates.
async function center(expr) {
  const r = await evaluate("(() => { const e = " + expr + "; if (!e) return null; e.scrollIntoView({ block: 'nearest' }); const b = e.getBoundingClientRect(); return [b.left + b.width / 2, b.top + b.height / 2]; })()");
  if (!r) throw new Error("no element for " + expr);
  return r;
}
async function mouse(type, x, y, modifiers, buttons) {
  await send("Input.dispatchMouseEvent", { type, x, y, button: type === "mouseMoved" && !buttons ? "none" : "left", buttons: buttons == null ? (type === "mousePressed" ? 1 : 0) : buttons, clickCount: type === "mouseMoved" ? 0 : 1, modifiers: modifiers || 0 });
}
async function clickAt(expr, modifiers) {
  const [x, y] = await center(expr);
  await mouse("mouseMoved", x, y, modifiers, 0); await mouse("mousePressed", x, y, modifiers); await mouse("mouseReleased", x, y, modifiers);
  await sleep(120);
}
// dragFromTo presses on the first element, moves in steps to the second,
// and releases there.
async function dragFromTo(from, to) {
  const [x0, y0] = await center(from), [x1] = await center(to);
  await mouse("mouseMoved", x0, y0, 0, 0); await mouse("mousePressed", x0, y0);
  for (let i = 1; i <= 8; i++) { await mouse("mouseMoved", x0 + (x1 - x0) * i / 8, y0, 0, 1); await sleep(15); }
  await mouse("mouseReleased", x1, y0);
  await sleep(200);
}
const Q = () => evaluate("window.__wgoStore.query");
const row = (facet, key) => "document.querySelector('.lv-left section[data-facet=" + facet + "] [data-fk=\"" + facet + "\\u0001" + key + "\"]')";
const dayBar = (field, k) => "document.querySelector('.mult.fx-chart [data-fk=\"" + field + "\\u0001d" + k + "\"]')";

async function facetChecks(facetsURL) {
  await send("Browser.grantPermissions", { origin: new URL(facetsURL).origin, permissions: ["clipboardReadWrite", "clipboardSanitizedWrite"] }, null);
  await send("Emulation.setDeviceMetricsOverride", { width: 1600, height: 1000, deviceScaleFactor: 1, mobile: false });
  await send("Page.navigate", { url: "about:blank" });
  await send("Page.navigate", { url: facetsURL });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap) && !!document.querySelector('.lv-left section.fx')", "the facet panel");
  // 1. The panel lists the default facets in order, left of the views; the
  // overview leads with the attention summary, then at most three charts.
  check((await evaluate("Array.from(document.querySelectorAll('.lv-left section.fx')).map(s => s.dataset.facet).join()")) === "move,ci,prstate,repo,author,reviewer,waiting,age,size,ticket_status,sprint,mismatch", "default facets in order");
  check((await evaluate("document.querySelector('.lv-main').firstElementChild.className")) === "lv-left", "the panel is left of the views");
  const over = await evaluate("Array.from(document.querySelectorAll('.multiples > .mult')).filter(m => !m.hidden).map(m => m.querySelector('h2').firstChild.textContent)");
  check(over.join() === "Attention,When,PRs by state,Agent sessions by effort", "overview: " + over);
  check(!(await evaluate("!!document.querySelector('select.presets')")) && (await evaluate("document.querySelectorAll('.fchips button.fchip').length")) >= 9, "chips replace the saved filter menu");

  // 2. Ghost and coupled counts: a filter on PR state changes the repo
  // facet's dark bars, not its ghosts; the PR-state facet ignores its own.
  await evaluate("window.__wgoStore.setQuery('prstate:open', { commit: true })");
  await sleep(100);
  check((await evaluate(row("repo", "acme/web") + ".querySelector('.fx-n').textContent")) === "5/12", "repo acme/web under prstate:open: " + (await evaluate(row("repo", "acme/web") + ".textContent")));
  check((await evaluate(row("prstate", "merged") + ".querySelector('.fx-n').textContent")) === "6", "prstate's own clause is ignored");
  check((await evaluate(row("repo", "acme/web") + ".querySelector('.fx-solid').style.width")) !== (await evaluate(row("repo", "acme/web") + ".querySelector('.fx-ghost').style.width")), "solid and ghost bars differ");

  // 3. Pointer gestures, with ✓ and ✕ and aria-pressed, never colour alone.
  await clickAt(row("repo", "acme/api"));
  check((await Q()) === "prstate:open;repo:acme/api", "click: only this value: " + (await Q()));
  await clickAt(row("repo", "acme/web"), 8);
  check((await Q()) === "prstate:open;repo:acme/api,acme/web", "Shift-click adds: " + (await Q()));
  await clickAt(row("repo", "acme/web"), 4);
  check((await Q()) === "prstate:open;repo:acme/api", "⌘-click removes: " + (await Q()));
  await clickAt(row("repo", "acme/infra"), 1);
  check((await Q()) === "prstate:open;repo:acme/api;!repo:acme/infra", "Alt-click excludes: " + (await Q()));
  const marks = await evaluate("[" + row("repo", "acme/api") + ", " + row("repo", "acme/infra") + "].map(r => [r.className, r.querySelector('.fx-g').textContent, r.getAttribute('aria-pressed'), getComputedStyle(r.querySelector('.fx-l')).textDecorationLine])");
  check(marks[0][0].includes("sel") && marks[0][1] === "✓" && marks[0][2] === "true" && marks[1][0].includes("exc") && marks[1][1] === "✕" && marks[1][3] === "line-through", "chosen ✓, excluded ✕ and struck: " + JSON.stringify(marks));
  check((await evaluate("document.querySelector('.lv-left section[data-facet=repo] .fx-q').textContent")) === "repo:acme/api;!repo:acme/infra", "the facet shows its clauses");
  await clickAt(row("repo", "acme/api"));
  check((await Q()) === "prstate:open;!repo:acme/infra", "click again clears: " + (await Q()));
  // Back and forward step through the committed facet changes.
  await evaluate("history.back()"); await sleep(250);
  check((await Q()) === "prstate:open;repo:acme/api;!repo:acme/infra", "Back: " + (await Q()));
  await evaluate("history.back()"); await sleep(250);
  check((await Q()) === "prstate:open;repo:acme/api", "Back again: " + (await Q()));
  await evaluate("history.forward()"); await sleep(250);
  check((await Q()) === "prstate:open;repo:acme/api;!repo:acme/infra", "Forward: " + (await Q()));

  // 4. The same gestures by keyboard: Enter, Shift+Enter, Alt+Enter, arrows.
  await evaluate("window.__wgoStore.setQuery('kind:pr', { commit: true })"); await sleep(100);
  await evaluate(row("author", "alice") + ".focus()");
  await keyMod("Enter", 0);
  check((await Q()) === "kind:pr;author:alice", "Enter: " + (await Q()));
  check((await evaluate("document.activeElement.getAttribute('data-fk')")) === "author\u0001alice", "focus stays on the row");
  await keyMod("ArrowDown", 0);
  check((await evaluate("document.activeElement.getAttribute('data-fk')")) === "author\u0001bob", "ArrowDown moves");
  await keyMod("Enter", 8);
  check((await Q()) === "kind:pr;author:alice,bob", "Shift+Enter adds: " + (await Q()));
  await keyMod("ArrowDown", 0);
  await keyMod("Enter", 1);
  check((await Q()) === "kind:pr;author:alice,bob;!author:carol", "Alt+Enter excludes: " + (await Q()));
  await keyMod(" ", 0);
  check((await Q()) === "kind:pr;author:carol", "Space chooses only this value: " + (await Q()));
  await evaluate("window.__wgoStore.select(['pr:acme/web#203'], false)"); await sleep(100);
  check((await evaluate("document.activeElement.getAttribute('data-fk')")) === "author\u0001carol", "a re-render from elsewhere keeps the keyboard focus");

  // 5. A histogram: a bin click sets its range; dragging a handle is live
  // and one history entry; handles by keyboard; exact bounds.
  await evaluate("window.__wgoStore.setQuery('kind:pr', { commit: true })"); await sleep(100);
  await clickAt("document.querySelector('.lv-left section[data-facet=size] g.hb[data-fk=\"size\\u0001b4\"]')");
  check((await Q()) === "kind:pr;size:30..99", "a bin click: " + (await Q()));
  await dragFromTo("document.querySelector('.lv-left section[data-facet=size] .fx-handle-hi')", "document.querySelector('.lv-left section[data-facet=size] g.hb[data-fk=\"size\\u0001b6\"]')");
  const dragged = await Q();
  check(/^kind:pr;size:30\.\.(299|999)$/.test(dragged), "a handle drag sets the range: " + dragged);
  await evaluate("history.back()"); await sleep(250);
  check((await Q()) === "kind:pr;size:30..99", "one Back undoes the drag: " + (await Q()));
  await evaluate("document.querySelector('.lv-left section[data-facet=size] .fx-handle-lo').focus()");
  await keyMod("ArrowLeft", 0);
  check((await Q()) === "kind:pr;size:10..99", "ArrowLeft on the low handle: " + (await Q()));
  check((await evaluate("document.activeElement.getAttribute('data-fk')")) === "size\u0001lo", "the handle keeps focus");
  await evaluate("(() => { const i = document.querySelector('.lv-left section[data-facet=size] .fx-inputs input'); i.focus(); i.value = '50'; i.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); })()");
  await sleep(100);
  check((await Q()) === "kind:pr;size:50..99", "exact bounds: " + (await Q()));
  await clickAt("document.querySelector('.lv-left section[data-facet=size] .fx-clear')");
  check((await Q()) === "kind:pr", "a facet's Clear: " + (await Q()));
  // A press, key or drag that moves no handle changes nothing: no rewrite,
  // no history entry.
  await evaluate("window.__wgoStore.setQuery('kind:pr;size:5', { commit: true })"); await sleep(100);
  await evaluate("document.querySelector('.lv-left section[data-facet=size] .fx-handle-lo').focus()");
  await keyMod("Home", 0);
  check((await Q()) === "kind:pr;size:5", "Home on a handle already there keeps size:5: " + (await Q()));
  await evaluate("history.back()"); await sleep(250);
  check((await Q()) === "kind:pr", "and pushes no history entry: " + (await Q()));
  await evaluate("window.__wgoStore.setQuery('kind:pr;size:50..99', { commit: true })"); await sleep(100);
  await evaluate("document.querySelector('.lv-left section[data-facet=size] .fx-handle-lo').focus()");
  await keyMod("ArrowRight", 0);
  check((await Q()) === "kind:pr;size:50..99", "a clamped handle keeps exact bounds: " + (await Q()));
  const [hx, hy] = await center("document.querySelector('.lv-left section[data-facet=size] .fx-handle-hi')");
  await mouse("mouseMoved", hx, hy, 0, 0); await mouse("mousePressed", hx, hy);
  await mouse("mouseMoved", hx + 6, hy, 0, 1); await sleep(30); await mouse("mouseReleased", hx + 6, hy); await sleep(150);
  check((await Q()) === "kind:pr;size:50..99", "a drag under half a bin changes nothing: " + (await Q()));

  // 6. Time: brushing a past week from shipped replaces merged:-7d.. and
  // lists exactly that week's merges, one updated later; the previous
  // period is compared, or the page says the data does not cover it.
  await clickAt("Array.from(document.querySelectorAll('.fchips button.fchip')).find(b => b.textContent === 'shipped')");
  check((await Q()) === "kind:pr;prstate:merged;merged:-7d..", "the shipped chip");
  check((await evaluate("Array.from(document.querySelectorAll('.fchips button[aria-pressed=true]')).map(b => b.textContent).join()")) === "shipped", "its chip is pressed");
  check((await evaluate("document.querySelector('.mult.fx-chart h2').firstChild.textContent")) === "Merged per day", "the when chart shows merges");
  check((await evaluate("document.querySelector('.att-period').textContent")) === "2 merged · previous 3", "the attention summary compares: " + (await evaluate("document.querySelector('.att-period') && document.querySelector('.att-period').textContent")));
  // The window is 2026-10-03..2026-10-30, so 2026-10-16 is day 13.
  await dragFromTo(dayBar("merged", 13), dayBar("merged", 19));
  check((await Q()) === "kind:pr;prstate:merged;merged:2026-10-16..2026-10-22", "the brush: " + (await Q()));
  check((await evaluate("window.__wgoStore.qres.rows.slice().sort().join()")) === "pr:acme/api#108,pr:acme/web#204,pr:acme/web#205", "exactly the week's merges");
  check((await evaluate("document.querySelector('.att-period').textContent")) === "3 merged · previous 0" && (await evaluate("document.querySelector('.fx-period').textContent")) === "3 merged · previous 0", "previous period in the summary and the PR-state chart");
  check((await evaluate("getComputedStyle(document.querySelector('.mult.fx-chart .fx-band')).display")) !== "none", "the brushed band shows");
  await evaluate("history.back()"); await sleep(250);
  check((await Q()) === "kind:pr;prstate:merged;merged:-7d..", "one Back undoes the brush: it was one history entry");
  // By keyboard: Shift+arrows from a day, committed when Shift is released.
  await evaluate(dayBar("merged", 0) + ".focus()");
  for (let i = 0; i < 6; i++) await keyMod("ArrowRight", 8, "down");
  await keyMod("Shift", 0, "up");
  check((await Q()) === "kind:pr;prstate:merged;merged:2026-10-03..2026-10-09", "Shift+arrows brush: " + (await Q()));
  check((await evaluate("document.querySelector('.att-period').textContent")) === "1 merged · previous 7 days not covered: merges are known from 2026-10-02", "uncovered: " + (await evaluate("document.querySelector('.att-period').textContent")));
  await evaluate("history.back()"); await sleep(250);
  check((await Q()) === "kind:pr;prstate:merged;merged:-7d..", "the keyboard brush is one entry: " + (await Q()));
  // Without shipped, the chart is when, and a brush sets when:a..b.
  await evaluate("window.__wgoStore.setQuery('kind:pr', { commit: true })"); await sleep(100);
  await dragFromTo(dayBar("when", 20), dayBar("when", 27));
  check((await Q()) === "kind:pr;when:2026-10-23..2026-10-30", "a when brush: " + (await Q()));
  await clickAt(dayBar("when", 17));
  check((await Q()) === "kind:pr;when:2026-10-20", "a day click: " + (await Q()));
  // The When chart's own Clear; local activity is hatched where it was not
  // recorded, and each recorded day names its jj changes.
  await clickAt("document.querySelector('.mult.fx-chart .fx-clear')");
  check((await Q()) === "kind:pr", "the When chart's Clear: " + (await Q()));
  check((await evaluate("document.querySelector('.mult.fx-chart .fx-hatch-text').textContent")) === "local activity not recorded before 10-17", "the unrecorded days say so");
  check((await evaluate("!!document.querySelector('.mult.fx-chart rect.fx-hatch')")) && /local activity not recorded/.test(await evaluate(dayBar("when", 0) + ".getAttribute('aria-label')")), "and are hatched, not zero");
  check(/; \d+ local changes?/.test(await evaluate(dayBar("when", 26) + ".getAttribute('aria-label')")), "a recorded day names its changes: " + (await evaluate(dayBar("when", 26) + ".getAttribute('aria-label')")));
  // From shipped, a merge day click keeps the chart on merges; a second
  // click replaces it.
  await evaluate("window.__wgoStore.setQuery('kind:pr;prstate:merged;merged:-7d..', { commit: true })"); await sleep(100);
  await clickAt(dayBar("merged", 25));
  check((await Q()) === "kind:pr;prstate:merged;merged:2026-10-28", "a merge day click: " + (await Q()));
  check((await evaluate("document.querySelector('.mult.fx-chart h2').firstChild.textContent")) === "Merged per day" && (await evaluate("document.activeElement.getAttribute('data-fk')")) === "merged\u0001d25", "the chart and the focus stay on merges");
  check((await evaluate("document.querySelector('.att-period').textContent")) === "1 merged · previous 0", "the previous period stays: " + (await evaluate("document.querySelector('.att-period') && document.querySelector('.att-period').textContent")));
  await clickAt(dayBar("merged", 26));
  check((await Q()) === "kind:pr;prstate:merged;merged:2026-10-29" && (await evaluate("window.__wgoStore.qres.rows.join()")) === "pr:acme/infra#303", "a second day click replaces the first: " + (await Q()));

  // 7. The attention summary: each count is its saved filter's row count,
  // and its link applies the filter; only "your move" takes the accent.
  for (const name of ["my-move", "waiting-reviewers", "nobody"]) {
    const n = await evaluate("document.querySelector('[data-fk=\"\\u0001att" + name + "\"] .att-n').textContent");
    await clickAt("document.querySelector('[data-fk=\"\\u0001att" + name + "\"]')");
    const got = await evaluate("({ q: window.__wgoStore.query, rows: window.__wgoStore.qres.rows.length, chip: Array.from(document.querySelectorAll('.fchips button[aria-pressed=true]')).map(b => b.textContent).join() })");
    check(got.chip === name && String(got.rows) === n, name + ": shows " + n + ", applies " + JSON.stringify(got));
  }
  const accent = await evaluate("Array.from(document.querySelectorAll('.att-link')).map(a => a.classList.contains('mine'))");
  check(accent.join() === "true,false,false", "the accent is for your move only");

  // 8. Copy as config, More filters, the selection's share, Clear all.
  await send("Runtime.evaluate", { expression: "Array.from(document.querySelectorAll('.fchips button')).find(b => b.textContent === 'Copy as config').click()", userGesture: true });
  await sleep(200);
  check((await evaluate("navigator.clipboard.readText()", { userGesture: true })) === "my-filter = \"kind:pr;move:nobody\"", "Copy as config");
  await evaluate("(() => { const s = document.querySelector('.lv-left select.fx-more'); s.value = 'size_bucket'; s.dispatchEvent(new Event('change', { bubbles: true })); })()");
  await sleep(150);
  check((await evaluate("!!document.querySelector('.lv-left section[data-facet=size_bucket]')")) && (await evaluate("location.hash")).includes("facets=size_bucket"), "More filters adds a facet, in the URL");
  await evaluate("history.back()"); await sleep(250);
  check(!(await evaluate("!!document.querySelector('.lv-left section[data-facet=size_bucket]')")), "Back removes it");
  await evaluate("window.__wgoStore.select(['pr:acme/api#101'], false)"); await sleep(100);
  check((await evaluate("document.querySelectorAll('.lv-left .fx-part').length")) > 3 && (await evaluate("document.querySelectorAll('.mult.fx-chart rect.part').length")) >= 2, "the selection shows as inner bars in every facet");
  await clickAt("document.querySelector('.lv-left .fx-clearall')");
  check((await Q()) === "", "Clear all");
  const problems = events.filter((e) => (e.method === "Log.entryAdded" && e.params.entry.level === "error" && !/\/favicon\.ico$/.test(e.params.entry.url || "")) || e.method === "Runtime.exceptionThrown");
  check(problems.length === 0, "console errors: " + JSON.stringify(problems.slice(0, 2)));
  await send("Emulation.setDeviceMetricsOverride", { width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
}

// timeFacets times facet changes on the analysis fixture scaled to 2,000
// nodes (median of 7, synchronous and to the next frame): a committed
// facet click and a live drag step, re-rendering the facets, the overview
// and the active view (graph, then table).
async function timeFacets(tableURL) {
  await send("Page.navigate", { url: "about:blank" });
  await send("Page.navigate", { url: tableURL + "#view=graph" });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap) && !!document.querySelector('.lv-left section.fx')", "the scaled analysis fixture");
  await sleep(1500);
  const out = [];
  for (const view of ["graph", "table"]) {
    const r = await evaluate(`(async () => {
      const s = window.__wgoStore, frame = () => new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)));
      const med = (a) => a.slice().sort((x, y) => x - y)[a.length >> 1];
      s.setState({ view: ${JSON.stringify(view)} }, { commit: true }); await frame(); await frame();
      const t = { click: [], clickF: [], drag: [], dragF: [] };
      for (let i = 0; i < 7; i++) {
        for (const [k, q, opts] of [["click", "kind:pr;repo:acme/api;!ci:failing", { commit: true }], ["drag", "kind:pr;repo:acme/api;!ci:failing;size:30..299", {}], ["drag", "kind:pr;repo:acme/api;!ci:failing;size:30..999", {}], ["click", "kind:pr", { commit: true }]]) {
          const t0 = performance.now(); s.setQuery(q, opts); const t1 = performance.now(); await frame();
          t[k].push(t1 - t0); t[k + "F"].push(performance.now() - t0);
        }
      }
      return { n: s.idx.byId.size, facets: document.querySelectorAll('.lv-left section.fx').length, click: med(t.click), clickF: med(t.clickF), drag: med(t.drag), dragF: med(t.dragF) };
    })()`);
    out.push(view + ": facet click " + r.click.toFixed(1) + " ms (" + r.clickF.toFixed(1) + " to frame), drag step " + r.drag.toFixed(1) + " ms (" + r.dragF.toFixed(1) + " to frame)");
    if (view === "graph") out.unshift("facets on the scaled analysis fixture (" + r.n + " nodes, " + r.facets + " facets)");
  }
  return out.join("; ");
}
}
// ---------- end gh-102 ----------

// ---------- gh-103: the scatterplot and the CI grid ----------
// The scatterplot and the CI grid on the analysis fixture (--scatter-ci):
// what each view draws, and every gesture by pointer and by keyboard alone;
// then "+n more" and their timings on the --perf-table page. --set=gh103
// shoots both views on the analysis fixture, light and dark, then under each
// colour-vision-deficiency simulation.
{
SHOT_SETS.gh103 = [
  ["scatter-light.png", "light", "#view=scatter&q=kind:pr&sel=pr:acme/web%23203"],
  ["scatter-dark.png", "dark", "#view=scatter&q=kind:pr&sel=pr:acme/web%23203"],
  ["scatter-age-median-light.png", "light", "#view=scatter&q=kind:pr&x=age&median=on"],
  ["scatter-age-median-dark.png", "dark", "#view=scatter&q=kind:pr&x=age&median=on"],
  ["ci-light.png", "light", "#view=ci&q=kind:pr&sel=pr:acme/api%23101&check=pr:acme/api%23101%7Cvendor-hsm-tests"],
  ["ci-dark.png", "dark", "#view=ci&q=kind:pr&sel=pr:acme/api%23101&check=pr:acme/api%23101%7Cvendor-hsm-tests"],
];
for (const type of ["protanopia", "deuteranopia", "tritanopia", "achromatopsia"]) {
  for (const scheme of ["light", "dark"]) {
    SHOT_SETS.gh103.push(["scatter-age-" + type + "-" + scheme + ".png", scheme, "#view=scatter&q=kind:pr&x=age", "cvd:" + type]);
    SHOT_SETS.gh103.push(["ci-" + type + "-" + scheme + ".png", scheme, "#view=ci&q=kind:pr", "cvd:" + type]);
  }
}
SHOT_SETS.gh103.viewport = { width: 1400, height: 900 };
SUITES.push({ name: "scatterplot and CI grid", flag: "scatter-ci", checks: gh103Checks, perf: () => flag("--perf-table") ? timeGh103(flag("--perf-table")) : "", deadlineMs: 90000 });

async function gh103Nav(base, hash, ready) {
  await send("Page.navigate", { url: "about:blank" });
  await send("Page.navigate", { url: base + hash });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap)" + (ready ? " && " + ready : ""), hash);
  await sleep(250);
}
// mouse drives the pointer as a person does (Input.dispatchMouseEvent);
// path is [[x, y], …] from press to release, shift holds Shift.
async function mouse(path, shift) {
  const modifiers = shift ? 8 : 0, [x0, y0] = path[0], [x1, y1] = path[path.length - 1];
  await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: x0, y: y0, modifiers });
  await send("Input.dispatchMouseEvent", { type: "mousePressed", x: x0, y: y0, button: "left", buttons: 1, clickCount: 1, modifiers });
  for (const [x, y] of path.slice(1)) await send("Input.dispatchMouseEvent", { type: "mouseMoved", x, y, button: "left", buttons: 1, modifiers });
  await send("Input.dispatchMouseEvent", { type: "mouseReleased", x: x1, y: y1, button: "left", buttons: 0, clickCount: 1, modifiers });
  await sleep(150);
}
// press sends a printable key (key() covers the named ones).
async function press(k) {
  await send("Input.dispatchKeyEvent", { type: "keyDown", key: k, text: k });
  await send("Input.dispatchKeyEvent", { type: "keyUp", key: k });
  await sleep(80);
}
const scMarkBoxes = (sel) => evaluate("Array.from(document.querySelectorAll(" + JSON.stringify(sel) + ")).map(g => { const r = g.getBoundingClientRect(); return { id: g.dataset.id, x: r.left + r.width / 2, y: r.top + r.height / 2 } })");
const sortedSel = () => evaluate("Array.from(window.__wgoStore.selection).sort()");

async function gh103Checks(analysisURL) {
  // S1. The scatter draws the fixture: log axes labelled, shapes by move,
  // unknowns in the strips, the accent and × where they belong, and at most
  // five outlier labels.
  await gh103Nav(analysisURL, "#view=scatter&q=kind:pr", "!!document.querySelector('.sc-marks .sc-mk')");
  const sc = await evaluate(`(() => { const q = (s) => Array.from(document.querySelectorAll(s)); return {
    titles: q('.sc-title').map(t => t.textContent), shapes: ['circle', 'triangle', 'square', 'ring', 'diamond'].map(s => q('.sc-marks .sc-mk.s-' + s).length),
    marks: q('.sc-marks .sc-mk').length, zones: q('.sc-marks .sc-mk').reduce((a, g) => (a[g.dataset.zone] = (a[g.dataset.zone] || 0) + 1, a), {}),
    strips: q('.sc-stripl').map(t => t.firstChild.textContent), outl: q('.sc-lbl.out').map(t => t.textContent), mine: q('.sc-marks .sc-mk.mine').map(g => g.dataset.id).sort(),
    fail: q('.sc-marks .sc-mk.fail').map(g => g.dataset.id).sort(), aria: q('.sc-marks .sc-mk').map(g => g.getAttribute('aria-label')), ticks: q('.sc-tick').map(t => t.textContent),
    key: document.querySelector('.sc-key').textContent, tabs: q('.view-tabs button').map(b => b.textContent) } })()`);
  check(sc.tabs.includes("Scatter") && sc.tabs.includes("CI grid"), "both views are tabs: " + sc.tabs);
  check(sc.titles.join("|") === "Waiting (days, log scale)|Size (lines changed, log scale)", "log axes labelled: " + sc.titles);
  check(sc.marks === 20 && sc.shapes.every((n) => n > 0), "every PR row a mark, every shape drawn: " + sc.marks + " " + sc.shapes);
  check(sc.zones.plot === 5 && sc.zones.xstrip === 14 && sc.zones.corner === 1, "unknowns in the strips, not at zero: " + JSON.stringify(sc.zones));
  check(sc.strips.includes("Waiting unknown: 1 · not waiting: 13") && sc.strips.includes("both"), "the strips are labelled: " + sc.strips);
  check(sc.outl.length >= 1 && sc.outl.length <= 5, "at most five outlier labels: " + sc.outl);
  check(sc.mine.join() === "pr:acme/api#103,pr:acme/api#104,pr:acme/infra#301,pr:acme/web#202", "the accent marks only alice's move: " + sc.mine);
  check(sc.fail.join() === "pr:acme/api#103,pr:acme/infra#301,pr:acme/web#203", "× marks a CI failing on its own checks: " + sc.fail);
  check(sc.ticks.includes("1,000") && sc.ticks.includes("7d"), "gridlines at readable steps: " + sc.ticks);
  const ariaRE = /^acme\/(api|web|infra)#\d+: Waiting .+, Size .+, .*move/;
  check(sc.aria.every((a) => ariaRE.test(a)), "every mark says ref, values and move: " + sc.aria.find((a) => !ariaRE.test(a)));
  check(sc.key.includes("reviewer's move") && sc.key.includes("your move") && sc.key.includes("CI failing"), "one compact key: " + sc.key);
  check((await evaluate("getComputedStyle(document.querySelector('.sc-chart')).position")) === "relative", "the addon's stylesheet applies under the CSP");

  // S2. Axis switching is in the URL and in history.
  await evaluate("(() => { const s = document.querySelectorAll('.sc-bar select')[0]; s.value = 'age'; s.dispatchEvent(new Event('change', { bubbles: true })); })()");
  await sleep(150);
  check((await evaluate("location.hash")).includes("x=age") && (await evaluate("window.__wgoStore.getState('x')")) === "age", "x= follows the axis: " + (await evaluate("location.hash")));
  check((await evaluate("document.querySelector('.sc-title').textContent")) === "Age (days, log scale)" && (await evaluate("document.querySelectorAll('.sc-marks .sc-mk[data-zone=plot]').length")) === 19, "the age axis plots 19");
  await evaluate("(() => { const s = document.querySelectorAll('.sc-bar select')[1]; s.value = 'files'; s.dispatchEvent(new Event('change', { bubbles: true })); })()");
  await sleep(150);
  check((await evaluate("location.hash")).includes("y=files"), "y= follows the axis");
  // (One Back only: by now Chrome's 50-entry session history has dropped
  // this document's first entry.)
  await evaluate("history.back()"); await sleep(300);
  check(!(await evaluate("location.hash")).includes("y=") && (await evaluate("window.__wgoStore.getState('x')")) === "age", "Back restores the axes: y= goes, x= stays");
  await evaluate("(() => { const s = document.querySelectorAll('.sc-bar select')[0]; s.value = 'waiting'; s.dispatchEvent(new Event('change', { bubbles: true })); })()");
  await sleep(150);
  check(!(await evaluate("location.hash")).includes("x=") && (await evaluate("document.querySelector('.sc-title').textContent")) === "Waiting (days, log scale)", "the default axis leaves the URL");

  // S3. Brush select: a drag selects the marks inside, as one history entry;
  // Shift adds; the table shows the same selection.
  const plotted = await scMarkBoxes(".sc-marks .sc-mk[data-zone=plot]");
  const xs = plotted.map((m) => m.x), ys = plotted.map((m) => m.y);
  const r = [Math.min(...xs) - 9, Math.min(...ys) - 9, Math.max(...xs) + 9, Math.max(...ys) + 9];
  await mouse([[r[0], r[1]], [(r[0] + r[2]) / 2, (r[1] + r[3]) / 2], [r[2], r[3]]]);
  const brushed = await sortedSel();
  check(brushed.join() === plotted.map((m) => m.id).sort().join(), "the brush selects the marks inside: " + brushed);
  check(decodeURIComponent(await evaluate("location.hash")).includes("sel="), "the selection is in the URL");
  const corner = (await scMarkBoxes(".sc-marks .sc-mk[data-zone=corner]"))[0];
  await mouse([[corner.x - 8, corner.y - 8], [corner.x, corner.y + 2], [corner.x + 8, corner.y + 8]], true);
  check((await sortedSel()).length === 6 && (await sortedSel()).includes(corner.id), "Shift+drag adds: " + (await sortedSel()));
  await evaluate("history.back()"); await sleep(300);
  check((await sortedSel()).join() === brushed.join(), "one Back undoes the Shift+drag");
  check((await evaluate("document.querySelectorAll('.sc-selring').length")) === 5 && (await evaluate("document.querySelectorAll('.sc-lbl.sel').length")) >= 1, "selected marks are ringed and labelled");
  await evaluate("window.__wgoStore.setState({ view: 'table' }, { commit: true })"); await sleep(200);
  check((await evaluate("document.querySelectorAll('table.atable tbody tr.sel').length")) === 5, "the table shows the scatter's selection");
  await evaluate("history.back()"); await sleep(300);

  // S4. A click selects one mark; a click on nothing clears.
  const one = plotted.find((m) => m.id === "pr:acme/api#101");
  await mouse([[one.x, one.y]]);
  check((await sortedSel()).join() === "pr:acme/api#101", "a click selects the mark: " + (await sortedSel()));
  check((await evaluate("Array.from(document.querySelectorAll('.sc-lbl.sel')).map(t => t.textContent).join()")) === "acme/api#101", "the selected mark has a direct label");
  check((await evaluate("!document.querySelector('.sc-tip').hidden && document.querySelector('.sc-tip').textContent.includes('acme/api#101')")), "details on demand: the tooltip");
  await mouse([[r[2] + 40, r[1] - 4]]);
  check((await evaluate("window.__wgoStore.selection.size")) === 0, "a click on nothing clears the selection");

  // S5. Median lines are off by default, and in the URL when on.
  check((await evaluate("document.querySelectorAll('.sc-med').length")) === 0, "no median lines by default");
  await evaluate("document.querySelector('.sc-bar input[type=checkbox]').click()"); await sleep(150);
  const meds = await evaluate("Array.from(document.querySelectorAll('.sc-medl')).map(t => t.textContent)");
  check((await evaluate("location.hash")).includes("median=on") && meds.length === 2 && meds.every((t) => t.startsWith("median ")), "median lines labelled with their values: " + meds);

  // S6. The wheel zooms; Reset zoom returns.
  const plot = await evaluate("(() => { const r = document.querySelector('.sc-over').getBoundingClientRect(); return { x: r.left + r.width * 0.6, y: r.top + r.height * 0.45 } })()");
  await send("Input.dispatchMouseEvent", { type: "mouseWheel", x: plot.x, y: plot.y, deltaX: 0, deltaY: -400 }); await sleep(200);
  check(!(await evaluate("document.querySelector('.sc-bar button').disabled")), "the wheel zooms in");
  await evaluate("document.querySelector('.sc-bar button').click()"); await sleep(100);
  check((await evaluate("document.querySelector('.sc-bar button').disabled")), "Reset zoom");

  // S7. Keyboard only, in the table's sort order: arrows move and select,
  // Shift+arrows only move, Shift+Enter adds, Enter selects; + zooms, 0 resets.
  await gh103Nav(analysisURL, "#view=scatter&q=kind:pr&sort=-size", "!!document.querySelector('.sc-marks .sc-mk')");
  const order = await evaluate("(() => { const s = window.__wgoStore, T = WGOLive.tableLogic, ix = s.qi; return T.sortRows(ix, s.qres.rowIdx.filter(i => ix.kinds[i] === 'pr'), T.sortKeys(s.qi.reg, s.getState('sort'))).map(i => ix.ids[i]) })()");
  await evaluate("document.querySelector('.sc-marks .sc-mk[tabindex=\"0\"]').focus()");
  const focused = () => evaluate("document.activeElement.dataset.id || ''");
  check((await focused()) === order[0], "the keyboard enters at the first mark in the table's order: " + (await focused()));
  await key("ArrowDown");
  check((await focused()) === order[1] && (await sortedSel()).join() === order[1], "ArrowDown moves to the next and selects it");
  check((await evaluate("document.activeElement.getAttribute('aria-label')")).startsWith(order[1].replace(/^pr:/, "") + ": Waiting"), "the focused mark's aria-label");
  await key("ArrowDown", true);
  await key("Enter", true);
  check((await sortedSel()).join() === [order[1], order[2]].sort().join(), "Shift+ArrowDown only moves; Shift+Enter adds");
  await key("Enter");
  check((await sortedSel()).join() === order[2], "Enter selects the focused mark alone");
  await press("+");
  check(!(await evaluate("document.querySelector('.sc-bar button').disabled")) && (await focused()) !== "", "+ zooms and keeps the keyboard on a mark");
  await press("0");
  check((await evaluate("document.querySelector('.sc-bar button').disabled")), "0 resets the zoom");

  // S8. From 1,000 marks the canvas draws them (forced here): a quadtree
  // still hit-tests the brush, and one proxy carries the keyboard.
  await evaluate("WGOLive.scatterOptions = { canvasMin: 0 }; window.__wgoStore.setState({ x: 'age' }, { commit: true })"); await sleep(200);
  const cv = await evaluate("({ canvas: !document.querySelector('.sc-canvas').hidden, svgMarks: document.querySelectorAll('.sc-marks .sc-mk:not(.sc-proxy)').length, proxy: document.querySelector('.sc-proxy') && document.querySelector('.sc-proxy').getAttribute('aria-label') })");
  check(cv.canvas && cv.svgMarks === 0 && /^acme\//.test(cv.proxy || ""), "the canvas path: " + JSON.stringify(cv));
  const over = await evaluate("(() => { const r = document.querySelector('.sc-over').getBoundingClientRect(); return [r.left, r.top, r.right, r.bottom] })()");
  await mouse([[over[0] + 60, over[1] + 4], [over[2] - 4, over[3] - 30]]);
  check((await evaluate("window.__wgoStore.selection.size")) >= 15, "the canvas brush selects through the quadtree: " + (await evaluate("window.__wgoStore.selection.size")));
  await evaluate("document.querySelector('.sc-proxy').focus()");
  const p0 = await evaluate("({ label: document.activeElement.getAttribute('aria-label'), pos: document.activeElement.getAttribute('aria-posinset') })");
  await key("ArrowDown");
  const p1 = await evaluate("({ label: document.activeElement.getAttribute('aria-label'), live: document.querySelector('.sc-chart [aria-live]').textContent, pos: document.activeElement.getAttribute('aria-posinset') })");
  check(p1.label !== p0.label && p1.live === p1.label && +p1.pos === +p0.pos + 1, "the proxy walks the marks and announces each: " + JSON.stringify([p0, p1]));
  await evaluate("WGOLive.scatterOptions = null");

  // G1. The CI grid: one table per repository; the systemic check is a
  // labelled solid row in acme/api and not in acme/web; PRs failing only it
  // have no isolated failing cells.
  await gh103Nav(analysisURL, "#view=ci&q=kind:pr", "!!document.querySelector('table.cg')");
  const cg = await evaluate(`(() => { const sec = (k) => document.querySelector('section.cg-repo[data-repo="' + k + '"]'), api = sec('acme/api'), web = sec('acme/web');
    const own = (s, id) => s.querySelectorAll('td.cg-cell.failure:not(.sys)[data-pr="' + id + '"]').length;
    const row = (s, n) => s.querySelector('th.cg-row[data-check="' + n + '"]').parentNode;
    return { repos: Array.from(document.querySelectorAll('section.cg-repo h3')).map(h => h.firstChild.textContent),
      cols: Array.from(api.querySelectorAll('th.cg-col')).map(t => t.textContent), apiSys: Array.from(api.querySelectorAll('tr.sys th')).map(t => t.textContent),
      apiSysCells: Array.from(row(api, 'vendor-hsm-tests').querySelectorAll('td')).map(td => td.className.replace('cg-cell ', '') + td.textContent),
      webSys: web.querySelectorAll('tr.sys').length, webRow: Array.from(row(web, 'vendor-hsm-tests').querySelectorAll('td.failure')).map(td => td.className),
      ownApi: ['101', '102', '104', '106'].map(n => own(api, 'pr:acme/api#' + n)), own103: own(api, 'pr:acme/api#103'),
      trunc: Array.from(web.querySelectorAll('th.cg-col.trunc')).map(t => t.dataset.pr), unread: web.querySelector('.cg-unread') ? web.querySelector('.cg-unread').textContent : '',
      skipped: web.querySelector('td[data-pr="pr:acme/web#201"][data-check="docs-preview"]').getAttribute('aria-label'),
      labels: Array.from(document.querySelectorAll('td.cg-cell')).every(td => (td.getAttribute('aria-label') || '').length > 10) } })()`);
  check(cg.repos.join() === "acme/api,acme/infra,acme/web", "one grid per repository: " + cg.repos);
  check(cg.cols.join() === "#103,#101,#102,#104,#106,#105,#107,#108,#109", "columns by failures, then number: " + cg.cols);
  check(cg.apiSys.length === 1 && cg.apiSys[0] === "vendor-hsm-testsfails everywhere — 5 of 6 PRs", "the systemic row is labelled in acme/api: " + cg.apiSys);
  check(cg.apiSysCells.slice(0, 5).every((c) => c === "failure sys×") && cg.apiSysCells.slice(5).every((c) => !c.startsWith("failure")), "it reads as a solid line from the left: " + cg.apiSysCells);
  check(cg.webSys === 0 && cg.webRow.length === 1 && !cg.webRow[0].includes("sys"), "not systemic in acme/web: an isolated failing cell there");
  check(cg.ownApi.every((n) => n === 0) && cg.own103 === 1, "PRs failing only the systemic check have no isolated failing cells: " + cg.ownApi + " / " + cg.own103);
  check(cg.trunc.join() === "pr:acme/web#202" && cg.unread.includes("acme/web#207"), "truncated checks marked on the column; unread checks named: " + cg.trunc + " " + cg.unread);
  check(cg.skipped.endsWith("docs-preview: skipped") && cg.labels, "blank cells carry an aria-label: " + cg.skipped);

  // G2. A row header applies check_failing:, again removes it; a cell
  // selects the PR and shows the check in the detail panel; a column header
  // selects the PR.
  const clickCell = (sel) => evaluate("document.querySelector(" + JSON.stringify(sel) + ").dispatchEvent(new MouseEvent('click', { bubbles: true }))");
  await clickCell("section[data-repo='acme/api'] th.cg-row[data-check='vendor-hsm-tests']"); await sleep(150);
  check((await evaluate("window.__wgoStore.query")) === "kind:pr;check_failing:vendor-hsm-tests", "a row header applies check_failing:");
  check((await evaluate("document.querySelectorAll('section[data-repo=\"acme/api\"] th.cg-col').length")) === 5 && (await evaluate("document.querySelector('th.cg-row.on') !== null")), "the grid follows the filter and marks its row");
  await clickCell("section[data-repo='acme/api'] th.cg-row[data-check='vendor-hsm-tests']"); await sleep(150);
  check((await evaluate("window.__wgoStore.query")) === "kind:pr", "again removes it");
  await clickCell("td[data-pr='pr:acme/api#101'][data-check='vendor-hsm-tests']"); await sleep(150);
  const det = await evaluate("({ sel: Array.from(window.__wgoStore.selection).join(), hash: decodeURIComponent(location.hash), text: document.querySelector('.detail .ckd') ? document.querySelector('.detail .ckd').textContent : '', href: document.querySelector('.detail .ckd a') ? document.querySelector('.detail .ckd a').href : '', ck: document.querySelectorAll('td.cg-cell.ck').length })");
  check(det.sel === "pr:acme/api#101" && det.hash.includes("check=pr:acme/api#101|vendor-hsm-tests") && det.text === "Check vendor-hsm-tests: failure · details" && det.href.endsWith("/runs/7100004") && det.ck === 1,
    "a cell selects the PR and the detail panel links its check: " + JSON.stringify(det));
  // The check belongs to that PR in the CI grid: another view hides it, and
  // any selection the grid did not make (a scatter click, Esc) clears it.
  const ckd = () => evaluate("document.querySelector('.detail .ckd') ? document.querySelector('.detail .ckd').textContent : ''");
  const hashCheck = async () => /(^|&)check=/.test((await evaluate("location.hash")).slice(1));
  await evaluate("window.__wgoStore.setState({ view: 'scatter', x: 'age' }, { commit: true })"); await sleep(250);
  check((await ckd()) === "" && (await hashCheck()), "in another view the detail panel leaves the check out (the URL keeps it with its PR)");
  const m208 = (await scMarkBoxes(".sc-marks .sc-mk[data-id='pr:acme/web#208']"))[0];
  await mouse([[m208.x, m208.y]]);
  check((await sortedSel()).join() === "pr:acme/web#208" && !(await hashCheck()) && (await ckd()) === "", "a scatter click on another PR drops the check: " + (await evaluate("location.hash")));
  await evaluate("window.__wgoStore.setState({ view: 'ci', x: '' }, { commit: true })"); await sleep(200);
  check((await ckd()) === "", "back in the grid with acme/web#208 selected: no stale check");
  await clickCell("td[data-pr='pr:acme/api#101'][data-check='vendor-hsm-tests']"); await sleep(150);
  check((await ckd()).startsWith("Check vendor-hsm-tests"), "the cell again");
  await key("Escape");
  check(!(await hashCheck()) && (await evaluate("window.__wgoStore.selection.size")) === 0, "Esc clears the selection and its check");
  await gh103Nav(analysisURL, "#view=ci&q=kind:pr&sel=pr:acme/web%23208&check=pr:acme/api%23101%7Cvendor-hsm-tests", "!!document.querySelector('table.cg')");
  check((await ckd()) === "", "a shared link whose check names another PR shows none");
  await gh103Nav(analysisURL, "#view=ci&q=kind:pr&sel=pr:acme/api%23101&check=pr:acme/api%23101%7Cvendor-hsm-tests", "!!document.querySelector('table.cg')");
  check((await ckd()) === "Check vendor-hsm-tests: failure · details" && (await evaluate("document.querySelectorAll('td.cg-cell.ck').length")) === 1, "a shared link to the PR and its check shows it");
  await clickCell("section[data-repo='acme/api'] th.cg-col[data-pr='pr:acme/api#103']"); await sleep(150);
  check((await sortedSel()).join() === "pr:acme/api#103" && !(await evaluate("location.hash")).includes("check="), "a column header selects the PR");

  // G3. Keyboard only: arrows move through a grid; Enter on a check filters
  // (and the keyboard stays on it), on a PR or a cell selects.
  await evaluate("document.querySelector('section[data-repo=\"acme/api\"] table.cg [tabindex=\"0\"]').focus()");
  const at = () => evaluate("(() => { const a = document.activeElement; return (a.dataset.r || '') + ',' + (a.dataset.c || '') + ':' + (a.dataset.check || '') + ':' + (a.dataset.pr || '') })()");
  await key("ArrowUp");
  check((await at()).startsWith("0,"), "ArrowUp reaches the column headers: " + (await at()));
  await key("ArrowDown");
  await send("Input.dispatchKeyEvent", { type: "rawKeyDown", key: "Home", code: "Home", windowsVirtualKeyCode: 36 }); await send("Input.dispatchKeyEvent", { type: "keyUp", key: "Home", code: "Home", windowsVirtualKeyCode: 36 }); await sleep(60);
  check((await at()) === "1,0:vendor-hsm-tests:", "Home reaches the row header: " + (await at()));
  await key("Enter");
  check((await evaluate("window.__wgoStore.query")) === "kind:pr;check_failing:vendor-hsm-tests" && (await at()) === "1,0:vendor-hsm-tests:", "Enter on a check filters, and the keyboard stays on it: " + (await at()));
  await key("Enter");
  check((await evaluate("window.__wgoStore.query")) === "kind:pr", "Enter again removes the filter");
  await send("Input.dispatchKeyEvent", { type: "rawKeyDown", key: "ArrowRight", code: "ArrowRight", windowsVirtualKeyCode: 39 }); await send("Input.dispatchKeyEvent", { type: "keyUp", key: "ArrowRight", code: "ArrowRight", windowsVirtualKeyCode: 39 }); await sleep(60);
  await send("Input.dispatchKeyEvent", { type: "rawKeyDown", key: "ArrowRight", code: "ArrowRight", windowsVirtualKeyCode: 39 }); await send("Input.dispatchKeyEvent", { type: "keyUp", key: "ArrowRight", code: "ArrowRight", windowsVirtualKeyCode: 39 }); await sleep(60);
  check((await at()) === "1,2:vendor-hsm-tests:pr:acme/api#101", "ArrowRight walks the cells: " + (await at()));
  await key("Enter");
  check((await sortedSel()).join() === "pr:acme/api#101" && decodeURIComponent(await evaluate("location.hash")).includes("check=pr:acme/api#101|vendor-hsm-tests"), "Enter on a cell selects its PR and names its check");
  await key("ArrowUp");
  await key("Enter", true);
  check((await sortedSel()).length === 0, "Shift+Enter on the selected PR's header toggles it off: " + (await sortedSel()));

  // G4. Copy as Markdown from each view lists the rows with its fields.
  await send("Browser.grantPermissions", { origin: new URL(analysisURL).origin, permissions: ["clipboardReadWrite", "clipboardSanitizedWrite"] }, null);
  const copy = async () => {
    await evaluate("document.querySelector('.status').textContent = ''");
    await send("Runtime.evaluate", { expression: "Array.from(document.querySelectorAll('button')).find(b => b.textContent === 'Copy as Markdown').click()", userGesture: true });
    await waitFor("document.querySelector('.status').textContent.startsWith('Copied') || document.querySelector('.status').textContent.includes('failed') || !document.querySelector('.copyout').hidden", "the copy");
    return evaluate("navigator.clipboard.readText()", { userGesture: true });
  };
  const ciCopy = await copy();
  check(ciCopy.includes("acme/api#101") && ciCopy.includes(" · ci: systemic_only") && ciCopy.includes("check_failing: vendor-hsm-tests"), "the CI grid's copy: " + ciCopy.slice(0, 300));
  await evaluate("window.__wgoStore.setState({ view: 'scatter' }, { commit: true })"); await sleep(200);
  const scCopy = await copy();
  check(/waiting: \d+d · size: \d+ · move: reviewer/.test(scCopy), "the scatterplot's copy names its axes and the move: " + scCopy.slice(0, 300));

  const problems = events.filter((e) => (e.method === "Log.entryAdded" && e.params.entry.level === "error" && !/\/favicon\.ico$/.test(e.params.entry.url || "")) || e.method === "Runtime.exceptionThrown")
    .map((e) => e.method === "Runtime.exceptionThrown" ? e.params.exceptionDetails.text + " " + ((e.params.exceptionDetails.exception || {}).description || "") : e.params.entry.text + " " + (e.params.entry.url || ""));
  check(problems.length === 0, "gh-103: console errors: " + problems.join(" | "));
}

// timeGh103 checks "+n more" on the scaled analysis fixture (--perf-table)
// and times both views there (median of 7, synchronous and to the next
// frame): showing each, and applying and clearing a query; the scatterplot
// on SVG (its ~800 marks) and forced onto the canvas.
async function timeGh103(tableURL) {
  await gh103Nav(tableURL, "#view=ci&q=kind:pr", "!!document.querySelector('table.cg')");
  const more = await evaluate("(() => { const s = document.querySelector('section[data-repo=\"acme/api\"]'); return { cols: s.querySelectorAll('th.cg-col').length, more: s.querySelector('.cg-more button').textContent } })()");
  check(more.cols === 60 && /^\+\d+ more PRs — narrow to acme\/api$/.test(more.more), "past 60 PRs: the 60 with the most failures and +n more: " + JSON.stringify(more));
  await evaluate("document.querySelector('section[data-repo=\"acme/api\"] .cg-more button').click()"); await sleep(150);
  check((await evaluate("window.__wgoStore.query")) === "kind:pr;repo:acme/api" && (await evaluate("document.querySelector('.cg-more button').textContent")).endsWith("the table lists every one"), "+n more narrows the query to the repository");
  await gh103Nav(tableURL, "#view=graph", "");
  await sleep(1000);
  const r = await evaluate(`(async () => {
    const s = window.__wgoStore, frame = () => new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)));
    const med = (a) => a.slice().sort((x, y) => x - y)[a.length >> 1], out = {};
    const time = async (name, a, b) => {
      const sync = [], to = [];
      for (let i = 0; i < 7; i++) {
        const t0 = performance.now(); a(); const t1 = performance.now(); await frame(); sync.push(t1 - t0); to.push(performance.now() - t0);
        b(); await frame();
      }
      out[name] = med(sync).toFixed(1) + " ms (" + med(to).toFixed(1) + " to frame)";
    };
    for (const [name, view, canvasMin] of [["svg", "scatter", 1000], ["canvas", "scatter", 0], ["ci", "ci", 1000]]) {
      WGOLive.scatterOptions = { canvasMin };
      s.setQuery("", { commit: true });
      await time(name + "Show", () => s.setState({ view }, { commit: true }), () => s.setState({ view: "graph" }, { commit: true }));
      s.setState({ view }, { commit: true }); await frame();
      if (view === "scatter") out[name + "Marks"] = document.querySelectorAll(".sc-marks .sc-mk:not(.sc-proxy)").length + (document.querySelector(".sc-canvas").hidden ? " SVG" : " canvas") + " of " + document.querySelector(".sc-sum").textContent.split(" ")[0];
      else out.ciCells = document.querySelectorAll("td.cg-cell").length;
      await time(name + "Query", () => s.setQuery("kind:pr;prstate:open,draft", { commit: true }), () => s.setQuery("", { commit: true }));
      // An axis switch re-renders the scatterplot (and the overview panels) only.
      if (view === "scatter") await time(name + "Axis", () => s.setState({ x: "age" }, { commit: true }), () => s.setState({ x: "" }, { commit: true }));
      s.setState({ view: "graph" }, { commit: true }); await frame();
    }
    WGOLive.scatterOptions = null;
    return out;
  })()`);
  return "gh-103 on the scaled analysis fixture: scatter SVG (" + r.svgMarks + " marks) show " + r.svgShow + ", query " + r.svgQuery + ", axis " + r.svgAxis +
    "; scatter canvas (" + r.canvasMarks + ") show " + r.canvasShow + ", query " + r.canvasQuery + ", axis " + r.canvasAxis + "; CI grid (" + r.ciCells + " cells) show " + r.ciShow + ", query " + r.ciQuery;
}
}
// ---------- end gh-103 ----------

// ---------- gh-105: the sprint view (web/live-sprint.js) ----------
// The sprint view on the analysis fixture (--sprint) and on a snapshot
// without [jira] board (--sprint-noboard): the header, bars and direct
// labels, the selector and sprint= in the URL, every mismatch on its ticket
// filtering when clicked, the points labels and segments filtering, dimming
// and the hide toggle, shared selection, keyboard-only use, Copy as
// Markdown as /api/extract?view=sprint serves it, and the explanation
// without a board; then its timings on the --perf-table page. --set=sprint
// shoots the view light and dark, then under each colour-vision-deficiency
// simulation.
{
SHOT_SETS.sprint = [
  ["sprint-light.png", "light", "#view=sprint"],
  ["sprint-dark.png", "dark", "#view=sprint"],
  ["sprint-query-light.png", "light", "#view=sprint&q=status_category:in_progress&sel=ticket:jira:TEAM-105"],
  ["sprint-query-dark.png", "dark", "#view=sprint&q=mismatch:ticket_done_code_open&outside=hide"],
  ["sprint-second-light.png", "light", "#view=sprint&sprint=1240"],
  ["sprint-unknown-light.png", "light", "#view=sprint&sprint=999"],
];
for (const type of ["protanopia", "deuteranopia", "tritanopia", "achromatopsia"]) {
  for (const scheme of ["light", "dark"]) SHOT_SETS.sprint.push(["sprint-" + type + "-" + scheme + ".png", scheme, "#view=sprint&q=status_category:in_progress", "cvd:" + type]);
}
SHOT_SETS.sprint.viewport = { width: 1400, height: 900 };
SUITES.push({ name: "sprint view", flag: "sprint", checks: sprintChecks, perf: () => flag("--perf-table") ? timeSprint(flag("--perf-table")) : "", deadlineMs: 90000 });

async function sprintChecks(sprintURL) {
  const from = events.length, sprintNoBoardURL = flag("--sprint-noboard");
  await send("Browser.grantPermissions", { origin: new URL(sprintURL).origin, permissions: ["clipboardReadWrite", "clipboardSanitizedWrite"] }, null);
  await send("Page.navigate", { url: "about:blank" });
  await send("Page.navigate", { url: sprintURL + "#view=sprint" });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap) && !!document.querySelector('.sv-head')", "the sprint view");
  const q = () => evaluate("window.__wgoStore.query");
  const text = (sel) => evaluate("(() => { const e = document.querySelector(" + JSON.stringify(sel) + "); return e ? e.textContent : null })()");
  const rowsOf = () => evaluate("Array.from(document.querySelectorAll('.sv-table tr[data-id]')).map(tr => (tr.querySelector('td.nm a') || tr.querySelector('td.nm')).textContent.split(' ')[0] + (tr.classList.contains('out') ? ':out' : '') + (tr.classList.contains('sel') ? ':sel' : ''))");
  const click = (sel) => evaluate("(() => { const e = document.querySelector(" + JSON.stringify(sel) + "); e.dispatchEvent(new MouseEvent('click', { bubbles: true })); return window.__wgoStore.query })()");
  const active = () => evaluate("(() => { const a = document.activeElement; return a ? (a.dataset.k || a.tagName) : '' })()");

  // The header, the bars and their direct labels.
  check((await evaluate("Array.from(document.querySelectorAll('.view-tabs button')).map(b => b.textContent).join()")).includes("Sprint"), "the sprint view is a tab");
  check((await text(".sv-head h2")) === "Sprint 12" && (await text(".sv-meta")) === "Sprint 1234 · board 42 · 2026-10-20 to 2026-11-03", "header: " + (await text(".sv-meta")));
  check((await text(".sv-goal")) === "Ship token refresh and rate limits", "the goal verbatim");
  check((await evaluate("document.querySelector('.sv-time').getAttribute('aria-label')")) === "Time: day 11 of 14; 74% of the sprint's time has passed" && (await text(".sv-row .sv-val")) === "day 11 of 14", "elapsed time");
  check((await evaluate("getComputedStyle(document.querySelector('.sv-pts')).display")) === "flex", "the addon's stylesheet (live-sprint.css) applies under the CSP");
  const labels = await evaluate("Array.from(document.querySelectorAll('.sv-plabels .plab')).map(b => b.textContent)");
  check(labels.join("|") === "Done3 pts · 1 ticket|In Progress15 pts · 3 tickets|To Do0 pts · 2 tickets", "direct labels: " + labels.join("|"));
  const placed = await evaluate("(() => { const bar = document.querySelector('.sv-pts').getBoundingClientRect(), segs = Array.from(document.querySelectorAll('.sv-pts .seg')).map(s => s.getBoundingClientRect()), ls = Array.from(document.querySelectorAll('.plab')).map(b => b.getBoundingClientRect()); return { placed: document.querySelector('.sv-plabels').classList.contains('placed'), ip: ls[1].left >= segs[1].left - 1 && ls[1].left < segs[1].right, overlap: ls[0].right > ls[1].left || ls[1].right > ls[2].left, inside: ls[2].right <= bar.right + 1 } })()");
  check(placed.placed && placed.ip && !placed.overlap && placed.inside, "labels sit under their segments without overlapping: " + JSON.stringify(placed));
  check((await text(".sv-apart")) === "Not in the points: 1 unestimated · 1 with points unknown", "unestimated counted apart from unknown: " + (await text(".sv-apart")));
  check((await rowsOf()).join() === "TEAM-102,TEAM-101,TEAM-103,TEAM-105,TEAM-104,TEAM-106", "grouped by category, then assignee: " + (await rowsOf()));
  const prs = await evaluate("Array.from(document.querySelectorAll('.sv-table tr[data-id=\"ticket:jira:TEAM-101\"] .pr')).map(s => s.textContent + '|' + s.title.split('\\n')[1])");
  check(prs.join() === "○acme/api#101|Whose move: reviewer (bob),●acme/api#107|Whose move: none,○acme/web#201|Whose move: author (alice)" || prs.length === 3 && prs.every((p) => /^[○●]acme\/\w+#\d+\|Whose move: /.test(p)), "linked PR glyphs with whose move on hover: " + prs);

  // The selector and sprint= in the URL; Back returns.
  const picks = await evaluate("Array.from(document.querySelectorAll('.sv-pick button')).map(b => b.textContent + ':' + b.getAttribute('aria-pressed'))");
  check(picks.join() === "Sprint 12 · 6 tickets:true,Web Sprint 4 · 1 ticket:false", "selector: " + picks);
  await click(".sv-pick button[data-sprint='1240']");
  check((await evaluate("location.hash")).includes("sprint=1240") && (await text(".sv-head h2")) === "Web Sprint 4", "choosing a sprint writes sprint= and shows it");
  check((await text(".sv-goal")) === "no sprint goal" && (await evaluate("document.querySelector('.sv-goal').classList.contains('none')")), "a sprint without a goal says so");
  // The fixture's in_progress_idle mismatch sits on TEAM-107 here.
  check((await click(".sv-table tr[data-id='ticket:jira:TEAM-107'] .mmb[data-code='in_progress_idle']")) === "mismatch:in_progress_idle", "the idle mismatch filters");
  await evaluate("window.__wgoStore.setQuery('', { commit: true })");
  for (let i = 0; i < 3; i++) { await evaluate("history.back()"); await sleep(300); }
  check(!(await evaluate("location.hash")).includes("sprint=") && (await text(".sv-head h2")) === "Sprint 12", "Back returns to the default sprint");

  // Every gh-99 mismatch appears on its ticket and filters when clicked;
  // tickets outside the query are dimmed, not hidden; a second click clears.
  for (const [ticket, code] of [["TEAM-102", "ticket_done_code_open"], ["TEAM-103", "code_merged_ticket_open"], ["TEAM-105", "in_progress_no_code"], ["TEAM-106", "unassigned_in_sprint"]]) {
    const sel = ".sv-table tr[data-id='ticket:jira:" + ticket + "'] .mmb[data-code='" + code + "']";
    check((await evaluate("document.querySelectorAll(" + JSON.stringify(sel) + ").length")) === 1, code + " on " + ticket);
    check((await click(sel)) === "mismatch:" + code, code + " applies mismatch:" + code);
    const rows = await rowsOf();
    check(rows.length === 6 && rows.filter((r) => r.endsWith(":out")).length === 5 && rows.includes(ticket), code + ": the sprint stays whole, the rest dimmed: " + rows);
    check((await evaluate("document.querySelector(" + JSON.stringify(sel) + ").getAttribute('aria-pressed')")) === "true", code + " shows it is active");
    check((await click(sel)) === "", code + " clicked again clears");
  }

  // Points labels and segments apply status_category:<c>, replacing each other.
  check((await click(".plab[data-cat='in_progress']")) === "status_category:in_progress", "the In Progress label filters");
  check((await rowsOf()).filter((r) => r.endsWith(":out")).length === 3, "To Do and Done dimmed");
  check((await click(".sv-pts .seg[data-cat='done']")) === "status_category:done", "a segment replaces the clause");
  check((await evaluate("document.querySelector('.sv-pts .seg.done').classList.contains('on') && document.querySelector('.plab.done').getAttribute('aria-pressed') === 'true'")), "the active part is marked");
  // The toggle hides tickets outside the query, as URL state; Back undoes it.
  check((await text(".sv-bar button")) === "Hide 5 tickets outside the query", "the toggle names what it hides");
  await click(".sv-bar button");
  check((await evaluate("location.hash")).includes("outside=hide") && (await rowsOf()).join() === "TEAM-102", "hidden: " + (await rowsOf()));
  await evaluate("history.back()"); await sleep(300);
  check((await rowsOf()).length === 6, "Back shows them dimmed again");
  // A combination the query language refuses is shown here with its fix.
  await evaluate("window.__wgoStore.setQuery('kind:pr', { commit: true })");
  check((await click(".plab[data-cat='todo']")) === "kind:pr" && /leaves no rows/.test(await text(".sv-msg")), "an invalid click is explained, not applied: " + (await text(".sv-msg")));
  await click(".sv-msg button");
  check((await q()) === "status_category:todo", "its fix applies");
  await evaluate("window.__wgoStore.setQuery('', { commit: true })");

  // Selection is shared with every view.
  await click(".sv-table tr[data-id='ticket:jira:TEAM-103'] td.st");
  check((await evaluate("Array.from(window.__wgoStore.selection).join()")) === "ticket:jira:TEAM-103" && (await rowsOf()).includes("TEAM-103:sel"), "a row click selects");
  await evaluate("window.__wgoStore.setState({ view: 'table' }, { commit: true })");
  check((await evaluate("document.querySelectorAll('table.atable tr.sel').length")) === 1 && (await text(".detail h3")) === "TEAM-103", "the table and detail panel share the selection");
  await evaluate("window.__wgoStore.setState({ view: 'sprint', sel: '' }, { commit: true })");

  // Keyboard only: the picker, the points labels, the toggle, then the rows.
  await evaluate("document.querySelector('.sv-pick button').focus()");
  await key("Tab");
  check((await active()) === "sprint:1240", "Tab moves along the picker: " + (await active()));
  await key("Enter");
  check((await text(".sv-head h2")) === "Web Sprint 4" && (await active()) === "sprint:1240", "Enter chooses; the focus stays: " + (await active()));
  await key("Tab", true); await key("Enter");
  check((await text(".sv-head h2")) === "Sprint 12", "Shift+Tab, Enter: back to Sprint 12");
  await key("Tab"); await key("Tab"); await key("Tab");
  check((await active()) === "cat:in_progress", "Tab reaches the points labels: " + (await active()));
  await key("Enter");
  check((await q()) === "status_category:in_progress" && (await active()) === "cat:in_progress", "Enter on a label filters and keeps the focus");
  await key("Tab"); await key("Tab");
  check((await active()) === "outside", "then the toggle: " + (await active()));
  await key("Tab");
  check((await active()) === "row:ticket:jira:TEAM-102", "then the table, one tab stop: " + (await active()));
  await key("ArrowDown");
  check((await active()) === "row:ticket:jira:TEAM-101" && (await evaluate("Array.from(window.__wgoStore.selection).join()")) === "ticket:jira:TEAM-101", "ArrowDown moves and selects");
  await key("ArrowDown"); await key("ArrowDown", true);
  await key("Enter", true);
  check((await evaluate("Array.from(window.__wgoStore.selection).sort().join()")) === "ticket:jira:TEAM-103,ticket:jira:TEAM-105", "Shift+ArrowDown moves without selecting; Shift+Enter adds");
  await key("Tab");
  check((await active()) === "link:ticket:jira:TEAM-105", "Tab enters the focused row's links: " + (await active()));
  await key("Tab");
  check((await active()) === "mm:ticket:jira:TEAM-105:in_progress_no_code", "and its mismatch: " + (await active()));
  await key("Enter");
  check((await q()) === "status_category:in_progress;mismatch:in_progress_no_code" && (await active()) === "mm:ticket:jira:TEAM-105:in_progress_no_code", "Enter on a mismatch adds its clause; the focus stays: " + (await q()) + " " + (await active()));

  // Copy as Markdown asks /api/extract for the sprint summary.
  await send("Runtime.evaluate", { expression: "Array.from(document.querySelectorAll('button')).find(b => b.textContent === 'Copy as Markdown').click()", userGesture: true });
  await waitFor("document.querySelector('.status').textContent.startsWith('Copied') || document.querySelector('.status').textContent.includes('failed') || !document.querySelector('.copyout').hidden", "the sprint copy");
  const clip = await evaluate("navigator.clipboard.readText()", { userGesture: true });
  const served = await evaluate("fetch('/api/extract?view=sprint&sprint=1234&q=' + encodeURIComponent('status_category:in_progress;mismatch:in_progress_no_code')).then(r => r.text())");
  check(clip === served && clip.startsWith("## Sprint 12\n") && clip.includes("Query: `status_category:in_progress;mismatch:in_progress_no_code`") && clip.includes("· sprint 1234 on board 42"), "the copy is the sprint summary: " + clip.slice(0, 160));
  await evaluate("window.__wgoStore.setState({ sprint: '999' }, { commit: true })");
  check(/No sprint 999 in this snapshot; the active sprints are 1234 \(Sprint 12\), 1240 \(Web Sprint 4\)/.test(await text(".sv-gap")), "an unknown sprint= names the active sprints");

  // Without a [jira] board the view explains what to configure.
  if (sprintNoBoardURL) {
    await send("Page.navigate", { url: "about:blank" });
    await send("Page.navigate", { url: sprintNoBoardURL + "#view=sprint" });
    await waitFor("!!document.querySelector('.sv-gap')", "the no-board explanation");
    check((await text(".sv-gap h2")) === "No sprint to show: No [jira] board is configured" && (await text(".sv-gap pre code")) === "[jira]\nboard = <id>", "no board: " + (await text(".sv-gap")));
  }
  const problems = events.slice(from).filter((e) => (e.method === "Log.entryAdded" && e.params.entry.level === "error" && !/\/favicon\.ico$/.test(e.params.entry.url || "")) || e.method === "Runtime.exceptionThrown")
    .map((e) => e.method === "Runtime.exceptionThrown" ? e.params.exceptionDetails.text + " " + ((e.params.exceptionDetails.exception || {}).description || "") : e.params.entry.text + " " + (e.params.entry.url || ""));
  check(problems.length === 0, "sprint view console errors: " + problems.join(" | "));
}

// timeSprint times the sprint view on the analysis fixture scaled to 2,000
// nodes, whose copies of Sprint 12 make one sprint of about 250 tickets
// (median of 7, synchronous and to the next frame): showing it, a points
// click, a mismatch click and a selection.
async function timeSprint(tableURL) {
  await send("Page.navigate", { url: "about:blank" });
  await send("Page.navigate", { url: tableURL + "#view=graph" });
  await waitFor("!!(window.__wgoStore && window.__wgoStore.snap)", "the scaled analysis fixture");
  await sleep(1500);
  const r = await evaluate(`(async () => {
    const s = window.__wgoStore, frame = () => new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)));
    const med = (a) => a.slice().sort((x, y) => x - y)[a.length >> 1], out = {};
    const time = async (name, a, b) => {
      const sync = [], to = [];
      for (let i = 0; i < 7; i++) {
        const t0 = performance.now(); a(); const t1 = performance.now(); await frame(); sync.push(t1 - t0); to.push(performance.now() - t0);
        b(); await frame();
      }
      out[name] = med(sync).toFixed(1) + " ms (" + med(to).toFixed(1) + " to frame)";
    };
    await time("show", () => s.setState({ view: "sprint" }, { commit: true }), () => s.setState({ view: "graph" }, { commit: true }));
    s.setState({ view: "sprint" }, { commit: true }); await frame();
    out.rows = document.querySelectorAll(".sv-table tr[data-id]").length;
    await time("cat", () => document.querySelector(".plab[data-cat='in_progress']").click(), () => s.setQuery("", { commit: true }));
    await time("mm", () => document.querySelector(".mmb[data-code='ticket_done_code_open']").click(), () => s.setQuery("", { commit: true }));
    const ids = Array.from(document.querySelectorAll(".sv-table tr[data-id]")).map((tr) => tr.dataset.id);
    await time("select", () => s.select([ids[5]], false), () => s.clear());
    return out;
  })()`);
  check(r.rows >= 200, "the scaled sprint has its tickets: " + r.rows);
  return "sprint view on the scaled analysis fixture (" + r.rows + " tickets in one sprint): show " + r.show + ", points click " + r.cat + ", mismatch click " + r.mm + ", select " + r.select;
}
}
// ---------- end gh-105 ----------
