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

test("prstate filter treats a known-empty bookmark with an attached PR as having one, like the Go counts", () => {
  const sn = {
    nodes: [
      { id: "bm-empty", kind: "bookmark", label: "a", bookmark: { pr_lookup: "fresh", pr_count: 0 } },
      { id: "bm-att", kind: "bookmark", label: "b", bookmark: { pr_lookup: "fresh", pr_count: 0 } },
      { id: "bm-unk", kind: "bookmark", label: "c", bookmark: { pr_lookup: "unknown", pr_count: 0 } },
      { id: "pr-att", kind: "pr", label: "#1", pr: { state: "open" } },
      { id: "pr-unk", kind: "pr", label: "#2", pr: { state: "open" } },
    ],
    edges: [{ source: "bm-att", target: "pr-att", kind: "pr" }, { source: "bm-unk", target: "pr-unk", kind: "pr" }],
  };
  const ix = L.index(sn);
  assert.deepEqual(L.seeds(sn, ix, { by: "prstate", value: "none" }), ["bm-empty"]);
  // An attached PR does not make an unknown lookup known.
  assert.deepEqual(L.seeds(sn, ix, { by: "prstate", value: "unknown" }), ["bm-unk"]);
});

test("sortTracked lists PRs before tickets, each by label", () => {
  const list = [{ id: "t2", kind: "ticket", label: "WGO-2" }, { id: "p9", kind: "pr", label: "acme/web#9" },
    { id: "t1", kind: "ticket", label: "WGO-1" }, { id: "p3", kind: "pr", label: "acme/api#3" }];
  assert.deepEqual(L.sortTracked(list).map((n) => n.id), ["p3", "p9", "t1", "t2"]);
  assert.equal(list[0].id, "t2", "the input is not reordered");
});

test("every repo and sprint group in the analysis fixture has tracked PRs or tickets to list", () => {
  const fx = JSON.parse(require("node:fs").readFileSync(require("node:path").join(__dirname, "../../dash/testdata/analysis_fixture.json"), "utf8"));
  const idx = L.index(fx);
  const groups = fx.nodes.filter((n) => n.kind === "effort" && (n.effort.group === "repo" || n.effort.group === "sprint"));
  assert.ok(groups.length > 0, "the fixture has repo or sprint groups");
  groups.forEach((g) => {
    assert.equal(L.childrenOf(idx, g.id, "contains").length, 0, g.id + " has no workspaces");
    const tracked = L.sortTracked(L.childrenOf(idx, g.id, "tracks"));
    assert.ok(tracked.length > 0, g.id + " tracks nothing");
    assert.ok(tracked.every((n) => n.kind === "pr" || n.kind === "ticket"), g.id + " tracks only PRs and tickets");
  });
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
  assert.deepEqual(T.defaultColumns(TREG, ["pr"]), ["repo", "author", "move", "reason", "waiting", "age", "size", "ci", "reviewer", "ticket"]);
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

test("table: CI marks merge runs of one check name, the worst run winning (as gh-99)", () => {
  const pr = { repo: "acme/api", check_details: [
    { name: "build", state: "success" }, { name: "build", state: "failure" }, // push and pull_request runs
    { name: "lint", state: "neutral" }, { name: "lint", state: "skipped" },
    { name: "e2e", state: "success" }, { name: "e2e", state: "pending" },
    { name: "docs", state: "skipped" }, { name: "docs", state: "skipped" },
    { name: "vendor-hsm-tests", state: "failure" }, { name: "vendor-hsm-tests", state: "failure" }] };
  const ci = T.ciMarks(pr, TV.snapshot.check_stats);
  assert.deepEqual(ci.marks.map((m) => m.kind + ":" + m.name), ["fail:build", "systemic:vendor-hsm-tests", "pending:e2e", "pass:lint"]);
  assert.equal(ci.label, "2 failing (1 fails everywhere), 1 pending, 1 passing");
});

test("table: sort text matches Go's lowercasing and byte order", () => {
  assert.equal(T.sortText("İris"), "iris", "İ lowercases to i, as strings.ToLower does");
  assert.equal(T.sortText("ΟΔΟΣ"), "οδοσ", "no final sigma");
  assert.ok(T.sortText("ｚoe") < T.sortText("🐛bot"), "U+FF5A before U+1F41B, as in UTF-8");
  assert.ok(T.sortText("é") > T.sortText("z") && T.sortText("Alice") === "alice");
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
  assert.deepEqual(L.runQuery("kind:pr,ticket;!kind:ticket", tix).res.rowKinds, ["pr"], "ctx.query.rowKinds: the kinds kind: clauses allow");
  assert.deepEqual(L.runQuery("kind:*;repo:acme/api", tix).res.rowKinds, L.QUERY_KINDS);
  assert.equal(L.runQuery("repo:acme/api", tix).res.rowKinds, null, "no kind: clause");
});

test("table: column info names the scales", () => {
  assert.match(T.colInfo(TREG, "size").title, /log scale/);
  assert.match(T.colInfo(TREG, "waiting").title, /linear from zero/);
  assert.equal(T.colInfo(TREG, "reviewer").label, "Reviews");
  assert.equal(T.colInfo(TREG, "state").local, true);
  assert.ok(T.numeric(T.colInfo(TREG, "points")) && !T.numeric(T.colInfo(TREG, "author")));
});

// ===== gh-105: the sprint view's pure logic (web/live-sprint.js) =====
// Loaded beside live.js, on the analysis fixture beside fields.json; the
// day count, default sprint and points must agree with extract_sprint.go
// (the last test reads its golden summary).
const SP = require(require("node:path").join(require("node:path").dirname(process.argv[2]), "live-sprint.js"))(L);
const SPDATA = require("node:path").dirname(process.argv[3]);
const AFIX = JSON.parse(fs.readFileSync(require("node:path").join(SPDATA, "analysis_fixture.json"), "utf8"));
const keysOf = (nodes) => nodes.map((x) => x.label);

test("sprint: active sprints, their tickets and the default", () => {
  const list = SP.activeSprints(AFIX);
  assert.deepEqual(list.map((a) => [a.id, a.tickets.length, a.people]), [[1234, 6, 5], [1240, 1, 0]], "Sprint 12: five of six for alice and bob; Web Sprint 4: carol's");
  assert.equal(SP.defaultSprint(list).id, 1234);
  const mk = (id, n, people, start) => ({ id, sprint: { id, start }, tickets: new Array(n), people });
  assert.equal(SP.defaultSprint([mk(1, 9, 1, "2026-10-01T00:00:00Z"), mk(2, 3, 2, "2026-10-02T00:00:00Z")]).id, 2, "people first");
  assert.equal(SP.defaultSprint([mk(1, 3, 2, "2026-10-01T00:00:00Z"), mk(2, 4, 2, "2026-10-02T00:00:00Z")]).id, 2, "then tickets");
  assert.equal(SP.defaultSprint([mk(1, 4, 2, "2026-10-01T00:00:00Z"), mk(2, 4, 2, "2026-10-02T00:00:00Z")]).id, 1, "then the first");
  assert.equal(SP.defaultSprint([]), null);
  assert.ok(SP.assignedToPeople({ assignee_id: "ACCT-ALICE" }, AFIX.people), "by account ID, any case");
  assert.ok(SP.assignedToPeople({ assignee: "bob" }, AFIX.people), "by display name");
  assert.ok(!SP.assignedToPeople({ assignee: "Carol", assignee_id: "acct-carol" }, AFIX.people));
});

test("sprint: sprint= chooses a sprint or names the active ones", () => {
  const list = SP.activeSprints(AFIX);
  assert.equal(SP.chooseSprint(AFIX, list, "").sprint.id, 1234);
  assert.equal(SP.chooseSprint(AFIX, list, "1240").sprint.id, 1240);
  assert.equal(SP.chooseSprint(AFIX, list, "sprint:1240").sprint.id, 1240);
  assert.equal(SP.chooseSprint(AFIX, list, "1235").error, "sprint 1235 (Sprint 13) is future, and the sprint view shows active sprints only; the active sprints are 1234 (Sprint 12), 1240 (Web Sprint 4)");
  assert.equal(SP.chooseSprint(AFIX, list, "999").error, "no sprint 999 in this snapshot; the active sprints are 1234 (Sprint 12), 1240 (Web Sprint 4)");
  assert.match(SP.chooseSprint(AFIX, list, "Sprint 12").error, /^sprint "Sprint 12" is not a sprint ID/);
});

test("sprint: points by status category, unestimated and unknown apart", () => {
  const p = SP.progress(SP.activeSprints(AFIX)[0].tickets);
  assert.deepEqual(p.parts.map((x) => [x.key, x.points, x.tickets]), [["done", 3, 1], ["in_progress", 15, 3], ["todo", 0, 2]]);
  assert.equal(p.points, 18);
  assert.deepEqual(keysOf(p.unestimated), ["TEAM-104"], "read without points: unestimated, not zero");
  assert.deepEqual(keysOf(p.unknownPoints), ["TEAM-106"], "points never read (no story_points_known): unknown, not unestimated");
  assert.deepEqual(p.parts.map((x) => [+x.start.toFixed(3), +x.frac.toFixed(3)]), [[0, 0.167], [0.167, 0.833], [1, 0]], "bar starts and lengths, done first");
  const odd = SP.progress([{ label: "X-1", ticket: { story_points_known: true, story_points: 2.5 } }, { label: "X-2", ticket: { status_category: "todo" } }]);
  assert.deepEqual(keysOf(odd.unknownCategory), ["X-1"], "unknown category: listed, not in a part");
  assert.equal(odd.points, 0, "its points are in no part");
  assert.deepEqual(SP.pointsOf({ story_points_known: true, story_points: 0 }), { kind: "known", value: 0, text: "0" }, "zero is an estimate");
  assert.equal(SP.pointsOf({ story_points: 5 }).kind, "unknown", "points without story_points_known are not trusted");
  assert.equal(SP.ptsText(1) + "|" + SP.ptsText(2.5), "1 pt|2.5 pts");
});

test("sprint: tickets by status category, then assignee, then key", () => {
  const g = SP.groups(SP.activeSprints(AFIX)[0].tickets);
  assert.deepEqual(g.map((x) => [x.label, x.rows.map((r) => r.node.label + ":" + r.assignee.text)]), [
    ["Done", ["TEAM-102:Alice"]],
    ["In Progress", ["TEAM-101:Alice", "TEAM-103:Alice", "TEAM-105:Alice"]],
    ["To Do", ["TEAM-104:Bob", "TEAM-106:Unassigned"]],
  ]);
  const t = (key, cat, assignee, freshness) => ({ id: key, label: key, ticket: { key, status_category: cat, assignee, freshness } });
  const g2 = SP.groups([t("A-10", "todo", "", "unknown"), t("A-9", "todo", "", "fresh"), t("A-2", "todo", "zed"), t("A-1", "todo", "Amy"), t("A-3", "", "Amy")]);
  assert.deepEqual(g2.map((x) => [x.key, x.rows.map((r) => r.node.label)]), [["todo", ["A-1", "A-2", "A-9", "A-10"]], ["", ["A-3"]]], "named, unassigned, unknown; unknown category last");
  assert.equal(SP.assigneeOf({ freshness: "unknown" }).text, "unknown", "no Jira data: the assignee is unknown, not unassigned");
  assert.equal(SP.compareKeys("TEAM-9", "TEAM-10") < 0, true);
});

test("sprint: linked PR glyphs, mismatches and whose move on hover", () => {
  assert.deepEqual(["draft", "open", "approved", "merged", "closed", "", "weird"].map((s) => SP.prGlyph(s).glyph).join(""), "◇○✓●✕??");
  assert.equal(SP.prGlyph("open").text, "open, awaiting review");
  const byId = new Map(AFIX.nodes.map((x) => [x.id, x]));
  const t102 = byId.get("ticket:jira:TEAM-102").ticket;
  assert.deepEqual(SP.mismatchesOf(t102).list.map((m) => m.code), ["ticket_done_code_open"]);
  assert.deepEqual(SP.mismatchesOf(byId.get("ticket:jira:TEAM-101").ticket).list, []);
  assert.deepEqual(SP.mismatchesOf(byId.get("ticket:jira:TEAM-108").ticket), { unknown: true, why: "could not be decided: status_category unknown" });
  assert.equal(SP.mismatchesOf({ key: "X" }).unknown, true);
  const lp = t102.facts.linked_prs[0], tip = SP.prTitle(lp, byId.get(lp.id).pr);
  assert.match(tip, /^acme\/api#102: open, awaiting review — /);
  assert.match(tip, /\nWhose move: \w+/);
  assert.equal(SP.prTitle({ id: "pr:x", ref: "acme/x#1", state: "merged" }, null), "acme/x#1: merged\nNot in the snapshot");
  assert.equal(SP.codeText("ticket_done_code_open"), "ticket done code open");
});

test("sprint: day N of M in the snapshot's timezone (TestSprintDay's cases)", () => {
  const day = (start, end, now, tz) => SP.sprintDay(start, end, Date.parse(now), L.queryEnv({ generated_at: now, timezone: tz }).dayOf).text;
  for (const [s, e, now, tz, want] of [
    ["2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-10-30T16:00:00Z", "UTC", "day 11 of 14"],
    ["2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-10-20T08:00:00Z", "UTC", "day 1 of 14"],
    ["2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-11-03T07:00:00Z", "UTC", "day 14 of 14"],
    ["2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-11-04T09:00:00Z", "UTC", "day 14 of 14, past its end"],
    ["2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-10-18T09:00:00Z", "UTC", "starts in 2 days"],
    ["2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-10-20T07:00:00Z", "UTC", "starts today"],
    ["2026-10-05T14:00:00Z", "2026-10-26T14:00:00Z", "2026-10-08T13:00:00Z", "America/New_York", "day 4 of 21"],
    ["2026-10-05T14:00:00Z", "2026-10-26T14:00:00Z", "2026-10-09T03:30:00Z", "America/New_York", "day 4 of 21"],
    ["2026-10-05T14:00:00Z", "2026-10-26T14:00:00Z", "2026-10-09T03:30:00Z", "UTC", "day 5 of 21"],
    ["2026-10-26T13:00:00Z", "2026-11-09T14:00:00Z", "2026-11-08T15:00:00Z", "America/New_York", "day 14 of 14"],
  ]) assert.equal(day(s, e, now, tz), want, s + ".." + e + " at " + now + " in " + tz);
  const d = SP.sprintDay("2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", Date.parse("2026-10-30T16:00:00Z"), L.queryEnv(AFIX).dayOf);
  assert.equal(d.frac.toFixed(3), "0.738", "the bar: the share of the sprint's time passed");
  assert.equal(SP.sprintDay(undefined, "2026-11-03T08:00:00Z", 0, L.queryEnv(AFIX).dayOf).ok, false, "unknown dates");
});

test("sprint: explains a missing board or missing sprint data", () => {
  const base = { generated_at: "2026-10-30T16:00:00Z", nodes: [], edges: [] };
  const ex = (sources, nodes) => SP.explain(Object.assign({}, base, { sources, nodes: nodes || [] }), []);
  assert.deepEqual(ex({}), { title: "No sprint data", config: true, detail: ["This snapshot does not report the jira_sprints source, so it holds no sprints: it was made by a wgo dash without sprint support, or before a [jira] board was set."] });
  const noBoard = ex({ jira_sprints: { state: "unknown", detail: "no [jira] board is configured; set board in the [jira] section of ~/.wgo/config.toml to show its sprints" } });
  assert.equal(noBoard.title + "|" + noBoard.config, "No [jira] board is configured|true");
  assert.match(SP.BOARD_SNIPPET, /^\[jira\]\nboard = <id>$/);
  const miss = ex({ jira_sprints: { state: "unknown", detail: "board 42's sprints are not cached yet", parts: [{ name: "board 42", state: "unknown" }] } });
  assert.deepEqual([miss.title, miss.config, miss.detail], ["Board 42's sprints are not known yet", false, ["Board 42's sprints are not cached yet."]]);
  const failed = ex({ jira_sprints: { state: "error", detail: "board 42: the sprint lookup failed", parts: [{ name: "board 42", state: "error", error: "acli: not logged in" }] } });
  assert.deepEqual([failed.title, failed.detail], ["Board 42's sprints could not be read", ["The lookup failed: acli: not logged in.", "Board 42: the sprint lookup failed."]]);
  const none = ex({ jira_sprints: { state: "fresh", parts: [{ name: "board 42", state: "fresh" }] } }, [{ id: "sprint:1235", kind: "sprint", sprint: { id: 1235, name: "Sprint 13", state: "future", start: "2026-11-03T08:00:00Z" } }]);
  assert.deepEqual([none.title, none.detail], ["Board 42 has no active sprint", ["The sprint view shows active sprints only.", "Future: Sprint 13 (1235) from 2026-11-03."]]);
  assert.equal(SP.explain(AFIX, SP.activeSprints(AFIX)), null, "an active sprint needs no explanation");
});

test("sprint: direct labels sit under their segments without overlapping", () => {
  assert.deepEqual(SP.labelLayout([0, 85, 514], [60, 100, 70], 514, 8), [0, 85, 444], "each at its start; the last pulled inside");
  assert.deepEqual(SP.labelLayout([0, 20, 40], [60, 60, 60], 300, 8), [0, 68, 136], "pushed right past the previous label");
  assert.deepEqual(SP.labelLayout([0, 300, 300], [60, 60, 60], 300, 8), [0, 172, 240], "pulled back from the right");
  assert.equal(SP.labelLayout([0, 50, 100], [80, 80, 80], 200, 8), null, "too wide: the labels wrap instead");
});

test("sprint: the page counts as extract_sprint.go does (its golden summary)", () => {
  const golden = fs.readFileSync(require("node:path").join(SPDATA, "sprint_extract.golden.md"), "utf8");
  const sp = SP.activeSprints(AFIX)[0], p = SP.progress(sp.tickets);
  for (const part of p.parts) assert.ok(golden.includes("- " + part.label + ": " + part.points + " points · " + part.tickets + " ticket" + (part.tickets === 1 ? "" : "s") + "\n"), part.label);
  assert.ok(golden.includes("Not in the points: " + p.unestimated.length + " unestimated (TEAM-104) · " + p.unknownPoints.length + " with points unknown (TEAM-106)\n"));
  const d = SP.sprintDay(sp.sprint.start, sp.sprint.end, L.queryEnv(AFIX).nowMs, L.queryEnv(AFIX).dayOf);
  assert.ok(golden.includes("## " + SP.sprintName(sp.sprint) + "\n") && golden.includes(" · " + d.text + "\n"), "same default sprint and day");
});
// ===== end gh-105 =====

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

// ---------- gh-102: linked filters (web/live-facets.js) ----------
// The addon and the analysis fixture sit beside live.js and fields.json.
// The block's own braces keep its names from sibling addons' blocks.
{
const path = require("node:path");
const FX = require(path.join(path.dirname(process.argv[2]), "live-facets.js"))(L);
const AFIX = JSON.parse(fs.readFileSync(path.join(path.dirname(process.argv[3]), "analysis_fixture.json"), "utf8"));
const AREG = L.registry(FIELDS);
const aix = L.queryIndex(AFIX, AREG);

// bruteCoupled counts, by scanning nodes, the rows of expr without field
// name's clauses that pass has(value of name on the node).
function bruteRows(ix, expr, name) {
  const q = L.parseQuery(expr, ix.reg);
  const rest = q.clauses.filter((c) => c.name !== name).map((c) => c.raw).join(";");
  const r = L.runQuery(rest, ix);
  assert.ok(!r.error, rest + ": " + (r.error && r.error.error));
  return r.res.rowIdx;
}
function bruteCount(ix, rows, name, has) {
  const vals = ix.values.get(name);
  return rows.filter((i) => !vals[i].na && has(vals[i])).length;
}
const FACET_QUERIES = ["", "kind:pr", "repo:acme/api;prstate:open", "kind:pr;prstate:open,draft;!ci:failing;author:alice,carol", "move:reviewer,nobody;size:30..299",
  "kind:pr;prstate:merged;merged:2026-10-16..2026-10-22", "kind:ticket;mismatch:*;sprint:\"Sprint 12\"", "when:2026-10-20..;!repo:acme/infra;kind:pr,ticket", "waiting:2..;age:..13;!reviewer:*"];
const FACET_FIELDS = FX.DEFAULT_FACETS.concat(["kind", "when", "merged", "stale", "points", "effort"]);

test("facets: coupled counts ignore the facet's own clauses and honour all others", () => {
  let checked = 0;
  for (const expr of FACET_QUERIES) {
    const qm = FX.queryMasks(aix, expr);
    assert.equal(qm.error, null, expr);
    for (const name of FACET_FIELDS) {
      const fi = FX.fieldIndex(aix, name), rows = bruteRows(aix, expr, name), c = FX.facetCounts(fi, FX.others(qm, name), null);
      const at = expr + " / " + name;
      assert.equal(c.unknown.coupled, bruteCount(aix, rows, name, (v) => v.unknown), at + " unknown");
      assert.equal(c.none.coupled, bruteCount(aix, rows, name, (v) => !v.unknown && !v.norm.length && !v.nums.length), at + " none");
      if (fi.type === "bars") c.values.forEach((r) => { assert.equal(r.coupled, bruteCount(aix, rows, name, (v) => !v.unknown && v.norm.includes(r.key)), at + " " + r.key); checked++; });
      if (fi.type === "hist") c.bins.forEach((r, k) => {
        const b = fi.bins[k];
        assert.equal(r.coupled, bruteCount(aix, rows, name, (v) => !v.unknown && v.nums.some((x) => x >= b.lo && (b.open || x < b.hi))), at + " bin " + b.label); checked++;
      });
      if (fi.type === "days") c.bins.forEach((r, k) => { assert.equal(r.coupled, bruteCount(aix, rows, name, (v) => !v.unknown && v.norm.includes(fi.days[k])), at + " " + fi.days[k]); checked++; });
    }
  }
  assert.ok(checked > 1000, "checked " + checked);
  // Concretely: under repo:acme/api;prstate:open the repo facet still counts
  // acme/web's open PRs (its own clause is ignored), and the prstate facet
  // counts only acme/api's (the repo clause holds).
  const qm = FX.queryMasks(aix, "repo:acme/api;prstate:open");
  const repo = FX.facetCounts(FX.fieldIndex(aix, "repo"), FX.others(qm, "repo"), null).values.find((v) => v.key === "acme/web");
  assert.deepEqual([repo.coupled, repo.ghost], [5, 12]);
  const st = FX.facetCounts(FX.fieldIndex(aix, "prstate"), FX.others(qm, "prstate"), null).values;
  assert.deepEqual(st.map((v) => v.key + ":" + v.coupled + "/" + v.ghost).sort(), ["closed:1/1", "draft:1/2", "merged:2/6", "none:0/1", "open:5/11"]);
});

test("facets: ghosts are the whole snapshot; the selection's share is its own", () => {
  const fi = FX.fieldIndex(aix, "author"), c = FX.facetCounts(fi, FX.others(FX.queryMasks(aix, "kind:ticket"), "author"), FX.fromIds(aix, ["pr:acme/api#101", "pr:acme/web#202"]));
  const by = Object.fromEntries(c.values.map((v) => [v.key, v]));
  assert.deepEqual([by.alice.ghost, by.alice.coupled, by.alice.part, by.bob.part, by.carol.part], [10, 0, 1, 1, 0], "author applies only to PRs, which kind:ticket leaves none of");
  const rows = FX.valueRows(aix, fi, c, FX.fieldState(L.parseQuery("", AREG), "author"));
  assert.deepEqual(rows.map((r) => r.label), ["Alice (you)", "Bob", "carol", "unknown"], "configured people first, by display name, then others by count");
  assert.equal(rows[0].person, "alice");
  const sprint = FX.valueRows(aix, FX.fieldIndex(aix, "sprint"), FX.facetCounts(FX.fieldIndex(aix, "sprint"), FX.full(aix.nodes.length), null), FX.fieldState(L.parseQuery("", AREG), "sprint"));
  assert.ok(sprint.some((r) => r.key === "sprint 12") && !sprint.some((r) => r.key === "1234"), "a sprint's ID beside its name is listed once: " + sprint.map((r) => r.key));
  // (gh-105 put the fixture's one ticket in no sprint into Web Sprint 4;
  // components still have an unknown and a none.)
  const comp = FX.valueRows(aix, FX.fieldIndex(aix, "component"), FX.facetCounts(FX.fieldIndex(aix, "component"), FX.full(aix.nodes.length), null), FX.fieldState(L.parseQuery("", AREG), "component"));
  assert.deepEqual(comp.slice(-2).map((r) => r.kind), ["unknown", "none"], "unknown and none are their own rows");
  assert.equal(sprint[sprint.length - 1].kind, "unknown", "a sprint unknown is its own row");
  const named = FX.valueRows(aix, FX.fieldIndex(aix, "sprint"), FX.facetCounts(FX.fieldIndex(aix, "sprint"), FX.full(aix.nodes.length), null), FX.fieldState(L.parseQuery("sprint:1234", AREG), "sprint"));
  assert.deepEqual(named.filter((r) => r.key === "1234").map((r) => r.ghost + ":" + r.sel), ["6:true"], "an ID the query names shows with its count");
  const shown = FX.shownRows(FX.valueRows(aix, FX.fieldIndex(aix, "repo"), FX.facetCounts(FX.fieldIndex(aix, "repo"), FX.full(aix.nodes.length), null), FX.fieldState(L.parseQuery("", AREG), "repo")), "WEB", 8);
  assert.deepEqual(shown.rows.map((r) => r.label), ["acme/web", "unknown"], "the type-ahead finds values, case-insensitively");
});

test("facets: every gesture produces the gh-100 clause", () => {
  const g = (expr, name, v, mode) => FX.gesture(expr, AREG, name, v, mode);
  assert.equal(g("kind:pr", "repo", "acme/api", "only"), "kind:pr;repo:acme/api", "click: only this value");
  assert.equal(g("kind:pr;repo:acme/api", "repo", "acme/api", "only"), "kind:pr", "click again clears");
  assert.equal(g("repo:acme/api,acme/web;kind:pr", "repo", "acme/infra", "only"), "kind:pr;repo:acme/infra", "click replaces the field's clauses");
  assert.equal(g("repo:acme/api;kind:pr", "repo", "acme/web", "or"), "repo:acme/api,acme/web;kind:pr", "Shift-click adds to the OR list");
  assert.equal(g("repo:acme/api,acme/web", "repo", "acme/web", "or"), "repo:acme/api", "Shift-click removes from it");
  assert.equal(g("repo:acme/api", "repo", "acme/api", "or"), "", "removing the last value drops the clause");
  assert.equal(g("!repo:acme/web", "repo", "acme/web", "or"), "repo:acme/web", "Shift-click takes a value out of the exclusions");
  assert.equal(g("kind:pr", "repo", "acme/web", "exclude"), "kind:pr;!repo:acme/web", "Alt-click excludes");
  assert.equal(g("!repo:acme/web", "repo", "acme/api", "exclude"), "!repo:acme/web,acme/api", "Alt-click adds to the exclusions");
  assert.equal(g("!repo:acme/web,acme/api", "repo", "acme/web", "exclude"), "!repo:acme/api", "Alt-click again includes it again");
  assert.equal(g("repo:acme/api,acme/web", "repo", "acme/web", "exclude"), "repo:acme/api;!repo:acme/web", "excluding a chosen value unchooses it");
  assert.equal(g("move_people:@me", "move_people", "bob", "or"), "move_people:@me,bob", "macros are kept as written");
  assert.equal(g("", "ticket_status", "In Progress", "only"), "ticket_status:\"In Progress\"", "values are quoted when they must be");
  assert.equal(g("mergeable:mergeable", "mergeable", "unknown", "or"), "mergeable:mergeable,unknown", "unknown joins a list");
  assert.equal(FX.noneGesture("kind:ticket", AREG, "mismatch", "only"), "kind:ticket;!mismatch:*", "none: known and empty");
  assert.equal(FX.noneGesture("kind:ticket;!mismatch:*", AREG, "mismatch", "only"), "kind:ticket", "again clears");
  assert.equal(FX.noneGesture("mismatch:code_merged_ticket_open;kind:ticket", AREG, "mismatch", "exclude"), "kind:ticket;mismatch:*", "excluding none: any value");
  for (const e of [g("kind:pr", "repo", "acme/web", "exclude"), g("repo:acme/api;kind:pr", "repo", "acme/web", "or"), FX.noneGesture("kind:ticket", AREG, "mismatch", "only")]) assert.ok(!L.parseQuery(e, AREG).error, e);
  // Ranges: a histogram's bins, a slider's edges, a brush's days.
  const size = FX.fieldIndex(aix, "size");
  assert.deepEqual(size.bins.map((b) => b.label), ["0", "1", "3", "10", "30", "100", "300", "1k"], "log bins in 1-3-10 steps");
  assert.deepEqual(FX.edgeRange(size, 4, 6), ["size:30..299"]);
  assert.deepEqual(FX.edgeRange(size, 0, 3), ["size:..9"]);
  assert.deepEqual(FX.edgeRange(size, 5, size.bins.length), ["size:100.."]);
  assert.equal(FX.binExclusion(size, 3), "!size:10..29");
  assert.deepEqual(FX.rangeTexts("points", 0.5, 2, true), ["points:>=0.5", "points:<2"], "fractions get exact bounds");
  assert.deepEqual(FX.rangeEdges(size, FX.fieldState(L.parseQuery("size:30..299", AREG), "size")), { a: 4, b: 6, active: true }, "handles sit at the range's edges");
  assert.deepEqual(FX.rangeEdges(size, FX.fieldState(L.parseQuery("size:>40", AREG), "size")), { a: 4, b: 8, active: true });
  const age = FX.fieldIndex(aix, "age");
  assert.deepEqual(age.bins.map((b) => b.text), ["0 days", "1 day", "2–3 days", "4–6 days", "7–13 days", "14–29 days", "30–59 days", "60–89 days", "≥ 90 days"], "durations: day bins on a log scale, the last open");
  const pts = FX.numberBins({ type: "number", scale: "linear", unit: "points" }, [1, 2, 3, 5, 8]);
  assert.deepEqual(pts.map((b) => b.lo), [0, 1, 2, 3, 4, 5, 6, 7, 8], "small integers: one bin each");
  const lin = FX.numberBins({ type: "duration", scale: "linear" }, [0, 3, 40]);
  assert.equal(lin[lin.length - 1].text, "≥ 14", "linear durations: a bin per day for two weeks, then open");
  assert.equal(FX.setClauses("kind:pr;!when:2026-10-20;when:2026-10-21;prstate:open", AREG, "when", ["when:2026-10-16..2026-10-22"]), "kind:pr;!when:2026-10-20;when:2026-10-16..2026-10-22;prstate:open", "a brush replaces the positive clauses in place");
  assert.equal(FX.setClauses("kind:pr;when:2026-10-21", AREG, "when", []), "kind:pr", "an empty brush clears");
});

test("facets: brushing from shipped replaces merged:-7d.. and finds a past week's merges by merge date", () => {
  const shipped = "kind:pr;prstate:merged;merged:-7d..";
  assert.equal(FX.timeTarget(L.parseQuery(shipped, AREG)), "merged");
  assert.equal(FX.timeTarget(L.parseQuery("kind:pr;when:2026-10-01..", AREG)), "when");
  assert.equal(FX.timeTarget(L.parseQuery("kind:pr", AREG)), "when");
  const brushed = FX.setClauses(shipped, AREG, "merged", FX.dayTexts("merged", "2026-10-16", "2026-10-22"));
  assert.equal(brushed, "kind:pr;prstate:merged;merged:2026-10-16..2026-10-22");
  const rows = L.runQuery(brushed, aix).res.rows.slice().sort();
  assert.deepEqual(rows, ["pr:acme/api#108", "pr:acme/web#204", "pr:acme/web#205"]);
  const updatedLater = rows.filter((id) => aix.nodes[aix.byId.get(id)].pr.updated_at.slice(0, 10) > "2026-10-22");
  assert.deepEqual(updatedLater, ["pr:acme/api#108", "pr:acme/web#205"], "merged that week, updated in a later one");
  assert.deepEqual(L.runQuery("when:2026-10-16..2026-10-22;kind:pr;prstate:merged", aix).res.rows.slice().sort(), rows, "when is the merge date of a merged PR");
  // The when chart counts merged PRs per merge day across the window, the
  // merged clause being its own.
  const fi = FX.fieldIndex(aix, "merged"), c = FX.facetCounts(fi, FX.others(FX.queryMasks(aix, brushed), "merged"), null);
  assert.deepEqual(fi.days.filter((d, k) => c.bins[k].coupled).map((d, k) => d), ["2026-10-06", "2026-10-19", "2026-10-20", "2026-10-22", "2026-10-28", "2026-10-29"]);
  const states = FX.dayStates(aix, fi, FX.fieldState(L.parseQuery(brushed, AREG), "merged"));
  assert.deepEqual(fi.days.filter((d, k) => states[k].sel), ["2026-10-16", "2026-10-17", "2026-10-18", "2026-10-19", "2026-10-20", "2026-10-21", "2026-10-22"]);
});

test("facets: the previous period is compared only where the PR inventory covers it", () => {
  const per = (expr, snap) => FX.periodText(FX.period(aix, expr, snap || AFIX));
  assert.equal(FX.coverDay(aix, AFIX), "2026-10-02");
  assert.equal(per("kind:pr;prstate:merged;merged:-7d.."), "2 merged · previous 3", "the last eight days against the eight before");
  assert.equal(per("kind:pr;prstate:merged;merged:2026-10-16..2026-10-22"), "3 merged · previous 0");
  assert.equal(per("when:2026-10-16..2026-10-22"), "3 merged · previous 0", "a when range counts the merged PRs among its rows");
  assert.equal(per("when:2026-10-19"), "1 merged · previous 0", "a single day is a one-day period");
  assert.equal(per("kind:pr;prstate:merged;merged:2026-10-03..2026-10-09"), "1 merged · previous 7 days not covered: merges are known from 2026-10-02");
  assert.equal(per("kind:pr;merged:..2026-10-09"), "at least 1 merged · no previous period: the range has no start");
  const noCover = JSON.parse(JSON.stringify(AFIX));
  delete noCover.sources.github_inventory.covered_since;
  assert.equal(per("kind:pr;prstate:merged;merged:2026-10-16..2026-10-22", noCover), "at least 3 merged · previous 7 days not known: the PR inventory does not say from when merges are known");
  assert.equal(FX.period(aix, "kind:pr;repo:acme/api", AFIX), null, "no time range, no comparison");
  assert.equal(FX.period(aix, "when:2026-10-16,2026-10-18", AFIX), null, "several separate days are no period");
  assert.match(FX.periodTitle(FX.period(aix, "when:2026-10-16..2026-10-22", AFIX)), /2026-10-16 to 2026-10-22 \(7 days\), against the same query 2026-10-09 to 2026-10-15/);
});

test("facets: the attention summary counts are its saved filters' counts", () => {
  const filters = [{ name: "my-move", expr: "move_people:@me" }, { name: "waiting-reviewers", expr: "kind:pr;move:reviewer" }, { name: "nobody", expr: "kind:pr;move:nobody" }];
  const sum = FX.summary(aix, filters);
  assert.deepEqual(sum.map((s) => s.label + "=" + s.count), ["your move=4", "waiting on reviewers=4", "nobody's move=2"], "the numbers TestAttentionSummaryCountsOnTheFixture asserts in Go");
  sum.forEach((s) => assert.equal(s.count, L.runQuery(s.expr, aix).res.rows.length, s.name));
  assert.deepEqual(sum.map((s) => s.accent), [true, false, false], "only your move takes the accent");
  const noMe = L.queryIndex(Object.assign({}, AFIX, { people: [{ login: "alice" }] }), AREG);
  const unknown = FX.summary(noMe, filters)[0];
  assert.ok(unknown.count === null && /@me is unknown/.test(unknown.why), "no person marked me: unknown, never 0: " + JSON.stringify(unknown));
  assert.match(FX.summary(aix, [{ name: "nobody", expr: "kind:pr;move:nobody", hidden: true, reason: "needs fields" }])[2].why, /needs fields/);
});

test("facets: the panel lists the configured facets, added ones, and every field the query names", () => {
  assert.deepEqual(FX.facetNames(AREG, FX.DEFAULT_FACETS, [], null, "when"), FX.DEFAULT_FACETS);
  assert.deepEqual(FX.facetNames(AREG, ["repo", "nope", "text"], ["size_bucket", "repo"], L.parseQuery("kind:ticket;!label:x;when:-7d..", AREG), "when"), ["repo", "size_bucket", "kind", "label"],
    "unknown names and text are left out, the when chart's field too");
  assert.equal(L.filterConfigLine("kind:pr;text:\"a \\\"b\\\"\"", "my-filter"), "my-filter = \"kind:pr;text:\\\"a \\\\\\\"b\\\\\\\"\\\"\"", "Copy as config writes a TOML basic string");
  assert.equal(L.filterConfigLine("text:a\tb"), "my-filter = \"text:a\\u0009b\"");
});

// Coupled counts for 12 facets on the analysis fixture scaled to 2,000
// nodes (scaleAnalysisFixture in analysis_fixture_test.go, in brief).
function scaleFixture(base, min) {
  const copies = Math.ceil(min / base.nodes.length), out = Object.assign({}, base, { nodes: [], edges: [] });
  out.counts = { days: base.counts.days, activity: {}, pr_states: {}, agents: {} };
  for (let k = 0; k < copies; k++) {
    const sfx = k ? "~" + k : "", c = JSON.parse(JSON.stringify(base));
    c.nodes.forEach((nd) => { nd.id += sfx; if (nd.effort) nd.effort.key += sfx; out.nodes.push(nd); });
    c.edges.forEach((e) => out.edges.push({ source: e.source + sfx, target: e.target + sfx, kind: e.kind }));
    Object.keys(c.counts.activity).forEach((id) => { out.counts.activity[id + sfx] = c.counts.activity[id]; });
  }
  return out;
}
test("facets: coupled counts for 12 facets at 2,000 nodes (timing logged)", () => {
  const big = scaleFixture(AFIX, 2000), bix = L.queryIndex(big, AREG);
  let t0 = process.hrtime.bigint();
  FX.DEFAULT_FACETS.forEach((name) => FX.fieldIndex(bix, name));
  const indexMs = Number(process.hrtime.bigint() - t0) / 1e6;
  const exprs = ["kind:pr;prstate:open,draft;!ci:failing;repo:acme/api,acme/web", "move:reviewer;size:30..299;age:..13", "kind:pr;prstate:open,draft;!ci:failing;repo:acme/api,acme/web;author:alice"];
  let total = 0, runs = 0, bars = 0;
  for (let i = 0; i < 12; i++) {
    const expr = exprs[i % exprs.length] + (i >= 3 ? ";!text:zz" + i : ""); // new expressions: no cached masks
    t0 = process.hrtime.bigint();
    const qm = FX.queryMasks(bix, expr);
    FX.DEFAULT_FACETS.forEach((name) => { const c = FX.facetCounts(FX.fieldIndex(bix, name), FX.others(qm, name), null); bars += c.values.length + c.bins.length; });
    if (i >= 2) { total += Number(process.hrtime.bigint() - t0) / 1e6; runs++; }
  }
  assert.ok(bars > 0);
  perfLine += "; facets: " + bix.nodes.length + " nodes, 12 facet indexes " + indexMs.toFixed(1) + " ms once, coupled counts for 12 facets " + (total / runs).toFixed(2) + " ms per query change";
});

test("facets: a merge day chosen from shipped keeps the chart on merges", () => {
  const shipped = "kind:pr;prstate:merged;merged:-7d..";
  const one = FX.gesture(shipped, AREG, "merged", "2026-10-28", "only");
  assert.equal(one, "kind:pr;prstate:merged;merged:2026-10-28", "a day click replaces merged:-7d..");
  assert.equal(FX.timeTarget(L.parseQuery(one, AREG)), "merged", "any positive merged clause keeps the target");
  const two = FX.gesture(one, AREG, "merged", "2026-10-29", "only");
  assert.equal(two, "kind:pr;prstate:merged;merged:2026-10-29", "a second click replaces the first, never ANDs a conflicting day");
  assert.deepEqual(L.runQuery(two, aix).res.rows, ["pr:acme/infra#303"]);
  assert.equal(FX.periodText(FX.period(aix, one, AFIX)), "1 merged · previous 0", "the previous period survives the click");
  assert.equal(FX.timeTarget(L.parseQuery("kind:pr;prstate:merged;!merged:2026-10-28", AREG)), "when", "an exclusion alone is not the chart's");
});

test("facets: days before the local activity window are unknown, not zero", () => {
  const when = FX.localDays(aix, AREG.field("when")), act = FX.localDays(aix, AREG.field("activity"));
  assert.equal(when.from, "2026-10-17", "counts.days starts the local window");
  assert.deepEqual([when.all, act.all], [false, true], "when is local only for efforts and workspaces; activity for every kind");
  assert.equal(FX.localDays(aix, AREG.field("merged")), null, "a PR date has no local part");
  assert.equal(FX.localText(when, "2026-10-10"), "local activity not recorded");
  const totals = L.dayTotals({ days: aix.days, activity: aix.activity }, null);
  const busiest = aix.days[totals.indexOf(Math.max.apply(null, totals))];
  assert.equal(FX.localText(when, busiest), Math.max.apply(null, totals) + " local changes", "each recorded day carries its jj change count");
  const none = FX.localDays(L.queryIndex(Object.assign({}, AFIX, { counts: { days: [], activity: {} } }), AREG), AREG.field("when"));
  assert.equal(none.from, null);
  assert.equal(FX.localText(none, "2026-10-29"), "local activity not recorded", "no window: nothing recorded");
});
}
// ---------- end gh-102 ----------

// ---------- gh-103: the scatterplot and the CI grid ----------
{
// web/live-scatter.js and web/live-cigrid.js, beside live.js, on
// internal/dash/testdata/analysis_fixture.json, beside fields.json.
const path = require("node:path");
const SC = require(path.join(path.dirname(process.argv[2]), "live-scatter.js"))(L, T);
const CG = require(path.join(path.dirname(process.argv[2]), "live-cigrid.js"))(L);
const AFX = JSON.parse(fs.readFileSync(path.join(path.dirname(process.argv[3]), "analysis_fixture.json"), "utf8"));
const aix = L.queryIndex(AFX, BASE);
const aprs = L.runQuery("kind:pr", aix).res.rowIdx;
const F = (name) => BASE.field(name);
const near = (a, b) => Math.abs(a - b) < 1e-9;
const refs = (marks) => marks.map((m) => m.ref);

test("scatter: the symmetric log puts zero at zero and undoes itself", () => {
  assert.equal(SC.fwd(0), 0);
  assert.ok(near(SC.fwd(9), 1) && near(SC.fwd(99), 2) && near(SC.fwd(-9), -1), "log10(1 + |v|), signed");
  for (const v of [0, 0.5, 7, 1234, -3]) assert.ok(Math.abs(SC.inv(SC.fwd(v)) - v) < 1e-9, String(v));
  assert.ok(SC.fwd(1) > SC.fwd(0) && SC.fwd(10) > SC.fwd(1), "monotonic through zero");
});

test("scatter: axes are PR number and duration fields; log for log scales and durations, said in the title", () => {
  const names = SC.axisFields(BASE).map((f) => f.name);
  for (const n of ["waiting", "size", "age", "updated_ago", "additions", "deletions", "files"]) assert.ok(names.includes(n), n);
  for (const n of ["points", "activity", "created", "move"]) assert.ok(!names.includes(n), n + " is not a PR number or duration");
  assert.equal(SC.axisField(BASE, "", SC.DEFAULT_X).name, "waiting");
  assert.equal(SC.axisField(BASE, "", SC.DEFAULT_Y).name, "size");
  assert.equal(SC.axisField(BASE, "age", "waiting").name, "age");
  assert.equal(SC.axisField(BASE, "points", "waiting").name, "waiting", "a ticket field falls back to the default");
  assert.equal(SC.axisField(BASE, "nope", "size").name, "size");
  assert.equal(SC.axisField(PRE99, "", "waiting").name, "age", "before gh-99 the first field that can be an axis");
  assert.equal(SC.axisField(L.registry({ fields: [] }), "", "waiting"), null);
  assert.ok(SC.isLog(F("waiting")) && SC.isLog(F("size")) && SC.isLog(F("files")) && !SC.isLog(F("points")));
  assert.equal(SC.axisTitle(F("waiting")), "Waiting (days, log scale)");
  assert.equal(SC.axisTitle(F("size")), "Size (lines changed, log scale)", "the unit the label names is not repeated");
  assert.equal(SC.axisTitle(F("additions")), "Additions (lines, log scale)");
  assert.equal(SC.axisTitle(F("points")), "Story points", "linear: no log scale");
  assert.equal(SC.shortLabel(F("size")), "Size");
});

test("scatter: gridlines at 1, 10, 100, 1,000 lines and 1, 7, 30, 90 days; the axis ends on them", () => {
  const size = F("size"), age = F("age"), waiting = F("waiting");
  const d = SC.niceDomain(size, [16, 2230]);
  assert.ok(near(SC.inv(d[0]), 10) && Math.abs(SC.inv(d[1]) - 10000) < 1e-6, "16 to 2,230 lines: 10 to 10,000");
  assert.deepEqual(SC.ticks(size, d).map((t) => t.label), ["10", "100", "1,000", "10,000"]);
  const a = SC.niceDomain(age, [0, 200]);
  assert.deepEqual(SC.ticks(age, a).map((t) => t.label), ["0", "1d", "7d", "30d", "90d", "365d"]);
  const w = SC.niceDomain(waiting, [2, 5]);
  assert.ok(near(SC.inv(w[0]), 1) && Math.abs(SC.inv(w[1]) - 7) < 1e-9);
  assert.deepEqual(SC.ticks(waiting, w).map((t) => t.label), ["1d", "2d", "3d", "5d", "7d"], "a narrow range takes the denser steps");
  assert.deepEqual(SC.ticks(size, [SC.fwd(15), SC.fwd(80)]).map((t) => t.label), ["20", "50"], "zoomed in between two decades");
  assert.ok(near(SC.inv(SC.niceDomain(size, [10])[1]), 100), "a value on a gridline gets the next one");
  assert.deepEqual(SC.niceDomain(waiting, []).map((t) => Math.round(SC.inv(t))), [0, 7], "no values: a week");
  const pts = { name: "points", label: "Story points", type: "number", scale: "linear" }, lin = SC.niceDomain(pts, [1, 13]);
  assert.deepEqual(lin, [0, 15], "the step the widened range takes");
  assert.deepEqual(SC.ticks(pts, lin).map((t) => t.v), [0, 5, 10, 15]);
  assert.deepEqual(SC.ticks(pts, SC.niceDomain(pts, [2, 9])).map((t) => t.v), [2, 4, 6, 8, 10]);
  assert.deepEqual(SC.zoomDomain([0, 10], 5, 0.5), [2.5, 7.5]);
  assert.equal(SC.tickText(waiting, 0.1 + 0.2), "0.3d", "no floating-point noise in a narrow zoom's labels");
  assert.equal(SC.tickText(size, 0.1 + 0.2), "0.3");
  assert.equal(SC.tickText(size, 12000), "12,000");
});

test("scatter: zooming stays inside the marks' range: in at one edge, then out at the other", () => {
  const base = [0, 4];
  let r = SC.zoomWithin(base, base, 0, 0.5);
  assert.deepEqual(r, [0, 2], "zoom in at the low edge");
  r = SC.zoomWithin(base, r, 4, 1.5);
  assert.deepEqual(r, [0, 3], "zooming out at the far edge would start at -2: it slides back to 0");
  assert.deepEqual(SC.zoomWithin(base, [3, 4], 4, 1.5), [2.5, 4]);
  const past = SC.zoomWithin(base, [2, 4.5], 4.4, 1.2);
  assert.ok(near(past[0], 1) && past[1] === 4, "a range past the high end slides back below it: " + past);
  assert.deepEqual(SC.zoomWithin(base, [0, 3], 1, 2), [0, 4], "never wider than every mark");
  const size = F("size"), dom = SC.niceDomain(size, [16, 2230]);
  let z = SC.zoomWithin(dom, dom, dom[0], 0.5);
  z = SC.zoomWithin(dom, z, dom[1], 1.8);
  assert.ok(z[0] >= dom[0] && z[1] <= dom[1], "inside 10 to 10,000 lines");
  assert.ok(SC.ticks(size, z).every((t) => t.v >= 10), "no gridline below the axis's first: " + SC.ticks(size, z).map((t) => t.label));
});

test("scatter: a mark with an unknown value sits in that axis's strip, never at zero", () => {
  const marks = SC.marks(aix, aprs, F("waiting"), F("size")), z = SC.partition(marks);
  assert.equal(marks.length, 20, "every PR row is a mark");
  assert.deepEqual(refs(z.plot), ["acme/api#101", "acme/api#102", "acme/api#104", "acme/web#201", "acme/web#202"]);
  assert.deepEqual(refs(z.corner), ["acme/web#207"], "neither value known");
  assert.equal(z.ystrip.length, 0);
  assert.equal(z.xstrip.length, 14);
  for (const m of z.xstrip.concat(z.corner)) assert.notEqual(m.xv.k, "v", m.ref + " has no x to plot");
  assert.deepEqual(SC.value(aix, F("waiting"), aix.byId.get("pr:acme/web#208")), { k: "unknown" }, "an unknown move: waiting unknown");
  assert.deepEqual(SC.value(aix, F("waiting"), aix.byId.get("pr:acme/api#103")), { k: "na", tag: "not waiting" }, "the author's move waits on no reviewer");
  assert.deepEqual(SC.value(aix, F("waiting"), aix.byId.get("pr:acme/api#107")), { k: "na", tag: "not waiting" }, "merged");
  assert.equal(SC.stripText(z.xstrip.map((m) => m.xv)), "unknown: 1 · not waiting: 13");
  assert.deepEqual(SC.value(aix, F("waiting"), aix.byId.get("pr:acme/web#202")), { k: "v", v: 4, lower: true });
  const byAge = SC.partition(SC.marks(aix, aprs, F("age"), F("size")));
  assert.equal(byAge.plot.length, 19);
  assert.deepEqual(refs(byAge.corner), ["acme/web#207"]);
  assert.deepEqual(SC.marks(aix, L.runQuery("kind:ticket", aix).res.rowIdx, F("age"), F("size")), [], "only PR rows are marks");
});

test("scatter: shape by move, the accent only for my move, × only for a failing CI of its own", () => {
  const marks = new Map(SC.marks(aix, aprs, F("waiting"), F("size")).map((m) => [m.ref, m.info]));
  const shape = (r) => marks.get(r).shape;
  assert.deepEqual(["acme/api#101", "acme/api#105", "acme/web#201", "acme/web#208", "acme/api#107"].map(shape), ["circle", "triangle", "square", "ring", "diamond"]);
  assert.deepEqual(marks.get("acme/api#103"), { move: "author", shape: "triangle", mine: true, failing: true }, "alice's own draft, failing");
  assert.equal(marks.get("acme/api#104").mine, true, "a review asked of alice through a team");
  assert.equal(marks.get("acme/api#105").mine, false, "carol's move");
  assert.equal(marks.get("acme/api#101").failing, false, "a check failing everywhere is not this PR's failure");
  assert.equal(marks.get("acme/web#203").failing, true);
  assert.deepEqual(SC.markInfo(undefined, "alice"), { move: "unknown", shape: "ring", mine: false, failing: false }, "no facts: unknown");
  assert.equal(new Set(["circle", "triangle", "square", "ring", "diamond"].map((s) => SC.shapePath(s, 5))).size, 4, "ring and circle share a path; the ring is hollow");
  assert.deepEqual(SC.KEY.map((k) => k.shape), ["circle", "triangle", "square", "ring", "diamond"], "one key names every shape");
});

test("scatter: each mark's aria-label says its ref, both values and the move", () => {
  const lbl = new Map(SC.marks(aix, aprs, F("waiting"), F("size")).map((m) => [m.ref, m.label]));
  assert.equal(lbl.get("acme/api#101"), "acme/api#101: Waiting 4 days, Size 222 lines, reviewer's move");
  assert.equal(lbl.get("acme/api#103"), "acme/api#103: Waiting n/a (not waiting), Size 630 lines, author's move (yours), CI failing");
  assert.equal(lbl.get("acme/web#202"), "acme/web#202: Waiting at least 4 days, Size 25 lines, reviewer's move (yours)");
  assert.equal(lbl.get("acme/web#207"), "acme/web#207: Waiting unknown, Size unknown, nobody's move");
  assert.equal(lbl.get("acme/web#208"), "acme/web#208: Waiting unknown, Size 350 lines, move unknown");
  assert.equal(SC.shortText(F("waiting"), { k: "v", v: 4.7, lower: true }), "≥4d");
  assert.equal(SC.valueText(F("files"), { k: "v", v: 1 }), "1 file");
});

test("scatter: at most five outlier labels, by the largest rank sum on the two axes", () => {
  assert.deepEqual(SC.ranks([3, 1, 3, 2]), [3.5, 1, 3.5, 2], "ties share their average rank");
  const pt = (ref, x, y) => ({ ref, id: ref, xv: { k: "v", v: x }, yv: { k: "v", v: y } });
  const pts = [pt("a#1", 1, 1), pt("a#2", 9, 9), pt("a#3", 8, 2), pt("a#4", 2, 8), pt("a#10", 5, 5), pt("a#9", 5, 5), pt("a#5", 7, 7)];
  assert.deepEqual(refs(SC.outliers(pts)), ["a#2", "a#5", "a#3", "a#4", "a#9"], "rank sums 14, 10, 8, 8, 7 (a#9 and a#10 tie: natural order)");
  assert.equal(SC.outliers(pts, 2).length, 2);
  assert.deepEqual(refs(SC.outliers(pts.concat([pt("a#2b", 9, 9)]), 2)), ["a#2", "a#5"], "one label per point: a#2b sits on a#2");
  assert.deepEqual(SC.outliers([]), []);
  const plot = SC.partition(SC.marks(aix, aprs, F("age"), F("size"))).plot;
  assert.deepEqual(refs(SC.outliers(plot)), ["acme/api#105", "acme/web#206", "acme/api#109", "acme/web#208", "acme/api#101"]);
  assert.equal(SC.median([1, 3, 2]), 2);
  assert.equal(SC.median([4, 1, 2, 3]), 2.5);
  assert.equal(SC.median([]), null);
});

test("scatter: the keyboard walks the marks in the table's sort order", () => {
  const marks = SC.marks(aix, aprs, F("waiting"), F("size"));
  const bySize = refs(SC.order(aix, BASE, marks, "-size"));
  assert.equal(bySize[0], "acme/api#105");
  assert.equal(bySize[bySize.length - 1], "acme/web#207", "unknown sorts last, as in the table");
  const idx = marks.map((m) => m.i);
  for (const sort of ["", "-size,waiting", "author,-age", "bogus,-waiting"]) {
    assert.deepEqual(SC.order(aix, BASE, marks, sort).map((m) => m.i), T.sortRows(aix, idx, T.sortKeys(BASE, sort)), "the table's own comparator: " + sort);
  }
  assert.deepEqual(T.sortKeys(BASE, "bogus,-size,name"), [{ key: "size", desc: true }, { key: "name", desc: false }], "a stale key is dropped");
  const noTable = require(path.join(path.dirname(process.argv[2]), "live-scatter.js"))(L, null);
  assert.deepEqual(refs(noTable.order(aix, BASE, marks, "-size")).slice(0, 2), ["acme/api#101", "acme/api#102"], "without the table: by ref");
});

test("scatter: the brush, label placement and strip jitter", () => {
  const m = (id, px, py, vis) => ({ id, px, py, vis });
  const ms = [m("a", 10, 10, true), m("b", 50, 50, true), m("c", 20, 30, false), m("d", 30, 20, true)];
  assert.deepEqual(SC.inRect(ms, { x0: 40, y0: 5, x1: 5, y1: 40 }).map((x) => x.id), ["a", "d"], "either corner first; hidden marks are out");
  const box = { x0: 0, y0: 0, x1: 200, y1: 100 };
  const placed = SC.placeLabels([{ key: "p", x: 50, y: 50, w: 40, h: 12, r: 5 }, { key: "q", x: 52, y: 50, w: 40, h: 12, r: 5 }, { key: "r", x: 198, y: 2, w: 40, h: 12, r: 5 }], box);
  assert.deepEqual(placed.map((p) => p.key), ["p", "q", "r"]);
  assert.ok(placed[0].x > 50 && placed[1].x + placed[1].w < 52, "the second label goes to the left");
  assert.ok(placed[2].x + placed[2].w <= 200 && placed[2].y >= 0, "a label at the edge stays inside");
  assert.equal(SC.placeLabels([{ key: "z", x: 5, y: 5, w: 300, h: 12, r: 5 }], box).length, 0, "a label that fits nowhere is left to the tooltip");
  const j = SC.jitter("pr:acme/api#101");
  assert.ok(j >= 0 && j <= 1 && j === SC.jitter("pr:acme/api#101"));
});

test("CI grid: columns by failing checks then number; rows by failures then name", () => {
  const grids = CG.grids(aix, aprs, AFX.check_stats);
  assert.deepEqual(grids.map((g) => g.repo), ["acme/api", "acme/infra", "acme/web"]);
  const api = grids[0];
  assert.deepEqual(api.columns.map((c) => c.number), [103, 101, 102, 104, 106, 105, 107, 108, 109]);
  assert.deepEqual(api.rows.map((r) => r.name + ":" + r.failures), ["vendor-hsm-tests:5", "unit-tests:1", "build:0", "codecov:0", "lint:0"]);
  assert.deepEqual(grids[2].columns.map((c) => c.number), [203, 201, 202, 204, 205, 206, 208], "#207's checks were not read");
  assert.deepEqual(grids[2].unread.map((i) => aix.ids[i]), ["pr:acme/web#207"]);
  assert.ok(CG.compareColumns({ failures: 2, number: 9 }, { failures: 1, number: 1 }) < 0 && CG.compareColumns({ failures: 1, number: 9, label: "", id: "" }, { failures: 1, number: 10, label: "", id: "" }) < 0);
  assert.ok(CG.compareRows({ failures: 0, name: "e2e" }, { failures: 0, name: "lint" }) < 0);
});

test("CI grid: the systemic check is a labelled solid row in acme/api, not in acme/web; its PRs have no isolated failing cells", () => {
  const grids = CG.grids(aix, aprs, AFX.check_stats), api = grids[0], web = grids[2];
  const sys = api.rows.filter((r) => r.systemic);
  assert.deepEqual(sys.map((r) => r.name), ["vendor-hsm-tests"]);
  assert.equal(CG.systemicText(sys[0].systemic), "fails everywhere — 5 of 6 PRs");
  assert.deepEqual(api.columns.map((c) => CG.cell(c, "vendor-hsm-tests")), ["failure", "failure", "failure", "failure", "failure", "success", "success", "absent", "absent"], "a solid run of failures from the left");
  assert.ok(!web.rows.some((r) => r.systemic), "fails on 1 of 3 in acme/web: not systemic there");
  for (const c of api.columns) {
    const ci = aix.nodes[c.i].pr.facts.ci;
    if (ci === "systemic_only") {
      assert.equal(c.failures, 1);
      assert.deepEqual(CG.isolated(api, c), [], c.label + " fails only the systemic check");
    }
  }
  assert.deepEqual(CG.isolated(api, api.columns[0]), ["unit-tests"], "acme/api#103's own failure");
  assert.deepEqual(CG.isolated(web, web.columns[0]), ["vendor-hsm-tests"], "in acme/web the same check is acme/web#203's own failure");
  assert.match(CG.cellLabel(api.columns[1], sys[0], "failure"), /^acme\/api#101, vendor-hsm-tests: failing \(fails everywhere — 5 of 6 PRs\)$/);
  assert.match(CG.cellLabel(api.columns[0], api.rows[1], "failure"), /this PR's own failure/);
});

test("CI grid: cell states, truncated check lists and unread checks", () => {
  const grids = CG.grids(aix, aprs, AFX.check_stats), api = grids[0], web = grids[2];
  const col = (g, n) => g.columns.find((c) => c.number === n);
  assert.equal(CG.cell(col(web, 201), "docs-preview"), "skipped");
  assert.equal(CG.cell(col(api, 105), "codecov"), "neutral");
  assert.equal(CG.cell(col(api, 103), "lint"), "pending");
  assert.equal(CG.cell(col(api, 101), "codecov"), "absent", "did not run");
  assert.equal(col(web, 202).truncated, true);
  assert.equal(CG.cell(col(web, 202), "docs-preview"), "unknown", "missing from a truncated list: unknown, not absent");
  assert.equal(CG.checkStates({}), null, "checks not read");
  const dup = CG.checkStates({ check_details: [{ name: "a", state: "success" }, { name: "a", state: "FAILURE" }, { name: "b", state: "weird" }, { state: "failure" }] });
  assert.deepEqual(Array.from(dup.entries()).map(([k, v]) => k + ":" + v.state), ["a:failure", "b:pending"], "the worst state of a repeated name; an unknown state is in flight");
  for (const st of Object.keys(CG.CELL)) assert.ok(CG.CELL[st].text, st + " has words for its aria-label");
  assert.ok(["failure", "success", "pending", "unknown"].every((st) => CG.CELL[st].glyph), "failing, passing, pending and unknown have glyphs, never colour alone");
});

test("CI grid: past 60 PRs in a repository, the 60 with the most failures and n more", () => {
  const nodes = [];
  for (let k = 1; k <= 65; k++) nodes.push({ id: "pr:acme/api#" + k, kind: "pr", label: "acme/api#" + k, pr: { number: k, repo: "acme/api", state: "open", check_details: [{ name: "unit", state: k % 13 === 0 ? "failure" : "success" }] } });
  const ix = L.queryIndex({ nodes, edges: [] }, BASE), all = nodes.map((nd, i) => i);
  const g = CG.grids(ix, all, {})[0];
  assert.equal(g.columns.length, 60);
  assert.equal(g.more, 5);
  assert.equal(g.total, 65);
  assert.deepEqual(g.columns.slice(0, 5).map((c) => c.number), [13, 26, 39, 52, 65], "the failing PRs first");
  assert.deepEqual(g.columns.slice(-1).map((c) => c.number), [59], "then by number, cut at 60");
  assert.equal(CG.grids(ix, all, {}, 10)[0].more, 55);
});

test("detail panel: check= names a check of the selected PR", () => {
  const pr = (id) => aix.nodes[aix.byId.get(id)].pr;
  assert.deepEqual(L.checkDetail(pr("pr:acme/api#101"), "vendor-hsm-tests"), { text: "failure", url: "https://github.com/acme/api/actions/runs/7100004" });
  assert.equal(L.checkDetail(pr("pr:acme/api#101"), ""), null);
  assert.equal(L.checkDetail(pr("pr:acme/api#101"), "codecov").text, "did not run on this PR's head");
  assert.match(L.checkDetail(pr("pr:acme/web#202"), "docs-preview").text, /^unknown: not among the checks read/);
  assert.match(L.checkDetail(pr("pr:acme/web#207"), "build").text, /^unknown: this PR's checks were not read/);
  assert.equal(L.checkDetail({ check_details: [{ name: "x", state: "failure", url: "javascript:alert(1)" }] }, "x").url, "", "only http(s) links");
  const raw = L.checkValue("pr:acme/api#101", "lint|strict");
  assert.deepEqual(L.parseCheck(raw), { pr: "pr:acme/api#101", name: "lint|strict" }, "the PR ID, then the name, which may hold |");
  for (const bad of ["", "vendor-hsm-tests", "|x", "pr:a#1|"]) assert.equal(L.parseCheck(bad), null, JSON.stringify(bad));
  assert.equal(L.checkShown(raw, "pr:acme/api#101", "ci"), "lint|strict");
  assert.equal(L.checkShown(raw, "pr:acme/web#208", "ci"), "", "never for another PR");
  assert.equal(L.checkShown(raw, "pr:acme/api#101", "scatter"), "", "only in the CI grid");
  assert.equal(L.checkShown("vendor-hsm-tests", "pr:acme/api#101", "ci"), "", "a bare name (an old link) names no PR");
});

{
  // The analysis fixture's PRs forty times over (about 800 marks, as the
  // scaled fixture), for the logged timing of the two views' pure parts.
  const nodes = [], copies = 40;
  for (let k = 0; k < copies; k++) AFX.nodes.forEach((nd) => { if (nd.kind === "pr") nodes.push(Object.assign({}, nd, { id: nd.id + "~" + k })); });
  const big = Object.assign({}, AFX, { nodes, edges: [] }), bix = L.queryIndex(big, BASE), all = nodes.map((nd, i) => i);
  const time = (fn) => { for (let i = 0; i < 3; i++) fn(); const t0 = process.hrtime.bigint(); for (let i = 0; i < 10; i++) fn(); return Number(process.hrtime.bigint() - t0) / 1e6 / 10; };
  const sc = time(() => { const ms = SC.marks(bix, all, F("waiting"), F("size")); SC.order(bix, BASE, ms, "-size"); SC.outliers(SC.partition(ms).plot); });
  const cg = time(() => CG.grids(bix, all, big.check_stats));
  perfLine += "; gh-103 on " + nodes.length + " PRs: scatter marks, order and outliers " + sc.toFixed(2) + " ms, CI grids " + cg.toFixed(2) + " ms";
}
}
// ---------- end gh-103 ----------

console.log("ok " + n + " (" + vectorCases + " shared query vectors) " + perfLine);
