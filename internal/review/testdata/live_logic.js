// Exercises the pure logic of web/live.js (window.WGOLive / module.exports)
// under node. Run by TestLiveJSLogic:
//
//	node live_logic.js <live.js> <dash fields.json> <dash query_vectors.json> <live-table.js> <dash table_vectors.json>
//
// Prints "ok <n> ..." on success; any failure exits non-zero with a message.
"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const L = require(process.argv[2]);
const FIELDS = JSON.parse(fs.readFileSync(process.argv[3], "utf8"));
const VECTORS = JSON.parse(fs.readFileSync(process.argv[4], "utf8"));
const T = require(process.argv[5])(L); // the analysis table's pure logic (gh-101)
const TV = JSON.parse(fs.readFileSync(process.argv[6], "utf8"));

let n = 0;
function test(name, fn) {
  try { fn(); n++; } catch (e) { console.error("FAIL " + name + ": " + e.message); process.exit(1); }
}
const sorted = (set) => Array.from(set).sort();

// effort A -> ws1 -> bm1 -> pr1; effort B -> ws2; a disconnected island t1 -> t2.
const snap = {
  nodes: [
    { id: "effort:a", kind: "effort", label: "A" },
    { id: "effort:b", kind: "effort", label: "B" },
    { id: "ws1", kind: "workspace", label: "ws1" },
    { id: "ws2", kind: "workspace", label: "ws2" },
    { id: "bm1", kind: "bookmark", label: "bm1", bookmark: { pr_lookup: "fresh", pr_count: 1 } },
    { id: "bm2", kind: "bookmark", label: "bm2", bookmark: { pr_lookup: "unknown" } },
    { id: "bm3", kind: "bookmark", label: "bm3", bookmark: { pr_lookup: "fresh", pr_count: 0 } },
    { id: "pr1", kind: "pr", label: "#1", pr: { state: "open", is_draft: true } },
    { id: "pr2", kind: "pr", label: "#2", pr: { state: "merged" } },
    { id: "t1", kind: "ticket", label: "T1" },
    { id: "t2", kind: "ticket", label: "T2" },
  ],
  edges: [
    { source: "effort:a", target: "ws1", kind: "contains" },
    { source: "ws1", target: "bm1", kind: "on" },
    { source: "bm1", target: "pr1", kind: "pr" },
    { source: "effort:b", target: "ws2", kind: "contains" },
    { source: "ws2", target: "bm2", kind: "on" },
    { source: "ws2", target: "bm3", kind: "on" },
    { source: "bm2", target: "pr2", kind: "pr" },
    { source: "t1", target: "t2", kind: "ticket" },
    { source: "ws1", target: "gone", kind: "on" }, // dangling: ignored
  ],
  counts: {
    days: ["2026-10-03", "2026-10-04", "2026-10-05"],
    activity: { "effort:a": [0, 2, 1], "effort:b": [3, 0, 0], "effort:missing": [1, 1, 1] },
  },
};
const idx = L.index(snap);

test("reach follows edges one way and stays in its component", () => {
  assert.deepEqual(sorted(L.reach(idx.out, ["effort:a"])), ["bm1", "effort:a", "pr1", "ws1"]);
  assert.deepEqual(sorted(L.reach(idx.inc, ["pr1"])), ["bm1", "effort:a", "pr1", "ws1"]);
  assert.deepEqual(sorted(L.reach(idx.out, ["t1"])), ["t1", "t2"]);
  assert.deepEqual(sorted(L.reach(idx.out, ["nope"])), ["nope"]);
});

test("closure joins up and down but never crosses components", () => {
  assert.deepEqual(sorted(L.closure(idx, ["bm1"])), ["bm1", "effort:a", "pr1", "ws1"]);
  assert.deepEqual(sorted(L.closure(idx, ["t2"])), ["t1", "t2"]);
  assert.deepEqual(sorted(L.closure(idx, [])), []);
});

test("seeds for a day pick efforts active that day that exist", () => {
  assert.deepEqual(L.seeds(snap, idx, { by: "day", value: "2026-10-04" }), ["effort:a"]);
  assert.deepEqual(L.seeds(snap, idx, { by: "day", value: "2026-10-03" }), ["effort:b"]);
  assert.deepEqual(L.seeds(snap, idx, { by: "day", value: "1999-01-01" }), []);
});

test("seeds for a PR state bucket PRs and PR-less bookmarks, unknown is not none", () => {
  assert.deepEqual(L.seeds(snap, idx, { by: "prstate", value: "draft" }), ["pr1"]);
  assert.deepEqual(L.seeds(snap, idx, { by: "prstate", value: "merged" }), ["pr2"]);
  assert.deepEqual(L.seeds(snap, idx, { by: "prstate", value: "unknown" }), ["bm2"]);
  assert.deepEqual(L.seeds(snap, idx, { by: "prstate", value: "none" }), ["bm3"]);
  assert.deepEqual(L.seeds(snap, idx, { by: "prstate", value: "open" }), []);
});

test("seeds for an effort, and visible for a filter", () => {
  assert.deepEqual(L.seeds(snap, idx, { by: "effort", value: "effort:b" }), ["effort:b"]);
  assert.deepEqual(L.seeds(snap, idx, { by: "effort", value: "effort:missing" }), []);
  assert.deepEqual(L.seeds(snap, idx, null), []);
  assert.equal(L.visible(snap, idx, null), null);
  assert.deepEqual(sorted(L.visible(snap, idx, { by: "effort", value: "effort:b" })), ["bm2", "bm3", "effort:b", "pr2", "ws2"]);
});

test("toggleFilter clears on a repeat click and replaces otherwise", () => {
  const day = { by: "day", value: "2026-10-04" };
  assert.equal(L.toggleFilter(day, { by: "day", value: "2026-10-04" }), null);
  assert.deepEqual(L.toggleFilter(day, { by: "day", value: "2026-10-05" }), { by: "day", value: "2026-10-05" });
  assert.deepEqual(L.toggleFilter(day, { by: "prstate", value: "2026-10-04" }), { by: "prstate", value: "2026-10-04" });
  assert.deepEqual(L.toggleFilter(null, day), day);
});

test("select replaces, adds and toggles", () => {
  let sel = L.select(new Set(), ["ws1"], false);
  assert.deepEqual(sorted(sel), ["ws1"]);
  sel = L.select(sel, ["pr1"], true);
  assert.deepEqual(sorted(sel), ["pr1", "ws1"]);
  sel = L.select(sel, ["ws1"], true);
  assert.deepEqual(sorted(sel), ["pr1"]);
  assert.deepEqual(sorted(L.select(sel, ["t1"], false)), ["t1"]);
});

test("prune after a generation swap keeps only surviving IDs", () => {
  const next = { nodes: snap.nodes.filter((x) => x.id !== "pr1" && x.id !== "t2").concat([{ id: "pr3", kind: "pr" }]), edges: [] };
  const sel = new Set(["pr1", "ws1", "t2"]);
  const pruned = L.prune(sel, L.index(next).byId);
  assert.deepEqual(sorted(pruned), ["ws1"]);
  assert.deepEqual(sorted(sel), ["pr1", "t2", "ws1"], "prune must not mutate its input");
});

test("a workspace is on its bookmark, behind bookmarks that moved on, or has none", () => {
  const ws = (w) => L.stateText({ kind: "workspace", workspace: w });
  assert.equal(ws({ bookmark: "feat", stack: { position: 2, size: 3 } }), "on feat (2/3 in stack)");
  assert.equal(ws({ bookmark: "feat", bookmarks_ahead: ["feat"] }), "behind feat");
  assert.equal(ws({ bookmarks_ahead: ["one", "two"] }), "behind one, two");
  assert.equal(ws({}), "no bookmark");
  assert.equal(ws({ error: "jj exploded" }), "unavailable: jj exploded");
});

test("ticket facts: category and points only when known", () => {
  assert.deepEqual(L.ticketFacts({ status_category: "in_progress", story_points: 5, story_points_known: true }), { category: "in progress", points: "5 points" });
  assert.deepEqual(L.ticketFacts({ status_category: "todo", story_points: 1, story_points_known: true }), { category: "to do", points: "1 point" });
  assert.deepEqual(L.ticketFacts({ status_category: "done", story_points_known: true }), { category: "done", points: "unestimated" });
  assert.deepEqual(L.ticketFacts({ status: "Open" }), { category: "", points: "" }, "unread points are unknown, never unestimated");
  assert.deepEqual(L.ticketFacts(null), { category: "", points: "" });
});

test("derived facts: details on demand, unknown spelled out (gh-99)", () => {
  const node = (id) => VECTORS.snapshot.nodes.find((x) => x.id === id);
  const team = L.prFactsRows(node("pr:acme/web#12").pr, "alice", (t) => t.slice(0, 10));
  assert.deepEqual(team, [
    ["Whose move", "reviewer — team:web, alice (you)"],
    ["awaiting review", "ready since Oct 14; review requested from team:web only; alice through a team"],
    ["team only", "every outstanding request is a team: team:web"],
    ["Waiting since", "2026-10-14 on team:web"],
    ["CI", "green"],
    ["Size", "unknown"],
    ["Not known", "additions, deletions, requested_at:team:web"],
  ]);
  const lower = L.prFactsRows({ facts: { move: "reviewer", reasons: [], waiting_since: "2026-10-14T10:00:00Z", waiting_lower_bound: true, ci: "systemic_only", size: 0, size_bucket: "xs" } });
  assert.deepEqual(lower.slice(1), [["Waiting since", "2026-10-14T10:00:00Z or earlier"], ["CI", "only checks that fail across the repository fail"], ["Size", "0 lines changed (xs)"]]);
  // With check_stats, the CI row gives the counts, never "most".
  const sysPR = { repo: "acme/api", check_details: [{ name: "vendor-hsm-tests", state: "failure" }, { name: "build", state: "success" }], facts: { move: "reviewer", ci: "systemic_only" } };
  const stats = { "acme/api": [{ name: "build", runs: 3, failures: 0, systemic: false }, { name: "vendor-hsm-tests", runs: 3, failures: 1, systemic: true }] };
  assert.deepEqual(L.prFactsRows(sysPR, "", String, stats)[1], ["CI", "only checks failing across the repository fail: vendor-hsm-tests fails on 1 of 3 open PRs in acme/api"]);
  assert.deepEqual(L.ticketFactsRows({ system: "jira", facts: { mismatches: [], linked_prs: [{ id: "pr:acme/api#7", ref: "acme/api#7" }] } })[0], ["Linked PRs", "acme/api#7 state unknown"]);
  assert.deepEqual(L.prFactsRows({ facts: {} })[0], ["Whose move", "unknown"]);
  assert.deepEqual(L.prFactsRows({ state: "open" }), [], "a snapshot from before gh-99 has no fact rows");
  assert.deepEqual(L.ticketFactsRows(node("ticket:jira:TEAM-99").ticket), [["Linked PRs", "none in the snapshot"], ["Mismatch: unassigned in sprint", "in Sprint 12 with no assignee"]]);
  assert.deepEqual(L.ticketFactsRows(node("ticket:jira:TEAM-123").ticket), [["Linked PRs", "acme/api#38 merged, acme/api#41 open, acme/api#42 draft"], ["Jira vs code", "no mismatch"]]);
  assert.deepEqual(L.ticketFactsRows({ system: "jira", facts: { linked_prs: [], uncertain: ["status_category"] } }), [["Linked PRs", "none in the snapshot"], ["Jira vs code", "unknown"], ["Not known", "status_category"]]);
  assert.deepEqual(L.ticketFactsRows(node("ticket:gh:acme/web#5").ticket), [["Linked PRs", "acme/web#12 open"]], "a GitHub issue has no Jira comparison");
  assert.equal(L.factsTip(node("pr:acme/api#42")), "move: author · draft, ci pending, stale");
  assert.equal(L.factsTip(node("ticket:jira:TEAM-99")), "Jira vs code: unassigned in sprint");
  assert.equal(L.factsTip(node("ticket:jira:TEAM-123")), "");
  assert.equal(L.factsTip(node("pr:acme/api#38")), "");
});

test("the effort list's CI badge follows the derived facts (gh-99)", () => {
  // A failure every open PR shares is neutral and says so; an own failure
  // keeps the failure accent; without facts the rollup shows.
  assert.deepEqual(L.ciBadge({ checks: "failure", facts: { ci: "systemic_only" } }), { text: "~ CI fails everywhere", cls: "ci-systemic" });
  assert.deepEqual(L.ciBadge({ checks: "failure", facts: { ci: "failing" } }), { text: "CI failure", cls: "s-bad" });
  assert.deepEqual(L.ciBadge({ checks: "failure" }), { text: "CI failure", cls: "s-bad" });
  assert.deepEqual(L.ciBadge({ checks: "failure", facts: { ci: "unknown" } }), { text: "CI failure", cls: "s-bad" }, "unknown facts fall back on the rollup");
  assert.deepEqual(L.ciBadge({ checks: "pending", facts: { ci: "pending" } }), { text: "CI pending", cls: "s-warn" });
  assert.deepEqual(L.ciBadge({ checks: "success", facts: { ci: "green" } }), { text: "CI success", cls: "s-good" });
  assert.deepEqual(L.ciBadge({ checks: "none", facts: { ci: "none" } }), { text: "no CI checks", cls: "" });
  assert.deepEqual(L.ciBadge({}), { text: "CI not reported", cls: "f-unknown" });
});

test("sprints: state and dates, and their group's state text", () => {
  assert.equal(L.sprintText({ state: "active", start: "2026-10-20T08:00:00Z", end: "2026-11-03T08:00:00Z" }), "active sprint · 2026-10-20 to 2026-11-03");
  assert.equal(L.sprintText({ state: "future", start: "2026-11-03T08:00:00Z" }), "future sprint · 2026-11-03 to ?");
  assert.equal(L.sprintText({}), "sprint state unknown");
  assert.equal(L.stateText({ kind: "sprint", sprint: { state: "closed" } }), "closed sprint");
  assert.equal(L.dataText({ kind: "sprint", sprint: { freshness: "stale" } }), "stale");
  const sp = { state: "active", start: "2026-10-20T08:00:00Z", end: "2026-11-03T08:00:00Z", goal: "Ship token refresh" };
  assert.equal(L.sprintGroupText({ group: "sprint", description: "Ship token refresh" }, sp), "active sprint · 2026-10-20 to 2026-11-03", "the description already shows the goal");
  assert.equal(L.sprintGroupText({ group: "sprint" }, sp), "active sprint · 2026-10-20 to 2026-11-03 · goal: Ship token refresh");
  assert.equal(L.stateText({ kind: "effort", effort: { group: "sprint" } }), "sprint group");
  assert.equal(L.stateText({ kind: "effort", effort: { group: "repo" } }), "repository group");
});

test("tracked lists an effort's PRs and tickets in natural order", () => {
  const s = {
    nodes: [
      { id: "effort:repo:acme/api", kind: "effort", label: "acme/api" },
      { id: "pr:acme/api#10", kind: "pr", label: "acme/api#10" }, { id: "pr:acme/api#9", kind: "pr", label: "acme/api#9" },
      { id: "ticket:jira:TEAM-12", kind: "ticket", label: "TEAM-12" }, { id: "ticket:jira:TEAM-3", kind: "ticket", label: "TEAM-3" },
      { id: "ticket:jira:TEAM-7", kind: "ticket", label: "TEAM-7" }, { id: "ws1", kind: "workspace", label: "api" },
    ],
    edges: [
      { source: "effort:repo:acme/api", target: "pr:acme/api#10", kind: "tracks" }, { source: "effort:repo:acme/api", target: "pr:acme/api#9", kind: "tracks" },
      { source: "effort:repo:acme/api", target: "ticket:jira:TEAM-12", kind: "tracks" }, { source: "effort:repo:acme/api", target: "ws1", kind: "contains" },
      { source: "pr:acme/api#10", target: "ticket:jira:TEAM-3", kind: "ticket" }, { source: "pr:acme/api#9", target: "ticket:jira:TEAM-3", kind: "ticket" },
      { source: "pr:acme/api#9", target: "ticket:jira:TEAM-7", kind: "ticket" },
    ],
  };
  const idx = L.index(s), ids = (l) => l.map((n) => n.id);
  const all = L.tracked(idx, "effort:repo:acme/api");
  assert.deepEqual(ids(all.prs), ["pr:acme/api#9", "pr:acme/api#10"]);
  assert.deepEqual(ids(all.tickets), ["ticket:jira:TEAM-3", "ticket:jira:TEAM-7", "ticket:jira:TEAM-12"], "tracked and PR tickets, once each");
  const some = L.tracked(idx, "effort:repo:acme/api", (id) => id !== "pr:acme/api#9" && id !== "ticket:jira:TEAM-12");
  assert.deepEqual(ids(some.prs), ["pr:acme/api#10"]);
  assert.deepEqual(ids(some.tickets), ["ticket:jira:TEAM-3"], "only the visible PRs' tickets");
  assert.deepEqual(L.tracked(idx, "ws1"), { prs: [], tickets: [] });
});

test("sortRows sorts both directions with stable tie-breaks", () => {
  const rows = [
    { id: "c", label: "same", kind: "pr", activity: "2026-10-01" },
    { id: "a", label: "same", kind: "pr", activity: "2026-10-01" },
    { id: "z", label: "Alpha", kind: "effort", activity: "2026-10-03" },
    { id: "y", label: "beta", kind: "workspace", activity: "" },
    { id: "a", label: "same", kind: "pr", activity: "2026-10-01", dup: 2 },
  ];
  const before = rows.map((r) => r.id);
  const asc = L.sortRows(rows, "activity", 1);
  assert.deepEqual(asc.map((r) => r.id), ["y", "a", "a", "c", "z"]);
  // Full ties keep their input order, in both directions.
  assert.equal(asc[1].dup, undefined);
  assert.equal(asc[2].dup, 2);
  const desc = L.sortRows(rows, "activity", -1);
  assert.deepEqual(desc.map((r) => r.id), ["z", "a", "a", "c", "y"]);
  assert.equal(desc[1].dup, undefined);
  assert.deepEqual(L.sortRows(rows, "kind", 1).map((r) => r.kind), ["effort", "workspace", "pr", "pr", "pr"]);
  assert.deepEqual(L.sortRows(rows, "label", 1).map((r) => r.label), ["Alpha", "beta", "same", "same", "same"]);
  assert.deepEqual(rows.map((r) => r.id), before, "sortRows must not mutate its input");
});

test("fetch failures are named by cause", () => {
  assert.equal(L.fetchFailure({ status: 503 }), "wgo dash returned HTTP 503");
  assert.equal(L.fetchFailure(new TypeError("Failed to fetch")), "Cannot reach wgo dash");
  assert.equal(L.fetchFailure({ unreadable: true }), "wgo dash returned an unreadable response");
  assert.match(L.pollErrorText({ status: 500 }, true), /^wgo dash returned HTTP 500; still showing the last snapshot/);
  assert.match(L.pollErrorText(new TypeError("x"), false), /^Cannot reach wgo dash; no snapshot/);
});

test("warnings include review-run errors and an unavailable-links note", () => {
  const w = L.warnings({ diagnostics: ["view"] }, { diagnostics: ["snap"] }, { errors: ["review run bad: broken"] }, "wgo dash returned HTTP 500");
  assert.equal(w.length, 4);
  assert.deepEqual(w.slice(0, 3), ["view", "snap", "review run bad: broken"]);
  assert.match(w[3], /^Review links unavailable \(wgo dash returned HTTP 500\)/);
  assert.deepEqual(L.warnings(null, null, null, ""), []);
});

test("a poll without a snapshot is never shown as fresh data", () => {
  // Before the first snapshot, loading is normal: the header says so.
  assert.equal(L.payloadIssue({ status: "loading" }, null), "");
  assert.equal(L.payloadIssue({ status: "ready", snapshot: {} }, 4), "");
  // After one, a restarted server keeps the old generation on screen and says why.
  assert.match(L.payloadIssue({ status: "loading", diagnostics: ["collection failed: boom"] }, 4),
    /^wgo dash has no snapshot right now .*still showing generation 4 .*Problems: collection failed: boom$/);
  // Anything else is an unexpected response, not "loading".
  assert.match(L.payloadIssue({ status: "teapot" }, null), /^wgo dash returned an unexpected response \(status "teapot"\)\.$/);
  assert.match(L.payloadIssue({ status: "ready" }, 2), /unexpected response.*generation 2/);
  assert.match(L.payloadIssue(null, null), /^wgo dash returned an unexpected response\.$/);
});

test("URLs are allowlisted: http(s) outside, same-origin paths inside", () => {
  assert.equal(L.safeURL("https://github.com/a/b/pull/1"), "https://github.com/a/b/pull/1");
  assert.equal(L.safeURL("HTTP://x.example/"), "HTTP://x.example/");
  for (const bad of ["javascript:alert(1)", "data:text/html,x", "//evil.example", "/review/a/", " https://x", "", null, 7]) {
    assert.equal(L.safeURL(bad), "", String(bad));
  }
  assert.equal(L.localURL("/review/2026/#focus=x"), "/review/2026/#focus=x");
  for (const bad of ["//evil.example", "/\\evil.example", "https://x", "javascript:x", "review/a", "", null]) {
    assert.equal(L.localURL(bad), "", String(bad));
  }
});

test("action buttons need the page token; Resume only when configured", () => {
  assert.deepEqual(L.actionButtons(null), []);
  assert.deepEqual(L.actionButtons({ action_api: "/api/action" }), [], "no token: read-only");
  assert.deepEqual(L.actionButtons({ token: "t" }), [], "no action endpoint");
  const kinds = (b) => L.actionButtons(b).map((a) => a.kind);
  assert.deepEqual(kinds({ token: "t", action_api: "/api/action" }), ["terminal", "editor", "reveal", "plan", "spec"]);
  assert.deepEqual(kinds({ token: "t", action_api: "/api/action", resume: "claude" }), ["terminal", "resume", "editor", "reveal", "plan", "spec"]);
  assert.equal(L.actionButtons({ token: "t", action_api: "/a", resume: "claude" })[1].label, "Resume claude");
  assert.equal(L.actionButtons({ token: "t", action_api: "/a" })[0].label, "Open tab");
});

test("action results show the method, fallbacks and what to copy", () => {
  const ok = L.actionResult({ launched: true, method: "ghostty", message: "Opened a new Ghostty tab." });
  assert.deepEqual(ok, { ok: true, text: "Opened a new Ghostty tab.", copy: "", tried: "" });
  const fell = L.actionResult({ launched: true, method: "terminal_command", message: "Opened with wezterm.", fallbacks: ["ghostty: not installed"] });
  assert.equal(fell.tried, "Tried first: ghostty: not installed");
  const copy = L.actionResult({ launched: false, method: "copy", message: "No terminal could be opened.", copy: "cd '/tmp/it'\\''s'", fallbacks: ["ghostty: not installed", "terminal_command: not found"] });
  assert.equal(copy.ok, false);
  assert.equal(copy.copy, "cd '/tmp/it'\\''s'");
  assert.equal(copy.tried, "Tried first: ghostty: not installed; terminal_command: not found");
  assert.equal(L.actionResult({ launched: true, method: "code" }).text, "Opened with code.");
  assert.equal(L.actionResult({}).text, "Nothing could be launched.");
  assert.equal(L.actionResult(null).ok, false);
});

test("action errors prefer the server's explanation", () => {
  assert.equal(L.actionError({ status: 404, detail: "workspace ws-1 is no longer discovered; refresh" }), "workspace ws-1 is no longer discovered; refresh (HTTP 404)");
  assert.match(L.actionError({ status: 403 }), /refused the request \(HTTP 403\); reload the page/);
  assert.equal(L.actionError({ status: 500 }), "wgo dash returned HTTP 500");
  assert.equal(L.actionError(new TypeError("Failed to fetch")), "Cannot reach wgo dash");
  assert.equal(L.actionError({ unreadable: true }), "wgo dash returned an unreadable response");
  assert.match(L.ackText({ ok: true, generation: 7 }), /^Marked generation 7 seen/);
});

test("localURL accepts only same-origin paths", () => {
  for (const ok of ["/", "/review/2026-H1/", "/review/x/?focus=pr%3A1#view=table"]) assert.equal(L.localURL(ok), ok, ok);
  for (const bad of ["//evil.example/", "/\\evil.example/", "/a\\b", "\\\\evil", "https://evil.example/", "javascript:alert(1)",
    "review/x", "", "/\t/evil.example", "/\n/evil.example", "/ /x", "/\u0000x", null, undefined, 42, {}]) {
    assert.equal(L.localURL(bad), "", JSON.stringify(bad));
  }
});

test("effortOrder puts repo and sprint groups after themes and before Ungrouped", () => {
  const e = (id, group, label) => ({ id, kind: "effort", label: label || id, effort: { group } });
  const list = [e("u", "ungrouped"), e("s", "sprint"), e("r2", "repo", "acme/web"), e("r1", "repo", "acme/api"),
    e("th", "theme"), e("t", "ticket"), e("n", "effort"), e("x", "mystery")];
  assert.deepEqual(list.sort(L.effortOrder).map((n) => n.id), ["n", "t", "th", "x", "r1", "r2", "s", "u"]);
});

test("Resume is labelled with the configured tool", () => {
  const r = L.actionButtons({ token: "t", action_api: "/api/action", resume: "codex" }).find((a) => a.kind === "resume");
  assert.equal(r.label, "Resume codex");
  assert.match(r.title, /last codex session/);
});

// ---------- query language: the vectors shared with internal/dash ----------
const BASE = L.registry(FIELDS);
const VREG = L.registry({ fields: FIELDS.fields, planned: Object.assign({}, FIELDS.planned, VECTORS.planned) }, VECTORS.fields);
test("the evaluator knows every node kind the registry does", () => {
  assert.deepEqual(L.QUERY_KINDS, BASE.field("kind").values);
});
let vectorCases = 0;
VECTORS.cases.forEach((c) => {
  test("vector: " + c.name, () => {
    const reg = c.base ? BASE : VREG;
    const q = L.parseQuery(c.query, reg);
    if (c.error) {
      assert.ok(q.error, "want error " + c.error);
      assert.ok(q.error.error.includes(c.error), q.error.error + "\n  want " + c.error);
      assert.equal(q.error.pos, c.pos, q.error.error);
      if (c.fixed != null) assert.equal(q.error.fixed, c.fixed);
      vectorCases++;
      return;
    }
    assert.ok(!q.error, q.error && q.error.error);
    const snap = Object.assign({}, VECTORS.snapshot, c.env || {});
    const res = L.evaluate(q, L.queryIndex(snap, reg));
    assert.deepEqual(res.matches.slice().sort(), c.expect.matches, "matches");
    assert.deepEqual(res.rows.slice().sort(), c.expect.rows, "rows");
    if (c.expect.visible === "all") assert.equal(res.vis, null, "visible");
    else if (c.expect.visible) assert.deepEqual(sorted(res.vis), c.expect.visible, "visible");
    assert.deepEqual(res.warnings, c.warnings || [], "warnings");
    vectorCases++;
  });
});

test("the registry knows every catalogue field; the vectors add a planned one", () => {
  assert.ok(BASE.field("move") && BASE.field("linked_prs"));
  assert.equal(BASE.planned.size, 0);
  assert.equal(VREG.planned.get("example_planned"), "gh-0");
  assert.deepEqual(BASE.names.slice(0, 3), ["kind", "text", "effort"]);
});

test("chart clicks toggle one clause and keep the others", () => {
  assert.equal(L.toggleClause("", BASE, "activity", "2026-10-14"), "activity:2026-10-14");
  assert.equal(L.toggleClause("activity:2026-10-14", BASE, "prstate", "open"), "activity:2026-10-14;prstate:open");
  assert.equal(L.toggleClause("activity:2026-10-14;prstate:open", BASE, "prstate", "draft"), "activity:2026-10-14;prstate:draft");
  assert.equal(L.toggleClause("activity:2026-10-14;prstate:draft", BASE, "prstate", "draft"), "activity:2026-10-14");
  assert.equal(L.toggleClause("kind:pr; prstate:open,draft", BASE, "prstate", "open"), "kind:pr;prstate:open", "a multi-value clause is replaced, not toggled off");
  assert.equal(L.toggleClause("effort:a;!effort:b", BASE, "effort", "c"), "effort:c");
  assert.equal(L.toggleClause("", BASE, "effort", "my theme"), "effort:\"my theme\"");
  assert.ok(L.clauseActive("kind:pr;prstate:open,draft", BASE, "prstate", "draft"));
  assert.ok(!L.clauseActive("!prstate:draft", BASE, "prstate", "draft"));
  assert.ok(!L.clauseActive("prstate:", BASE, "prstate", "open"), "an invalid query has no active clause");
});

test("quoteValue round-trips through the parser", () => {
  for (const v of ["2026-10-14", "ticket-team-7", "repo:acme/api", "my theme", "a\"b\\c", "@me", "1..2", "*", "unknown"]) {
    const q = L.parseQuery("effort:" + L.quoteValue(v), BASE);
    assert.ok(!q.error, v + ": " + (q.error && q.error.error));
    assert.equal(q.clauses[0].vals[0].text, v);
  }
  assert.equal(L.quoteValue("@me"), "\"@me\"", "a literal @ value must not become a macro");
});

test("the URL hash round-trips and keeps unknown keys", () => {
  const pairs = L.parseHash("#view=table&x=a%2Fb&q=kind:pr;text:%22a%20b%22&sel=pr:acme%2Fapi%2341,ws-1&sort=-waiting,size");
  const st = L.hashState(pairs);
  assert.deepEqual(st, { view: "table", q: "kind:pr;text:\"a b\"", sel: ["pr:acme/api#41", "ws-1"], values: { x: "a/b", sort: "-waiting,size" } });
  // Unchanged values, and keys the page does not own, keep their spelling;
  // sort stays the raw multi-key string for the view that owns it.
  assert.equal(L.buildHash(pairs, st), "#view=table&x=a%2Fb&q=kind:pr;text:%22a%20b%22&sel=pr:acme/api%2341,ws-1&sort=-waiting,size");
  const h = L.buildHash(pairs, { view: "table", q: st.q, sel: st.sel, values: Object.assign({}, st.values, { cols: "prstate,repo", y: "size", x: "" }) });
  assert.equal(h, "#view=table&q=kind:pr;text:%22a%20b%22&sel=pr:acme/api%2341,ws-1&sort=-waiting,size&cols=prstate,repo&y=size");
  assert.deepEqual(L.hashState(L.parseHash(h)).sel, ["pr:acme/api#41", "ws-1"]);
  assert.equal(L.buildHash(L.parseHash("#view=graph&q=a"), { view: "graph", q: "", sel: [], values: {} }), "", "defaults are dropped");
  assert.deepEqual(L.hashState(L.parseHash("#view=table")), { view: "table", q: "", sel: [], values: {} }, "old #view=table links still work");
  assert.deepEqual(L.hashState(L.parseHash("#sel=a%2Cb,c")).sel, ["a,b", "c"], "an ID with a comma survives");
  assert.equal(L.hashState(L.parseHash("#q=%E0%A4%A")).q, "%E0%A4%A", "a malformed escape is kept, not thrown");
});

test("history: a drag is one undoable entry, and a commit closes it", () => {
  const steps = [];
  let open = false;
  for (const mode of ["live", "live", "live", "commit", "commit", "live", "normalize", "commit"]) {
    const st = L.historyStep(open, mode, false);
    steps.push(st.op); open = st.open;
  }
  // The first live change pushes, the next ones replace it, the release
  // replaces it too; a plain click pushes; tidying the URL never pushes.
  assert.deepEqual(steps, ["push", "replace", "replace", "replace", "push", "push", "replace", "replace"]);
  assert.deepEqual(L.historyStep(true, "commit", true), { op: "", open: false }, "a commit with nothing new closes the open entry");
  assert.deepEqual(L.historyStep(true, "live", true), { op: "", open: true });
  assert.deepEqual(L.historyStep(false, "normalize", false), { op: "replace", open: false });
});

test("addons get the bootstrap without the action token", () => {
  const b = L.addonBoot({ mode: "live", token: "secret", action_api: "/api/action", ack_api: "/api/ack", extract: "/api/extract", me: "alice" });
  assert.deepEqual(b, { mode: "live", extract: "/api/extract", me: "alice" });
  assert.ok(Object.isFrozen(b));
  assert.ok(L.STATE_KEY.test("sprint") && L.STATE_KEY.test("x") && !L.STATE_KEY.test("a=b") && !L.STATE_KEY.test(""));
});

test("extractURL encodes the page state for /api/extract", () => {
  assert.equal(L.extractURL("/api/extract", { q: "kind:pr;text:\"a b\"", cols: ["prstate", "repo"], group: "effort", view: "rows" }),
    "/api/extract?q=kind%3Apr%3Btext%3A%22a%20b%22&cols=prstate%2Crepo&group=effort&view=rows");
  assert.equal(L.extractURL("/api/extract", { q: "", cols: [] }), "/api/extract");
});

test("registered views become tabs; re-registering replaces in place", () => {
  const before = L.views.length;
  L.registerView({ id: "graph", label: "Graph", render() {} });
  L.registerView({ id: "table", label: "Table", render() {} });
  L.registerView({ id: "probe", label: "Probe", question: "Does an addon view show up?", render() {} });
  L.registerView({ id: "table", label: "Analysis table", render() {} });
  assert.deepEqual(L.viewTabs(L.views, "probe").map((t) => [t.id, t.label, t.pressed]),
    [["graph", "Graph", false], ["table", "Analysis table", false], ["probe", "Probe", true]]);
  assert.equal(L.viewTabs(L.views, "probe")[2].title, "Does an addon view show up?");
  assert.throws(() => L.registerView({ id: "", render() {} }), /needs a string id/);
  assert.throws(() => L.registerPanel({ id: "x" }), /render\(ctx\)/);
  let seen = null;
  L.onRegister = (what, spec) => { seen = what + ":" + spec.id; };
  L.registerPanel({ id: "late", render() {} });
  assert.equal(seen, "panel:late");
  L.onRegister = null;
  L.views.length = before;
  L.panels.length = 0;
});

test("valueText never prints blank for unknown or empty", () => {
  const ix = L.queryIndex(VECTORS.snapshot, VREG);
  assert.equal(L.valueText(ix, "checks", "pr:acme/web#9"), "unknown");
  assert.equal(L.valueText(ix, "effort", "ticket:jira:TEAM-99"), "sprint:1234");
  assert.equal(L.valueText(ix, "effort", "sprint:1235"), "none");
  assert.equal(L.valueText(ix, "points", "ticket:jira:TEAM-99"), "none", "unestimated");
  assert.equal(L.valueText(ix, "points", "ticket:gh:acme/web#5"), "unknown");
  assert.equal(L.valueText(ix, "ticket_status", "ticket:jira:TEAM-123"), "In Progress");
  assert.equal(L.valueText(ix, "updated_ago", "pr:acme/api#41"), "1d");
  assert.equal(L.valueText(ix, "checks", "ws-0000000000000001"), "", "a field that does not apply is blank, not unknown");
});

// ---------- the analysis table (web/live-table.js, spec/gh-101.md) ----------
// TV is internal/dash/testdata/table_vectors.json: a snapshot with gh-99's
// facts and check_stats per its contract, and the fields gh-99 registers.
const TREG = L.registry(FIELDS, TV.fields);
// PRE99 is the registry before gh-99 registered its fields.
const GH99 = ["move", "move_people", "waiting_on", "reason", "waiting", "ci", "size", "size_bucket", "stale", "mismatch", "linked_prs"];
const PRE99 = L.registry({ fields: FIELDS.fields.filter((f) => !GH99.includes(f.name)) });
const tix = L.queryIndex(TV.snapshot, TREG);
const tnode = (id) => tix.nodes[tix.byId.get(id)];
const PR = (n) => tnode(n).pr;

test("table: default columns follow the rows' kinds and skip fields not registered yet", () => {
  assert.deepEqual(T.defaultColumns(TREG, ["pr"]), ["repo", "author", "move", "waiting", "age", "size", "ci", "reviewer", "ticket"]);
  assert.deepEqual(T.defaultColumns(BASE, ["pr"]), T.defaultColumns(TREG, ["pr"]), "gh-99's fields are registered");
  assert.deepEqual(T.defaultColumns(PRE99, ["pr"]), ["repo", "author", "age", "reviewer", "ticket"], "before gh-99: no move, waiting, size or CI");
  assert.deepEqual(T.defaultColumns(TREG, ["ticket"]), ["ticket_status", "assignee", "points", "sprint", "linked_prs", "mismatch"]);
  assert.deepEqual(T.defaultColumns(PRE99, ["ticket"]), ["ticket_status", "assignee", "points", "sprint"]);
  for (const kinds of [["pr", "ticket"], ["effort", "workspace", "pr"], []]) {
    assert.deepEqual(T.defaultColumns(TREG, kinds), ["kind", "effort", "state", "data", "last_activity"], "mixed: today's six with name");
  }
});

test("table: cols= round-trips, the defaults stay out of the URL, unknown names drop", () => {
  assert.deepEqual(T.columns(PRE99, "repo, move,nope,repo,name,text", ["pr"]), { cols: ["repo"], custom: true }, "a field not registered yet is dropped");
  assert.deepEqual(T.columns(TREG, "", ["ticket"]), { cols: T.defaultColumns(TREG, ["ticket"]), custom: false });
  assert.equal(T.colsValue(TREG, T.defaultColumns(TREG, ["pr"]), ["pr"]), "");
  assert.equal(T.colsValue(TREG, ["ci", "repo"], ["pr"]), "ci,repo");
  assert.equal(T.colsValue(TREG, [], ["pr"]), "name", "no columns but the name is not the defaults");
  assert.deepEqual(T.columns(TREG, "name", ["pr"]), { cols: [], custom: true });
  const cols = ["a", "b", "c"];
  assert.deepEqual(T.moveColumn(cols, "b", -1), ["b", "a", "c"]);
  assert.deepEqual(T.moveColumn(cols, "b", 1), ["a", "c", "b"]);
  assert.deepEqual(T.moveColumn(cols, "a", -1), cols, "the first column cannot move left");
  assert.deepEqual(T.moveColumn(cols, "c", 1), cols);
  assert.deepEqual(T.addColumn(cols, "d"), ["a", "b", "c", "d"]);
  assert.deepEqual(T.addColumn(cols, "a"), cols);
  assert.deepEqual(T.removeColumn(cols, "b"), ["a", "c"]);
  assert.deepEqual(cols, ["a", "b", "c"], "column edits return new lists");
  const avail = T.available(BASE, ["ticket"]);
  assert.deepEqual(avail.slice(0, 3), ["state", "data", "last_activity"], "the table's own columns first");
  assert.ok(avail.includes("points") && avail.includes("effort") && !avail.includes("author") && !avail.includes("text") && !avail.includes("name"));
  assert.ok(T.available(BASE, []).includes("author"), "with no rows every field is offered");
});

test("table: header clicks sort, reverse, add, reverse and remove keys", () => {
  assert.deepEqual(T.parseSort("-waiting, size,,-waiting"), [{ key: "waiting", desc: true }, { key: "size", desc: false }]);
  assert.equal(T.formatSort(T.parseSort("-waiting,size")), "-waiting,size");
  let s = T.nextSort([], "waiting", false, true);
  assert.equal(T.formatSort(s), "-waiting", "durations start longest first");
  s = T.nextSort(s, "size", true, true); assert.equal(T.formatSort(s), "-waiting,-size", "Shift+click adds a key");
  s = T.nextSort(s, "size", true, true); assert.equal(T.formatSort(s), "-waiting,size", "and reverses it");
  s = T.nextSort(s, "repo", true, false); assert.equal(T.formatSort(s), "-waiting,size,repo", "a tertiary key");
  s = T.nextSort(s, "size", true, true); assert.equal(T.formatSort(s), "-waiting,repo", "the third Shift+click removes it");
  s = T.nextSort(s, "waiting", false, true); assert.equal(T.formatSort(s), "waiting,repo", "a plain click on the first key reverses it");
  s = T.nextSort(s, "author", false, false); assert.equal(T.formatSort(s), "author", "a plain click elsewhere sorts by that alone");
  assert.equal(T.defaultDesc(TREG, "age"), true);
  assert.equal(T.defaultDesc(TREG, "last_activity"), true);
  assert.equal(T.defaultDesc(TREG, "author"), false);
});

TV.sorts.forEach((c) => {
  test("table vector: " + c.name, () => {
    const r = L.runQuery(c.query, tix);
    assert.ok(!r.error, r.error && r.error.error);
    assert.deepEqual(T.sortRows(tix, r.res.rowIdx, T.parseSort(c.sort)).map((i) => tix.ids[i]), c.order);
  });
});

test("table: unknown sorts last in both directions, before rows the field does not apply to", () => {
  const C = T.CLASS, known = (n) => ({ c: C.known, r: 0, n });
  for (const desc of [false, true]) {
    assert.ok(T.compareValues(known(1), { c: C.unknown }, desc) < 0, "known before unknown, desc " + desc);
    assert.ok(T.compareValues({ c: C.empty }, { c: C.unknown }, desc) < 0, "none before unknown");
    assert.ok(T.compareValues({ c: C.unknown }, { c: C.na }, desc) < 0, "unknown before n/a");
    assert.equal(T.compareValues({ c: C.unknown }, { c: C.unknown }, desc), 0);
  }
  assert.ok(T.compareValues(known(1), known(2), false) < 0);
  assert.ok(T.compareValues(known(1), known(2), true) > 0);
  assert.deepEqual(T.sortValue(tix, "waiting", tix.byId.get("pr:acme/api#2")), { c: C.known, r: 0, n: 10 });
  assert.equal(T.sortValue(tix, "waiting", tix.byId.get("pr:acme/api#5")).c, C.unknown, "no facts: unknown");
  assert.equal(T.sortValue(tix, "waiting", tix.byId.get("pr:acme/api#1")).c, C.na, "an author move waits on no one");
  assert.equal(T.sortValue(tix, "waiting", tix.byId.get("pr:acme/web#4")).c, C.na, "a closed PR waits on no one");
  assert.equal(T.sortValue(tix, "waiting", tix.byId.get("ws-1")).c, C.na);
  assert.equal(T.sortValue(tix, "ci", tix.byId.get("pr:acme/api#2")).r, 2, "systemic_only is the third CI value");
  assert.equal(T.sortValue(tix, "last_activity", tix.byId.get("ws-1")).c, C.known);
  assert.equal(T.sortValue(tix, "last_activity", tix.byId.get("pr:acme/api#5")).c, C.unknown, "a PR with no update time");
  assert.equal(T.sortValue(tix, "last_activity", tix.byId.get("effort:api")).c, C.na);
  assert.equal(T.sortValue(tix, "name", tix.byId.get("effort:api")).s, "api work,effort:api", "name sorts as text: label, title, ID");
  const prsTickets = L.runQuery("kind:pr,ticket", tix).res.rowIdx;
  assert.deepEqual(T.sortRows(tix, prsTickets, T.parseSort("-name")), T.sortRows(tix, prsTickets, T.parseSort("-text")), "the extract sorts name as text");
  assert.equal(T.sortValue(tix, "nope", tix.byId.get("pr:acme/api#2")).c, C.na, "a column with no field (a stale cols=) is n/a");
});

test("table: CI marks put failures first, systemic hollow, skipped out, at most 20", () => {
  const stats = TV.snapshot.check_stats;
  const many = T.ciMarks(PR("pr:acme/api#5"), stats);
  assert.equal(many.label, "3 failing (2 fail everywhere), 25 passing");
  assert.equal(many.marks.length, 20);
  assert.equal(many.more, 8);
  assert.deepEqual(many.marks.slice(0, 4).map((m) => m.kind + ":" + m.name), ["fail:unit-tests", "systemic:flaky-e2e", "systemic:vendor-hsm-tests", "pass:check-00"]);
  assert.ok(!many.marks.some((m) => m.name === "docs"), "skipped checks are left out");
  const one = T.ciMarks(PR("pr:acme/api#1"), stats);
  assert.deepEqual(one.marks.map((m) => m.kind + ":" + m.name), ["fail:unit-tests", "systemic:vendor-hsm-tests", "pending:lint", "pass:build", "pass:e2e"]);
  assert.equal(one.label, "2 failing (1 fails everywhere), 1 pending, 2 passing", "a neutral check passes");
  assert.equal(T.ciMarks(PR("pr:acme/api#1"), null).label, "2 failing, 1 pending, 2 passing", "without check_stats nothing is systemic");
  assert.equal(T.ciMarks(PR("pr:acme/api#1"), { "acme/web": stats["acme/api"] }).counts.systemic, 0, "systemic is per repository");
  assert.equal(T.ciMarks(PR("pr:acme/web#4"), stats), null, "checks not read are unknown, not none");
  assert.equal(T.ciMarks({ repo: "acme/api", check_details: [] }, stats).label, "no checks reported");
  assert.match(T.ciMarks({ repo: "acme/api", check_details: [], checks_truncated: true }, stats).label, /more checks than were read$/);
});

test("table: one review glyph per reviewer, an outstanding request first-class", () => {
  const rv = (id) => T.reviewMarks(PR(id));
  assert.deepEqual(rv("pr:acme/api#1").marks.map((m) => T.REVIEW[m.state].glyph + m.who), ["○bob", "✓carol"]);
  assert.equal(rv("pr:acme/api#1").label, "Reviews: bob review requested, carol approved");
  const two = rv("pr:acme/api#2");
  assert.deepEqual(two.marks.map((m) => T.REVIEW[m.state].glyph + m.who), ["✕carol", "○alice", "○team:web"]);
  assert.match(two.label, /alice review requested \(again, after commented\)/);
  assert.equal(rv("pr:acme/api#5"), null, "requests and reviews unknown");
  assert.deepEqual(rv("pr:acme/web#3").marks, []);
  const onlyReviews = T.reviewMarks({ reviews: [{ author: "bob", state: "COMMENTED" }, { author: "dan", state: "DISMISSED" }] });
  assert.deepEqual(onlyReviews.marks.map((m) => T.REVIEW[m.state].glyph + m.who), ["…bob"]);
  assert.equal(onlyReviews.requestsKnown, false);
  assert.match(onlyReviews.label, /requested reviewers unknown$/);
});

test("table: the move is a word, 'you' and accented only when move_people holds @me", () => {
  assert.equal(tix.env.me, "alice");
  assert.deepEqual(T.moveWord(PR("pr:acme/api#2").facts, "alice"), { word: "you", move: "reviewer", mine: true, unknown: false }, "a review requested from me");
  assert.deepEqual(T.moveWord(PR("pr:acme/api#2").facts, "ALICE").mine, true);
  assert.deepEqual(T.moveWord(PR("pr:acme/web#3").facts, "alice"), { word: "author", move: "author", mine: false, unknown: false });
  assert.deepEqual(T.moveWord(PR("pr:acme/web#6").facts, "alice").word, "nobody");
  assert.equal(T.moveWord(PR("pr:acme/api#5").facts, "alice").word, "unknown", "no facts: unknown, never nobody");
  assert.equal(T.moveWord({ move: "reviewer", move_people: ["alice"] }, "").mine, false, "no @me, no accent");
  assert.match(T.moveTitle(PR("pr:acme/api#2").facts), /^Held by team:web, alice\nawaiting review: ready since Oct 20/);
  assert.match(T.moveTitle(undefined), /unknown/);
});

test("table: Waiting does not apply to author and none moves, unless changes were requested", () => {
  assert.equal(T.waitingNA({ move: "none" }), true);
  assert.equal(T.waitingNA({ move: "author", reasons: [{ code: "draft" }, { code: "ci_failing" }] }), true);
  assert.equal(T.waitingNA({ move: "author", reasons: [{ code: "changes_requested" }] }), false, "rule 3 waits on the author since the blocking review");
  assert.equal(T.waitingNA({ move: "author" }), true);
  for (const m of ["reviewer", "nobody", "unknown", undefined]) assert.equal(T.waitingNA({ move: m }), false, String(m));
  assert.equal(T.waitingNA(undefined), false, "no facts: unknown, not n/a");
});

test("table: bars are linear from zero on the column's scale; size is log", () => {
  assert.equal(T.barFraction(5, 10, false), 0.5);
  assert.equal(T.barFraction(0, 10, false), 0);
  assert.equal(T.barFraction(3, 0, false), 0);
  assert.equal(T.barFraction(-1, 10, false), 0);
  assert.equal(T.barFraction(20, 10, false), 1);
  assert.ok(Math.abs(T.barFraction(9, 99, true) - 0.5) < 1e-12, "log(1+9)/log(1+99)");
  assert.equal(T.daysText(10.7, false), "10d");
  assert.equal(T.daysText(10, true), "≥10d");
  assert.equal(T.daysLabel("Waiting", 1, true), "Waiting: at least 1 day");
  assert.equal(T.sizeText(PR("pr:acme/api#1"), 150), "+120 −30");
  assert.equal(T.sizeText({}, 150), "150 lines");
  assert.equal(T.sizeLabel(PR("pr:acme/api#1"), 150), "size 150 lines (120 added, 30 deleted); bar on a log scale");
});

test("table: a kind clause drops context rows; otherwise they are listed unless hidden", () => {
  const r = L.runQuery("move:reviewer", tix).res;
  assert.deepEqual(r.rows, ["pr:acme/api#2"]);
  const shown = T.tableRows(tix, r, false, true);
  assert.deepEqual(shown.kinds, ["pr"], "defaults come from the rows, not the context");
  assert.ok(shown.contextCount > 0 && shown.rows.length === 1 + shown.contextCount);
  assert.ok(shown.context.has(tix.byId.get("effort:api")) && !shown.context.has(tix.byId.get("pr:acme/api#2")));
  assert.deepEqual(T.tableRows(tix, r, false, false).rows.map((i) => tix.ids[i]), ["pr:acme/api#2"], "context hidden");
  const k = L.runQuery("kind:pr;move:reviewer", tix).res;
  assert.equal(T.tableRows(tix, k, true, true).contextCount, 0, "kind: keeps only rows");
  assert.equal(T.tableRows(tix, null, false, true).rows.length, tix.nodes.length, "no query: every node");
  assert.deepEqual(T.kindClauseKinds(L.parseQuery("kind:pr,ticket;!kind:ticket", TREG)), ["pr"]);
  assert.equal(T.kindClauseKinds(L.parseQuery("repo:acme/api", TREG)), null);
});

test("table: column info names the scales", () => {
  assert.match(T.colInfo(TREG, "size").title, /log scale/);
  assert.match(T.colInfo(TREG, "waiting").title, /linear from zero/);
  assert.equal(T.colInfo(TREG, "reviewer").label, "Reviews");
  assert.equal(T.colInfo(TREG, "state").local, true);
  assert.ok(T.numeric(T.colInfo(TREG, "points")) && !T.numeric(T.colInfo(TREG, "author")));
});

// ---------- performance (requirement 28): logged, not asserted ----------
// genSnapshot mirrors genSnapshot in internal/dash/query_test.go.
function genSnapshot(n) {
  const nodes = [], edges = [], activity = {}, days = [];
  for (let i = 13; i >= 0; i--) days.push(new Date(Date.UTC(2026, 9, 20 - i)).toISOString().slice(0, 10));
  const states = ["open", "open", "merged", "closed"], checks = ["success", "failure", "pending", ""];
  for (let r = 0; r < 20; r++) nodes.push({ id: "effort:repo:acme/repo-" + r, kind: "effort", label: "acme/repo-" + r, effort: { key: "repo:acme/repo-" + r, group: "repo" } });
  for (let e = 0; nodes.length < n; e++) {
    const eid = "effort:effort-" + e, ws = "ws-" + e, bm = "bm-" + e, tk = "ticket:jira:TEAM-" + e, ag = "agent:a-" + e, repo = "acme/repo-" + (e % 20);
    nodes.push({ id: eid, kind: "effort", label: "Effort " + e, effort: { key: "effort-" + e, group: "effort" } },
      { id: ws, kind: "workspace", label: "repo/ws-" + e, workspace: { slug: "ws", repo: "repo", repo_slug: repo, effort_id: eid } },
      { id: bm, kind: "bookmark", label: "team-" + e + "-work", bookmark: { name: "b", pr_lookup: "fresh", pr_count: 2 } },
      { id: tk, kind: "ticket", label: "TEAM-" + e, ticket: { key: "k", system: "jira", status: "In Progress", freshness: "fresh" } },
      { id: ag, kind: "agent", label: "claude", agent: { liveness: "active", effort_id: eid, workspace_id: ws } });
    edges.push({ source: eid, target: ws, kind: "contains" }, { source: ws, target: bm, kind: "on" }, { source: eid, target: ag, kind: "runs" });
    for (let p = 0; p < 2; p++) {
      const num = e * 2 + p, id = "pr:" + repo + "#" + num;
      const pr = { number: num, repo: repo, title: "Change " + num, state: states[(e + p) % 4], reviewers_known: true, freshness: "fresh", bookmark_id: bm,
        updated_at: new Date(Date.UTC(2026, 9, 20, 19) - (e * 7 + p) * 3600000).toISOString() };
      if ((e + p) % 5 === 0) pr.is_draft = true;
      if (checks[(e * 3 + p) % 4]) pr.checks = checks[(e * 3 + p) % 4];
      nodes.push({ id: id, kind: "pr", label: repo + "#" + num, pr: pr });
      edges.push({ source: bm, target: id, kind: "pr" }, { source: id, target: tk, kind: "ticket" });
    }
    for (let p = 0; p < 2; p++) {
      const num = 100000 + e * 2 + p, id = "pr:" + repo + "#" + num;
      const pr = { number: num, repo: repo, title: "Change " + num, state: states[(e + p + 1) % 4], reviewers_known: true, freshness: "fresh", sources: ["inventory"],
        updated_at: new Date(Date.UTC(2026, 9, 20, 19) - (e * 5 + p) * 3600000).toISOString() };
      if (checks[(e + p) % 4]) pr.checks = checks[(e + p) % 4];
      nodes.push({ id: id, kind: "pr", label: repo + "#" + num, pr: pr });
      edges.push({ source: "effort:repo:" + repo, target: id, kind: "tracks" }, { source: id, target: tk, kind: "ticket" });
    }
    const act = new Array(14).fill(0);
    act[e % 14] = 1 + (e % 3);
    activity[eid] = act;
  }
  return { schema: 1, generated_at: "2026-10-20T15:00:00-04:00", timezone: "America/New_York", days: 14, nodes: nodes, edges: edges, counts: { days: days, activity: activity } };
}
const PERF_QUERY = "kind:pr;prstate:open,draft,merged;!checks:pending;freshness:fresh,stale;repo:*;effort:*;activity:*;!text:zzz;review:none,approved;!prstate:closed;text:change;!repo:acme/repo-19";
let perfLine = "";
test("12 clauses over 2,000 nodes (timing logged)", () => {
  const snap = genSnapshot(2000);
  let t0 = process.hrtime.bigint();
  const ix = L.queryIndex(snap, BASE);
  const indexMs = Number(process.hrtime.bigint() - t0) / 1e6;
  const q = L.parseQuery(PERF_QUERY, BASE);
  assert.ok(!q.error && q.clauses.length === 12, q.error && q.error.error);
  for (let i = 0; i < 5; i++) L.evaluate(q, ix); // warm up the JIT
  const runs = 50;
  t0 = process.hrtime.bigint();
  let res;
  for (let i = 0; i < runs; i++) res = L.evaluate(q, ix);
  const evalMs = Number(process.hrtime.bigint() - t0) / 1e6 / runs;
  assert.ok(res.matches.length > 0);
  perfLine = "perf: " + ix.nodes.length + " nodes, index " + indexMs.toFixed(2) + " ms once, 12 clauses " + evalMs.toFixed(3) + " ms per evaluation (" + res.matches.length + " matches)";
  // gh-101: the table's sort over every node by two keys.
  const all = ix.nodes.map((nd, i) => i), sort = T.parseSort("-updated_ago,repo");
  for (let i = 0; i < 3; i++) T.sortRows(ix, all, sort);
  t0 = process.hrtime.bigint();
  for (let i = 0; i < 10; i++) T.sortRows(ix, all, sort);
  perfLine += "; table sort of " + all.length + " rows by 2 keys " + (Number(process.hrtime.bigint() - t0) / 1e6 / 10).toFixed(2) + " ms";
});

console.log("ok " + n + " (" + vectorCases + " shared query vectors) " + perfLine);
