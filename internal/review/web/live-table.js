/* wgo dash analysis table (spec/gh-101.md). An addon view, registered as
 * "table" through window.WGOLive, that replaces the live page's built-in
 * table: columns from the query field registry (cols= in the URL), a
 * multi-key sort (sort=-waiting,size), and word-sized graphics beside their
 * values — duration bars on a linear zero-based scale shared by the column,
 * a size bar on a shared log scale, one mark per CI check, review glyphs.
 * The keyboard model is the old table's: arrows move, Enter or Space
 * selects, Shift+Enter adds. Beyond 500 rows only the rows in view and a
 * margin are in the DOM.
 *
 * The pure logic is logic(WGOLive), also module.exports under node, so
 * internal/review/testdata/live_logic.js tests it without a page. Like
 * live.js it writes no markup from strings and issues no requests. */
(function () {
  "use strict";

  // ---------- pure logic ----------
  function logic(L) {
    var T = {};
    var hasOwn = Object.prototype.hasOwnProperty;
    function cmp(a, b) { return a < b ? -1 : a > b ? 1 : 0; }
    function trim(s) { return s.trim(); }
    // Sort text is compared as internal/dash/sort.go compares it: lowercased
    // a code point at a time as Go's strings.ToLower does (no final sigma,
    // İ to i), and in code point order, which is Go's UTF-8 byte order.
    // UTF-16 puts a surrogate pair (above U+FFFF) before U+E000-U+FFFF, so
    // cpKey prefixes each surrogate with U+FFFF to make < agree.
    var NON_ASCII = /[^\u0000-\u007f]/, SURROGATE = /[\ud800-\udfff]/g;
    function lower(s) {
      if (!NON_ASCII.test(s)) return s.toLowerCase();
      return Array.from(s).map(function (ch) { return ch === "\u0130" ? "i" : ch.toLowerCase(); }).join("");
    }
    function cpKey(s) { return NON_ASCII.test(s) ? s.replace(SURROGATE, "\uffff$&") : s; }
    T.sortText = function (s) { return cpKey(lower(String(s))); };

    // LOCAL columns are the table's own, worked out from a node by the page
    // rather than query fields, so the Markdown extract can neither print nor
    // sort by them. name is always the first column.
    var LOCAL = {
      name: { label: "Name", title: "Label and title" },
      state: { label: "State", title: "State: PR state, CI and review; ticket status; workspace bookmark; agent status" },
      data: { label: "Data", title: "Freshness of the node's data" },
      last_activity: { label: "Last activity", title: "Last local activity, or the PR's last update" }
    };
    T.LOCAL = LOCAL;
    // DEFAULTS are the columns after name for the rows' kinds (requirement 3);
    // a column whose field is not registered yet is left out.
    T.DEFAULTS = {
      pr: ["repo", "author", "move", "reason", "waiting", "age", "size", "ci", "reviewer", "ticket"],
      ticket: ["ticket_status", "assignee", "points", "sprint", "linked_prs", "mismatch"],
      mixed: ["kind", "effort", "state", "data", "last_activity"]
    };
    // text is label, title and ID: the name column already shows it.
    var NOT_COLUMNS = { text: true, name: true };
    // LABELS shorten registry labels that would widen a header.
    var LABELS = { reviewer: "Reviews", ticket_status: "Status", requested_from: "Requested from", linked_prs: "Linked PRs", mismatch: "Mismatch",
      updated_ago: "Since update", sprint: "Sprint", points: "Points", move: "Move", size: "Size" };

    // isColumn reports whether name can be a column after name.
    T.isColumn = function (reg, name) { return !NOT_COLUMNS[name] && (hasOwn.call(LOCAL, name) || !!reg.field(name)); };
    // colInfo describes a column: label, title, type and whether it is local.
    T.colInfo = function (reg, name) {
      if (hasOwn.call(LOCAL, name)) return { name: name, label: LOCAL[name].label, title: LOCAL[name].title, type: name === "last_activity" ? "date" : "text", local: true };
      var f = reg.field(name) || { label: name, type: "text" };
      var title = f.label + " (" + name + (f.unit ? ", " + f.unit : "") + ")";
      if (name === "size") title = "Size: lines added plus deleted. The bar is on a log scale shared by the column.";
      else if (f.type === "duration") title = f.label + " in days. The bar is linear from zero, on a scale shared by the column.";
      return { name: name, label: LABELS[name] || f.label, title: title, type: f.type, scale: f.scale || "", local: false };
    };
    T.numeric = function (info) { return info.type === "number" || info.type === "duration" || info.type === "date"; };

    // defaultKey names the default column set for the rows' kinds.
    T.defaultKey = function (kinds) { return kinds.length === 1 && (kinds[0] === "pr" || kinds[0] === "ticket") ? kinds[0] : "mixed"; };
    T.defaultColumns = function (reg, kinds) { return T.DEFAULTS[T.defaultKey(kinds)].filter(function (c) { return T.isColumn(reg, c); }); };
    // columns reads cols= (raw): the named columns that exist, or the
    // defaults when it is empty. cols=name means the name column alone.
    T.columns = function (reg, raw, kinds) {
      var names = String(raw || "").split(",").map(trim).filter(Boolean), out = [];
      if (!names.length) return { cols: T.defaultColumns(reg, kinds), custom: false };
      names.forEach(function (n) { if (T.isColumn(reg, n) && out.indexOf(n) < 0) out.push(n); });
      return { cols: out, custom: true };
    };
    // colsValue is cols= for a column list: "" when it is the defaults (so
    // they follow the rows' kinds), "name" when only the name column is left.
    T.colsValue = function (reg, cols, kinds) {
      if (cols.join(",") === T.defaultColumns(reg, kinds).join(",")) return "";
      return cols.length ? cols.join(",") : "name";
    };
    // available lists the columns the chooser offers for the rows' kinds: the
    // local ones, then every registered field that applies to one of them.
    T.available = function (reg, kinds) {
      var out = Object.keys(LOCAL).filter(function (n) { return !NOT_COLUMNS[n]; });
      reg.fields.forEach(function (f) {
        if (NOT_COLUMNS[f.name]) return;
        if (!kinds.length || f.kinds.some(function (k) { return kinds.indexOf(k) >= 0; })) out.push(f.name);
      });
      return out;
    };
    T.addColumn = function (cols, name) { return cols.indexOf(name) >= 0 ? cols.slice() : cols.concat([name]); };
    T.removeColumn = function (cols, name) { return cols.filter(function (c) { return c !== name; }); };
    // moveColumn moves name delta places (-1 left, +1 right), within bounds.
    T.moveColumn = function (cols, name, delta) {
      var i = cols.indexOf(name), out = cols.slice();
      if (i < 0) return out;
      var j = Math.max(0, Math.min(cols.length - 1, i + delta));
      out.splice(i, 1); out.splice(j, 0, name);
      return out;
    };

    // ---------- sort (requirements 5 and 6) ----------
    // parseSort reads sort=: keys in order, "-" for descending.
    T.parseSort = function (raw) {
      var out = [], seen = {};
      String(raw || "").split(",").map(trim).forEach(function (k) {
        var desc = k.charAt(0) === "-", key = desc ? k.slice(1) : k;
        if (!key || seen[key]) return;
        seen[key] = true; out.push({ key: key, desc: desc });
      });
      return out;
    };
    T.formatSort = function (sort) { return sort.map(function (s) { return (s.desc ? "-" : "") + s.key; }).join(","); };
    // sortKeys reads sort= into the keys the table sorts by: name and the
    // columns that exist (a stale key is dropped). Other views that follow
    // the table's order (gh-103) read it the same way.
    T.sortKeys = function (reg, raw) { return T.parseSort(raw).filter(function (s) { return s.key === "name" || T.isColumn(reg, s.key); }); };
    // defaultDesc: numbers, durations and dates sort largest or latest first.
    T.defaultDesc = function (reg, key) {
      if (key === "last_activity") return true;
      var f = reg.field(key);
      return !!f && (f.type === "number" || f.type === "duration" || f.type === "date");
    };
    // nextSort applies a header click. A plain click sorts by key alone, or
    // reverses key when it is already the first key (keeping the others); a
    // Shift+click adds key as the next key, reverses it, then removes it.
    T.nextSort = function (sort, key, add, desc0) {
      var i = sort.findIndex(function (s) { return s.key === key; });
      if (!add) {
        if (i === 0) return [{ key: key, desc: !sort[0].desc }].concat(sort.slice(1));
        return [{ key: key, desc: desc0 }];
      }
      if (i < 0) return sort.concat([{ key: key, desc: desc0 }]);
      if (sort[i].desc === desc0) return sort.map(function (s, j) { return j === i ? { key: key, desc: !s.desc } : s; });
      return sort.filter(function (s, j) { return j !== i; });
    };

    // Sort value classes, as internal/dash/sort.go: known values come first
    // in either direction, then known-empty ("none"), unknown, and rows the
    // field does not apply to.
    var KNOWN = 0, EMPTY = 1, UNKNOWN = 2, NA = 3;
    T.CLASS = { known: KNOWN, empty: EMPTY, unknown: UNKNOWN, na: NA };
    // LAST_ACTIVITY_KINDS have a last activity; for them a missing one is unknown.
    var LAST_ACTIVITY_KINDS = { workspace: true, agent: true, pr: true };
    function label(ix, i) { var l = ix.nodes[i] && ix.nodes[i].label; return typeof l === "string" && l ? l : ix.ids[i]; }
    T.label = label;
    // sortValue reduces a column's value on node i to {c, r, n | s}: the
    // first number, the latest day, a closed enum's position (then text), an
    // effort list's labels, or the lowercased text (sortValue in sort.go).
    T.sortValue = function (ix, name, i) {
      var n = ix.nodes[i];
      if (name === "name") {
        // The name column shows label and title: it sorts as the text field
        // (label, title, ID), which the extract can sort by too.
        if (ix.values.has("text")) return T.sortValue(ix, "text", i);
        return { c: KNOWN, r: 0, s: T.sortText(label(ix, i)) };
      }
      if (hasOwn.call(LOCAL, name)) {
        if (name === "last_activity") {
          var t = Date.parse(L.activityOf(n));
          if (isFinite(t)) return { c: KNOWN, r: 0, n: t };
          return { c: LAST_ACTIVITY_KINDS[n.kind] ? UNKNOWN : NA };
        }
        var s = T.sortText(name === "state" ? L.stateText(n) : L.dataText(n));
        return s ? { c: KNOWN, r: 0, s: s } : { c: EMPTY };
      }
      var f = ix.reg.field(name), vals = f && ix.values.get(name);
      if (!vals) return { c: NA };
      var v = vals[i];
      if (v.na || (v.unknown && name === "waiting" && T.waitingNA(n.pr && n.pr.facts))) return { c: NA };
      if (v.unknown) return { c: UNKNOWN };
      if (f.type === "number" || f.type === "duration") return v.nums.length ? { c: KNOWN, r: 0, n: v.nums[0] } : { c: EMPTY };
      if (f.derive === "efforts") {
        if (!ix.efforts[i].length) return { c: EMPTY };
        return { c: KNOWN, r: 0, s: T.sortText(ix.efforts[i].map(function (e) { return label(ix, e); }).join(",")) };
      }
      if (!v.norm.length) return { c: EMPTY };
      if (f.type === "date") return { c: KNOWN, r: 0, s: v.norm.reduce(function (a, b) { return b > a ? b : a; }) };
      if (f.type === "enum" && f.values && f.values.length) {
        var r = f.values.indexOf(v.norm[0]);
        return { c: KNOWN, r: r < 0 ? f.values.length : r, s: T.sortText(v.disp[0]) };
      }
      return { c: KNOWN, r: 0, s: T.sortText(v.disp.join(",")) };
    };
    // compareValues orders two sort values for one key; only known values
    // follow the direction.
    T.compareValues = function (a, b, desc) {
      if (a.c !== b.c) return a.c - b.c;
      if (a.c !== KNOWN) return 0;
      var c = a.r - b.r || (a.n != null ? cmp(a.n, b.n) : cmp(a.s, b.s));
      return desc ? -c : c;
    };
    // sortRows returns rows (node indices) sorted by the keys, then by
    // case-insensitive label and ID; with no keys by kind, label and ID
    // (the extract's default order). It does not change rows.
    T.sortRows = function (ix, rows, sort) {
      var keys = sort.map(function (s) { return rows.map(function (i) { return T.sortValue(ix, s.key, i); }); });
      var labels = rows.map(function (i) { return T.sortText(label(ix, i)); }), ids = rows.map(function (i) { return cpKey(ix.ids[i]); });
      var kinds = sort.length ? null : rows.map(function (i) { var k = L.KIND_ORDER[ix.kinds[i]]; return k != null ? k : 99; });
      var pos = rows.map(function (r, p) { return p; });
      pos.sort(function (a, b) {
        for (var k = 0; k < sort.length; k++) { var c = T.compareValues(keys[k][a], keys[k][b], sort[k].desc); if (c) return c; }
        if (kinds && kinds[a] !== kinds[b]) return kinds[a] - kinds[b];
        return cmp(labels[a], labels[b]) || cmp(ids[a], ids[b]) || a - b;
      });
      return pos.map(function (p) { return rows[p]; });
    };

    // ---------- rows (requirement 4) ----------
    // tableRows lists the rows: every node without a query; otherwise the
    // query's rows, plus the context rows (visible only because a match
    // leads to them) unless a kind: clause chooses the rows or showContext
    // is false. kinds are the kinds among the rows proper.
    T.tableRows = function (ix, res, hasKind, showContext) {
      var rows, context = [];
      if (!res) rows = ix.nodes.map(function (n, i) { return i; });
      else {
        rows = res.rowIdx.slice();
        if (!hasKind && res.vis) res.vis.forEach(function (id) { var i = ix.byId.get(id); if (i != null && !res.rowSet.has(id)) context.push(i); });
      }
      var seen = {};
      rows.forEach(function (i) { seen[ix.kinds[i]] = true; });
      var kinds = L.QUERY_KINDS.filter(function (k) { return seen[k]; });
      return { rows: showContext ? rows.concat(context) : rows, context: new Set(context), contextCount: context.length, kinds: kinds };
    };

    // ---------- word-sized graphics (requirements 7 to 11) ----------
    // barFraction is a bar's length as a fraction of its column's: linear
    // from zero, or log(1+v) over log(1+max) for sizes.
    T.barFraction = function (v, max, log) {
      if (!(v > 0) || !(max > 0)) return 0;
      return Math.min(1, log ? Math.log(1 + v) / Math.log(1 + max) : v / max);
    };
    T.daysText = function (d, lowerBound) { return (lowerBound ? "≥" : "") + Math.floor(d) + "d"; };
    T.daysLabel = function (name, d, lowerBound) {
      var n = Math.floor(d);
      return name + ": " + (lowerBound ? "at least " : "") + n + (n === 1 ? " day" : " days");
    };
    function int(v) { return typeof v === "number" && isFinite(v) ? v : null; }
    // sizeText is "+120 −30" when additions and deletions are known, else
    // the size in lines.
    T.sizeText = function (pr, size) {
      var a = int(pr && pr.additions), d = int(pr && pr.deletions);
      return a != null && d != null ? "+" + a + " −" + d : size + " lines";
    };
    T.sizeLabel = function (pr, size) {
      var a = int(pr && pr.additions), d = int(pr && pr.deletions);
      return "size " + size + (size === 1 ? " line" : " lines") + (a != null && d != null ? " (" + a + " added, " + d + " deleted)" : "") + "; bar on a log scale";
    };

    var CI_ORDER = { fail: 0, systemic: 1, pending: 2, pass: 3 };
    var CHECK_RANK = { failure: 4, pending: 3, success: 2, skipped: 1 };
    T.CI_MAX = 20;
    var COLLATOR = new Intl.Collator("en", { numeric: true });
    function natural(a, b) { return COLLATOR.compare(String(a), String(b)) || cmp(a, b); }
    // ciMarks lays out one mark per check name, failures first: failing, then
    // failing but systemic in its repository (gh-99's check_stats), pending,
    // passing (a neutral check passes); skipped checks are left out. At most
    // CI_MAX marks, then more. null when the PR's checks are unknown.
    T.ciMarks = function (pr, checkStats) {
      var details = pr && pr.check_details;
      if (!Array.isArray(details)) return null;
      var systemic = new Set(), stats = checkStats && typeof pr.repo === "string" ? checkStats[pr.repo.toLowerCase()] : null;
      (Array.isArray(stats) ? stats : []).forEach(function (s) { if (s && s.systemic === true && typeof s.name === "string") systemic.add(s.name); });
      // One mark per check name: a check run on both push and pull_request
      // is listed twice. As gh-99's checkStates, the worst run wins.
      var byName = new Map();
      details.forEach(function (d) {
        var st = String((d && d.state) || "").toLowerCase(), name = String((d && d.name) || "check");
        if (st === "neutral") st = "success";
        var prev = byName.get(name);
        if (!prev || (CHECK_RANK[st] || 0) > (CHECK_RANK[prev.state] || 0)) byName.set(name, { state: st, url: d && d.url });
      });
      var marks = [], n = { fail: 0, systemic: 0, pending: 0, pass: 0 };
      byName.forEach(function (d, name) {
        var st = d.state, kind;
        if (st === "skipped") return;
        if (st === "failure") kind = systemic.has(name) ? "systemic" : "fail";
        else if (st === "success") kind = "pass";
        else kind = "pending";
        n[kind]++;
        marks.push({ kind: kind, name: name, state: st || "unknown", url: d.url });
      });
      marks.sort(function (a, b) { return CI_ORDER[a.kind] - CI_ORDER[b.kind] || natural(a.name, b.name); });
      var parts = [], failing = n.fail + n.systemic;
      if (failing) parts.push(failing + " failing" + (n.systemic ? " (" + n.systemic + (n.systemic === 1 ? " fails" : " fail") + " everywhere)" : ""));
      if (n.pending) parts.push(n.pending + " pending");
      if (n.pass) parts.push(n.pass + " passing");
      var lbl = parts.length ? parts.join(", ") : "no checks reported";
      if (pr.checks_truncated) lbl += ", and more checks than were read";
      return { marks: marks.slice(0, T.CI_MAX), more: Math.max(0, marks.length - T.CI_MAX), counts: n, label: lbl };
    };

    var REVIEW_STATE = { APPROVED: "approved", CHANGES_REQUESTED: "changes", COMMENTED: "commented" };
    T.REVIEW = {
      changes: { glyph: "✕", text: "changes requested", order: 0 },
      requested: { glyph: "○", text: "review requested", order: 1 },
      commented: { glyph: "…", text: "commented", order: 2 },
      approved: { glyph: "✓", text: "approved", order: 3 }
    };
    // reviewMarks is one glyph per reviewer: an outstanding request (a team
    // as team:<slug>) wins over that reviewer's earlier review, then each
    // latest review; dismissed reviews are left out. requestsKnown is false
    // when only the reviews were read. null when neither is known.
    T.reviewMarks = function (pr) {
      if (!pr) return null;
      var reqs = null;
      if (Array.isArray(pr.review_requests)) reqs = pr.review_requests.map(function (r) { return r && r.reviewer; });
      else if (pr.reviewers_known && Array.isArray(pr.requested_reviewers)) reqs = pr.requested_reviewers.slice();
      var reviews = Array.isArray(pr.reviews) ? pr.reviews : null;
      if (!reqs && !reviews) return null;
      var out = [], at = {};
      (reqs || []).forEach(function (who) {
        if (typeof who !== "string" || !who || hasOwn.call(at, who)) return;
        at[who] = out.length; out.push({ who: who, state: "requested", after: "" });
      });
      (reviews || []).forEach(function (r) {
        var who = r && r.author, st = REVIEW_STATE[String((r && r.state) || "").toUpperCase()];
        if (typeof who !== "string" || !who || !st) return;
        if (hasOwn.call(at, who)) { if (out[at[who]].state === "requested") out[at[who]].after = T.REVIEW[st].text; return; }
        at[who] = out.length; out.push({ who: who, state: st, after: "" });
      });
      out.sort(function (a, b) { return T.REVIEW[a.state].order - T.REVIEW[b.state].order || cmp(a.who.toLowerCase(), b.who.toLowerCase()); });
      var lbl = out.map(function (m) { return m.who + " " + T.REVIEW[m.state].text + (m.after ? " (again, after " + m.after + ")" : ""); }).join(", ");
      if (!lbl) lbl = "no reviewers";
      if (!reqs) lbl += "; requested reviewers unknown";
      return { marks: out, requestsKnown: !!reqs, label: "Reviews: " + lbl };
    };

    // waitingNA: gh-99 sets waiting_since only for the reviewer and nobody
    // moves and for an author move decided by changes requested (the
    // blocking review's time). A known author or none move without it is
    // not waiting on anyone, which is "does not apply", not unknown
    // (waitingNA in internal/dash/sort.go).
    T.waitingNA = function (facts) {
      var m = facts && facts.move;
      if (m === "none") return true;
      if (m !== "author") return false;
      return !(Array.isArray(facts.reasons) ? facts.reasons : []).some(function (r) { return r && r.code === "changes_requested"; });
    };

    // moveWord and moveTitle, the Move column's word and the details behind
    // it, are WGOLive's: the sprint view's PR tooltips use them too.
    T.moveWord = L.moveWord;
    T.moveTitle = L.moveTitle;
    return T;
  }

  if (typeof module !== "undefined" && module.exports) module.exports = logic;
  if (typeof window === "undefined" || !window.WGOLive || typeof document === "undefined") return;

  // ---------- the view ----------
  var L = window.WGOLive, T = logic(L);
  // Views that order their marks as the table orders its rows (gh-103's
  // scatterplot, gh-104's timeline) use this same logic rather than a copy.
  L.tableLogic = T;
  var VIRTUAL_MIN = 500, MARGIN = 15, SHOW_REVIEWERS = 3, SHOW_ITEMS = 2;
  var cur = null; // the latest ctx: handlers read the live store through it
  var V = null; // the view's DOM and model, built once per container

  function textOf(name) { return String(name).replace(/_/g, " "); }

  function build(ctx) {
    var dom = ctx.dom, el = dom.el, host = ctx.container;
    host.textContent = "";
    host.classList.add("atable-host");
    V = { host: host, focus: -1, focusId: null, chooserOpen: false, rowH: 24, model: null, win: [0, 0], keyShift: 0, restore: null, raf: 0 };
    V.bar = el("div", "atable-bar", null, host);
    V.summary = el("span", "atable-sum", null, V.bar);
    V.colBtn = dom.button(V.bar, "Columns…", null, function () { V.chooserOpen = !V.chooserOpen; paintChooser(); if (V.chooserOpen) focusFirst(V.chooser); });
    V.colBtn.setAttribute("aria-expanded", "false"); V.colBtn.setAttribute("aria-controls", "atable-chooser");
    V.colBtn.title = "Choose, order and remove columns (kept in the URL as cols=)";
    V.ctxBtn = dom.button(V.bar, "", null, function () { cur.setState({ ctx: V.model.showCtx ? "hide" : "" }, { commit: true }); });
    // It hides itself once clicked, so the keyboard goes to the first header.
    V.sortBtn = dom.button(V.bar, "Default order", null, function () { V.restore = { head: "name" }; cur.setState({ sort: "" }, { commit: true }); });
    V.sortBtn.title = "Clear the sort: kind, then name";
    V.chooser = el("div", "atable-chooser", null, host); V.chooser.id = "atable-chooser"; V.chooser.hidden = true;
    V.chooser.setAttribute("role", "group"); V.chooser.setAttribute("aria-label", "Columns");
    V.scroll = el("div", "atable-scroll", null, host);
    // The scroller holds the keyboard when a wheel scroll draws the focused
    // row away; arrows then resume from that row (V.focus).
    V.scroll.tabIndex = -1;
    V.table = el("table", "live atable", null, V.scroll); V.table.setAttribute("role", "grid");
    el("caption", "sr", "Rows of the current query. Arrow keys move, Enter or Space selects, Shift+Enter adds. Column headers sort: Enter sorts, Shift+Enter adds a sort key.", V.table);
    V.thead = el("thead", null, null, V.table);
    V.tbody = el("tbody", null, null, V.table);

    V.thead.addEventListener("keydown", function (ev) { if (ev.key === "Enter" || ev.key === " ") V.keyShift = ev.shiftKey ? Date.now() : 0; });
    V.thead.addEventListener("click", function (ev) {
      var b = ev.target.closest("button[data-col]"); if (!b) return;
      var add = ev.shiftKey || Date.now() - V.keyShift < 500; V.keyShift = 0;
      var key = b.dataset.col, next = T.nextSort(V.model.sort, key, add, T.defaultDesc(cur.registry, key));
      V.restore = { head: key };
      cur.setState({ sort: T.formatSort(next) }, { commit: true });
    });
    V.tbody.addEventListener("click", function (ev) {
      if (ev.target.closest("a")) return;
      var row = ev.target.closest("tr[data-i]"); if (!row) return;
      setFocus(+row.dataset.i); rove();
      cur.select([rowId(V.focus)], ev.shiftKey || ev.metaKey);
    });
    V.tbody.addEventListener("keydown", onRowKey);
    V.scroll.addEventListener("keydown", function (ev) { if (ev.target === V.scroll) rowKey(ev, Math.max(0, V.focus)); });
    V.tbody.addEventListener("focusin", function (ev) { var row = ev.target.closest && ev.target.closest("tr[data-i]"); if (row) setFocus(+row.dataset.i); });
    V.tbody.addEventListener("mouseover", function (ev) { var row = ev.target.closest("tr[data-i]"); hover(row ? rowId(+row.dataset.i) : null); });
    V.table.addEventListener("mouseleave", function () { hover(null); });
    V.scroll.addEventListener("scroll", function () {
      if (!V.model || !V.model.virtual || V.raf) return;
      V.raf = requestAnimationFrame(function () { V.raf = 0; var w = windowFor(V.model.rows.length); if (w[0] !== V.win[0] || w[1] !== V.win[1]) paintBody(); });
    });
    V.chooser.addEventListener("click", onChooserClick);
    V.chooser.addEventListener("keydown", function (ev) { if (ev.key === "Escape") { ev.preventDefault(); ev.stopPropagation(); V.chooserOpen = false; paintChooser(); V.colBtn.focus(); } });
  }

  function rowId(p) { return V.model.ix.ids[V.model.rows[p]]; }
  // setFocus records the keyboard's row by position and node, so a re-sort
  // or a new snapshot keeps it on the same node.
  function setFocus(p) { V.focus = p; V.focusId = p >= 0 && p < V.model.rows.length ? rowId(p) : null; }
  // rove gives the focus row the only tabindex 0 among the drawn rows.
  function rove() { Array.prototype.forEach.call(V.tbody.querySelectorAll("tr[data-i]"), function (tr) { tr.tabIndex = +tr.dataset.i === V.focus ? 0 : -1; }); }
  // hover marks a row's node in the graph and charts through the store.
  function hover(id) {
    if (cur.hover) cur.hover(id);
    else if (cur.store.hover !== id) cur.store.hover = id;
  }

  // model computes the rows, columns, sort and column scales for ctx.
  function model(ctx) {
    // ctx.query.rowKinds says whether a kind: clause chooses the rows, and
    // which kinds it allows: the default columns of a query with no rows.
    var ix = ctx.queryIndex, reg = ctx.registry, res = ctx.query, hasKind = !!res && res.rowKinds != null;
    var showCtx = ctx.getState("ctx") !== "hide";
    var tr = T.tableRows(ix, res, hasKind, showCtx);
    var kinds = tr.kinds.length ? tr.kinds : hasKind ? res.rowKinds : [];
    var cs = T.columns(reg, ctx.getState("cols"), kinds);
    var sort = T.sortKeys(reg, ctx.getState("sort"));
    var m = { ix: ix, reg: reg, kinds: kinds, cols: cs.cols, custom: cs.custom, sort: sort, hasKind: hasKind, showCtx: showCtx,
      context: tr.context, ctxCount: tr.contextCount, nRows: tr.rows.length - (showCtx ? tr.contextCount : 0), scales: {} };
    m.rows = T.sortRows(ix, tr.rows, sort);
    m.virtual = m.rows.length > VIRTUAL_MIN;
    m.info = {};
    ["name"].concat(m.cols).forEach(function (c) { m.info[c] = T.colInfo(reg, c); });
    m.cols.forEach(function (c) {
      var info = m.info[c], vals = !info.local && ix.values.get(c);
      if (!vals || (info.type !== "duration" && c !== "size")) return;
      var max = 0;
      m.rows.forEach(function (i) { var v = vals[i]; if (!v.na && !v.unknown && v.nums.length && v.nums[0] > max) max = v.nums[0]; });
      m.scales[c] = max;
    });
    return m;
  }

  function render(ctx) {
    cur = ctx;
    if (!V || V.host !== ctx.container) build(ctx);
    if (!ctx.queryIndex) return;
    if (ctx.reason === "selection" && V.model && V.model.ix === ctx.queryIndex) { refreshSelection(); return; }
    var hadFocus = V.tbody.contains(document.activeElement);
    V.model = model(ctx);
    var ids = V.model.ix.ids, at = V.focusId == null ? -1 : V.model.rows.findIndex(function (i) { return ids[i] === V.focusId; });
    if (at >= 0) V.focus = at;
    else if (V.focus >= V.model.rows.length) V.focus = V.model.rows.length - 1;
    if (V.focus < 0 && V.model.rows.length) {
      var sel = cur.store.selection, first = V.model.rows.findIndex(function (i) { return sel.has(ids[i]); });
      V.focus = first >= 0 ? first : 0;
    }
    setFocus(V.focus);
    // A re-sort, Back or a new snapshot may move the focused node: bring it
    // into the drawn window first, so the keyboard stays on it.
    if (hadFocus && V.model.virtual && V.focus >= 0) scrollToRow(V.focus);
    paintBar(); paintChooser(); paintHead(); paintBody();
    if (hadFocus) focusRow(V.focus);
    else if (V.restore && V.restore.head) { var b = V.thead.querySelector("button[data-col=\"" + V.restore.head + "\"]"); if (b) b.focus(); }
    V.restore = null;
  }

  // ---------- bar and chooser ----------
  function sortText(m) {
    if (!m.sort.length) return "default order: kind, then name";
    return "sorted by " + m.sort.map(function (s) { return T.colInfo(m.reg, s.key).label + (s.desc ? " ▼" : " ▲"); }).join(", then ");
  }
  function paintBar() {
    var m = V.model, plural = cur.dom.plural;
    V.summary.textContent = plural(m.nRows, "row") + (m.showCtx && m.ctxCount ? " + " + plural(m.ctxCount, "context row") + " (dimmed)" : "") + " · " + sortText(m) + (m.virtual ? " · drawing the rows in view" : "");
    V.ctxBtn.hidden = !m.ctxCount;
    V.ctxBtn.textContent = (m.showCtx ? "Hide " : "Show ") + plural(m.ctxCount, "context row");
    V.ctxBtn.title = "Context rows are not matches: the query keeps them because a match leads to them or from them. A kind: clause hides them.";
    V.sortBtn.hidden = !m.sort.length;
  }
  function focusFirst(box) { var b = box.querySelector("button:not([disabled])"); if (b) b.focus(); }
  function paintChooser() {
    var box = V.chooser, m = V.model, el = cur.dom.el, button = cur.dom.button;
    box.hidden = !V.chooserOpen;
    V.colBtn.setAttribute("aria-expanded", String(V.chooserOpen));
    V.colBtn.textContent = "Columns (" + (m.cols.length + 1) + ")…";
    if (!V.chooserOpen) return;
    var active = document.activeElement, again = active && box.contains(active) ? { act: active.dataset.act, col: active.dataset.col, at: active.dataset.at } : null;
    box.textContent = "";
    var shown = el("div", "shown", null, box);
    el("h3", null, "Shown, in order", shown);
    var ol = el("ol", null, null, shown);
    el("li", "fixed", "Name (always first)", ol);
    m.cols.forEach(function (c, k) {
      var info = T.colInfo(cur.registry, c), li = el("li", null, null, ol);
      el("span", "cl", info.label, li).title = info.title;
      var left = button(li, "←", null, null, "Move " + info.label + " left"), right = button(li, "→", null, null, "Move " + info.label + " right"), rm = button(li, "✕", null, null, "Remove " + info.label);
      left.dataset.act = "left"; right.dataset.act = "right"; rm.dataset.act = "remove";
      [left, right, rm].forEach(function (b) { b.dataset.col = c; b.dataset.at = String(k); });
      left.disabled = k === 0; right.disabled = k === m.cols.length - 1;
    });
    var add = el("div", "add", null, box);
    el("h3", null, "Add a column" + (m.kinds.length ? " (fields for " + m.kinds.join(", ") + ")" : ""), add);
    var ul = el("ul", null, null, add), left = T.available(cur.registry, m.kinds).filter(function (c) { return m.cols.indexOf(c) < 0; });
    left.forEach(function (c, k) {
      var info = T.colInfo(cur.registry, c), b = button(el("li", null, null, ul), "+ " + info.label, null, null, "Add the " + info.label + " column");
      b.title = info.title; b.dataset.act = "add"; b.dataset.col = c; b.dataset.at = String(k);
    });
    if (!left.length) el("li", "empty", "Every column is shown.", ul);
    var foot = el("div", "foot", null, box);
    var reset = button(foot, "Reset to defaults", null, null); reset.dataset.act = "reset"; reset.disabled = !m.custom;
    reset.title = "The default columns for these rows (" + T.defaultKey(m.kinds) + ")";
    button(foot, "Done", null, null).dataset.act = "done";
    if (again) {
      // Keep the keyboard where it was: the same button, or its neighbour
      // once the column moved away or was removed.
      var sel = again.act === "left" || again.act === "right" ? "button[data-act=\"" + again.act + "\"][data-col=\"" + again.col + "\"]" : null;
      var b = sel ? box.querySelector(sel) : null;
      if (b && b.disabled) b = box.querySelector("button[data-act=\"" + (again.act === "left" ? "right" : "left") + "\"][data-col=\"" + again.col + "\"]");
      if (!b && again.act) {
        var list = box.querySelectorAll("button[data-act=\"" + again.act + "\"]:not([disabled])");
        b = list[Math.min(+again.at || 0, list.length - 1)] || null;
      }
      if (!b) b = box.querySelector("button[data-act=\"done\"]");
      if (b) b.focus();
    }
  }
  function onChooserClick(ev) {
    var b = ev.target.closest("button[data-act]"); if (!b || b.disabled) return;
    var m = V.model, act = b.dataset.act, c = b.dataset.col, cols = m.cols;
    if (act === "done") { V.chooserOpen = false; paintChooser(); V.colBtn.focus(); return; }
    if (act === "reset") { cur.setState({ cols: "" }, { commit: true }); return; }
    if (act === "left" || act === "right") cols = T.moveColumn(cols, c, act === "left" ? -1 : 1);
    else if (act === "remove") cols = T.removeColumn(cols, c);
    else if (act === "add") cols = T.addColumn(cols, c);
    cur.setState({ cols: T.colsValue(cur.registry, cols, m.kinds) }, { commit: true });
  }

  // ---------- header ----------
  function paintHead() {
    var m = V.model, el = cur.dom.el;
    V.thead.textContent = "";
    var tr = el("tr", null, null, V.thead);
    V.table.setAttribute("aria-rowcount", String(m.rows.length + 1));
    V.table.setAttribute("aria-colcount", String(m.cols.length + 1));
    ["name"].concat(m.cols).forEach(function (c) {
      var info = m.info[c], th = el("th", T.numeric(info) ? "num" : null, null, tr), k = m.sort.findIndex(function (s) { return s.key === c; });
      th.scope = "col";
      th.setAttribute("aria-sort", k === 0 ? (m.sort[0].desc ? "descending" : "ascending") : "none");
      var b = cur.dom.button(th, null, null, null);
      b.dataset.col = c;
      el("span", null, info.label, b);
      var dir = k >= 0 ? (m.sort[k].desc ? "descending" : "ascending") : "";
      if (k >= 0) {
        var si = el("span", "si", m.sort[k].desc ? "▼" : "▲", b);
        if (m.sort.length > 1) el("sup", null, String(k + 1), si);
      }
      b.title = info.title + "\nClick to sort, again to reverse; Shift+click adds it as another sort key." + (info.local && c !== "name" ? "\nThis column is the table's own: Copy as Markdown can neither show it nor sort by it." : "");
      b.setAttribute("aria-label", info.label + (info.scale === "log" && c === "size" ? ", log scale" : "") + (k >= 0 ? ", sort key " + (k + 1) + " of " + m.sort.length + ", " + dir : ", not sorted"));
    });
  }

  // ---------- body ----------
  function windowFor(n) {
    if (!V.model.virtual) return [0, n];
    var top = V.scroll.scrollTop, h = V.scroll.clientHeight || 600, rh = V.rowH;
    return [Math.max(0, Math.floor(top / rh) - MARGIN), Math.min(n, Math.ceil((top + h) / rh) + MARGIN)];
  }
  function spacer(parent, rows) {
    var tr = cur.dom.el("tr", "spacer", null, parent), td = cur.dom.el("td", null, null, tr);
    tr.setAttribute("aria-hidden", "true");
    td.colSpan = V.model.cols.length + 1;
    td.style.height = (rows * V.rowH) + "px";
  }
  function paintBody() {
    var m = V.model, n = m.rows.length, el = cur.dom.el, w = windowFor(n), frag = document.createDocumentFragment();
    var hadFocus = V.tbody.contains(document.activeElement);
    V.win = w;
    V.tbody.textContent = "";
    if (w[0] > 0) spacer(frag, w[0]);
    var tab = V.focus >= w[0] && V.focus < w[1] ? V.focus : w[0];
    for (var p = w[0]; p < w[1]; p++) frag.appendChild(rowEl(p, p === tab));
    if (w[1] < n) spacer(frag, n - w[1]);
    V.tbody.appendChild(frag);
    if (!n) { var td = el("td", "empty", cur.query ? "Nothing matches the query." : "No nodes.", el("tr", null, null, V.tbody)); td.colSpan = m.cols.length + 1; }
    // Measure the real row height once, for the virtual window.
    var r = V.tbody.querySelector("tr[data-i]");
    if (r && r.offsetHeight && Math.abs(r.offsetHeight - V.rowH) > 0.5) { V.rowH = r.offsetHeight; if (m.virtual) { paintBody(); return; } }
    // Redrawing the window must not drop the keyboard or move it to another
    // node: refocus the focused row, or, when a scroll drew it away, park
    // the keyboard on the scroller (arrows resume from V.focus).
    if (hadFocus && n) {
      var f = V.tbody.querySelector("tr[data-i=\"" + V.focus + "\"]");
      if (f) { rove(); f.focus({ preventScroll: true }); } else V.scroll.focus({ preventScroll: true });
    }
  }
  function refreshSelection() {
    var sel = cur.store.selection;
    Array.prototype.forEach.call(V.tbody.querySelectorAll("tr[data-i]"), function (tr) {
      var on = sel.has(rowId(+tr.dataset.i));
      tr.classList.toggle("sel", on); tr.setAttribute("aria-selected", String(on));
    });
  }
  // scrollToRow scrolls a virtual table so row p is in view below the
  // sticky header.
  function scrollToRow(p) {
    var head = V.thead.offsetHeight || V.rowH, top = p * V.rowH, s = V.scroll;
    if (top < s.scrollTop) s.scrollTop = top;
    else if (top + V.rowH + head > s.scrollTop + s.clientHeight) s.scrollTop = top + V.rowH + head - s.clientHeight;
  }
  // focusRow focuses row p, scrolling it into view (and into the virtual
  // window) first.
  function focusRow(p) {
    if (p < 0 || !V.model.rows.length) return;
    setFocus(p);
    if (V.model.virtual) {
      scrollToRow(p);
      var w = windowFor(V.model.rows.length);
      if (w[0] !== V.win[0] || w[1] !== V.win[1]) paintBody();
    }
    rove();
    var row = V.tbody.querySelector("tr[data-i=\"" + p + "\"]");
    if (row) { row.focus({ preventScroll: V.model.virtual }); if (!V.model.virtual && row.scrollIntoView) row.scrollIntoView({ block: "nearest" }); }
  }
  function onRowKey(ev) {
    var row = ev.target.closest("tr[data-i]"); if (!row || ev.target !== row) return;
    rowKey(ev, +row.dataset.i);
  }
  // rowKey applies a key to row p: Enter or Space selects (Shift adds),
  // arrows and paging move, selecting unless Shift is held.
  function rowKey(ev, p) {
    if (!V.model || !V.model.rows.length) return;
    if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); focusRow(p); cur.select([rowId(p)], ev.shiftKey); return; }
    var nx = L.nextIndex(V.model.rows.length, p, ev.key);
    if (nx < 0) return;
    ev.preventDefault();
    focusRow(nx);
    if (!ev.shiftKey) cur.select([rowId(nx)], false, "replace");
  }

  function rowEl(p, tabbable) {
    var m = V.model, el = cur.dom.el, i = m.rows[p], id = m.ix.ids[i], n = m.ix.nodes[i];
    var sel = cur.store.selection.has(id), context = m.context.has(i);
    var tr = el("tr", (sel ? "sel" : "") + (context ? " ctx" : "") || null, null, null);
    tr.dataset.i = String(p); tr.tabIndex = tabbable ? 0 : -1;
    tr.setAttribute("aria-selected", String(sel)); tr.setAttribute("aria-rowindex", String(p + 2));
    if (context) tr.title = "Context: shown because a match leads to it or from it";
    nameCell(tr, n);
    for (var k = 0; k < m.cols.length; k++) cell(tr, m.cols[k], i, n);
    return tr;
  }
  function nameCell(tr, n) {
    var el = cur.dom.el, td = el("td", "nm", null, tr), url = (n.pr && n.pr.url) || (n.ticket && n.ticket.url) || "";
    var title = (n.pr && n.pr.title) || (n.ticket && n.ticket.title) || "";
    if (L.safeURL(url)) cur.dom.link(td, n.label || n.id, url);
    else el("span", "lbl", n.label || n.id, td);
    if (title) el("span", "ttl", title, td);
    td.title = (n.label || n.id) + (title ? " — " + title : "");
  }
  function unknown(td) { td.classList.add("unk"); td.textContent = "unknown"; }
  function cell(tr, c, i, n) {
    var m = V.model, info = m.info[c], el = cur.dom.el, td = el("td", T.numeric(info) ? "num" : null, null, tr);
    if (info.local) return localCell(td, c, n);
    var v = m.ix.values.get(c)[i];
    if (v.na) { td.classList.add("na"); td.textContent = "—"; td.title = info.label + " does not apply to a " + n.kind; return; }
    var r = RENDER[c] || (info.type === "duration" ? durationCell : null);
    if (r) return r(td, c, i, n, v);
    if (v.unknown) return unknown(td);
    td.textContent = L.valueText(m.ix, c, m.ix.ids[i]);
    if (info.type === "set" || info.type === "text") td.title = td.textContent;
  }
  function localCell(td, c, n) {
    var t;
    if (c === "state") t = L.stateText(n);
    else if (c === "data") t = L.dataText(n);
    else {
      var a = L.activityOf(n);
      if (a) { td.textContent = L.relTime(a, Date.now()) || "unknown"; td.title = a; return; }
      if ({ workspace: 1, agent: 1, pr: 1 }[n.kind]) return unknown(td);
      td.classList.add("na"); td.textContent = "—"; td.title = "A " + n.kind + " has no activity of its own"; return;
    }
    td.textContent = t || "—";
    if (t) td.title = t;
  }

  // graphic is a word-sized graphic: role img with an aria-label sentence
  // (the text beside the marks says the same in short).
  function graphic(td, cls, labelText) {
    var g = cur.dom.el("span", "g " + cls, null, td);
    g.setAttribute("role", "img"); g.setAttribute("aria-label", labelText); g.title = labelText;
    return g;
  }
  function bar(g, frac) {
    var b = cur.dom.el("span", "gbar", null, g), f = cur.dom.el("span", "fill", null, b);
    f.style.width = (frac * 100).toFixed(1) + "%";
  }
  // none is a known-empty value: reported, and nothing in it.
  function none(td) { td.textContent = "none"; }
  function durationCell(td, c, i, n, v) {
    var facts = n.pr && n.pr.facts;
    if (v.unknown && c === "waiting" && T.waitingNA(facts)) {
      var why = "not waiting on a reviewer: whose move is " + facts.move;
      td.classList.add("na"); td.textContent = "—"; td.title = why; td.setAttribute("aria-label", why);
      return;
    }
    if (v.unknown) return unknown(td);
    if (!v.nums.length) return none(td);
    var lower = c === "waiting" && !!facts && facts.waiting_lower_bound === true, info = V.model.info[c];
    var g = graphic(td, "dur", T.daysLabel(info.label, v.nums[0], lower));
    bar(g, T.barFraction(v.nums[0], V.model.scales[c], false));
    cur.dom.el("span", "v", T.daysText(v.nums[0], lower), g);
  }
  var RENDER = {
    size: function (td, c, i, n, v) {
      if (v.unknown) return unknown(td);
      if (!v.nums.length) return none(td);
      var g = graphic(td, "size", T.sizeLabel(n.pr, v.nums[0]));
      bar(g, T.barFraction(v.nums[0], V.model.scales.size, true));
      cur.dom.el("span", "v", T.sizeText(n.pr, v.nums[0]), g);
    },
    move: function (td, c, i, n) {
      var facts = n.pr && n.pr.facts, w = T.moveWord(facts, V.model.ix.env.me);
      if (w.unknown) unknown(td);
      else { var s = cur.dom.el("span", "mv" + (w.mine ? " mine" : ""), w.word, td); if (w.mine) s.setAttribute("aria-label", "your move (" + w.move + ")"); }
      td.title = T.moveTitle(facts);
    },
    reason: function (td, c, i, n, v) {
      var reasons = n.pr && n.pr.facts && n.pr.facts.reasons;
      if (v.unknown || !Array.isArray(reasons)) return unknown(td);
      if (!reasons.length) return none(td);
      var labels = reasons.map(function (r) { return textOf(r.code); });
      var evidence = reasons.map(function (r) { return textOf(r.code) + ": " + r.evidence; }).join("\n");
      td.textContent = labels.join(", ");
      td.title = evidence;
      td.setAttribute("aria-label", "Reasons: " + evidence.replace(/\n/g, "; "));
      var row = td.parentNode;
      row.title = (row.title ? row.title + "\n" : "") + evidence;
      row.setAttribute("aria-label", (n.label || n.id) + "; reasons: " + evidence.replace(/\n/g, "; "));
    },
    ci: function (td, c, i, n, v) {
      var el = cur.dom.el, ci = T.ciMarks(n.pr, cur.snapshot && cur.snapshot.check_stats);
      var word = v.unknown ? "unknown" : textOf(L.valueText(V.model.ix, c, V.model.ix.ids[i]));
      if (!ci) {
        if (v.unknown) return unknown(td);
        td.textContent = word; td.title = "CI " + word + "; per-check results were not read"; return;
      }
      var g = graphic(td, "ci", "CI " + word + ": " + ci.label), marks = el("span", "marks", null, g);
      var glyph = { fail: "×", systemic: "~", pending: "·", pass: "✓" };
      ci.marks.forEach(function (mk) { el("span", "m " + mk.kind, glyph[mk.kind], marks); });
      if (ci.more) el("span", "more", "+" + ci.more, g);
      el("span", "v" + (v.unknown ? " unk" : ""), word, g);
      var failing = ci.marks.filter(function (mk) { return mk.kind === "fail" || mk.kind === "systemic"; }).map(function (mk) { return mk.name + (mk.kind === "systemic" ? " (fails everywhere)" : ""); });
      g.title = "CI " + word + ": " + ci.label + (failing.length ? "\nFailing: " + failing.join(", ") : "");
    },
    reviewer: function (td, c, i, n, v) {
      var el = cur.dom.el, rv = T.reviewMarks(n.pr);
      if (v.unknown) return unknown(td);
      // No glyphs: the field's own value, as it sorts (none, or the people
      // involvement names), with what is unknown in the tooltip.
      if (!rv || !rv.marks.length) { td.textContent = L.valueText(V.model.ix, c, V.model.ix.ids[i]); td.title = rv ? rv.label : "Reviews: requested reviewers and reviews unknown"; return; }
      var g = graphic(td, "rv", rv.label);
      rv.marks.slice(0, SHOW_REVIEWERS).forEach(function (mk) {
        var r = el("span", "r " + mk.state, null, g);
        el("span", "gl", T.REVIEW[mk.state].glyph, r);
        r.appendChild(document.createTextNode(mk.who));
      });
      if (rv.marks.length > SHOW_REVIEWERS) el("span", "more", "+" + (rv.marks.length - SHOW_REVIEWERS), g);
    },
    effort: function (td, c, i) {
      var ix = V.model.ix, labels = ix.efforts[i].map(function (e) { return T.label(ix, e); });
      if (!labels.length) return none(td);
      td.textContent = labels.join(", "); td.title = td.textContent;
    },
    sprint: function (td, c, i, n, v) {
      var sp = n.ticket && n.ticket.sprints;
      if (v.unknown || !Array.isArray(sp)) return unknown(td);
      var t = sp.map(function (s) { return (s.name || "sprint " + s.id) + (s.state ? " (" + s.state + ")" : ""); });
      if (!t.length) return none(td);
      td.textContent = t.join(", "); td.title = td.textContent;
    },
    linked_prs: function (td, c, i, n, v) {
      var l = n.ticket && n.ticket.facts && n.ticket.facts.linked_prs;
      if (v.unknown || !Array.isArray(l)) return unknown(td);
      var t = l.map(function (x) { return (x.ref || x.id) + " " + (x.state || "unknown"); });
      if (!t.length) return none(td);
      td.textContent = t.slice(0, SHOW_ITEMS).join(", ") + (t.length > SHOW_ITEMS ? " +" + (t.length - SHOW_ITEMS) : "");
      td.title = t.join("\n");
    },
    mismatch: function (td, c, i, n, v) {
      var l = n.ticket && n.ticket.facts && n.ticket.facts.mismatches;
      if (v.unknown || !Array.isArray(l)) return unknown(td);
      if (!l.length) return none(td);
      td.textContent = "≠ " + l.map(function (x) { return textOf(x.code); }).join(", ");
      td.title = l.map(function (x) { return textOf(x.code) + (x.evidence ? ": " + x.evidence : ""); }).join("\n");
    }
  };

  // extract is what Copy as Markdown asks /api/extract for: the table's
  // order as one list (group none: no effort sections, each row once), its
  // registry columns and sort keys (name sorts as text). Its own state,
  // data and last activity columns have no Markdown form; note says when
  // a sort key on one of them had to be left out.
  function extract(ctx) {
    var m = V && V.model && V.model.ix === ctx.queryIndex ? V.model : model(ctx);
    var reg = ctx.registry, isField = function (c) { return !!reg.field(c) && !T.LOCAL[c]; };
    var sort = m.sort.map(function (s) { return s.key === "name" ? { key: "text", desc: s.desc } : s; });
    var dropped = sort.filter(function (s) { return !isField(s.key); }).map(function (s) { return T.colInfo(reg, s.key).label; });
    return { cols: m.cols.filter(isField), sort: T.formatSort(sort.filter(function (s) { return isField(s.key); })), group: "none",
      note: dropped.length ? "Markdown cannot sort by " + dropped.join(" or ") + " (the table's own column" + (dropped.length === 1 ? "" : "s") + "), so the copy leaves that out of its order." : "" };
  }

  window.WGOLive.registerView({ id: "table", label: "Table", question: "Across the rows I have filtered, how do they compare, and which come first by the measure I care about?",
    render: render, extract: extract,
    destroy: function () { if (cur) hover(null); } });
})();
