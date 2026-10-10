// Exercises the pure logic of web/live.js (window.WGOLive / module.exports)
// under node. Run by TestLiveJSLogic: node live_logic.js <path to live.js>.
// Prints "ok <n>" on success; any failure exits non-zero with a message.
"use strict";
const assert = require("node:assert/strict");
const L = require(process.argv[2]);

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

console.log("ok " + n);
