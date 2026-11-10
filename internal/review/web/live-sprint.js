/* wgo dash sprint view (spec/gh-105.md). An addon view, registered as
 * "sprint" through window.WGOLive: "Does Jira agree with the code for this
 * sprint, and how far along is it against its goal?"
 *
 * One active sprint at a time (sprint= in the URL; by default the one with
 * the most tickets for the configured people): its dates, a thin bar of
 * elapsed time over a bar of story points by status category (done first,
 * so the two compare), its goal verbatim, and its tickets grouped by status
 * category, then assignee, with their linked PRs as glyphs and gh-99's
 * mismatches. Clicking a points label or segment applies
 * status_category:<c>, a mismatch mismatch:<code>. Tickets the query does
 * not show are dimmed, or hidden with outside=hide. Copy as Markdown asks
 * /api/extract for view=sprint&sprint=<id>, which
 * internal/dash/extract_sprint.go renders by the same rules (default
 * sprint, day count, points, explanations); keep the two in step.
 *
 * The pure logic is logic(WGOLive), also module.exports under node, so
 * internal/review/testdata/live_logic.js tests it without a page. Like
 * live.js it writes no markup from strings and issues no requests. */
(function () {
  "use strict";

  // ---------- pure logic ----------
  function logic(L) {
    var S = {};
    function cmp(a, b) { return a < b ? -1 : a > b ? 1 : 0; }
    function lower(s) { return String(s || "").toLowerCase(); }
    function isObj(v) { return v && typeof v === "object" && !Array.isArray(v); }

    // CATEGORIES in display order: done first, so the done share of the
    // points bar lines up with the elapsed-time bar above it.
    S.CATEGORIES = [{ key: "done", label: "Done" }, { key: "in_progress", label: "In Progress" }, { key: "todo", label: "To Do" }];
    // PR glyphs by gh-99's linked PR state (requirement 4).
    S.PR = {
      draft: { glyph: "◇", text: "draft" },
      open: { glyph: "○", text: "open, awaiting review" },
      approved: { glyph: "✓", text: "approved" },
      merged: { glyph: "●", text: "merged" },
      closed: { glyph: "✕", text: "closed" }
    };
    S.prGlyph = function (state) { return S.PR[state] || { glyph: "?", text: "state unknown" }; };
    S.codeText = function (code) { return String(code).replace(/_/g, " "); };
    S.sprintName = function (sp) { return (sp && sp.name) || "Sprint " + (sp && sp.id); };
    S.compareKeys = function (a, b) { return String(a).localeCompare(String(b), "en", { numeric: true }) || cmp(a, b); };

    // forPeople tells whether ticket t of active sprint sp is assigned to a
    // followed person (forPeople in extract_sprint.go): true when its
    // assignee's account ID is a followed person's Jira account ID or sp's
    // search for the followed people's tickets found it; false when it is
    // unassigned, its account ID was compared with every followed person's,
    // or sp's complete search, covering everyone, did not find it; null
    // (unknown) otherwise, as for currentUser() or an email.
    function accountID(j) { return typeof j === "string" && j !== "" && !/[@(]/.test(j); }
    S.forPeople = function (t, sp, people) {
      people = (people || []).filter(isObj);
      var compared = people.length > 0, covered = people.length > 0, aid = lower(t.assignee_id);
      for (var i = 0; i < people.length; i++) {
        var j = people[i].jira;
        if (accountID(j)) { if (aid && aid === lower(j)) return true; } else compared = false;
        if (!j) covered = false;
      }
      if (Array.isArray(t.sprint_search) && t.sprint_search.indexOf(sp.id) >= 0) return true;
      if (!t.assignee && !t.assignee_id && (t.freshness === "fresh" || t.freshness === "stale")) return false;
      if (aid && compared) return false;
      if (sp.searched === true && sp.truncated !== true && covered) return false;
      return null;
    };

    // activeSprints lists the snapshot's active sprints by start, then ID,
    // each ID once, with the tickets that have an in_sprint edge to it (in
    // snapshot order) and how many a configured person is assigned.
    S.activeSprints = function (snap) {
      var byId = new Map(), sprintOf = new Map(), out = [];
      var nodes = (snap && snap.nodes) || [], edges = (snap && snap.edges) || [];
      nodes.forEach(function (n) {
        var sp = n && n.kind === "sprint" && isObj(n.sprint) ? n.sprint : null;
        if (!sp || sp.state !== "active" || typeof sp.id !== "number") return;
        sprintOf.set(n.id, sp.id);
        if (!byId.has(sp.id)) { var a = { id: sp.id, node: n, sprint: sp, tickets: [], people: 0, unknownPeople: 0, members: new Set() }; byId.set(sp.id, a); out.push(a); }
      });
      if (!out.length) return out;
      edges.forEach(function (e) {
        var sid = e && e.kind === "in_sprint" ? sprintOf.get(e.target) : undefined;
        if (sid != null) byId.get(sid).members.add(e.source);
      });
      nodes.forEach(function (n) {
        if (!n || !isObj(n.ticket)) return;
        out.forEach(function (a) {
          if (!a.members.has(n.id)) return;
          a.tickets.push(n);
          var f = S.forPeople(n.ticket, a.sprint, snap.people);
          if (f === true) a.people++; else if (f == null) a.unknownPeople++;
        });
      });
      out.forEach(function (a) { delete a.members; });
      var t = function (a) { var v = Date.parse(a.sprint.start || ""); return isFinite(v) ? v : -Infinity; };
      out.sort(function (a, b) { return t(a) - t(b) || a.id - b.id; });
      return out;
    };

    // peopleKnown: whose tickets they are is known for every sprint's.
    S.peopleKnown = function (list) { return list.every(function (a) { return !a.unknownPeople; }); };
    // defaultSprint: the most tickets for the followed people, then the
    // most tickets, then the first by start and ID; the most tickets when
    // whose they are is unknown for some sprint's (defaultSprint in Go).
    S.defaultSprint = function (list) {
      var known = S.peopleKnown(list), best = null;
      list.forEach(function (a) {
        if (!best) best = a;
        else if (known && a.people !== best.people) { if (a.people > best.people) best = a; }
        else if (a.tickets.length > best.tickets.length) best = a;
      });
      return best;
    };
    // peopleText says how many of a sprint's tickets are the followed
    // people's, saying what is unknown rather than counting it as no.
    S.peopleText = function (a) {
      var n = a.tickets.length, of = n === 1 ? " ticket" : " tickets";
      if (!a.unknownPeople) return a.people + " of its " + n + of + " " + (a.people === 1 ? "is" : "are") + " assigned to the people wgo dash follows";
      return "whose tickets these are is unknown for " + a.unknownPeople + " of its " + n + of + (a.people ? " (" + a.people + (a.people === 1 ? " is" : " are") + " known to be assigned to the people wgo dash follows)" : "") +
        ": a Jira identity such as currentUser() cannot be matched to an assignee; set jira = \"<account ID>\" in [[dash.people]]";
    };

    // chooseSprint reads sprint= ("1234" or "sprint:1234"): {sprint} or
    // {error, list}, where the error names the active sprints.
    S.chooseSprint = function (snap, list, raw) {
      raw = String(raw || "").trim();
      if (!raw) return { sprint: S.defaultSprint(list) };
      var m = /^(?:sprint:)?([0-9]+)$/.exec(raw), id = m ? +m[1] : 0;
      var names = list.map(function (a) { return a.id + " (" + S.sprintName(a.sprint) + ")"; }).join(", ");
      var listText = "the active sprint" + (list.length === 1 ? " is " : "s are ") + names;
      if (!(id > 0)) return { error: "sprint " + JSON.stringify(raw) + " is not a sprint ID; " + listText };
      var hit = list.filter(function (a) { return a.id === id; })[0];
      if (hit) return { sprint: hit };
      var other = ((snap && snap.nodes) || []).filter(function (n) { return n && isObj(n.sprint) && n.sprint.id === id; })[0];
      if (other) return { error: "sprint " + id + " (" + S.sprintName(other.sprint) + ") is " + (other.sprint.state || "of unknown state") + ", and the sprint view shows active sprints only; " + listText };
      return { error: "no sprint " + id + " in this snapshot; " + listText };
    };

    function upperFirst(s) { return s ? s.charAt(0).toUpperCase() + s.slice(1) : s; }
    // explain says why there is no active sprint to show (explainNoSprint in
    // extract_sprint.go): {title, detail, config} or null. config is set
    // when [jira] board is not set.
    S.explain = function (snap, list) {
      if (list.length) return null;
      var src = snap && snap.sources && snap.sources.jira_sprints;
      if (!isObj(src)) return { title: "No sprint data", config: true, detail: ["This snapshot does not report the jira_sprints source, so it holds no sprints: it was made by a wgo dash without sprint support, or before a [jira] board was set."] };
      var parts = Array.isArray(src.parts) ? src.parts : [];
      var board = parts.filter(function (p) { return isObj(p) && /^board /.test(p.name || ""); })[0];
      if (!board && !parts.length && src.state === "unknown") return { title: "No [jira] board is configured", config: true, detail: ["wgo dash shows the sprints of the Jira board that [jira] board names, and none is set."] };
      var name = board ? "Board " + board.name.replace(/^board /, "") : "The board", state = board ? board.state : src.state, detail = [];
      if (state === "unknown") {
        if (src.detail) detail.push(upperFirst(src.detail) + ".");
        return { title: name + "'s sprints are not known yet", detail: detail, config: false };
      }
      if (state === "error") {
        if (board && board.error) detail.push("The lookup failed: " + board.error + ".");
        if (src.detail) detail.push(upperFirst(src.detail) + ".");
        return { title: name + "'s sprints could not be read", detail: detail, config: false };
      }
      var env = L.queryEnv(snap), future = [];
      ((snap && snap.nodes) || []).forEach(function (n) {
        if (!n || !isObj(n.sprint) || n.sprint.state !== "future") return;
        var t = Date.parse(n.sprint.start || ""), f = S.sprintName(n.sprint) + " (" + n.sprint.id + ")";
        if (isFinite(t) && env.dayOf) f += " from " + env.dayOf(t);
        future.push(f);
      });
      detail.push("The sprint view shows active sprints only.");
      if (future.length) detail.push("Future: " + future.join(", ") + ".");
      return { title: name + " has no active sprint", detail: detail, config: false };
    };
    S.BOARD_HINT = "Add the board whose sprints to show to ~/.wgo/config.toml, using the number in the board's URL (…/boards/<id>):";
    S.BOARD_SNIPPET = "[jira]\nboard = <id>";

    function dayNum(day) { var p = String(day).split("-"); return Date.UTC(+p[0], +p[1] - 1, +p[2]) / 864e5; }
    // sprintDay places now (ms) in a sprint (sprintDay in extract_sprint.go):
    // length counts calendar days from the start's day to the end's in the
    // snapshot's timezone (dayOf), so whole-week Jira sprints are 7, 14, 21
    // or 28 days; day counts from the start's day (day 1), at most length.
    // frac is the share of the sprint's time that has passed, for the bar.
    S.sprintDay = function (start, end, now, dayOf) {
      var s = Date.parse(start || ""), e = Date.parse(end || "");
      if (!start || !end || !isFinite(s) || !isFinite(e) || now == null || !isFinite(now) || !dayOf) return { ok: false, text: "", frac: 0 };
      var ds = dayNum(dayOf(s)), de = dayNum(dayOf(e)), dn = dayNum(dayOf(now));
      var length = Math.max(1, de - ds), day = Math.min(length, dn - ds + 1), before = now < s, after = now >= e, text;
      if (before) text = ds === dn ? "starts today" : "starts in " + (ds - dn) + (ds - dn === 1 ? " day" : " days");
      else if (after) text = "day " + length + " of " + length + ", past its end";
      else text = "day " + day + " of " + length;
      var frac = e > s ? Math.max(0, Math.min(1, (now - s) / (e - s))) : (after ? 1 : 0);
      return { ok: true, day: day, length: length, before: before, after: after, text: text, frac: frac };
    };

    // pointsOf: a ticket's story points, "unestimated" (read, none set) or
    // "unknown" (never read: story_points_known unset).
    S.pointsOf = function (t) {
      if (!t || t.story_points_known !== true) return { kind: "unknown", value: null, text: "unknown" };
      if (typeof t.story_points !== "number" || !isFinite(t.story_points)) return { kind: "unestimated", value: null, text: "unestimated" };
      return { kind: "known", value: t.story_points, text: S.num(t.story_points) };
    };
    S.num = function (v) { return String(Math.round(v * 100) / 100); };
    S.ptsText = function (v) { return S.num(v) + (v === 1 ? " pt" : " pts"); };

    // progress counts story points by status category; unestimated tickets
    // and those whose points are unknown are listed apart, never as zero.
    S.progress = function (tickets) {
      var at = {}, p = { parts: [], unknownCategory: [], unestimated: [], unknownPoints: [], total: tickets.length, points: 0 };
      S.CATEGORIES.forEach(function (c, i) { at[c.key] = i; p.parts.push({ key: c.key, label: c.label, points: 0, tickets: 0 }); });
      tickets.forEach(function (n) {
        var t = n.ticket, pts = S.pointsOf(t);
        if (pts.kind === "unknown") p.unknownPoints.push(n);
        else if (pts.kind === "unestimated") p.unestimated.push(n);
        var i = at[t.status_category];
        if (i == null) { p.unknownCategory.push(n); return; }
        p.parts[i].tickets++;
        if (pts.kind === "known") { p.parts[i].points += pts.value; p.points += pts.value; }
      });
      var x = 0;
      p.parts.forEach(function (part) { part.start = p.points > 0 ? x / p.points : 0; part.frac = p.points > 0 ? part.points / p.points : 0; x += part.points; });
      return p;
    };

    // labelLayout places direct labels under a bar: each at its segment's
    // start (px), pushed right past the previous label, then pulled back
    // within the bar from the right. null when they cannot fit on one line.
    S.labelLayout = function (starts, widths, total, gap) {
      var n = starts.length, left = new Array(n), i;
      for (i = 0; i < n; i++) left[i] = Math.max(starts[i], i ? left[i - 1] + widths[i - 1] + gap : 0);
      for (i = n - 1; i >= 0; i--) left[i] = Math.min(left[i], (i < n - 1 ? left[i + 1] - gap : total) - widths[i]);
      return n && left[0] < 0 ? null : left;
    };

    // assigneeOf: the display name, "Unassigned" once Jira data was read
    // (gh-99 decides unassigned_in_sprint the same way), else unknown.
    S.assigneeOf = function (t) {
      if (t.assignee) return { text: t.assignee, rank: 0 };
      if (t.freshness === "fresh" || t.freshness === "stale") return { text: "Unassigned", rank: 1 };
      return { text: "unknown", rank: 2, unknown: true };
    };

    // groups lists the tickets by status category (CATEGORIES, then unknown),
    // each sorted by assignee (named, unassigned, unknown), then key.
    S.groups = function (tickets) {
      var cats = S.CATEGORIES.concat([{ key: "", label: "Status category unknown" }]), by = {};
      cats.forEach(function (c) { by[c.key] = []; });
      tickets.forEach(function (n) { var k = n.ticket.status_category; (by.hasOwnProperty(k) && k ? by[k] : by[""]).push(n); });
      return cats.map(function (c) {
        var rows = by[c.key].map(function (n) { return { node: n, assignee: S.assigneeOf(n.ticket) }; });
        rows.sort(function (a, b) {
          return a.assignee.rank - b.assignee.rank || cmp(lower(a.assignee.text), lower(b.assignee.text)) || S.compareKeys(a.node.ticket.key || a.node.label, b.node.ticket.key || b.node.label);
        });
        return { key: c.key, label: c.label, rows: rows };
      }).filter(function (g) { return g.rows.length; });
    };

    // mismatchesOf: {list} (possibly empty) or {unknown, why} when gh-99
    // could not decide them.
    S.mismatchesOf = function (t) {
      var f = t && t.facts;
      if (!isObj(f)) return { unknown: true, why: "no derived facts for this ticket" };
      if (!Array.isArray(f.mismatches)) {
        if (t.system !== "jira") return { list: [] };
        return { unknown: true, why: Array.isArray(f.uncertain) && f.uncertain.length ? "could not be decided: " + f.uncertain.join(", ") + " unknown" : "not decided" };
      }
      return { list: f.mismatches.filter(function (m) { return isObj(m) && typeof m.code === "string" && m.code; }) };
    };

    // prTitle is a linked PR's details on demand: its state, then gh-99's
    // whose move ("you" when it is the operator's, me) and the details the
    // analysis table shows (L.moveTitle: who holds it, why, what is
    // uncertain).
    S.prTitle = function (lp, pr, me) {
      var g = S.prGlyph(lp.state), lines = [(lp.ref || lp.id) + ": " + g.text + (pr && pr.title ? " — " + pr.title : "")];
      var f = pr && isObj(pr.facts) ? pr.facts : null;
      if (!pr) lines.push("Not in the snapshot");
      else {
        if (f) { var w = L.moveWord(f, me); lines.push("Whose move: " + (w.mine ? "you (" + w.move + ")" : w.word)); }
        var d = L.moveTitle(f);
        if (d) lines.push(d);
      }
      return lines.join("\n");
    };

    // notes are what may make the sprint's tickets incomplete (sprintNotes in
    // extract_sprint.go).
    S.notes = function (snap, sp) {
      var out = [], src = snap && snap.sources && snap.sources.jira_sprints;
      if (sp.freshness === "stale") out.push("The sprint data is stale" + (sp.fetched_at ? " (fetched " + sp.fetched_at + ")" : "") + ".");
      if (sp.truncated) out.push("Jira found more tickets in this sprint than wgo reads, so some are missing.");
      ((isObj(src) && Array.isArray(src.parts)) ? src.parts : []).forEach(function (p) {
        if (!isObj(p) || p.name !== "sprint " + sp.id || !p.error) return;
        out.push(p.state === "error" ? "This sprint's ticket search failed (" + p.error + "), so its tickets are unknown." : "This sprint's last ticket search failed (" + p.error + "); its tickets are from an earlier search.");
      });
      return out;
    };
    return S;
  }

  if (typeof module !== "undefined" && module.exports) module.exports = logic;
  if (typeof window === "undefined" || !window.WGOLive || typeof document === "undefined") return;

  // ---------- the view ----------
  var L = window.WGOLive, S = logic(L);
  var SHOW_PRS = 3;
  var cur = null; // the latest ctx: handlers read the live store through it
  var V = null; // the view's DOM and state, built once per container
  var memo = { snap: null, list: null };

  function sprintsOf(snap) {
    if (memo.snap !== snap) { memo.snap = snap; memo.list = S.activeSprints(snap); }
    return memo.list;
  }

  function build(ctx) {
    var host = ctx.container;
    host.textContent = "";
    host.classList.add("sv-host");
    V = { host: host, focusId: null, rows: [], rowEls: [], m: null, placeLabels: null, width: 0 };
    if (window.ResizeObserver) new ResizeObserver(function () {
      if (V && V.placeLabels && host.clientWidth !== V.width) { V.width = host.clientWidth; V.placeLabels(); }
    }).observe(host);
    V.root = ctx.dom.el("div", "sv", null, host);
    V.root.addEventListener("click", onClick);
    V.root.addEventListener("keydown", onKey);
    V.root.addEventListener("focusin", function (ev) { var tr = ev.target.closest && ev.target.closest("tr[data-id]"); if (tr) { V.focusId = tr.dataset.id; rove(); } });
    V.root.addEventListener("mouseover", function (ev) { var h = ev.target.closest("[data-hover]"); hover(h ? h.dataset.hover : null); });
    V.root.addEventListener("mouseleave", function () { hover(null); });
  }
  function hover(id) { if (cur && cur.hover) cur.hover(id); }

  // model is what the view draws for ctx.
  function model(ctx) {
    var snap = ctx.snapshot, list = sprintsOf(snap), gap = S.explain(snap, list);
    var m = { list: list, gap: gap };
    if (gap) return m;
    var ch = S.chooseSprint(snap, list, ctx.getState("sprint"));
    if (ch.error) { m.error = ch.error; return m; }
    var res = ctx.query, sp = ch.sprint;
    m.sprint = sp;
    m.inside = function (id) { return !res || !res.vis || res.vis.has(id); };
    m.hide = ctx.getState("outside") === "hide";
    m.filtered = !!(res && res.vis);
    m.progress = S.progress(sp.tickets);
    m.groups = S.groups(sp.tickets);
    m.outside = sp.tickets.filter(function (n) { return !m.inside(n.id); }).length;
    m.mismatched = sp.tickets.filter(function (n) { var x = S.mismatchesOf(n.ticket); return x.list && x.list.length; }).length;
    m.day = S.sprintDay(sp.sprint.start, sp.sprint.end, ctx.queryIndex ? ctx.queryIndex.env.nowMs : null, ctx.queryIndex ? ctx.queryIndex.env.dayOf : null);
    m.dayOf = ctx.queryIndex ? ctx.queryIndex.env.dayOf : null;
    return m;
  }

  function render(ctx) {
    cur = ctx;
    if (!V || V.host !== ctx.container) build(ctx);
    if (!ctx.snapshot) return;
    if (ctx.reason === "selection" && V.m && V.m.sprint && V.snap === ctx.snapshot) { paintSelection(); return; }
    // Keep the keyboard where it was across a redraw (the same control) and
    // the scroll positions: the view's, and the table's sideways.
    var a = document.activeElement, key = a && V.root.contains(a) && a.dataset ? a.dataset.k : null;
    var top = V.host.scrollTop, sc = V.root.querySelector(".sv-scroll"), left = sc ? sc.scrollLeft : 0;
    V.snap = ctx.snapshot;
    V.m = model(ctx);
    V.root.textContent = "";
    V.rowEls = []; V.placeLabels = null;
    if (V.msgFor !== ctx.getState("q")) V.msg = null; // a query error belongs to the query it was shown for
    paintPicker();
    if (V.m.gap) paintGap();
    else if (V.m.error) paintError();
    else { paintHead(); paintBars(); paintBar(); paintTable(); }
    V.host.scrollTop = top;
    sc = V.root.querySelector(".sv-scroll");
    if (sc) sc.scrollLeft = left;
    if (key) {
      var back = Array.prototype.filter.call(V.root.querySelectorAll("[data-k]"), function (e) { return e.dataset.k === key; })[0];
      if (back) { back.focus({ preventScroll: true }); if (back.scrollIntoView) back.scrollIntoView({ block: "nearest", inline: "nearest" }); }
    }
  }

  // ---------- picker, explanations and errors ----------
  function paintPicker() {
    var m = V.m, el = cur.dom.el;
    if (m.list.length < 2) return;
    var box = el("div", "sv-pick", null, V.root);
    box.setAttribute("role", "group"); box.setAttribute("aria-label", "Active sprint");
    el("span", "sv-lab", "Active sprints", box);
    m.list.forEach(function (a) {
      var b = cur.dom.button(box, S.sprintName(a.sprint) + " · " + cur.dom.plural(a.tickets.length, "ticket"), null, null);
      b.dataset.act = "sprint"; b.dataset.sprint = String(a.id); b.dataset.k = "sprint:" + a.id;
      b.setAttribute("aria-pressed", String(!!m.sprint && m.sprint.id === a.id));
      b.title = "Sprint " + a.id + ": " + S.peopleText(a) + (a !== S.defaultSprint(m.list) ? "" : S.peopleKnown(m.list) ? "\nThe default: the most tickets for those people" : "\nThe default: the most tickets, since whose tickets these are is unknown for some sprint");
    });
  }
  function paintGap() {
    var g = V.m.gap, el = cur.dom.el, box = el("div", "sv-gap", null, V.root);
    box.setAttribute("role", "status");
    el("h2", null, "No sprint to show: " + g.title, box);
    g.detail.forEach(function (d) { el("p", null, d, box); });
    if (g.config) {
      el("p", null, S.BOARD_HINT, box);
      el("code", null, S.BOARD_SNIPPET, el("pre", null, null, box));
    }
  }
  function paintError() {
    var el = cur.dom.el, box = el("div", "sv-gap", null, V.root);
    box.setAttribute("role", "alert");
    el("h2", null, "Sprint " + cur.getState("sprint") + " cannot be shown", box);
    el("p", null, V.m.error.charAt(0).toUpperCase() + V.m.error.slice(1) + ".", box);
    if (V.m.list.length) {
      var p = el("p", null, "Show ", box);
      V.m.list.forEach(function (a, i) {
        if (i) p.appendChild(document.createTextNode(i === V.m.list.length - 1 ? " or " : ", "));
        var b = cur.dom.button(p, S.sprintName(a.sprint), "linkish", null);
        b.dataset.act = "sprint"; b.dataset.sprint = String(a.id); b.dataset.k = "fix:" + a.id;
      });
    }
  }

  // ---------- header and bars ----------
  function paintHead() {
    var m = V.m, sp = m.sprint.sprint, el = cur.dom.el, head = el("div", "sv-head", null, V.root);
    el("h2", null, S.sprintName(sp), head);
    var meta = ["Sprint " + sp.id];
    if (sp.board) meta.push("board " + sp.board);
    var s = Date.parse(sp.start || ""), e = Date.parse(sp.end || "");
    meta.push(isFinite(s) && isFinite(e) && m.dayOf ? m.dayOf(s) + " to " + m.dayOf(e) : "dates unknown");
    el("span", "sv-meta", meta.join(" · "), head);
    var goal = String(sp.goal || "").trim();
    var g = el("p", "sv-goal" + (goal ? "" : " none"), goal || "no sprint goal", V.root);
    g.setAttribute("aria-label", goal ? "Sprint goal: " + goal : "This sprint has no goal");
    S.notes(cur.snapshot, sp).forEach(function (n) { el("p", "sv-note", n, V.root); });
  }

  function paintBars() {
    var m = V.m, el = cur.dom.el, p = m.progress, box = el("div", "sv-bars", null, V.root);
    // Time: a thin bar of elapsed time with "day N of M".
    var tr = el("div", "sv-row", null, box);
    el("span", "sv-lab", "Time", tr);
    var track = el("div", "sv-time", null, tr);
    track.setAttribute("role", "img");
    track.setAttribute("aria-label", m.day.ok ? "Time: " + m.day.text + "; " + Math.round(m.day.frac * 100) + "% of the sprint's time has passed" : "Time: the sprint's dates are unknown");
    el("span", "fill", null, track).style.width = (m.day.frac * 100).toFixed(1) + "%";
    el("span", "sv-val", m.day.ok ? m.day.text : "dates unknown", tr);
    // Points: one bar divided by status category, on the same width.
    var pr = el("div", "sv-row", null, box);
    el("span", "sv-lab", "Points", pr);
    var bar = el("div", "sv-pts", null, pr);
    bar.setAttribute("aria-hidden", "true");
    p.parts.forEach(function (part) {
      if (!(part.frac > 0)) return;
      var seg = el("span", "seg " + part.key + (active("status_category", part.key) ? " on" : ""), null, bar);
      seg.style.flex = part.frac.toFixed(4) + " 1 0px"; // lengths in proportion to points, after the gaps
      seg.dataset.act = "cat"; seg.dataset.cat = part.key;
      seg.title = part.label + ": " + S.ptsText(part.points) + " · " + cur.dom.plural(part.tickets, "ticket") + "\nClick to filter status_category:" + part.key;
    });
    if (!(p.points > 0)) el("span", "empty", "no estimated points", bar);
    el("span", "sv-val", S.ptsText(p.points), pr);
    // Direct labels: one button per category under its segment.
    var lr = el("div", "sv-row sv-labels", null, box);
    el("span", "sv-lab", null, lr);
    var labels = el("div", "sv-plabels", null, lr);
    labels.setAttribute("role", "group"); labels.setAttribute("aria-label", "Story points by status category; each filters to its category");
    var btns = p.parts.map(function (part) {
      var b = cur.dom.button(labels, null, "plab " + part.key, null);
      var name = el("span", "nm", null, b);
      el("span", "sw " + part.key, null, name);
      el("b", null, part.label, name);
      el("span", "v", S.ptsText(part.points) + " · " + cur.dom.plural(part.tickets, "ticket"), b);
      b.dataset.act = "cat"; b.dataset.cat = part.key; b.dataset.k = "cat:" + part.key;
      b.setAttribute("aria-pressed", String(active("status_category", part.key)));
      b.setAttribute("aria-label", part.label + ": " + S.num(part.points) + " story points, " + cur.dom.plural(part.tickets, "ticket") + ". Filter status_category:" + part.key);
      return b;
    });
    // Each label sits under its segment when they fit on one line (the row
    // runs on under the value column), and wraps in order otherwise; a
    // resize places them again.
    V.placeLabels = function () {
      labels.classList.remove("placed");
      var total = labels.clientWidth, bw = bar.clientWidth || total;
      var lefts = total ? S.labelLayout(p.parts.map(function (x) { return x.start * bw; }), btns.map(function (b) { return b.offsetWidth; }), total, 8) : null;
      if (lefts) { labels.classList.add("placed"); btns.forEach(function (b, i) { b.style.left = lefts[i].toFixed(1) + "px"; }); }
    };
    V.placeLabels();
    // What the points leave out, never counted as zero.
    var apart = [];
    if (p.unestimated.length) apart.push([p.unestimated.length + " unestimated", p.unestimated]);
    if (p.unknownPoints.length) apart.push([p.unknownPoints.length + " with points unknown", p.unknownPoints]);
    if (p.unknownCategory.length) apart.push([cur.dom.plural(p.unknownCategory.length, "ticket") + " of unknown status category", p.unknownCategory]);
    if (apart.length) {
      var ap = el("div", "sv-row sv-apart", null, box);
      el("span", "sv-lab", null, ap);
      var t = el("span", null, "Not in the points: ", ap);
      apart.forEach(function (x, i) {
        if (i) t.appendChild(document.createTextNode(" · "));
        el("span", null, x[0], t).title = x[1].map(function (n) { return n.label; }).join(", ");
      });
    }
  }
  function active(field, value) { return L.clauseActive(cur.getState("q"), cur.registry, field, value); }

  function paintBar() {
    var m = V.m, el = cur.dom.el, plural = cur.dom.plural, bar = el("div", "sv-bar", null, V.root);
    var sum = plural(m.sprint.tickets.length, "ticket") + " · " + m.mismatched + " with mismatches";
    if (m.filtered) sum += " · the query shows " + (m.sprint.tickets.length - m.outside) + (m.outside ? ", " + m.outside + " outside it " + (m.hide ? "(hidden)" : "(dimmed)") : "");
    el("span", "sv-sum", sum, bar);
    if (m.outside) {
      var b = cur.dom.button(bar, (m.hide ? "Show " : "Hide ") + plural(m.outside, "ticket") + " outside the query", null, null);
      b.dataset.act = "outside"; b.dataset.k = "outside";
      b.title = "Tickets the query does not show are dimmed so the sprint stays whole; this hides them instead (outside=hide in the URL).";
    }
    if (V.msg) {
      var msg = el("div", "sv-msg", null, V.root);
      msg.setAttribute("role", "alert");
      el("span", null, V.msg.error, msg);
      if (V.msg.fixed) {
        msg.appendChild(document.createTextNode(" "));
        var f = cur.dom.button(msg, "Use " + V.msg.fixed, "linkish", null);
        f.dataset.act = "fixq"; f.dataset.q = V.msg.fixed; f.dataset.k = "fixq";
      }
    }
  }

  // ---------- table ----------
  // What the view asks about comes first: where Jira and the code disagree
  // and the PRs, so a narrow view scrolls points and status out of sight.
  var COLS = ["Ticket", "Assignee", "Mismatches", "Linked PRs", "Points", "Status"];
  function paintTable() {
    var m = V.m, el = cur.dom.el, wrap = el("div", "sv-scroll", null, V.root);
    var table = el("table", "sv-table", null, wrap);
    table.setAttribute("role", "grid");
    el("caption", "sr", "Tickets of the sprint by status category, then assignee. Arrow keys move between tickets, Enter or Space selects, Shift+Enter adds. Tickets outside the query are dimmed.", table);
    var hr = el("tr", null, null, el("thead", null, null, table));
    COLS.forEach(function (c) { var th = el("th", c === "Points" ? "num" : null, c, hr); th.scope = "col"; });
    V.rows = [];
    m.groups.forEach(function (g) {
      var rows = g.rows.filter(function (r) { return !m.hide || m.inside(r.node.id); });
      var tb = el("tbody", null, null, table);
      var part = m.progress.parts.filter(function (x) { return x.key === g.key; })[0];
      var th = el("th", null, g.label, el("tr", "grp", null, tb));
      th.colSpan = COLS.length; th.scope = "colgroup";
      el("span", "sub", (part ? " · " + S.ptsText(part.points) : "") + " · " + cur.dom.plural(g.rows.length, "ticket") + (rows.length < g.rows.length ? " (" + rows.length + " shown)" : ""), th);
      var prev = null;
      rows.forEach(function (r) {
        var first = !prev || prev.assignee.text !== r.assignee.text;
        tb.appendChild(rowEl(r, first));
        prev = r;
      });
    });
    if (!V.rows.length) { var td = el("td", "empty", m.sprint.tickets.length ? "Every ticket is outside the query." : "No tickets in this sprint for the people wgo dash follows.", el("tr", null, null, el("tbody", null, null, table))); td.colSpan = COLS.length; }
    if (V.focusId == null || V.rows.indexOf(V.focusId) < 0) {
      var sel = cur.store.selection, s = V.rows.filter(function (id) { return sel.has(id); })[0];
      V.focusId = s || V.rows[0] || null;
    }
    rove();
  }
  function rowEl(r, first) {
    var m = V.m, el = cur.dom.el, n = r.node, t = n.ticket, inside = m.inside(n.id), sel = cur.store.selection.has(n.id);
    var tr = el("tr", (sel ? "sel" : "") + (inside ? "" : " out") + (first ? " first" : "") || null, null, null);
    tr.dataset.id = n.id; tr.dataset.k = "row:" + n.id; tr.dataset.hover = n.id;
    tr.setAttribute("aria-selected", String(sel));
    if (!inside) tr.title = "Outside the query: dimmed so the sprint stays whole";
    V.rows.push(n.id); V.rowEls.push(tr);
    // Ticket: key (linked) and summary.
    var td = el("td", "nm", null, tr);
    var a = cur.dom.link(td, n.label || t.key || n.id, t.url);
    if (a) a.dataset.k = "link:" + n.id;
    if (t.title) el("span", "ttl", t.title, td);
    if (!inside) el("span", "sr", " (outside the query)", td);
    td.title = (n.label || n.id) + (t.title ? " — " + t.title : "");
    // Assignee, once per run; screen readers hear it on every row.
    var as = el("td", r.assignee.unknown ? "unk" : r.assignee.rank === 1 ? "muted" : null, null, tr);
    if (first) as.textContent = r.assignee.text; else el("span", "sr", r.assignee.text, as);
    mismatchCell(el("td", "mm", null, tr), n);
    prsCell(el("td", "prs", null, tr), n);
    // Points.
    var pts = S.pointsOf(t), pd = el("td", "num" + (pts.kind === "unknown" ? " unk" : pts.kind === "unestimated" ? " muted" : ""), pts.text, tr);
    if (pts.kind === "unknown") pd.title = "Story points unknown: the site's story points field was not read for this ticket";
    else if (pts.kind === "unestimated") pd.title = "Read from Jira: no estimate";
    // Status.
    el("td", t.status ? "st" : "st unk", t.status || "unknown", tr);
    return tr;
  }
  function prsCell(td, n) {
    var el = cur.dom.el, f = n.ticket.facts, list = f && Array.isArray(f.linked_prs) ? f.linked_prs : null;
    if (!list) { td.classList.add("unk"); td.textContent = "unknown"; td.title = "Linked PRs unknown: no derived facts for this ticket"; return; }
    if (!list.length) { td.classList.add("muted"); td.textContent = "none"; return; }
    var ix = cur.index;
    list.slice(0, SHOW_PRS).forEach(function (lp) {
      var pr = ix && ix.byId.get(lp.id), g = S.prGlyph(lp.state), s = el("span", "pr " + (lp.state || "unknown"), null, td);
      s.dataset.hover = lp.id;
      var tip = S.prTitle(lp, pr && pr.pr, cur.queryIndex && cur.queryIndex.env.me);
      el("span", "gl", g.glyph, s).setAttribute("aria-hidden", "true");
      var a = cur.dom.link(s, lp.ref || lp.id, pr && pr.pr && pr.pr.url);
      // The details sit on the link, so the keyboard reaches them too.
      if (a) { a.dataset.k = "pr:" + n.id + ":" + lp.id; a.title = tip; a.setAttribute("aria-label", tip.replace(/\n/g, ". ")); }
      else s.title = tip;
    });
    if (list.length > SHOW_PRS) {
      var more = el("span", "more", "+" + (list.length - SHOW_PRS), td);
      more.title = list.slice(SHOW_PRS).map(function (lp) { return (lp.ref || lp.id) + ": " + S.prGlyph(lp.state).text; }).join("\n");
    }
  }
  function mismatchCell(td, n) {
    var el = cur.dom.el, mm = S.mismatchesOf(n.ticket);
    if (mm.unknown) { td.classList.add("unk"); td.textContent = "unknown"; td.title = "Mismatches " + mm.why; return; }
    if (!mm.list.length) { td.classList.add("muted"); td.textContent = "none"; return; }
    mm.list.forEach(function (x) {
      var b = cur.dom.button(td, null, "mmb", null);
      el("span", "gl", "!", b).setAttribute("aria-hidden", "true");
      b.appendChild(document.createTextNode(S.codeText(x.code)));
      b.dataset.act = "mm"; b.dataset.code = x.code; b.dataset.k = "mm:" + n.id + ":" + x.code;
      var on = active("mismatch", x.code);
      b.setAttribute("aria-pressed", String(on));
      b.setAttribute("aria-label", "Mismatch: " + S.codeText(x.code) + (x.evidence ? ", " + x.evidence : "") + ". Filter mismatch:" + x.code);
      b.title = S.codeText(x.code) + (x.evidence ? ": " + x.evidence : "") + "\nClick to filter mismatch:" + x.code;
    });
  }

  // rove gives the keyboard's row, and the links and buttons in it, the
  // only tab stops in the table.
  function rove() {
    V.rowEls.forEach(function (tr) {
      var on = tr.dataset.id === V.focusId;
      tr.tabIndex = on ? 0 : -1;
      Array.prototype.forEach.call(tr.querySelectorAll("a, button"), function (x) { x.tabIndex = on ? 0 : -1; });
    });
  }
  function paintSelection() {
    var sel = cur.store.selection;
    V.rowEls.forEach(function (tr) { var on = sel.has(tr.dataset.id); tr.classList.toggle("sel", on); tr.setAttribute("aria-selected", String(on)); });
  }

  // ---------- gestures ----------
  // applyClause toggles name:value in the query as a chart click does; a
  // query that would not parse is shown here with its fix, not applied.
  function applyClause(name, value) {
    var q = cur.getState("q"), next = L.toggleClause(q, cur.registry, name, value), err = cur.setQuery(next, { commit: true });
    if (err) { V.msg = { error: err.error, fixed: err.fixed }; V.msgFor = q; render(Object.assign({}, cur, { reason: "state" })); }
  }
  function onClick(ev) {
    var t = ev.target.closest("[data-act]");
    if (t) {
      var act = t.dataset.act;
      if (act === "sprint") cur.setState({ sprint: t.dataset.sprint }, { commit: true });
      else if (act === "cat") applyClause("status_category", t.dataset.cat);
      else if (act === "mm") applyClause("mismatch", t.dataset.code);
      else if (act === "outside") cur.setState({ outside: V.m.hide ? "" : "hide" }, { commit: true });
      else if (act === "fixq") cur.setQuery(t.dataset.q, { commit: true });
      return;
    }
    if (ev.target.closest("a")) return;
    var tr = ev.target.closest("tr[data-id]");
    if (!tr) return;
    V.focusId = tr.dataset.id; rove();
    cur.select([tr.dataset.id], ev.shiftKey || ev.metaKey);
  }
  function onKey(ev) {
    var tr = ev.target.closest && ev.target.closest("tr[data-id]");
    if (!tr || ev.target !== tr) return;
    var p = V.rows.indexOf(tr.dataset.id);
    if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); cur.select([tr.dataset.id], ev.shiftKey); return; }
    var nx = L.nextIndex(V.rows.length, p, ev.key);
    if (nx < 0) return;
    ev.preventDefault();
    V.focusId = V.rows[nx]; rove();
    var row = V.rowEls[nx];
    row.focus();
    if (row.scrollIntoView) row.scrollIntoView({ block: "nearest" });
    if (!ev.shiftKey) cur.select([V.focusId], false, "replace");
  }

  // extract is what Copy as Markdown asks /api/extract for: the sprint
  // summary of the sprint shown (or of sprint= as written, so a bad one
  // gets the server's explanation); the rows view's columns do not apply.
  function extract(ctx) {
    var list = sprintsOf(ctx.snapshot), raw = ctx.getState("sprint"), ch = S.chooseSprint(ctx.snapshot, list, raw);
    return { view: "sprint", sprint: ch.sprint ? String(ch.sprint.id) : raw, cols: "" };
  }

  window.WGOLive.registerView({ id: "sprint", label: "Sprint", question: "Does Jira agree with the code for this sprint, and how far along is it against its goal?",
    render: render, extract: extract,
    destroy: function () { hover(null); } });
})();
