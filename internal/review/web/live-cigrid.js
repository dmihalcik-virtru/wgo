/* wgo dash CI grid (spec/gh-103.md): "Are these failures this PR's, or does
 * the check fail everywhere?" An addon view, registered as "ci" through
 * window.WGOLive: one HTML table per repository (small multiples, accessible
 * without a canvas) over the query's PR rows. Columns are PRs, by failing
 * checks then number; rows are the checks that ran on at least one of them,
 * by failures. A cell is the check's state on the PR's head: failing is a
 * filled cell with ×, passing a faint ✓, pending ·, skipped or neutral blank
 * (with an aria-label), and a check that did not run empty. A check that
 * fails everywhere in its repository (gh-99's check_stats) is labelled "fails
 * everywhere — 5 of 6 PRs" on a subtle background and its failures are
 * neutral, so it reads as a solid line while a PR's own failures stand out
 * as isolated red cells. State is never colour alone: each has its glyph.
 *
 * A cell click selects the PR and names the check in the URL (check=), which
 * the detail panel reads to show that check's state and link. A row header
 * applies check_failing:<name> (again removes it); a column header selects
 * the PR. A repository with more than 60 PRs shows the 60 with the most
 * failures and "+n more", which narrows the query to that repository. A
 * truncated check list (checks_truncated, gh-97) is marked on its column, and
 * there a check that is not listed is unknown (?), not "did not run". The
 * keyboard moves through each table as a grid: arrows, Home and End; Enter
 * or Space acts like a click.
 *
 * The pure logic is logic(WGOLive), also module.exports under node, so
 * internal/review/testdata/live_logic.js tests it without a page. Like
 * live.js it writes no markup from strings and issues no requests. */
(function () {
  "use strict";

  // ---------- pure logic ----------
  function logic(L) {
    var G = {};
    var hasOwn = Object.prototype.hasOwnProperty;
    var NATURAL = new Intl.Collator("en", { numeric: true });
    function cmp(a, b) { return a < b ? -1 : a > b ? 1 : 0; }
    G.MAX_COLUMNS = 60;
    // RANK keeps the worst state when a check name repeats on one head.
    var RANK = { failure: 5, pending: 4, success: 3, neutral: 2, skipped: 1 };
    // CELL says each state: glyph, words, and the cell's class.
    G.CELL = {
      failure: { glyph: "×", text: "failing" },
      success: { glyph: "✓", text: "passing" },
      pending: { glyph: "·", text: "pending" },
      skipped: { glyph: "", text: "skipped" },
      neutral: { glyph: "", text: "neutral" },
      absent: { glyph: "", text: "did not run" },
      unknown: { glyph: "?", text: "not among the checks read (the list is truncated)" }
    };

    // checkStates maps each check name on a PR's head to its state, or is
    // null when the checks were not read (unknown, never "no checks").
    G.checkStates = function (pr) {
      var details = pr && pr.check_details;
      if (!Array.isArray(details)) return null;
      var out = new Map();
      details.forEach(function (d) {
        var name = d && typeof d.name === "string" && d.name ? d.name : "", st = String((d && d.state) || "").toLowerCase();
        if (!name) return;
        if (!hasOwn.call(RANK, st)) st = "pending";
        var was = out.get(name);
        if (!was || RANK[st] > RANK[was.state]) out.set(name, { state: st, url: d.url || "" });
      });
      return out;
    };
    function prNumber(n) {
      var v = n && n.pr && n.pr.number;
      if (typeof v === "number" && isFinite(v)) return v;
      var m = /#(\d+)$/.exec((n && n.label) || "");
      return m ? +m[1] : Infinity;
    }
    // compareColumns orders PRs: more failing checks first, then by number.
    G.compareColumns = function (a, b) { return b.failures - a.failures || a.number - b.number || NATURAL.compare(a.label, b.label) || cmp(a.id, b.id); };
    // compareRows orders checks: more failures first, then by name.
    G.compareRows = function (a, b) { return b.failures - a.failures || NATURAL.compare(a.name, b.name) || cmp(a.name, b.name); };
    // systemicText is a systemic row's label.
    G.systemicText = function (st) { return "fails everywhere — " + st.failures + " of " + st.runs + " PR" + (st.runs === 1 ? "" : "s"); };

    // grids builds one grid per repository from the query's rows (node
    // indices of the query index ix; only PRs count) and the snapshot's
    // check_stats: {key, repo, total, columns, rows, more, unread}. columns
    // are {i, id, label, number, failures, truncated, states}; rows are
    // {name, failures, systemic (its check_stats entry when systemic, else
    // null)}; unread lists the PRs whose checks were not read.
    G.grids = function (ix, rows, checkStats, max) {
      if (max == null) max = G.MAX_COLUMNS;
      var byRepo = new Map(), order = [];
      rows.forEach(function (i) {
        if (ix.kinds[i] !== "pr") return;
        var n = ix.nodes[i], repo = n.pr && typeof n.pr.repo === "string" && n.pr.repo ? n.pr.repo : "", key = repo.toLowerCase();
        var g = byRepo.get(key);
        if (!g) { g = { key: key, repo: repo || "repository unknown", total: 0, all: [], unread: [] }; byRepo.set(key, g); order.push(g); }
        g.total++;
        var states = G.checkStates(n.pr);
        if (!states) { g.unread.push(i); return; }
        var failures = 0;
        states.forEach(function (s) { if (s.state === "failure") failures++; });
        g.all.push({ i: i, id: ix.ids[i], label: n.label || ix.ids[i], number: prNumber(n), failures: failures, truncated: !!n.pr.checks_truncated, states: states });
      });
      order.sort(function (a, b) { return NATURAL.compare(a.repo, b.repo) || cmp(a.key, b.key); });
      return order.map(function (g) {
        g.all.sort(G.compareColumns);
        g.columns = g.all.slice(0, max);
        g.more = g.all.length - g.columns.length;
        var stats = new Map();
        ((checkStats && checkStats[g.key]) || []).forEach(function (s) { if (s && typeof s.name === "string") stats.set(s.name, s); });
        var rowsBy = new Map();
        g.columns.forEach(function (c) {
          c.states.forEach(function (s, name) {
            var r = rowsBy.get(name);
            if (!r) { r = { name: name, failures: 0, systemic: null }; rowsBy.set(name, r); }
            if (s.state === "failure") r.failures++;
          });
        });
        g.rows = Array.from(rowsBy.values());
        g.rows.forEach(function (r) { var st = stats.get(r.name); if (st && st.systemic === true) r.systemic = st; });
        g.rows.sort(G.compareRows);
        delete g.all;
        return g;
      });
    };
    // cell is a check's state on a column's PR: one of CELL's keys. A check
    // missing from a truncated list is unknown, not absent.
    G.cell = function (col, name) {
      var s = col.states.get(name);
      if (s) return s.state;
      return col.truncated ? "unknown" : "absent";
    };
    // cellLabel says a cell in words, for its aria-label and title.
    G.cellLabel = function (col, row, state) {
      var t = col.label + ", " + row.name + ": " + G.CELL[state].text;
      if (state === "failure") t += row.systemic ? " (" + G.systemicText(row.systemic) + ")" : " (this PR's own failure)";
      return t;
    };
    // isolated lists the failing cells outside systemic rows: the PR's own
    // failures. A PR whose only failures are systemic has none.
    G.isolated = function (g, col) {
      return g.rows.filter(function (r) { return !r.systemic && G.cell(col, r.name) === "failure"; }).map(function (r) { return r.name; });
    };
    return G;
  }

  if (typeof module !== "undefined" && module.exports) module.exports = logic;
  if (typeof window === "undefined" || !window.WGOLive || typeof document === "undefined") return;

  // ---------- the view ----------
  var L = window.WGOLive, G = logic(L);
  var cur = null; // the latest ctx: handlers read the live store through it
  var V = null; // the view's DOM and model, built once per container

  function build(ctx) {
    var dom = ctx.dom, el = dom.el, host = ctx.container;
    host.textContent = "";
    host.classList.add("cigrid-host");
    V = { host: host, grids: [] };
    V.bar = el("div", "cg-bar", null, host);
    V.summary = el("span", "cg-sum", null, V.bar);
    var key = el("span", "cg-key", null, V.bar);
    key.setAttribute("aria-label", "Key");
    [["failure", "failing (own)"], ["sysfail", "failing everywhere"], ["success", "passing"], ["pending", "pending"], ["skipped", "blank: skipped or neutral"], ["absent", "empty: did not run"], ["unknown", "not read"]].forEach(function (k) {
      var it = el("span", "cg-ki", null, key), sw = el("span", "cg-sw " + (k[0] === "sysfail" ? "failure sys" : k[0]), k[0] === "sysfail" ? "×" : G.CELL[k[0]].glyph, it);
      sw.setAttribute("aria-hidden", "true");
      it.appendChild(document.createTextNode(k[1]));
    });
    V.status = el("span", "cg-status", null, V.bar); V.status.setAttribute("role", "status");
    V.scroll = el("div", "cg-scroll", null, host);
    V.scroll.addEventListener("click", onClick);
    V.scroll.addEventListener("keydown", onKey);
    V.scroll.addEventListener("mouseover", function (ev) { var t = ev.target.closest && ev.target.closest("[data-pr]"); hover(t ? t.dataset.pr : null); });
    V.scroll.addEventListener("mouseleave", function () { hover(null); });
    V.scroll.addEventListener("focusout", function (ev) { if (V && !V.scroll.contains(ev.relatedTarget)) hover(null); });
  }
  // The handlers outlive a destroyed view (V is null then) on its old DOM.
  function hover(id) { if (!V || V.hoverId === id) return; V.hoverId = id; cur.hover(id); }

  function render(ctx) {
    cur = ctx;
    if (!V || V.host !== ctx.container) build(ctx);
    if (!V.subscribed) { V.subscribed = true; ctx.subscribe(function (reason) { if (reason === "hover" && V) paintMarks(); }); }
    if (!ctx.queryIndex) return;
    if ((ctx.reason === "selection" || ctx.reason === "state") && V.ix === ctx.queryIndex && V.q === ctx.query && V.checkKey === (ctx.snapshot && ctx.snapshot.check_stats)) { paintMarks(); return; }
    // A click or Enter that changes the query re-renders the grid under the
    // keyboard: put it back on the same check or PR.
    var active = document.activeElement, again = active && V.scroll.contains(active) && active.dataset.r != null ? { repo: active.closest("section").dataset.repo, r: active.dataset.r, c: active.dataset.c, name: active.dataset.check || "", pr: active.dataset.pr || "" } : null;
    var ix = ctx.queryIndex, res = ctx.query, rows;
    if (res) rows = res.rowIdx; else { rows = []; for (var i = 0; i < ix.nodes.length; i++) rows.push(i); }
    V.ix = ix; V.q = ctx.query; V.checkKey = ctx.snapshot && ctx.snapshot.check_stats;
    V.grids = G.grids(ix, rows, ctx.snapshot && ctx.snapshot.check_stats);
    paint();
    if (again) restoreFocus(again);
  }

  function paint() {
    var el = cur.dom.el, plural = cur.dom.plural, frag = document.createDocumentFragment();
    var shown = 0, more = 0, unread = 0;
    V.grids.forEach(function (g) { shown += g.columns.length; more += g.more; unread += g.unread.length; });
    V.summary.textContent = V.grids.length ? "CI checks on " + plural(shown, "PR") + " in " + V.grids.length + (V.grids.length === 1 ? " repository" : " repositories") +
      (more ? " · " + plural(more, "more PR") + " past the " + G.MAX_COLUMNS + " per repository" : "") + (unread ? " · " + plural(unread, "PR") + " with checks not read" : "") : "";
    V.scroll.textContent = "";
    if (!V.grids.length) { el("p", "empty", cur.query ? "No PRs in the query: the CI grid shows PR rows (try kind:pr)." : "No PRs in this snapshot.", frag); V.scroll.appendChild(frag); return; }
    V.grids.forEach(function (g) { grid(frag, g); });
    V.scroll.appendChild(frag);
    paintMarks();
  }

  function grid(parent, g) {
    var el = cur.dom.el, plural = cur.dom.plural, sec = el("section", "cg-repo", null, parent);
    sec.dataset.repo = g.key;
    var h = el("h3", null, g.repo, sec);
    el("span", "cg-sub", " — " + plural(g.columns.length, "PR") + " × " + plural(g.rows.length, "check") + (g.more ? ", showing the " + g.columns.length + " with the most failures" : ""), h);
    if (g.columns.length) {
      var table = el("table", "cg", null, sec);
      table.setAttribute("role", "grid");
      table.setAttribute("aria-label", "CI checks in " + g.repo + ": rows are checks, columns are PRs");
      el("caption", "sr", "CI checks in " + g.repo + ". Arrow keys move; Enter on a check filters to PRs failing it, on a PR selects it, on a cell selects the PR and shows that check.", table);
      var thead = el("thead", null, null, table), hr = el("tr", null, null, thead);
      var corner = el("th", "cg-corner", "Check", hr); corner.scope = "col"; pos(corner, 0, 0);
      g.columns.forEach(function (c, k) {
        var th = el("th", "cg-col", null, hr), span = el("span", "cg-pr", "#" + (isFinite(c.number) ? c.number : c.label), th);
        th.scope = "col"; pos(th, 0, k + 1); th.dataset.pr = c.id;
        var n = cur.queryIndex.nodes[c.i], title = (n.pr && n.pr.title) || "";
        var lbl = c.label + (title ? " — " + title : "") + ": " + plural(c.failures, "failing check") + (c.truncated ? "; more checks than were read, so an empty cell is unknown" : "");
        if (c.truncated) { el("span", "cg-trunc", "+", span).setAttribute("aria-hidden", "true"); th.classList.add("trunc"); }
        th.title = lbl + "\nClick to select this PR.";
        th.setAttribute("aria-label", lbl);
      });
      var tbody = el("tbody", null, null, table);
      g.rows.forEach(function (r, ri) {
        var tr = el("tr", r.systemic ? "sys" : null, null, tbody), th = el("th", "cg-row", null, tr);
        th.scope = "row"; pos(th, ri + 1, 0); th.dataset.check = r.name;
        el("span", "cg-name", r.name, th);
        var lbl = r.name + ": failing on " + r.failures + " of these " + plural(g.columns.length, "PR");
        if (r.systemic) {
          el("span", "cg-systxt", G.systemicText(r.systemic), th);
          lbl += "; " + G.systemicText(r.systemic) + " (" + (r.systemic.evidence || "check_stats") + ")";
        }
        th.setAttribute("aria-label", lbl + ". Enter or click filters to PRs failing " + r.name + ".");
        th.title = (r.systemic ? (r.systemic.evidence || "") + "\n" : "") + "Click to filter with check_failing:" + r.name + " (again to remove it).";
        g.columns.forEach(function (c, k) {
          var st = G.cell(c, r.name), td = el("td", "cg-cell " + st + (r.systemic && st === "failure" ? " sys" : ""), G.CELL[st].glyph, tr);
          pos(td, ri + 1, k + 1); td.dataset.pr = c.id; td.dataset.check = r.name;
          td.setAttribute("aria-label", G.cellLabel(c, r, st));
          td.title = G.cellLabel(c, r, st);
        });
      });
    }
    if (g.more) {
      var narrowed = L.clauseActive(cur.getState("q"), cur.registry, "repo", g.repo);
      var more = cur.dom.button(el("p", "cg-more", null, sec), "+" + plural(g.more, "more PR") + (narrowed ? " — the table lists every one" : " — narrow to " + g.repo), null, null);
      more.dataset.act = narrowed ? "table" : "narrow"; more.dataset.repo = g.repo;
      more.title = narrowed ? "Open the table, which lists every PR of the query" : "Narrow the query to " + g.repo + " (repo:" + g.repo + ")";
    }
    if (g.unread.length) {
      var p = el("p", "cg-unread", "Checks not read for: ", sec);
      g.unread.forEach(function (i, k) {
        if (k) p.appendChild(document.createTextNode(", "));
        var b = cur.dom.button(p, cur.queryIndex.nodes[i].label || cur.queryIndex.ids[i], "linkish", null);
        b.dataset.act = "select"; b.dataset.pr = cur.queryIndex.ids[i];
        b.title = "Its check results are unknown (a PR known only through a bookmark, for example). Click to select it.";
      });
    }
  }
  function pos(cell, r, c) { cell.dataset.r = String(r); cell.dataset.c = String(c); cell.tabIndex = -1; }

  // paintMarks marks the selected and hovered PRs' columns, the check= cell,
  // the active check_failing: rows, and keeps one tab stop per table.
  function paintMarks() {
    if (!V || !V.scroll) return;
    var sel = cur.store.selection, hov = cur.store.hover, ck = cur.getState("check"), q = cur.getState("q");
    Array.prototype.forEach.call(V.scroll.querySelectorAll("[data-pr]"), function (c) {
      if (c.dataset.act) return;
      var on = sel.has(c.dataset.pr);
      c.classList.toggle("sel", on);
      c.classList.toggle("hov", c.dataset.pr === hov);
      if (c.tagName === "TH") c.setAttribute("aria-selected", String(on));
      if (c.tagName === "TD") c.classList.toggle("ck", on && c.dataset.check === ck);
    });
    Array.prototype.forEach.call(V.scroll.querySelectorAll("th.cg-row"), function (th) {
      var on = L.clauseActive(q, cur.registry, "check_failing", th.dataset.check);
      th.classList.toggle("on", on); th.setAttribute("aria-pressed", String(on));
    });
    Array.prototype.forEach.call(V.scroll.querySelectorAll("table.cg"), function (t) {
      if (!t.querySelector("[tabindex=\"0\"]")) { var first = t.querySelector("td.cg-cell") || t.querySelector("th"); if (first) first.tabIndex = 0; }
    });
  }

  // ---------- actions ----------
  function selectPR(id, add, check) {
    var ids = add ? Array.from(L.select(cur.store.selection, [id], true)) : [id];
    cur.setState({ sel: ids, check: check || "" }, { commit: true });
  }
  function filterCheck(name) {
    var err = cur.setQuery(L.toggleClause(cur.getState("q"), cur.registry, "check_failing", name), { commit: true });
    V.status.textContent = err ? "Cannot filter by " + name + ": " + err.error : "";
  }
  function act(cell, add) {
    var r = +cell.dataset.r, c = +cell.dataset.c;
    if (r === 0 && c === 0) return;
    rove(cell);
    if (r === 0) selectPR(cell.dataset.pr, add, "");
    else if (c === 0) filterCheck(cell.dataset.check);
    else selectPR(cell.dataset.pr, add, cell.dataset.check);
  }
  function onClick(ev) {
    if (!V) return;
    var b = ev.target.closest("button[data-act]");
    if (b) {
      if (b.dataset.act === "narrow") cur.setQuery(L.toggleClause(cur.getState("q"), cur.registry, "repo", b.dataset.repo), { commit: true });
      else if (b.dataset.act === "table") cur.setState({ view: "table" }, { commit: true });
      else if (b.dataset.act === "select") selectPR(b.dataset.pr, ev.shiftKey || ev.metaKey, "");
      return;
    }
    var cell = ev.target.closest("[data-r]");
    if (cell) act(cell, ev.shiftKey || ev.metaKey);
  }
  // rove gives cell its table's only tab stop.
  function rove(cell) {
    var t = cell.closest("table");
    Array.prototype.forEach.call(t.querySelectorAll("[tabindex=\"0\"]"), function (x) { if (x !== cell) x.tabIndex = -1; });
    cell.tabIndex = 0;
  }
  function at(t, r, c) { return t.querySelector("[data-r=\"" + r + "\"][data-c=\"" + c + "\"]"); }
  function onKey(ev) {
    var cell = V && ev.target.closest && ev.target.closest("table.cg [data-r]");
    if (!cell || ev.target !== cell) return;
    if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); act(cell, ev.shiftKey); return; }
    var t = cell.closest("table"), r = +cell.dataset.r, c = +cell.dataset.c, rows = t.rows.length, cols = t.rows[0].cells.length;
    switch (ev.key) {
      case "ArrowRight": c = Math.min(c + 1, cols - 1); break;
      case "ArrowLeft": c = Math.max(c - 1, 0); break;
      case "ArrowDown": r = Math.min(r + 1, rows - 1); break;
      case "ArrowUp": r = Math.max(r - 1, 0); break;
      case "Home": c = 0; if (ev.ctrlKey) r = 0; break;
      case "End": c = cols - 1; if (ev.ctrlKey) r = rows - 1; break;
      default: return;
    }
    ev.preventDefault();
    var next = at(t, r, c);
    if (next) { rove(next); next.focus(); hover(next.dataset.pr || null); }
  }
  // restoreFocus puts the keyboard back after a re-render: the same check
  // or PR in the same repository, else the same position.
  function restoreFocus(w) {
    var sec = V.scroll.querySelector("section[data-repo=\"" + (window.CSS && CSS.escape ? CSS.escape(w.repo) : w.repo) + "\"]");
    if (!sec) return;
    var t = sec.querySelector("table.cg"), cell = null;
    if (!t) return;
    var cells = Array.prototype.slice.call(t.querySelectorAll("[data-r]"));
    if (w.r === "0") cell = cells.filter(function (x) { return x.dataset.r === "0" && x.dataset.pr === w.pr; })[0];
    else if (w.c === "0") cell = cells.filter(function (x) { return x.dataset.c === "0" && x.dataset.check === w.name; })[0];
    else cell = cells.filter(function (x) { return x.dataset.pr === w.pr && x.dataset.check === w.name; })[0];
    cell = cell || at(t, w.r, w.c) || t.querySelector("[data-r]");
    if (cell) { rove(cell); cell.focus({ preventScroll: true }); }
  }

  // extract: Copy as Markdown lists the rows with their CI and failing checks.
  function extract(ctx) { return { cols: ["repo", "ci", "check_failing"].filter(function (c) { return !!ctx.registry.field(c); }) }; }

  window.WGOLive.registerView({ id: "ci", label: "CI grid", question: "Are these failures this PR's, or does the check fail everywhere?",
    render: render, extract: extract,
    destroy: function () { if (V && V.hoverId && cur) cur.hover(null); V = null; } });
})();
