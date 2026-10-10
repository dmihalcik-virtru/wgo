/* wgo dash live explorer (live mode of the shared explorer). It shares the review
 * explorer's visual language, styles and force library, but models live work:
 * efforts, workspaces, bookmarks, PRs, tickets and agent sessions. No eval and no
 * innerHTML: all data goes through textContent. It only issues GET requests.
 *
 * The filtering, selection and sorting logic is a set of small pure functions on
 * window.WGOLive (also module.exports under Node) so it can be exercised without
 * a canvas. One store holds the selection and the filter; the efforts list, the
 * graph, the table and the small multiples all render from it. */
(function () {
  "use strict";

  // ---------- pure logic ----------
  var L = {};
  var PR_STATES = ["open", "draft", "merged", "closed", "unknown", "none"];
  L.PR_STATES = PR_STATES;
  var KIND_ORDER = { effort: 0, workspace: 1, bookmark: 2, pr: 3, ticket: 4, agent: 5 };
  L.KIND_ORDER = KIND_ORDER;

  function push(m, k, v) { var l = m.get(k); if (!l) m.set(k, l = []); l.push(v); }

  // index builds lookup maps over a snapshot: nodes by ID and edges both ways.
  L.index = function (snap) {
    var byId = new Map(), out = new Map(), inc = new Map();
    ((snap && snap.nodes) || []).forEach(function (n) { byId.set(n.id, n); });
    ((snap && snap.edges) || []).forEach(function (e) {
      if (!byId.has(e.source) || !byId.has(e.target)) return;
      push(out, e.source, { o: e.target, kind: e.kind });
      push(inc, e.target, { o: e.source, kind: e.kind });
    });
    return { byId: byId, out: out, inc: inc };
  };

  // reach returns every node reachable from seeds along adj (seeds included).
  L.reach = function (adj, seeds) {
    var seen = new Set(seeds), queue = Array.from(seeds);
    while (queue.length) {
      var id = queue.shift();
      (adj.get(id) || []).forEach(function (a) { if (!seen.has(a.o)) { seen.add(a.o); queue.push(a.o); } });
    }
    return seen;
  };

  // closure is a filter's visible set: the seeds, what they lead to, and what
  // leads to them (so a PR keeps its bookmark, workspace and effort).
  L.closure = function (idx, seeds) {
    var s = new Set();
    L.reach(idx.out, seeds).forEach(function (id) { s.add(id); });
    L.reach(idx.inc, seeds).forEach(function (id) { s.add(id); });
    return s;
  };

  L.prBucket = function (pr) {
    if (!pr) return "unknown";
    if (pr.state === "open" && pr.is_draft) return "draft";
    return pr.state || "unknown";
  };

  // bookmarkBucket is the PR-state bucket of a bookmark that has no PR node:
  // "unknown" when the lookup has no data, "none" only when it is known empty.
  L.bookmarkBucket = function (bm) {
    if (!bm || bm.pr_lookup === "unknown" || bm.pr_lookup === "error" || !bm.pr_lookup) return "unknown";
    return bm.pr_count > 0 ? "" : "none";
  };

  // seeds lists the nodes a small-multiple filter selects.
  L.seeds = function (snap, idx, f) {
    if (!f) return [];
    var out = [], counts = (snap && snap.counts) || {};
    if (f.by === "day") {
      var i = (counts.days || []).indexOf(f.value), act = counts.activity || {};
      Object.keys(act).forEach(function (e) { if (i >= 0 && (act[e][i] || 0) > 0 && idx.byId.has(e)) out.push(e); });
    } else if (f.by === "prstate") {
      idx.byId.forEach(function (n) {
        if (n.kind === "pr" && L.prBucket(n.pr) === f.value) out.push(n.id);
        else if (n.kind === "bookmark" && L.bookmarkBucket(n.bookmark) === f.value) out.push(n.id);
      });
    } else if (f.by === "effort") {
      if (idx.byId.has(f.value)) out.push(f.value);
    }
    return out.sort();
  };

  // visible is the set of node IDs a filter keeps, or null for no filter.
  L.visible = function (snap, idx, f) {
    if (!f) return null;
    return L.closure(idx, L.seeds(snap, idx, f));
  };

  L.sameFilter = function (a, b) { return !!a && !!b && a.by === b.by && a.value === b.value; };
  // toggleFilter clears the filter when the same bar is chosen again.
  L.toggleFilter = function (cur, next) { return L.sameFilter(cur, next) ? null : next; };

  // effortsOf returns the effort IDs a node belongs to, walking edges backwards.
  L.effortsOf = function (idx, id) {
    var n = idx.byId.get(id);
    if (!n) return [];
    if (n.kind === "effort") return [id];
    var out = [];
    L.reach(idx.inc, [id]).forEach(function (o) { var m = idx.byId.get(o); if (m && m.kind === "effort") out.push(o); });
    return out.sort();
  };

  // selectionEfforts is the set of efforts touched by a selection.
  L.selectionEfforts = function (idx, sel) {
    var s = new Set();
    sel.forEach(function (id) { L.effortsOf(idx, id).forEach(function (e) { s.add(e); }); });
    return s;
  };

  L.dayTotals = function (counts, efforts) {
    var days = (counts && counts.days) || [], act = (counts && counts.activity) || {};
    var t = days.map(function () { return 0; });
    Object.keys(act).forEach(function (e) {
      if (efforts && !efforts.has(e)) return;
      (act[e] || []).forEach(function (v, i) { if (i < t.length) t[i] += v || 0; });
    });
    return t;
  };

  L.prTotals = function (counts, efforts) {
    var ps = (counts && counts.pr_states) || {}, t = {}, order = PR_STATES.slice();
    order.forEach(function (s) { t[s] = 0; });
    Object.keys(ps).forEach(function (e) {
      if (efforts && !efforts.has(e)) return;
      Object.keys(ps[e] || {}).forEach(function (s) {
        if (!(s in t)) { t[s] = 0; order.push(s); }
        t[s] += ps[e][s] || 0;
      });
    });
    return order.map(function (s) { return { state: s, n: t[s] }; });
  };

  L.agentTotals = function (counts, byId) {
    var ag = (counts && counts.agents) || {}, out = [];
    Object.keys(ag).forEach(function (e) {
      var by = ag[e] || {}, total = 0;
      Object.keys(by).forEach(function (k) { total += by[k] || 0; });
      if (total > 0) out.push({ effort: e, label: byId && byId.get(e) ? byId.get(e).label : e, total: total, by: by });
    });
    return out.sort(function (a, b) { return b.total - a.total || (a.label < b.label ? -1 : a.label > b.label ? 1 : 0); });
  };

  var FRESH = {
    fresh: { label: "fresh", glyph: "✓" },
    stale: { label: "stale", glyph: "~" },
    unknown: { label: "unknown", glyph: "?" },
    error: { label: "unavailable", glyph: "!" }
  };
  // fresh describes a freshness value; anything unrecognised is unknown, never "none".
  L.fresh = function (f) { var d = FRESH[f] || FRESH.unknown; return { key: FRESH[f] ? f : "unknown", label: d.label, glyph: d.glyph }; };

  L.ciText = function (pr) { return pr && pr.checks ? "CI " + String(pr.checks).toLowerCase() : "CI not reported"; };
  L.reviewText = function (pr) {
    if (!pr || !pr.review_decision) return "no review decision";
    return String(pr.review_decision).toLowerCase().replace(/_/g, " ");
  };
  L.bookmarkPRText = function (bm) {
    if (!bm) return "";
    var f = L.fresh(bm.pr_lookup);
    if (f.key === "unknown") return "PR lookup unknown (not cached yet)";
    if (f.key === "error") return "PR lookup unavailable" + (bm.pr_error ? ": " + bm.pr_error : "");
    if (bm.pr_count === 0) return "no PR" + (f.key === "stale" ? " (stale)" : "");
    return bm.pr_count + " PR" + (bm.pr_count === 1 ? "" : "s") + (f.key === "stale" ? " (stale)" : "");
  };
  L.livenessText = function (l) {
    if (l === "uncertain") return "uncertain (process not confirmed)";
    return l || "unknown liveness";
  };

  // stateText is the State column of the table.
  L.stateText = function (n) {
    switch (n.kind) {
      case "effort": return n.effort ? ({ effort: "named effort", ticket: "ticket fallback", theme: "agent theme", ungrouped: "ungrouped work" })[n.effort.group] || n.effort.group || "" : "";
      case "workspace": {
        var w = n.workspace || {};
        if (w.error) return "unavailable: " + w.error;
        var s = w.bookmark ? "on " + w.bookmark : "no bookmark";
        if (w.stack) s += " (" + w.stack.position + "/" + w.stack.size + " in stack)";
        return s;
      }
      case "bookmark": return L.bookmarkPRText(n.bookmark);
      case "pr": return L.prBucket(n.pr) + " · " + L.ciText(n.pr) + " · " + L.reviewText(n.pr);
      case "ticket": return n.ticket && n.ticket.status ? n.ticket.status : "status unknown";
      case "agent": {
        var a = n.agent || {};
        return (a.status || "status unknown") + " · " + L.livenessText(a.liveness) + (a.conflict ? " · same-bookmark conflict" : "");
      }
    }
    return "";
  };
  // dataText is the Data (freshness) column.
  L.dataText = function (n) {
    switch (n.kind) {
      case "workspace": return n.workspace && n.workspace.error ? "unavailable" : "local";
      case "bookmark": return L.fresh(n.bookmark && n.bookmark.pr_lookup).label;
      case "pr": return L.fresh(n.pr && n.pr.freshness).label;
      case "ticket": return L.fresh(n.ticket && n.ticket.freshness).label;
      case "agent": return "local";
      case "effort": return "local";
    }
    return "";
  };
  L.activityOf = function (n) {
    return (n.workspace && n.workspace.last_activity) || (n.agent && n.agent.last_activity) || (n.pr && n.pr.updated_at) || "";
  };

  // rows turns a snapshot into table rows.
  L.rows = function (snap, idx) {
    var rows = [];
    idx.byId.forEach(function (n) {
      var effs = L.effortsOf(idx, n.id).map(function (e) { var m = idx.byId.get(e); return m ? m.label : e; });
      rows.push({ id: n.id, label: n.label || n.id, kind: n.kind, effort: effs.join(", "), state: L.stateText(n), data: L.dataText(n), activity: L.activityOf(n) });
    });
    return rows;
  };

  function cmp(a, b) { return a < b ? -1 : a > b ? 1 : 0; }
  // sortRows sorts rows by key (dir 1 ascending, -1 descending), stably, with
  // label then ID as tie-breakers. It returns a new array.
  L.sortRows = function (rows, key, dir) {
    function val(r) {
      if (key === "kind") return KIND_ORDER[r.kind] != null ? KIND_ORDER[r.kind] : 99;
      var v = r[key];
      return typeof v === "string" ? v.toLowerCase() : v == null ? "" : v;
    }
    return rows.map(function (r, i) { return { r: r, i: i }; }).sort(function (a, b) {
      return cmp(val(a.r), val(b.r)) * dir || cmp(a.r.label.toLowerCase(), b.r.label.toLowerCase()) || cmp(a.r.id, b.r.id) || a.i - b.i;
    }).map(function (x) { return x.r; });
  };

  // filterRows keeps the rows a visible set allows (all rows for null).
  L.filterRows = function (rows, vis) { return vis ? rows.filter(function (r) { return vis.has(r.id); }) : rows.slice(); };

  // nextIndex maps a navigation key to the next row index, or -1.
  L.nextIndex = function (len, cur, key) {
    if (!len) return -1;
    switch (key) {
      case "ArrowDown": return cur < 0 ? 0 : Math.min(cur + 1, len - 1);
      case "ArrowUp": return cur < 0 ? 0 : Math.max(cur - 1, 0);
      case "Home": return 0;
      case "End": return len - 1;
      case "PageDown": return Math.min((cur < 0 ? 0 : cur) + 10, len - 1);
      case "PageUp": return Math.max((cur < 0 ? 0 : cur) - 10, 0);
    }
    return -1;
  };

  // select applies a click: replace the selection, or toggle id when adding.
  L.select = function (sel, ids, add) {
    var s = new Set(add ? sel : []);
    ids.forEach(function (id) { if (add && s.has(id)) s.delete(id); else s.add(id); });
    return s;
  };
  // prune drops selected IDs that are no longer in the snapshot.
  L.prune = function (sel, byId) { var s = new Set(); sel.forEach(function (id) { if (byId.has(id)) s.add(id); }); return s; };

  L.search = function (rows, q) {
    q = (q || "").trim().toLowerCase();
    if (!q) return [];
    return rows.filter(function (r) { return r.label.toLowerCase().indexOf(q) >= 0 || r.id.toLowerCase().indexOf(q) >= 0; }).map(function (r) { return r.id; });
  };

  L.ageText = function (sec) {
    if (sec == null || !isFinite(sec)) return "unknown";
    sec = Math.max(0, Math.round(sec));
    if (sec < 90) return sec + "s";
    if (sec < 5400) return Math.round(sec / 60) + "m";
    if (sec < 172800) return Math.round(sec / 3600) + "h";
    return Math.round(sec / 86400) + "d";
  };
  L.relTime = function (iso, nowMs) {
    if (!iso) return "";
    var t = Date.parse(iso);
    if (!isFinite(t)) return "";
    return L.ageText((nowMs - t) / 1000) + " ago";
  };

  L.reviewLink = function (links, id) {
    if (!links) return null;
    return (links.efforts && links.efforts[id]) || (links.entities && links.entities[id]) || null;
  };

  // effortOrder sorts efforts: named efforts, then ticket groups, themes, Ungrouped last.
  L.effortOrder = function (a, b) {
    var g = { effort: 0, ticket: 1, theme: 2, ungrouped: 3 };
    var ga = g[(a.effort || {}).group] != null ? g[a.effort.group] : 2, gb = g[(b.effort || {}).group] != null ? g[b.effort.group] : 2;
    return ga - gb || cmp(a.label.toLowerCase(), b.label.toLowerCase()) || cmp(a.id, b.id);
  };

  // childrenOf lists the targets of id's outgoing edges of one kind.
  L.childrenOf = function (idx, id, kind) {
    return (idx.out.get(id) || []).filter(function (a) { return !kind || a.kind === kind; }).map(function (a) { return idx.byId.get(a.o); }).filter(Boolean);
  };

  // fetchFailure names why a GET failed: an HTTP error status, a body that is
  // not JSON, or (anything else) a network failure.
  L.fetchFailure = function (e) {
    if (e && e.status) return "wgo dash returned HTTP " + e.status;
    if (e && e.unreadable) return "wgo dash returned an unreadable response";
    return "Cannot reach wgo dash";
  };
  // pollErrorText is the banner for a failed snapshot poll. The last snapshot
  // stays on screen, and the header keeps counting its age.
  L.pollErrorText = function (e, haveSnapshot) {
    return L.fetchFailure(e) + (haveSnapshot ? "; still showing the last snapshot (its age is in the header)." : "; no snapshot has loaded yet.") + " Retrying…";
  };
  // warnings lists the diagnostics shown under the header: the view's, the
  // snapshot's, review-run scan errors, and an unavailable links note.
  L.warnings = function (payload, snap, links, linksError) {
    var out = [].concat((payload && payload.diagnostics) || [], (snap && snap.diagnostics) || []);
    ((links && links.errors) || []).forEach(function (e) { out.push(e); });
    if (linksError) out.push("Review links unavailable (" + linksError + "); links to year-in-review runs may be missing or out of date.");
    return out;
  };

  // actionButtons lists the workspace action buttons the page offers: none
  // without a page token (a read-only server), and Resume only when wgo dash
  // has an allowlisted resume tool configured.
  L.actionButtons = function (boot) {
    if (!boot || !boot.token || !boot.action_api) return [];
    var out = [{ kind: "terminal", label: "Open tab", title: "Open a terminal tab in this workspace" }];
    if (boot.resume) out.push({ kind: "resume", label: "Resume " + boot.resume, title: "Open a terminal tab here and resume the last " + boot.resume + " session" });
    out.push({ kind: "editor", label: "Editor", title: "Open this workspace in your editor" });
    out.push({ kind: "reveal", label: "Finder", title: "Reveal this workspace in Finder" });
    out.push({ kind: "plan", label: "Plan", title: "Open this bookmark's entry in ~/.wgo/plan.md" });
    out.push({ kind: "spec", label: "Spec", title: "Open spec/<ticket>.md for this bookmark's ticket" });
    return out;
  };
  // actionResult turns an action response into what the page shows: ok is
  // false when nothing was launched (only something to copy), text is the
  // launcher's account, copy is the command or path to copy, and tried
  // names the methods that failed first.
  L.actionResult = function (res) {
    res = res || {};
    var ok = !!res.launched;
    var text = res.message || (ok ? "Opened with " + (res.method || "the launcher") + "." : "Nothing could be launched.");
    var fb = res.fallbacks || [];
    return { ok: ok, text: text, copy: res.copy || "", tried: fb.length ? "Tried first: " + fb.join("; ") : "" };
  };
  // actionError names why an action or Mark seen request failed: the
  // server's own explanation when it sent one, otherwise the cause.
  L.actionError = function (e) {
    if (e && e.detail) return e.detail + (e.status ? " (HTTP " + e.status + ")" : "");
    if (e && e.status === 403) return "wgo dash refused the request (HTTP 403); reload the page, since each wgo dash launch issues a new token";
    return L.fetchFailure(e);
  };
  // ackText reports a Mark seen outcome.
  L.ackText = function (res) {
    return "Marked generation " + ((res && res.generation) || "?") + " seen; changes are now counted from here (in every open tab).";
  };

  // localURL returns u when it is a same-origin path, else "". It rejects
  // protocol-relative "//host", "/\\host" (browsers treat \\ as /), any
  // backslash, and whitespace or control characters, which browsers strip
  // from URLs (so "/\t/host" would become "//host").
  L.localURL = function (u) {
    return typeof u === "string" && u.charAt(0) === "/" && u.charAt(1) !== "/" && !/[\\\u0000-\u0020\u007f]/.test(u) ? u : "";
  };

  if (typeof window !== "undefined") window.WGOLive = L;
  if (typeof module !== "undefined" && module.exports) module.exports = L;
  if (typeof document === "undefined") return;

  // ---------- page ----------
  var root = document.getElementById("app");
  var bootEl = document.getElementById("wgo-boot");
  if (!root) return;
  var BOOT = {};
  try { BOOT = bootEl ? JSON.parse(bootEl.textContent) : {}; } catch (e) { BOOT = {}; }
  if (BOOT.mode && BOOT.mode !== "live") return;
  var d3 = window.d3force;
  var reduceMotion = window.matchMedia && matchMedia("(prefers-reduced-motion: reduce)").matches;
  var API = BOOT.api || "/api/snapshot", LINKS_API = BOOT.links || "", POLL = Math.max(1000, BOOT.poll_ms || 5000);

  function el(tag, cls, text, parent) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    if (parent) parent.appendChild(e);
    return e;
  }
  var SVGNS = "http://www.w3.org/2000/svg";
  function svgEl(tag, attrs, parent) {
    var e = document.createElementNS(SVGNS, tag);
    for (var k in attrs) e.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(e);
    return e;
  }
  function safeURL(u) { return typeof u === "string" && /^https?:\/\//i.test(u) ? u : ""; }
  var localURL = L.localURL;
  function link(parent, text, url) {
    var u = safeURL(url);
    if (!u) { parent.appendChild(document.createTextNode(text)); return null; }
    var a = el("a", null, text, parent);
    a.href = u; a.target = "_blank"; a.rel = "noopener noreferrer";
    return a;
  }
  function button(parent, text, cls, onclick, label) {
    var b = el("button", cls, text, parent); b.type = "button";
    if (label) b.setAttribute("aria-label", label);
    if (onclick) b.addEventListener("click", onclick);
    return b;
  }
  function clamp(v, lo, hi) { return Math.max(lo, Math.min(hi, v)); }
  function plural(n, s) { return n.toLocaleString() + " " + s + (n === 1 ? "" : "s"); }
  function fmtTime(iso) { var t = Date.parse(iso); return isFinite(t) && iso ? new Date(t).toLocaleString() : "unknown"; }

  // ---------- store ----------
  var store = {
    payload: null, snap: null, idx: L.index(null), rows: [], links: null, linksError: "",
    selection: new Set(), filter: null, vis: null, hover: null,
    view: "graph", fetchedAt: 0, error: "", focusMissing: "",
    busy: new Set(), actionResult: null, ackResult: null,
    subs: [],
    subscribe: function (fn) { this.subs.push(fn); },
    notify: function (reason) { for (var i = 0; i < this.subs.length; i++) this.subs[i](reason); },
    select: function (ids, add) { this.selection = L.select(this.selection, ids, add); this.notify("selection"); },
    clear: function () { if (this.selection.size) { this.selection = new Set(); this.notify("selection"); } },
    setFilter: function (f) { this.filter = f; this.vis = L.visible(this.snap, this.idx, f); this.notify("filter"); },
    isVisible: function (id) { return !this.vis || this.vis.has(id); }
  };

  // ---------- skeleton ----------
  root.textContent = "";
  var hdr = el("div", "hdr", null, root);
  var h1 = el("h1", null, "wgo dash", hdr); el("span", "mode", "live work", h1);
  var countsEl = el("div", "counts", "Loading the snapshot…", hdr);
  var chipsEl = el("div", "chips", null, hdr); chipsEl.setAttribute("aria-label", "Data freshness by source");
  var bannerEl = el("div", "banner", null, hdr); bannerEl.hidden = true; bannerEl.setAttribute("role", "status");
  var warnsEl = el("ul", "warns", null, hdr); warnsEl.hidden = true; warnsEl.setAttribute("aria-label", "Warnings");
  var sinceEl = el("div", "since", null, hdr); sinceEl.setAttribute("aria-live", "polite");

  var bar = el("div", "bar", null, root);
  var search = el("input", null, null, bar);
  search.type = "search"; search.placeholder = "Search (press /)"; search.setAttribute("aria-label", "Search efforts, workspaces, PRs, tickets and agents");
  var tabs = el("div", "view-tabs", null, bar); tabs.setAttribute("role", "group"); tabs.setAttribute("aria-label", "View");
  var graphBtn = button(tabs, "Graph", null, function () { setView("graph"); });
  var tableBtn = button(tabs, "Table", null, function () { setView("table"); });
  var filterChip = button(bar, "", "filterchip", function () { store.setFilter(null); });
  filterChip.hidden = true;
  var fitBtn = button(bar, "Fit to view", null, function () { robustFit(); });
  var statusEl = el("span", "status", "", bar); statusEl.setAttribute("role", "status"); statusEl.setAttribute("aria-live", "polite");

  var mults = el("div", "multiples", null, root);
  var multDays = el("div", "mult", null, mults), multPR = el("div", "mult", null, mults), multAg = el("div", "mult", null, mults);

  var main = el("div", "lv-main", null, root);
  var effortsEl = el("div", "efforts", null, main); effortsEl.setAttribute("aria-label", "Efforts");
  var stage = el("div", "stage", null, main);
  var canvas = el("canvas", null, null, stage);
  canvas.tabIndex = 0; canvas.setAttribute("role", "img");
  canvas.setAttribute("aria-label", "Graph of efforts, workspaces, PRs, tickets and agents. The Table view lists the same nodes.");
  var tip = el("div", "tip", null, stage); tip.hidden = true;
  var tableWrap = el("div", "tablewrap", null, stage); tableWrap.hidden = true;
  var loadingEl = el("div", "loading", "Loading the snapshot… the page fills in as soon as wgo dash has one.", stage);
  var side = el("div", "side", null, main);
  var detailBox = el("div", "detail", null, side);
  var legendBox = el("div", null, null, side);
  var ctx = canvas.getContext("2d");

  function setView(v) {
    store.view = v;
    graphBtn.setAttribute("aria-pressed", String(v === "graph"));
    tableBtn.setAttribute("aria-pressed", String(v === "table"));
    store.notify("view");
  }

  // ---------- theme / encoding ----------
  var theme = {};
  function readTheme() {
    var cs = getComputedStyle(document.documentElement);
    ["--surface", "--ink", "--ink2", "--muted", "--grid", "--accent", "--k-workspace", "--k-pr", "--k-ticket", "--k-effort", "--k-bookmark", "--k-agent",
      "--s-good", "--s-warn", "--s-serious", "--s-critical"].forEach(function (k) { theme[k] = cs.getPropertyValue(k).trim() || "#888"; });
  }
  readTheme();
  var KINDS = [
    { k: "effort", label: "Effort" }, { k: "workspace", label: "Workspace" }, { k: "bookmark", label: "Bookmark" },
    { k: "pr", label: "Pull request" }, { k: "ticket", label: "Ticket / issue" }, { k: "agent", label: "Agent session (a tool)" }
  ];
  function kindColor(k) { return theme["--k-" + k] || theme["--muted"]; }
  var PR_RING = { merged: { cv: "--s-good", dash: [] }, closed: { cv: "--s-critical", dash: [2, 2] }, draft: { cv: "--muted", dash: [4, 2.5] }, open: { cv: "--muted", dash: [] } };
  function nodeFresh(d) {
    if (d.kind === "pr") return d.pr && d.pr.freshness;
    if (d.kind === "ticket") return d.ticket && d.ticket.freshness;
    if (d.kind === "bookmark") return d.bookmark && d.bookmark.pr_lookup;
    if (d.kind === "workspace") return d.workspace && d.workspace.error ? "error" : "fresh";
    return "fresh";
  }
  function radius(d) { return { effort: 11, workspace: 7, bookmark: 4, pr: 6, ticket: 6, agent: 7 }[d.kind] || 5; }
  function pathShape(c, kind, x, y, r) {
    var i, a;
    switch (kind) {
      case "pr": { var s = r * 0.9; c.rect(x - s, y - s, 2 * s, 2 * s); break; }
      case "ticket": { var t = r * 1.25; c.moveTo(x, y - t); c.lineTo(x + t, y); c.lineTo(x, y + t); c.lineTo(x - t, y); c.closePath(); break; }
      case "agent": { var q = r * 1.2; c.moveTo(x, y - q); c.lineTo(x + q * 0.95, y + q * 0.7); c.lineTo(x - q * 0.95, y + q * 0.7); c.closePath(); break; }
      case "effort": for (i = 0; i < 6; i++) { a = Math.PI / 3 * i + Math.PI / 6; if (i) c.lineTo(x + r * Math.cos(a), y + r * Math.sin(a)); else c.moveTo(x + r * Math.cos(a), y + r * Math.sin(a)); } c.closePath(); break;
      default: c.moveTo(x + r, y); c.arc(x, y, r, 0, 6.2832);
    }
  }
  function svgShape(kind, x, y, r) {
    // Mirror pathShape into SVG path data for the legend.
    var p = [], i, a;
    switch (kind) {
      case "pr": { var s = r * 0.9; return "M" + (x - s) + " " + (y - s) + "h" + 2 * s + "v" + 2 * s + "h" + (-2 * s) + "z"; }
      case "ticket": { var t = r * 1.25; return "M" + x + " " + (y - t) + "L" + (x + t) + " " + y + "L" + x + " " + (y + t) + "L" + (x - t) + " " + y + "z"; }
      case "agent": { var q = r * 1.2; return "M" + x + " " + (y - q) + "L" + (x + q * 0.95) + " " + (y + q * 0.7) + "L" + (x - q * 0.95) + " " + (y + q * 0.7) + "z"; }
      case "effort": for (i = 0; i < 6; i++) { a = Math.PI / 3 * i + Math.PI / 6; p.push((x + r * Math.cos(a)) + " " + (y + r * Math.sin(a))); } return "M" + p.join("L") + "z";
      default: return "M" + (x + r) + " " + y + "A" + r + " " + r + " 0 1 1 " + (x - r) + " " + y + "A" + r + " " + r + " 0 1 1 " + (x + r) + " " + y;
    }
  }

  // ---------- legend ----------
  function buildLegend() {
    legendBox.textContent = "";
    el("h2", null, "Legend", legendBox);
    var ul = el("ul", "legend", null, legendBox);
    var counts = {};
    store.idx.byId.forEach(function (n) { counts[n.kind] = (counts[n.kind] || 0) + 1; });
    KINDS.forEach(function (K) {
      var li = el("li", null, null, ul), s = svgEl("svg", { width: 22, height: 18, viewBox: "0 0 22 18", "aria-hidden": "true" }, li);
      svgEl("path", { d: svgShape(K.k, 11, 9, K.k === "bookmark" ? 4 : 6), fill: kindColor(K.k) }, s);
      el("span", null, K.label + " (" + (counts[K.k] || 0).toLocaleString() + ")", li);
    });
    el("h2", null, "Data state", legendBox);
    ul = el("ul", "legend", null, legendBox);
    [["fresh", "Filled: fresh data", null], ["stale", "Dashed outline: stale (last known value)", [3, 2]], ["unknown", "Hollow with ?: unknown (not cached; never \"none\")", null], ["error", "Dotted red outline: unavailable", [1, 2]]].forEach(function (r) {
      var li = el("li", null, null, ul), s = svgEl("svg", { width: 22, height: 18, viewBox: "0 0 22 18", "aria-hidden": "true" }, li);
      var hollow = r[0] === "unknown";
      svgEl("path", { d: svgShape("pr", 11, 9, 5.5), fill: hollow ? theme["--surface"] : kindColor("pr"), stroke: r[0] === "error" ? theme["--s-critical"] : theme["--ink"], "stroke-width": r[0] === "fresh" ? 0 : 1.3, "stroke-dasharray": r[2] ? r[2].join(" ") : "none" }, s);
      if (hollow) { var tx = svgEl("text", { x: 11, y: 12.5, "text-anchor": "middle", "font-size": 9, fill: theme["--ink"] }, s); tx.textContent = "?"; }
      el("span", null, r[1], li);
    });
    el("h2", null, "PR state (ring)", legendBox);
    ul = el("ul", "legend", null, legendBox);
    ["open", "draft", "merged", "closed"].forEach(function (k) {
      var o = PR_RING[k], li = el("li", null, null, ul), s = svgEl("svg", { width: 22, height: 18, viewBox: "0 0 22 18", "aria-hidden": "true" }, li);
      svgEl("path", { d: svgShape("pr", 11, 9, 4.5), fill: kindColor("pr") }, s);
      svgEl("circle", { cx: 11, cy: 9, r: 8, fill: "none", stroke: theme[o.cv], "stroke-width": 1.6, "stroke-dasharray": o.dash.join(" ") || "none" }, s);
      el("span", null, k.charAt(0).toUpperCase() + k.slice(1), li);
    });
    el("h2", null, "Agent liveness", legendBox);
    ul = el("ul", "legend", null, legendBox);
    [["active", "Filled: active"], ["live", "Filled: live process"], ["uncertain", "Hollow, dashed: uncertain"], ["conflict", "Orange ring: same-bookmark conflict"]].forEach(function (r) {
      var li = el("li", null, null, ul), s = svgEl("svg", { width: 22, height: 18, viewBox: "0 0 22 18", "aria-hidden": "true" }, li);
      var hollow = r[0] === "uncertain";
      svgEl("path", { d: svgShape("agent", 11, 10, 5.5), fill: hollow ? theme["--surface"] : kindColor("agent"), stroke: theme["--ink"], "stroke-width": hollow ? 1.2 : 0, "stroke-dasharray": hollow ? "2 1.5" : "none" }, s);
      if (r[0] === "conflict") svgEl("circle", { cx: 11, cy: 9, r: 8.2, fill: "none", stroke: theme["--s-serious"], "stroke-width": 1.6 }, s);
      el("span", null, r[1], li);
    });
  }

  // ---------- header, freshness, since last look ----------
  var SOURCE_LABEL = { jj: "jj (local)", plan: "Plan", agents: "Agents", github_prs: "GitHub PRs", github_issues: "GitHub issues", jira: "Jira" };
  function renderHeader() {
    var p = store.payload, snap = store.snap;
    chipsEl.textContent = ""; warnsEl.textContent = "";
    var warns = L.warnings(p, snap, store.links, store.linksError);
    warns.forEach(function (w) { el("li", null, w, warnsEl); });
    warnsEl.hidden = !warns.length;
    if (!p || !snap) {
      countsEl.textContent = p && p.status === "loading" ? "Collecting the first snapshot… this page updates by itself." : "Loading the snapshot…";
      return;
    }
    var c = {};
    store.idx.byId.forEach(function (n) { c[n.kind] = (c[n.kind] || 0) + 1; });
    var age = (p.age_seconds || 0) + (Date.now() - store.fetchedAt) / 1000;
    countsEl.textContent = plural(c.effort || 0, "effort") + " · " + plural(c.workspace || 0, "workspace") + " · " + plural(c.pr || 0, "PR") + " · " +
      plural(c.ticket || 0, "ticket") + " · " + plural(c.agent || 0, "agent session") + "   |   generation " + p.generation +
      " · snapshot age " + L.ageText(age) + (p.loaded_from_disk ? " · loaded from disk, refreshing" : "");
    Object.keys(snap.sources || {}).sort().forEach(function (k) {
      var st = snap.sources[k], f = L.fresh(st.state), chip = el("span", "chip f-" + f.key, null, chipsEl);
      el("span", "glyph", f.glyph, chip).setAttribute("aria-hidden", "true");
      var parts = [];
      ["fresh", "stale", "unknown", "error"].forEach(function (s) { if (st[s]) parts.push(st[s] + " " + L.fresh(s).label); });
      el("span", null, (SOURCE_LABEL[k] || k) + ": " + f.label + (parts.length ? " (" + parts.join(", ") + ")" : ""), chip);
      var t = [];
      if (st.oldest_fetch) t.push("oldest fetch " + fmtTime(st.oldest_fetch));
      if (st.detail) t.push(st.detail);
      if (t.length) chip.title = t.join("; ");
    });
  }
  function renderBanner() {
    var msgs = [];
    if (store.error) msgs.push(store.error);
    if (store.focusMissing) msgs.push(store.focusMissing);
    bannerEl.textContent = msgs.join(" ");
    bannerEl.hidden = !msgs.length;
  }

  function selButton(parent, text, id) {
    return button(parent, text, "linkish", function () { store.select([id], false); revealEffort(id); }, "Select " + text);
  }
  function renderSince() {
    sinceEl.textContent = "";
    var d = store.payload && store.payload.delta;
    if (!d) { sinceEl.hidden = true; return; }
    sinceEl.hidden = false;
    var head = el("div", null, null, sinceEl);
    el("b", null, "Since last look: ", head);
    if (d.status === "no_previous_look") {
      head.appendChild(document.createTextNode((d.message || "No previous look") + " — changes are counted once a snapshot has been marked seen."));
    } else {
      var s = d.summary || {}, parts = [];
      parts.push(plural(s.new_changes || 0, "new change"));
      parts.push(plural(s.pr_state_changes || 0, "PR state change"));
      parts.push(plural(s.new_review_requests || 0, "new review request"));
      var known = (s.new_changes || 0) + (s.pr_state_changes || 0) + (s.new_review_requests || 0);
      head.appendChild(document.createTextNode((known ? parts.join(" · ") : "no known changes") +
        (d.baseline_at ? " (compared with generation " + d.baseline_generation + ", seen " + fmtTime(d.baseline_at) + ")" : "")));
      if (s.unknown) el("div", null, "Unknown: " + plural(s.unknown, "item") + " could not be compared because data was missing then or now; they are not counted as unchanged.", sinceEl);
      if (s.truncated) el("div", null, plural(s.truncated, "workspace") + " had more history than the comparison window; older changes are not compared.", sinceEl);
      var items = [];
      (d.workspaces || []).forEach(function (w) { items.push([w.id, w.label, w.status === "unknown" ? "unknown" : w.status === "new" ? "new workspace, " + plural(w.new_changes, "change") : plural(w.new_changes, "new change")]); });
      (d.prs || []).forEach(function (p) {
        var t = p.status === "unknown" ? "unknown" : p.status === "new" ? "new PR (" + (p.to_state || "?") + ")" : (p.from_state || "?") + " → " + (p.to_state || "?") + (p.from_review !== p.to_review ? ", review " + (p.from_review || "none") + " → " + (p.to_review || "none") : "");
        items.push([p.id, p.label, t]);
      });
      (d.review_requests || []).forEach(function (r) { items.push([r.id, r.label, r.status === "unknown" ? "requested reviewers unknown" : "review requested from " + (r.added || []).join(", ")]); });
      if (items.length) {
        var ul = el("ul", null, null, sinceEl);
        items.slice(0, 8).forEach(function (it) {
          var li = el("li", null, null, ul);
          if (store.idx.byId.has(it[0])) selButton(li, it[1], it[0]); else li.appendChild(document.createTextNode(it[1]));
          li.appendChild(document.createTextNode(": " + it[2]));
        });
        if (items.length > 8) el("li", null, "+" + (items.length - 8) + " more", ul);
      }
    }
    var acts = el("div", "actions", null, sinceEl);
    var gen = store.payload && store.payload.generation;
    var mark = button(acts, "Mark seen", null, null);
    if (!BOOT.token || !BOOT.ack_api) {
      mark.disabled = true; mark.title = "This wgo dash server does not accept Mark seen.";
    } else {
      mark.title = "Count changes from this snapshot (generation " + gen + ") from now on. Only this button moves the baseline; loading or reloading the page never does.";
      mark.disabled = store.busy.has("ack") || !gen;
      mark.addEventListener("click", function () {
        store.busy.add("ack"); store.ackResult = null; renderSince();
        postJSON(BOOT.ack_api, { generation: gen }).then(function (res) {
          store.ackResult = { ok: true, text: L.ackText(res) };
          return getJSON(API).then(applyPayload);
        }, function (e) {
          store.ackResult = { ok: false, text: "Mark seen failed: " + L.actionError(e) };
        }).then(function () { store.busy.delete("ack"); renderSince(); }, function (e) {
          store.busy.delete("ack"); renderSince();
          if (window.console) console.error("wgo dash:", e);
        });
      });
    }
    if (store.ackResult) el("div", store.ackResult.ok ? "act-result" : "act-result err", store.ackResult.text, sinceEl).setAttribute("role", "status");
  }

  // ---------- small multiples ----------
  function multHeader(box, title, sub) {
    box.textContent = "";
    var h = el("h2", null, title, box);
    if (sub) el("span", "sub", " " + sub, h);
  }
  function barChart(box, items, opts) {
    // items: [{key, label, n, part, title}]; one series, so no legend; values labelled directly.
    var W = Math.max(160, Math.round(box.clientWidth || 300)), H = 92, top = 12, base = H - 16, n = items.length;
    var svg = svgEl("svg", { viewBox: "0 0 " + W + " " + H, width: W, height: H, role: "group", "aria-label": opts.aria }, box);
    var max = items.reduce(function (m, it) { return Math.max(m, it.n); }, 0) || 1;
    var slot = W / Math.max(n, 1), bw = Math.max(2, Math.min(28, slot - 2));
    svgEl("line", { x1: 0, x2: W, y1: base + 0.5, y2: base + 0.5, stroke: theme["--grid"], "stroke-width": 1 }, svg);
    var gs = [];
    items.forEach(function (it, i) {
      var x = i * slot + (slot - bw) / 2, h = (base - top) * it.n / max;
      var on = L.sameFilter(store.filter, { by: opts.by, value: it.key });
      var g = svgEl("g", { "class": "mbar" + (on ? " on" : ""), tabindex: i === 0 ? 0 : -1, role: "button", "aria-pressed": String(on), "aria-label": it.title }, svg);
      svgEl("title", {}, g).textContent = it.title;
      svgEl("rect", { "class": "hit", x: i * slot, y: 0, width: slot, height: H }, g);
      if (it.n > 0) {
        svgEl("rect", { "class": "v", x: x, y: base - h, width: bw, height: h, rx: Math.min(2, bw / 4) }, g);
        if (it.part > 0) { var ph = (base - top) * it.part / max; svgEl("rect", { "class": "part", x: x + bw * 0.25, y: base - ph, width: bw * 0.5, height: ph }, g); }
      }
      if (opts.values && it.n > 0) { var vt = svgEl("text", { x: x + bw / 2, y: base - h - 2, "text-anchor": "middle" }, g); vt.textContent = String(it.n); }
      if (opts.label(i)) { var lt = svgEl("text", { x: i * slot + slot / 2, y: H - 3, "text-anchor": "middle" }, g); lt.textContent = it.label; }
      function choose() { store.setFilter(L.toggleFilter(store.filter, { by: opts.by, value: it.key })); }
      g.addEventListener("click", choose);
      g.addEventListener("keydown", function (ev) {
        if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); choose(); }
        else if (ev.key === "ArrowRight" || ev.key === "ArrowLeft") {
          ev.preventDefault();
          var j = clamp(i + (ev.key === "ArrowRight" ? 1 : -1), 0, gs.length - 1);
          gs[i].setAttribute("tabindex", "-1"); gs[j].setAttribute("tabindex", "0"); gs[j].focus();
        }
      });
      gs.push(g);
    });
    return svg;
  }
  function renderMultiples() {
    // Bars show totals; the inner accent bar is the selected efforts' share.
    var counts = store.snap ? store.snap.counts || {} : {};
    var selE = store.selection.size ? L.selectionEfforts(store.idx, store.selection) : null;
    var active = document.activeElement, activeLabel = active && active.closest && active.closest(".mult") ? active.getAttribute("aria-label") : null;

    var days = counts.days || [], tot = L.dayTotals(counts, null), part = selE ? L.dayTotals(counts, selE) : [];
    multHeader(multDays, "Local activity per day", "(changes, last " + plural(days.length, "day") + ")");
    if (!days.length) el("p", "empty", store.snap ? "No activity window." : "Waiting for data…", multDays);
    else barChart(multDays, days.map(function (d, i) {
      return { key: d, label: d.slice(5), n: tot[i], part: part[i] || 0, title: d + ": " + plural(tot[i], "change") + (selE ? ", " + (part[i] || 0) + " in the selection" : "") };
    }), { by: "day", aria: "Local changes per day. Choose a day to filter.", values: false, label: function (i) { return i === 0 || i === days.length - 1 || i % 7 === 0; } });

    var prt = L.prTotals(counts, null), prp = selE ? L.prTotals(counts, selE) : [];
    multHeader(multPR, "PRs by state", "(bookmarks with unknown lookups count as unknown, not none)");
    if (!store.snap) el("p", "empty", "Waiting for data…", multPR);
    else barChart(multPR, prt.map(function (it, i) {
      return { key: it.state, label: it.state, n: it.n, part: prp[i] ? prp[i].n : 0, title: it.state + ": " + it.n + (selE ? ", " + (prp[i] ? prp[i].n : 0) + " in the selection" : "") };
    }), { by: "prstate", aria: "PRs by state. Choose a state to filter.", values: true, label: function () { return true; } });

    var ag = L.agentTotals(counts, store.idx.byId);
    multHeader(multAg, "Agent sessions by effort");
    if (!ag.length) el("p", "empty", store.snap ? "No agent sessions recorded." : "Waiting for data…", multAg);
    else barChart(multAg, ag.slice(0, 12).map(function (a) {
      var by = Object.keys(a.by).sort().map(function (k) { return a.by[k] + " " + k; }).join(", ");
      return { key: a.effort, label: a.label.length > 10 ? a.label.slice(0, 9) + "…" : a.label, n: a.total, part: selE && selE.has(a.effort) ? a.total : 0, title: a.label + ": " + plural(a.total, "session") + " (" + by + ")" };
    }), { by: "effort", aria: "Agent sessions per effort. Choose an effort to filter.", values: true, label: function () { return true; } });

    if (activeLabel) {
      var again = Array.prototype.filter.call(mults.querySelectorAll(".mbar"), function (g) { return g.getAttribute("aria-label") === activeLabel; })[0];
      if (again) { again.setAttribute("tabindex", "0"); again.focus(); }
    }
  }
  function filterText(f) {
    if (!f) return "";
    if (f.by === "day") return "Filter: activity on " + f.value;
    if (f.by === "prstate") return "Filter: PRs " + f.value;
    var n = store.idx.byId.get(f.value);
    return "Filter: effort " + (n ? n.label : f.value);
  }

  // ---------- efforts list ----------
  function stateBadge(parent, text, cls) { return el("span", "state " + (cls || ""), text, parent); }
  function prClass(pr) {
    var b = L.prBucket(pr);
    if (b === "merged") return "s-good";
    if (b === "closed") return "s-bad";
    return "";
  }
  function ciClass(pr) {
    var c = String((pr && pr.checks) || "").toLowerCase();
    if (/success|pass/.test(c)) return "s-good";
    if (/fail|error/.test(c)) return "s-bad";
    if (/pend|progress|queued/.test(c)) return "s-warn";
    return "f-unknown";
  }
  function freshBadge(parent, f) {
    var d = L.fresh(f);
    if (d.key === "fresh") return null;
    return stateBadge(parent, d.glyph + " " + d.label, "f-" + d.key);
  }
  function renderPR(ul, pr) {
    var li = el("li", "sub", null, ul), p = pr.pr || {};
    selButton(li, pr.label, pr.id);
    li.appendChild(document.createTextNode(" "));
    stateBadge(li, L.prBucket(p), prClass(p)); li.appendChild(document.createTextNode(" "));
    stateBadge(li, L.ciText(p), ciClass(p)); li.appendChild(document.createTextNode(" "));
    stateBadge(li, L.reviewText(p)); li.appendChild(document.createTextNode(" "));
    freshBadge(li, p.freshness);
    if (p.reviewers_known && p.requested_reviewers && p.requested_reviewers.length) el("div", "meta", "review requested: " + p.requested_reviewers.join(", "), li);
    else if (!p.reviewers_known) el("div", "meta", "requested reviewers unknown", li);
    if (p.title) el("div", "meta", p.title, li);
  }
  function renderTicket(ul, t) {
    var li = el("li", "sub", null, ul), ti = t.ticket || {};
    el("span", "meta", "ticket ", li);
    selButton(li, t.label, t.id); li.appendChild(document.createTextNode(" "));
    stateBadge(li, ti.status || "status unknown", ti.status ? "" : "f-unknown"); li.appendChild(document.createTextNode(" "));
    freshBadge(li, ti.freshness);
    if (ti.title) el("div", "meta", ti.title, li);
    if (ti.error) el("div", "err", ti.error, li);
  }
  function renderAgent(ul, a) {
    var li = el("li", null, null, ul), ai = a.agent || {}, now = Date.now();
    el("span", "meta", "agent ", li);
    selButton(li, ai.tool || a.label, a.id); li.appendChild(document.createTextNode(" "));
    stateBadge(li, ai.status || "status unknown"); li.appendChild(document.createTextNode(" "));
    stateBadge(li, L.livenessText(ai.liveness), ai.liveness === "uncertain" ? "f-unknown liveness-uncertain" : "");
    el("div", "meta", [ai.branch ? "bookmark " + ai.branch : "no bookmark recorded",
      "started " + (L.relTime(ai.start_time, now) || "unknown"), "last activity " + (L.relTime(ai.last_activity, now) || "unknown")].join(" · "), li);
    if (ai.conflict) el("div", "conflict", "Same bookmark as " + plural((ai.conflicts_with || []).length, "other active session"), li);
  }
  var effortEls = new Map();
  function renderEfforts() {
    effortsEl.textContent = ""; effortEls = new Map();
    if (!store.snap) { el("p", "empty", "Waiting for the first snapshot…", effortsEl); return; }
    var idx = store.idx, efforts = [];
    idx.byId.forEach(function (n) { if (n.kind === "effort" && store.isVisible(n.id)) efforts.push(n); });
    efforts.sort(L.effortOrder);
    el("h2", "sr", "Efforts", effortsEl);
    if (!efforts.length) { el("p", "empty", store.filter ? "Nothing matches the filter." : "No efforts or workspaces discovered.", effortsEl); return; }
    var selE = L.selectionEfforts(idx, store.selection);
    efforts.forEach(function (e) {
      var info = e.effort || {};
      var sec = el("section", "effort" + (selE.has(e.id) ? " sel" : "") + (store.selection.size && !selE.has(e.id) ? " dim" : ""), null, effortsEl);
      effortEls.set(e.id, sec);
      var head = el("header", null, null, sec);
      var nb = button(head, e.label, "name", function () { store.select([e.id], false); }, "Select effort " + e.label);
      nb.setAttribute("aria-pressed", String(store.selection.has(e.id)));
      if (info.group === "ungrouped") el("span", "badge warn", "Ungrouped", head);
      else if (info.group === "ticket") el("span", "badge", "ticket fallback", head);
      else if (info.group === "theme") el("span", "badge", "agent theme", head);
      if (info.source) el("span", "badge", info.source, head);
      if (info.description) el("div", "desc", info.description, sec);
      (info.conflicts || []).forEach(function (c) {
        var w = idx.byId.get(c.workspace_id);
        el("div", "conflict", "Attribution conflict: " + (w ? w.label : c.workspace_id || "a workspace") + " is claimed by " + (c.claimed_by || []).join(", "), sec);
      });
      var rl = L.reviewLink(store.links, e.id);
      if (rl && localURL(rl.url)) {
        var xl = el("div", "xlink", null, sec);
        xl.appendChild(document.createTextNode("Seen in review run "));
        var a = el("a", null, rl.run, xl); a.href = localURL(rl.url);
        if (rl.matches && rl.matches.length) xl.appendChild(document.createTextNode(" (" + plural(rl.matches.length, "exact match") + ")"));
      }
      var wss = L.childrenOf(idx, e.id, "contains").filter(function (w) { return store.isVisible(w.id); });
      var ul = el("ul", null, null, sec);
      if (!wss.length) el("li", "meta", "No workspaces discovered for this effort.", ul);
      wss.sort(function (a, b) { return a.label < b.label ? -1 : a.label > b.label ? 1 : 0; }).forEach(function (w) {
        var wi = w.workspace || {}, li = el("li", "ws", null, ul);
        button(li, w.label, null, function () { store.select([w.id], false); }, "Select workspace " + w.label);
        if (wi.is_main) el("span", "badge", "main clone", li);
        if (wi.error) { el("div", "err", "jj unavailable: " + wi.error, li); return; }
        var meta = [];
        meta.push(wi.bookmark ? "on " + wi.bookmark : "no bookmark");
        if (wi.stack) meta.push("stack " + wi.stack.position + "/" + wi.stack.size + (wi.stack.size > 1 ? " (" + wi.stack.bookmarks.join(" › ") + ")" : ""));
        if (wi.last_activity) meta.push("active " + L.relTime(wi.last_activity, Date.now()));
        el("div", "meta", meta.join(" · "), li);
        if (wi.description) el("div", "meta", wi.description, li);
        if (wi.annotation) el("div", "meta", "plan: " + wi.annotation, li);
        var sub = el("ul", null, null, li);
        L.childrenOf(idx, w.id, "on").forEach(function (bm) {
          var prs = L.childrenOf(idx, bm.id, "pr").filter(function (p) { return store.isVisible(p.id); });
          if (!prs.length) {
            var bli = el("li", "sub", null, sub);
            el("span", "meta", L.bookmarkPRText(bm.bookmark), bli);
            freshBadge(bli, bm.bookmark && bm.bookmark.pr_lookup);
          }
          prs.forEach(function (p) { renderPR(sub, p); });
          var tickets = new Map();
          L.childrenOf(idx, bm.id, "ticket").forEach(function (t) { tickets.set(t.id, t); });
          prs.forEach(function (p) { L.childrenOf(idx, p.id, "ticket").forEach(function (t) { tickets.set(t.id, t); }); });
          tickets.forEach(function (t) { renderTicket(sub, t); });
        });
      });
      var agents = L.childrenOf(idx, e.id, "runs").filter(function (a) { return store.isVisible(a.id); });
      if (agents.length) {
        var aul = el("ul", null, null, sec);
        agents.sort(function (a, b) { return a.id < b.id ? -1 : 1; }).forEach(function (a) { renderAgent(aul, a); });
      }
    });
  }
  function revealEffort(id) {
    var effs = L.effortsOf(store.idx, id), sec = effs.length ? effortEls.get(effs[0]) : null;
    if (sec && sec.scrollIntoView) sec.scrollIntoView({ block: "nearest" });
  }

  // ---------- detail panel ----------
  function kv(parent, k, v) { if (v === "" || v == null) return; el("div", "kv", k + ": " + v, parent); }
  function renderDetail() {
    detailBox.textContent = "";
    el("h2", null, "Selection", detailBox);
    if (!store.selection.size) { el("p", "empty", "Select an effort, workspace, PR, ticket or agent in the list, graph or table. Shift-click adds; Esc clears.", detailBox); return; }
    var now = Date.now();
    Array.from(store.selection).slice(0, 8).forEach(function (id) {
      var n = store.idx.byId.get(id); if (!n) return;
      var url = (n.pr && n.pr.url) || (n.ticket && n.ticket.url) || "";
      var h = el("h3", null, null, detailBox); link(h, n.label, url);
      el("div", "kv", (KINDS.filter(function (K) { return K.k === n.kind; })[0] || { label: n.kind }).label, detailBox);
      if (n.effort) {
        kv(detailBox, "Group", n.effort.group); kv(detailBox, "Description", n.effort.description); kv(detailBox, "Source", n.effort.source);
        (n.effort.conflicts || []).forEach(function (c) { el("div", "conflict", "Conflict: " + c.workspace_id + " claimed by " + (c.claimed_by || []).join(", "), detailBox); });
      } else if (n.workspace) {
        var w = n.workspace;
        kv(detailBox, "Repository", w.repo_slug || w.repo); kv(detailBox, "Path", w.path); kv(detailBox, "Bookmark", w.bookmark || "none");
        if (w.stack) kv(detailBox, "Stack", w.stack.position + "/" + w.stack.size + " — " + w.stack.bookmarks.join(" › "));
        kv(detailBox, "Change", w.change_id); kv(detailBox, "Description", w.description);
        kv(detailBox, "Visible changes", (w.changes || []).length + (w.changes_truncated ? " (+" + w.changes_truncated + " beyond the window)" : ""));
        kv(detailBox, "Last activity", w.last_activity ? fmtTime(w.last_activity) : "");
        if (w.error) el("div", "err", "jj unavailable: " + w.error, detailBox);
        renderActions(detailBox, n.id);
      } else if (n.bookmark) {
        kv(detailBox, "Repository", n.bookmark.repo); kv(detailBox, "PR lookup", L.bookmarkPRText(n.bookmark));
        kv(detailBox, "Fetched", n.bookmark.pr_fetched_at ? fmtTime(n.bookmark.pr_fetched_at) : "never");
      } else if (n.pr) {
        var p = n.pr;
        kv(detailBox, "Title", p.title); kv(detailBox, "State", L.prBucket(p)); kv(detailBox, "Checks", L.ciText(p)); kv(detailBox, "Review", L.reviewText(p));
        kv(detailBox, "Requested reviewers", p.reviewers_known ? ((p.requested_reviewers || []).join(", ") || "none requested") : "unknown");
        kv(detailBox, "Data", L.fresh(p.freshness).label); kv(detailBox, "Updated", p.updated_at ? fmtTime(p.updated_at) : "");
      } else if (n.ticket) {
        var t = n.ticket;
        kv(detailBox, "System", t.system); kv(detailBox, "Status", t.status || "unknown"); kv(detailBox, "Title", t.title); kv(detailBox, "Assignee", t.assignee);
        kv(detailBox, "Data", L.fresh(t.freshness).label); if (t.error) el("div", "err", t.error, detailBox);
      } else if (n.agent) {
        var a = n.agent;
        kv(detailBox, "Tool", a.tool); kv(detailBox, "Status", a.status || "unknown"); kv(detailBox, "Liveness", L.livenessText(a.liveness));
        kv(detailBox, "Bookmark", a.branch || "none recorded"); kv(detailBox, "Started", a.start_time ? fmtTime(a.start_time) + " (" + L.relTime(a.start_time, now) + ")" : "unknown");
        kv(detailBox, "Last activity", a.last_activity ? fmtTime(a.last_activity) + " (" + L.relTime(a.last_activity, now) + ")" : "unknown");
        kv(detailBox, "Session", a.session_id); kv(detailBox, "Source", a.source); kv(detailBox, "Path", a.path); kv(detailBox, "Theme", a.theme_id);
        if (a.conflict) {
          el("div", "conflict", "Another active session works on the same bookmark:", detailBox);
          var cul = el("ul", null, null, detailBox);
          (a.conflicts_with || []).forEach(function (sid) { var li = el("li", null, null, cul), oid = "agent:" + sid; if (store.idx.byId.has(oid)) selButton(li, sid, oid); else li.textContent = sid; });
        }
      }
      var rl = L.reviewLink(store.links, id);
      if (rl && localURL(rl.url)) {
        var xl = el("div", "kv", null, detailBox);
        xl.appendChild(document.createTextNode("Review run: "));
        var ra = el("a", null, rl.run, xl); ra.href = localURL(rl.url);
      }
      var rel = [];
      (store.idx.out.get(id) || []).forEach(function (x) { rel.push([x, "→"]); });
      (store.idx.inc.get(id) || []).forEach(function (x) { rel.push([x, "←"]); });
      if (rel.length) {
        el("h4", null, "Related (" + rel.length + ")", detailBox);
        var ul = el("ul", null, null, detailBox);
        rel.slice(0, 40).forEach(function (r) {
          var o = store.idx.byId.get(r[0].o); if (!o) return;
          var li = el("li", null, r[0].kind.replace(/_/g, " ") + " " + r[1] + " ", ul);
          selButton(li, o.label, o.id);
        });
      }
    });
    if (store.selection.size > 8) el("p", "empty", "+" + (store.selection.size - 8) + " more selected", detailBox);
  }

  // ---------- table ----------
  var sortKey = "kind", sortDir = 1, tableRows = [], focusRow = -1;
  var COLS = [["label", "Name"], ["kind", "Kind"], ["effort", "Effort"], ["state", "State"], ["data", "Data"], ["activity", "Last activity"]];
  function renderTable() {
    var show = store.view === "table" && !!store.snap;
    tableWrap.hidden = !show; canvas.style.visibility = show || !store.snap ? "hidden" : "";
    if (!show) return;
    var hadFocus = tableWrap.contains(document.activeElement) && document.activeElement.tagName === "TR";
    tableWrap.textContent = "";
    var tbl = el("table", "live", null, tableWrap); tbl.setAttribute("role", "grid");
    el("caption", "sr", "Nodes in the current filter. Use the arrow keys to move, Enter or Space to select, Shift+Enter to add.", tbl);
    var thead = el("thead", null, null, tbl), tr = el("tr", null, null, thead);
    COLS.forEach(function (c) {
      var th = el("th", null, null, tr); th.scope = "col";
      th.setAttribute("aria-sort", sortKey === c[0] ? (sortDir > 0 ? "ascending" : "descending") : "none");
      button(th, c[1] + (sortKey === c[0] ? (sortDir > 0 ? " ▲" : " ▼") : ""), null, function () {
        if (sortKey === c[0]) sortDir = -sortDir; else { sortKey = c[0]; sortDir = c[0] === "activity" ? -1 : 1; }
        renderTable();
      });
    });
    tableRows = L.sortRows(L.filterRows(store.rows, store.vis), sortKey, sortDir);
    var tb = el("tbody", null, null, tbl), frag = document.createDocumentFragment(), now = Date.now();
    if (focusRow >= tableRows.length) focusRow = tableRows.length - 1;
    if (focusRow < 0 && tableRows.length) {
      var firstSel = tableRows.findIndex(function (r) { return store.selection.has(r.id); });
      focusRow = firstSel >= 0 ? firstSel : 0;
    }
    tableRows.forEach(function (r, i) {
      var sel = store.selection.has(r.id), row = el("tr", sel ? "sel" : null, null, frag);
      row.setAttribute("aria-selected", String(sel)); row.tabIndex = i === focusRow ? 0 : -1; row.dataset.i = String(i);
      var n = store.idx.byId.get(r.id), url = n && ((n.pr && n.pr.url) || (n.ticket && n.ticket.url));
      link(el("td", null, null, row), r.label, url);
      el("td", null, r.kind, row); el("td", null, r.effort, row); el("td", null, r.state, row); el("td", null, r.data, row);
      el("td", null, r.activity ? L.relTime(r.activity, now) : "", row);
    });
    tb.appendChild(frag);
    if (!tableRows.length) { var er = el("tr", null, null, tb); var td = el("td", "empty", store.filter ? "Nothing matches the filter." : "No nodes.", er); td.colSpan = COLS.length; }
    tb.addEventListener("click", function (ev) {
      if (ev.target.closest("a")) return;
      var row = ev.target.closest("tr"); if (!row || row.dataset.i == null) return;
      focusRow = +row.dataset.i; store.select([tableRows[focusRow].id], ev.shiftKey || ev.metaKey);
    });
    tb.addEventListener("keydown", function (ev) {
      var row = ev.target.closest("tr"); if (!row || row.dataset.i == null) return;
      var cur = +row.dataset.i;
      if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); focusRow = cur; store.select([tableRows[cur].id], ev.shiftKey); return; }
      var nx = L.nextIndex(tableRows.length, cur, ev.key);
      if (nx < 0) return;
      ev.preventDefault();
      focusRow = nx;
      var rows = tb.querySelectorAll("tr");
      rows[cur].tabIndex = -1; rows[nx].tabIndex = 0; rows[nx].focus();
      if (!ev.shiftKey) store.select([tableRows[nx].id], false);
    });
    if (hadFocus) { var f = tb.querySelectorAll("tr")[focusRow]; if (f) f.focus(); }
  }

  // ---------- graph ----------
  var view = null, posCache = new Map(), sim = null, quad = null, quadDirty = true;
  var W = 0, H = 0, dpr = 1, T = { k: 1, x: 0, y: 0 }, dirty = true, autoFit = true;
  function resize() {
    dpr = window.devicePixelRatio || 1;
    W = stage.clientWidth; H = stage.clientHeight;
    canvas.width = Math.max(1, Math.round(W * dpr)); canvas.height = Math.max(1, Math.round(H * dpr));
    dirty = true;
  }
  if (window.ResizeObserver) {
    new ResizeObserver(function () { resize(); if (autoFit) robustFit(); }).observe(stage);
    var multW = 0;
    new ResizeObserver(function () { if (Math.abs(mults.clientWidth - multW) > 8) { multW = mults.clientWidth; renderMultiples(); } }).observe(mults);
  }
  function s2w(px, py) { return [(px - T.x) / T.k, (py - T.y) / T.k]; }
  function fitTo(list, pad) {
    if (!list.length || !W) return;
    var x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    list.forEach(function (n) { x0 = Math.min(x0, n.x - n.r); y0 = Math.min(y0, n.y - n.r); x1 = Math.max(x1, n.x + n.r); y1 = Math.max(y1, n.y + n.r); });
    var bw = Math.max(x1 - x0, 40), bh = Math.max(y1 - y0, 40);
    var k = clamp(Math.min((W - 2 * pad) / bw, (H - 2 * pad) / bh), 0.05, 3);
    T = { k: k, x: W / 2 - k * (x0 + x1) / 2, y: H / 2 - k * (y0 + y1) / 2 };
    dirty = true;
  }
  function robustFit() { autoFit = true; if (view) fitTo(view.nodes.filter(function (n) { return isFinite(n.x); }), 40); }
  function buildView() {
    var nodes = [], idx = new Map(), links = [];
    store.idx.byId.forEach(function (d) {
      if (!store.isVisible(d.id)) return;
      var o = { id: d.id, d: d, r: radius(d), deg: 0 }, p = posCache.get(d.id);
      if (p) { o.x = p.x; o.y = p.y; o.fx = p.fx; o.fy = p.fy; }
      idx.set(d.id, o); nodes.push(o);
    });
    var adj = new Map();
    ((store.snap && store.snap.edges) || []).forEach(function (e) {
      var a = idx.get(e.source), b = idx.get(e.target);
      if (!a || !b || a === b) return;
      links.push({ source: a, target: b, kind: e.kind });
      a.deg++; b.deg++;
      push(adj, a.id, b.id); push(adj, b.id, a.id);
    });
    return { nodes: nodes, links: links, idx: idx, adj: adj };
  }
  var LINK_DIST = { contains: 60, on: 26, pr: 30, ticket: 34, runs: 70, works_in: 40 };
  function startSim(fit) {
    if (sim) sim.stop();
    view = buildView();
    if (!d3 || !view.nodes.length) { dirty = true; return; }
    var fresh = view.nodes.some(function (n) { return n.x == null; });
    sim = d3.forceSimulation(view.nodes)
      .force("link", d3.forceLink(view.links).distance(function (l) { return LINK_DIST[l.kind] || 40; }).strength(function (l) { return 1 / Math.min(l.source.deg, l.target.deg, 6); }))
      .force("charge", d3.forceManyBody().strength(function (d) { return -30 - d.r * 4; }).distanceMax(400))
      .force("collide", d3.forceCollide(function (d) { return d.r + 3; }).iterations(1))
      .force("x", d3.forceX().strength(0.04)).force("y", d3.forceY().strength(0.04))
      .alphaDecay(0.03).velocityDecay(0.4).stop();
    var pre = reduceMotion ? 300 : (fresh ? 120 : 20);
    for (var i = 0; i < pre; i++) sim.tick();
    view.nodes.forEach(function (n) { posCache.set(n.id, n); });
    quadDirty = true;
    if (fit || fresh) robustFit();
    if (!reduceMotion) { sim.alpha(fresh ? 0.4 : 0.15); sim.on("tick", function () { quadDirty = true; dirty = true; if (autoFit) fitTo(view.nodes, 40); }).restart(); }
    dirty = true;
  }
  function hit(px, py) {
    if (!view || !d3 || !view.nodes.length) return null;
    if (quadDirty) { quad = d3.quadtree(view.nodes, function (d) { return d.x; }, function (d) { return d.y; }); quadDirty = false; }
    var w = s2w(px, py), best = null, bd = Infinity, tol = 4 / T.k;
    view.nodes.forEach(function (d) { var dist = Math.hypot(d.x - w[0], d.y - w[1]) - d.r; if (dist <= tol && dist < bd) { bd = dist; best = d; } });
    return best;
  }
  function neighbourhood() {
    if (!store.selection.size || !view) return null;
    var s = new Set();
    store.selection.forEach(function (id) { if (!view.idx.has(id)) return; s.add(id); (view.adj.get(id) || []).forEach(function (o) { s.add(o); }); });
    return s;
  }
  function drawNode(n, alpha) {
    var d = n.d, k = T.k, f = L.fresh(nodeFresh(d)).key, hollow = f === "unknown" || (d.kind === "agent" && d.agent && d.agent.liveness === "uncertain");
    ctx.globalAlpha = alpha;
    ctx.beginPath(); pathShape(ctx, d.kind, n.x, n.y, n.r);
    ctx.fillStyle = hollow ? theme["--surface"] : kindColor(d.kind); ctx.fill();
    if (hollow) { ctx.setLineDash([2 / k, 1.5 / k]); ctx.lineWidth = 1.4 / k; ctx.strokeStyle = theme["--ink"]; ctx.stroke(); ctx.setLineDash([]); }
    else { ctx.lineWidth = 1.1 / k; ctx.strokeStyle = theme["--surface"]; ctx.stroke(); }
    if (f === "stale") { ctx.beginPath(); pathShape(ctx, d.kind, n.x, n.y, n.r + 1.8); ctx.setLineDash([3 / k, 2 / k]); ctx.lineWidth = 1.2 / k; ctx.strokeStyle = theme["--ink"]; ctx.stroke(); ctx.setLineDash([]); }
    if (f === "error") { ctx.beginPath(); pathShape(ctx, d.kind, n.x, n.y, n.r + 1.8); ctx.setLineDash([1 / k, 2 / k]); ctx.lineWidth = 1.6 / k; ctx.strokeStyle = theme["--s-critical"]; ctx.stroke(); ctx.setLineDash([]); }
    if (hollow && d.kind !== "agent") { ctx.fillStyle = theme["--ink"]; ctx.font = (n.r * 1.4) + "px system-ui, sans-serif"; ctx.textAlign = "center"; ctx.textBaseline = "middle"; ctx.fillText("?", n.x, n.y + 0.5); ctx.textAlign = "start"; }
    if (d.kind === "pr" && d.pr) {
      var o = PR_RING[L.prBucket(d.pr)];
      if (o) { ctx.beginPath(); ctx.arc(n.x, n.y, n.r * 1.25 + 2.2, 0, 6.2832); ctx.setLineDash(o.dash.map(function (v) { return v / k; })); ctx.lineWidth = 1.6 / k; ctx.strokeStyle = theme[o.cv]; ctx.stroke(); ctx.setLineDash([]); }
    }
    if (d.kind === "agent" && d.agent && d.agent.conflict) { ctx.beginPath(); ctx.arc(n.x, n.y, n.r * 1.5 + 2, 0, 6.2832); ctx.lineWidth = 1.8 / k; ctx.strokeStyle = theme["--s-serious"]; ctx.stroke(); }
    ctx.globalAlpha = 1;
  }
  function draw() {
    dirty = false;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = theme["--surface"]; ctx.fillRect(0, 0, W, H);
    if (!view || store.view !== "graph") return;
    if (!view.nodes.length) {
      ctx.fillStyle = theme["--ink2"]; ctx.font = "13px system-ui, sans-serif"; ctx.textBaseline = "middle";
      ctx.fillText(store.filter ? "Nothing matches the filter." : "No nodes in this snapshot.", 16, 20);
      return;
    }
    var nb = neighbourhood(), sel = store.selection, k = T.k;
    ctx.setTransform(dpr * k, 0, 0, dpr * k, dpr * T.x, dpr * T.y);
    view.links.forEach(function (l) {
      var on = nb && (sel.has(l.source.id) || sel.has(l.target.id));
      ctx.globalAlpha = nb ? (on ? 0.85 : 0.07) : 0.4;
      ctx.beginPath(); ctx.moveTo(l.source.x, l.source.y); ctx.lineTo(l.target.x, l.target.y);
      ctx.setLineDash(l.kind === "runs" || l.kind === "works_in" ? [4 / k, 3 / k] : []);
      ctx.lineWidth = (on ? 1.6 : 1) / k; ctx.strokeStyle = l.kind === "runs" || l.kind === "works_in" ? theme["--ink2"] : theme["--muted"]; ctx.stroke();
    });
    ctx.setLineDash([]); ctx.globalAlpha = 1;
    view.nodes.forEach(function (n) { if (!nb || !nb.has(n.id)) drawNode(n, nb ? 0.15 : 1); });
    if (nb) view.nodes.forEach(function (n) { if (nb.has(n.id)) drawNode(n, 1); });
    sel.forEach(function (id) { var s = view.idx.get(id); if (!s) return; ctx.beginPath(); pathShape(ctx, s.d.kind, s.x, s.y, s.r + 4); ctx.lineWidth = 2 / k; ctx.strokeStyle = theme["--accent"]; ctx.stroke(); });
    var hov = store.hover ? view.idx.get(store.hover) : null;
    if (hov) { ctx.beginPath(); pathShape(ctx, hov.d.kind, hov.x, hov.y, hov.r + 3); ctx.lineWidth = 1.5 / k; ctx.strokeStyle = theme["--ink"]; ctx.stroke(); }
    // labels in screen space with greedy collision avoidance
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.font = "12px system-ui, -apple-system, 'Segoe UI', sans-serif"; ctx.textBaseline = "middle";
    var cand = [], placed = [], seen = new Set();
    sel.forEach(function (id) { var s = view.idx.get(id); if (s) cand.push([s, 0]); });
    if (hov) cand.push([hov, 0]);
    view.nodes.forEach(function (n) {
      if (n.d.kind === "effort") cand.push([n, 1]);
      else if (nb && nb.has(n.id)) cand.push([n, 2]);
      else if (n.d.kind === "workspace" || n.d.kind === "agent") cand.push([n, 3]);
    });
    cand.sort(function (a, b) { return a[1] - b[1]; });
    cand.forEach(function (c) {
      var n = c[0]; if (seen.has(n.id)) return; seen.add(n.id);
      var sx = n.x * k + T.x, sy = n.y * k + T.y, rr = n.r * k;
      if (sx < -50 || sx > W + 50 || sy < -20 || sy > H + 20) return;
      var text = n.d.kind === "agent" ? (n.d.agent ? n.d.agent.tool + " (agent)" : n.d.label) : n.d.label;
      var tw = ctx.measureText(text).width, bx = sx + rr + 4, by = sy - 8;
      if (bx + tw > W - 4) bx = sx - rr - 4 - tw;
      var box = { x: bx, y: by, w: tw, h: 16 };
      if (c[1] > 0) for (var j = 0; j < placed.length; j++) { var q = placed[j]; if (box.x < q.x + q.w + 3 && box.x + box.w + 3 > q.x && box.y < q.y + q.h && box.y + box.h > q.y) return; }
      placed.push(box);
      ctx.lineWidth = 3.5; ctx.lineJoin = "round"; ctx.strokeStyle = theme["--surface"]; ctx.strokeText(text, bx, by + 8);
      ctx.fillStyle = theme["--ink"]; ctx.font = c[1] === 1 ? "600 12px system-ui, -apple-system, 'Segoe UI', sans-serif" : "12px system-ui, -apple-system, 'Segoe UI', sans-serif";
      ctx.fillText(text, bx, by + 8);
    });
  }
  function frame() { if (dirty) draw(); requestAnimationFrame(frame); }
  requestAnimationFrame(frame);

  function showTip(n, px, py) {
    if (!n) { tip.hidden = true; return; }
    tip.textContent = "";
    el("b", null, n.d.label, tip);
    el("div", "dim", (KINDS.filter(function (K) { return K.k === n.d.kind; })[0] || { label: n.d.kind }).label, tip);
    var st = L.stateText(n.d); if (st) el("div", null, st, tip);
    el("div", "dim", "data: " + L.dataText(n.d), tip);
    tip.hidden = false;
    tip.style.left = clamp(px + 14, 4, Math.max(4, W - tip.offsetWidth - 4)) + "px";
    tip.style.top = clamp(py + 14, 4, Math.max(4, H - tip.offsetHeight - 4)) + "px";
  }
  var drag = null;
  function pos(ev) { var r = canvas.getBoundingClientRect(); return [ev.clientX - r.left, ev.clientY - r.top]; }
  canvas.addEventListener("pointerdown", function (ev) {
    if (ev.button !== 0) return;
    var p = pos(ev);
    drag = { sx: p[0], sy: p[1], n: hit(p[0], p[1]), moved: false, tx: T.x, ty: T.y, shift: ev.shiftKey };
    canvas.setPointerCapture(ev.pointerId); canvas.classList.add("dragging");
  });
  canvas.addEventListener("pointermove", function (ev) {
    var p = pos(ev);
    if (drag) {
      if (!drag.moved && Math.hypot(p[0] - drag.sx, p[1] - drag.sy) > 4) { drag.moved = true; tip.hidden = true; if (drag.n && sim) sim.alphaTarget(reduceMotion ? 0 : 0.2).restart(); }
      if (drag.moved) {
        if (drag.n) { var w = s2w(p[0], p[1]); drag.n.fx = drag.n.x = w[0]; drag.n.fy = drag.n.y = w[1]; quadDirty = true; if (reduceMotion && sim) sim.tick(); }
        else { T.x = drag.tx + p[0] - drag.sx; T.y = drag.ty + p[1] - drag.sy; autoFit = false; }
        dirty = true;
      }
      return;
    }
    var n = hit(p[0], p[1]), id = n ? n.id : null;
    canvas.classList.toggle("hot", !!n);
    if (id !== store.hover) { store.hover = id; dirty = true; }
    showTip(n, p[0], p[1]);
  });
  canvas.addEventListener("pointerup", function (ev) {
    if (!drag) return;
    var d = drag; drag = null; canvas.classList.remove("dragging");
    if (d.moved) { if (d.n && sim) sim.alphaTarget(0); return; }
    if (d.n) { store.select([d.n.id], ev.shiftKey || d.shift); revealEffort(d.n.id); } else store.clear();
  });
  canvas.addEventListener("pointercancel", function () { drag = null; canvas.classList.remove("dragging"); });
  canvas.addEventListener("pointerleave", function () { if (!drag && store.hover) { store.hover = null; tip.hidden = true; dirty = true; } });
  canvas.addEventListener("wheel", function (ev) {
    ev.preventDefault();
    autoFit = false;
    var p = pos(ev), f = Math.exp(-ev.deltaY * (ev.ctrlKey ? 0.01 : 0.0015)), k2 = clamp(T.k * f, 0.05, 8);
    f = k2 / T.k; T.x = p[0] - (p[0] - T.x) * f; T.y = p[1] - (p[1] - T.y) * f; T.k = k2; dirty = true;
  }, { passive: false });
  canvas.addEventListener("keydown", function (ev) {
    // Keyboard selection on the canvas: arrows walk the visible nodes in table order.
    if (!view || !view.nodes.length) return;
    var order = L.sortRows(L.filterRows(store.rows, store.vis), sortKey, sortDir).map(function (r) { return r.id; });
    var cur = order.indexOf(Array.from(store.selection)[0]);
    var key = ev.key === "ArrowRight" ? "ArrowDown" : ev.key === "ArrowLeft" ? "ArrowUp" : ev.key;
    var nx = L.nextIndex(order.length, cur, key);
    if (nx < 0) return;
    ev.preventDefault();
    store.select([order[nx]], false); revealEffort(order[nx]);
  });
  document.addEventListener("keydown", function (ev) {
    var tag = ((document.activeElement || {}).tagName || "");
    if (ev.key === "Escape") {
      if (document.activeElement === search && search.value) { search.value = ""; statusEl.textContent = ""; }
      if (store.selection.size) store.clear(); else if (store.filter) store.setFilter(null);
    } else if (ev.key === "/" && !/^(INPUT|TEXTAREA)$/.test(tag)) { ev.preventDefault(); search.focus(); }
  });

  var searchTimer = 0;
  function runSearch() {
    var q = search.value.trim();
    if (!q) { statusEl.textContent = ""; return; }
    var m = L.search(L.filterRows(store.rows, store.vis), q);
    statusEl.textContent = m.length ? plural(m.length, "match") + (m.length > 60 ? " (selecting the first 60)" : "") : "no matches in the current filter";
    if (!m.length) return;
    store.select(m.slice(0, 60), false); revealEffort(m[0]);
  }
  search.addEventListener("input", function () { clearTimeout(searchTimer); searchTimer = setTimeout(runSearch, 200); });
  search.addEventListener("keydown", function (ev) { if (ev.key === "Enter") { clearTimeout(searchTimer); runSearch(); } });

  // ---------- wiring ----------
  function renderAll(fit) {
    loadingEl.hidden = !!store.snap;
    filterChip.hidden = !store.filter; filterChip.textContent = store.filter ? filterText(store.filter) + " ✕" : "";
    filterChip.setAttribute("aria-label", store.filter ? filterText(store.filter) + ". Clear filter" : "");
    renderHeader(); renderBanner(); renderSince(); renderMultiples(); renderEfforts(); renderDetail(); renderTable(); buildLegend();
    startSim(fit);
  }
  store.subscribe(function (reason) {
    if (reason === "data") renderAll(false);
    else if (reason === "filter") { renderAll(true); }
    else if (reason === "selection") { renderMultiples(); renderEfforts(); renderDetail(); renderTable(); dirty = true; }
    else if (reason === "view") { renderTable(); dirty = true; if (store.view === "graph") { resize(); } }
    else if (reason === "status") { renderHeader(); renderBanner(); }
  });
  if (window.matchMedia) {
    var mq = matchMedia("(prefers-color-scheme: dark)");
    if (mq.addEventListener) mq.addEventListener("change", function () { readTheme(); buildLegend(); renderMultiples(); dirty = true; });
  }

  // ---------- actions ----------
  // postJSON posts body with the page token in a header (never in the URL).
  // Failures carry e.status and e.detail (the server's error message) for
  // L.actionError, or e.unreadable / a network error for L.fetchFailure.
  function postJSON(url, body) {
    return fetch(url, {
      method: "POST", cache: "no-store", credentials: "same-origin", referrerPolicy: "no-referrer",
      headers: { "Content-Type": "application/json", "X-Wgo-Token": BOOT.token || "" },
      body: JSON.stringify(body)
    }).then(function (r) {
      return r.json().then(function (j) {
        if (!r.ok || !j || j.ok === false) { var e = new Error((j && j.error) || "HTTP " + r.status); e.status = r.status; e.detail = j && j.error; throw e; }
        return j;
      }, function (e) {
        if (!r.ok) { var x = new Error("HTTP " + r.status); x.status = r.status; throw x; }
        e.unreadable = true; throw e;
      });
    });
  }
  function copyBox(parent, text) {
    var row = el("div", "copyrow", null, parent);
    var code = el("code", null, text, row);
    var b = button(row, "Copy", null, function () {
      var done = function () { b.textContent = "Copied"; setTimeout(function () { b.textContent = "Copy"; }, 1500); };
      var manual = function () {
        // No clipboard access: select the text so Cmd-C copies it.
        var range = document.createRange(); range.selectNodeContents(code);
        var sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(range);
        b.textContent = "Press ⌘C";
      };
      if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(text).then(done, manual); else manual();
    }, "Copy " + text);
  }
  function renderActions(parent, wsId) {
    var specs = L.actionButtons(BOOT);
    var acts = el("div", "actions", null, parent);
    if (!specs.length) {
      var ob = button(acts, "Open tab", null, null); ob.disabled = true;
      ob.title = "This wgo dash server is read-only.";
      return;
    }
    specs.forEach(function (a) {
      var key = a.kind + "\n" + wsId;
      var b = button(acts, a.label, null, function () { runAction(a, wsId); }, a.label + " for this workspace");
      b.title = a.title;
      b.disabled = store.busy.has(key);
    });
    var r = store.actionResult;
    if (!r || r.wsId !== wsId) return;
    var box = el("div", r.ok ? "act-result" : "act-result err", null, parent);
    box.setAttribute("role", "status");
    el("div", null, r.label + ": " + r.text, box);
    if (r.tried) el("div", "meta", r.tried, box);
    if (r.copy) copyBox(box, r.copy);
  }
  function runAction(a, wsId) {
    var key = a.kind + "\n" + wsId;
    store.busy.add(key);
    store.actionResult = { wsId: wsId, label: a.label, ok: true, text: "working…" };
    renderDetail();
    postJSON(BOOT.action_api, { kind: a.kind, workspace_id: wsId }).then(function (res) {
      var v = L.actionResult(res);
      store.actionResult = { wsId: wsId, label: a.label, ok: v.ok, text: v.text, copy: v.copy, tried: v.tried };
    }, function (e) {
      store.actionResult = { wsId: wsId, label: a.label, ok: false, text: L.actionError(e) };
    }).then(function () { store.busy.delete(key); renderDetail(); }, function (e) {
      store.busy.delete(key); renderDetail();
      if (window.console) console.error("wgo dash:", e);
    });
  }

  // ---------- polling ----------
  var focusPending = BOOT.focus || "", lastKey = "";
  function applyPayload(p) {
    store.fetchedAt = Date.now();
    var hadError = !!store.error;
    store.error = "";
    store.payload = p;
    if (p.status !== "ready" || !p.snapshot) { store.notify("status"); return; }
    var key = p.generation + "\n" + JSON.stringify(p.diagnostics || []) + "\n" + JSON.stringify(p.delta || null);
    if (key === lastKey && store.snap) { store.notify("status"); if (hadError) renderBanner(); return; }
    lastKey = key;
    var first = !store.snap;
    store.snap = p.snapshot;
    store.idx = L.index(store.snap);
    store.rows = L.rows(store.snap, store.idx);
    store.selection = L.prune(store.selection, store.idx.byId);
    if (store.filter) store.vis = L.visible(store.snap, store.idx, store.filter);
    if (focusPending) {
      if (store.idx.byId.has(focusPending)) { store.selection = new Set([focusPending]); store.focusMissing = ""; }
      else store.focusMissing = (BOOT.entity || focusPending) + " is not part of any active effort.";
      focusPending = "";
    }
    store.notify("data");
    if (first) { robustFit(); if (store.selection.size) revealEffort(Array.from(store.selection)[0]); }
  }
  // getJSON fetches url, tagging failures for L.fetchFailure: e.status for
  // an HTTP error, e.unreadable for a body that is not JSON.
  function getJSON(url) {
    return fetch(url, { cache: "no-store", credentials: "same-origin" }).then(function (r) {
      if (!r.ok) { var e = new Error("HTTP " + r.status); e.status = r.status; throw e; }
      return r.json().catch(function (e) { e.unreadable = true; throw e; });
    });
  }
  function pollLinks() {
    if (!LINKS_API) return;
    return getJSON(LINKS_API).then(function (lk) {
      var changed = JSON.stringify(store.links) !== JSON.stringify(lk) || store.linksError;
      store.links = lk; store.linksError = "";
      if (changed) { renderHeader(); renderEfforts(); renderDetail(); }
    }, function (e) {
      // Keep the last links, but say they may be out of date.
      var msg = L.fetchFailure(e);
      if (msg !== store.linksError) { store.linksError = msg; renderHeader(); }
    });
  }
  function poll() {
    var done = function () { setTimeout(poll, POLL); };
    getJSON(API).then(applyPayload, function (e) {
      store.error = L.pollErrorText(e, !!store.snap);
      store.notify("status");
    }).then(pollLinks).then(done, function (e) {
      // A rendering fault, not a fetch failure: keep polling, and log it.
      if (window.console) console.error("wgo dash:", e);
      done();
    });
  }
  setInterval(function () { if (store.snap) renderHeader(); }, 5000);
  setView(/(^|[#&])view=table(&|$)/.test(location.hash) ? "table" : "graph");
  resize();
  buildLegend();
  renderAll(true);
  poll();
  window.__wgoStore = store; // handle for debugging and browser tests
})();
