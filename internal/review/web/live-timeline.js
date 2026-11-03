/* wgo dash PR timelines (spec/gh-104.md). An addon view, registered as
 * "timeline" through window.WGOLive, that answers "where has each PR's time
 * gone — in draft, waiting on whom, or with changes requested?", after
 * Plaisant and Shneiderman's LifeLines: one row per PR row of the query, in
 * the analysis table's sort order, on one linear time axis shared by every
 * row, with a "now" line. Segments are neutral and told apart by pattern and
 * a text label where they are wide enough; the accent marks only time spent
 * waiting on the operator (@me, or a team the snapshot shows them in).
 * Review events are ticks with ✓ or ✕. Brushing the axis zooms (t=a..b in
 * the URL) without filtering; a button applies the range as when:a..b. A
 * selected row expands into one lane per reviewer. A PR whose history has
 * not been fetched shows one thin bar from its creation, labelled so.
 *
 * The pure logic is logic(WGOLive, tableLogic), also module.exports under
 * node, so internal/review/testdata/live_logic.js tests it without a page.
 * Like live.js it writes no markup from strings and issues no requests. */
(function () {
  "use strict";
  var HOUR = 3600000, DAY = 24 * HOUR;

  // ---------- pure logic ----------
  function logic(L, T) {
    var TL = {};
    function ms(iso) { var t = typeof iso === "string" && iso ? Date.parse(iso) : NaN; return isFinite(t) ? t : null; }
    TL.ms = ms;
    function joinNames(list) {
      if (list.length < 2) return list.join("");
      return list.slice(0, -1).join(", ") + " and " + list[list.length - 1];
    }
    TL.joinNames = joinNames;
    TL.TERMINAL = { merged: true, closed: true };
    // STATES are the segment states in legend order, with their words.
    TL.STATES = [
      { state: "draft", text: "draft" }, { state: "waiting", text: "waiting" }, { state: "changes_requested", text: "changes requested" },
      { state: "approved", text: "approved" }, { state: "unattended", text: "unattended" }, { state: "merged", text: "merged" }, { state: "closed", text: "closed" }
    ];
    var WORD = {};
    TL.STATES.forEach(function (s) { WORD[s.state] = s.text; });

    // rows lists the query's PR rows (every PR without a query) in the
    // analysis table's sort order (sort=, through its exported comparator),
    // and the sort keys that applied.
    TL.rows = function (ix, res, sortRaw) {
      var rows = [];
      if (res) res.rowIdx.forEach(function (i) { if (ix.kinds[i] === "pr") rows.push(i); });
      else ix.kinds.forEach(function (k, i) { if (k === "pr") rows.push(i); });
      if (!T) return { rows: rows, sort: [] };
      var sort = T.parseSort(sortRaw).filter(function (s) { return s.key === "name" || T.isColumn(ix.reg, s.key); });
      return { rows: T.sortRows(ix, rows, sort), sort: sort };
    };

    // ---------- the time axis ----------
    // windowDays is the default span: the closed-recent window (gh-97),
    // max(2 × days, 28), which the snapshot covers in full.
    TL.windowDays = function (days) { return Math.max(2 * (days > 0 ? days : 14), 28); };
    TL.dayOf = function (t, env) { return env && env.dayOf ? env.dayOf(t) : new Date(t + ((env && env.offsetMs) || 0)).toISOString().slice(0, 10); };
    // dayStart is the first instant of a local day, at the snapshot's offset.
    TL.dayStart = function (day, env) {
      var t = Date.parse(day + "T00:00:00Z");
      return isFinite(t) ? t - ((env && env.offsetMs) || 0) : null;
    };
    var RANGE = /^(\d{4}-\d{2}-\d{2})\.\.(\d{4}-\d{2}-\d{2})$/;
    // parseRange reads t=a..b, local days with both ends included, as
    // {a, b} in ms from the start of a to the end of b; null otherwise.
    TL.parseRange = function (raw, env) {
      var m = RANGE.exec(String(raw || "").trim());
      if (!m) return null;
      var a = TL.dayStart(m[1], env), b = TL.dayStart(m[2], env);
      return a == null || b == null || b < a ? null : { a: a, b: b + DAY };
    };
    // formatRange is t= (and when:) for [a, b): the local days it touches.
    TL.formatRange = function (a, b, env) { return TL.dayOf(a, env) + ".." + TL.dayOf(Math.max(a, b - 1), env); };
    // domain is the axis: t= when it parses (zoomed), else the default window
    // from the start of its first local day to now, with a little room after.
    TL.domain = function (raw, nowMs, days, env) {
      var r = TL.parseRange(raw, env);
      if (r) return { a: r.a, b: r.b, zoomed: true };
      var a = TL.dayStart(TL.dayOf(nowMs - (TL.windowDays(days) - 1) * DAY, env), env);
      return { a: a, b: nowMs + Math.max(HOUR, (nowMs - a) * 0.015), zoomed: false };
    };
    // pan moves the domain by frac of its span; zoomOut doubles it about its
    // middle. Both return t=.
    TL.pan = function (dom, frac, env) { var d = (dom.b - dom.a) * frac; return TL.formatRange(dom.a + d, dom.b + d, env); };
    TL.zoomOut = function (dom, env) { var h = (dom.b - dom.a) / 2; return TL.formatRange(dom.a - h, dom.b + h, env); };
    // brushRange turns a drag from x0 to x1 over an axis width px wide into
    // t=, whole local days; null for a drag under 4 px (a click).
    TL.brushRange = function (x0, x1, dom, width, env) {
      var lo = Math.max(0, Math.min(x0, x1)), hi = Math.min(width, Math.max(x0, x1));
      if (!(width > 0) || hi - lo < 4) return null;
      var k = (dom.b - dom.a) / width;
      return TL.formatRange(dom.a + lo * k, dom.a + hi * k, env);
    };
    // applyWhen replaces expr's when: clauses with when:<range>: the brushed
    // range as a filter, when the operator wants one.
    TL.applyWhen = function (expr, reg, range) {
      var q = L.parseQuery(expr || "", reg), keep = q.error ? [] : q.clauses.filter(function (c) { return c.name !== "when"; }).map(function (c) { return c.raw; });
      keep.push("when:" + range);
      return keep.join(";");
    };
    var STEPS = [HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR, DAY, 2 * DAY, 7 * DAY, 14 * DAY, 28 * DAY, 91 * DAY, 182 * DAY, 364 * DAY];
    var MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
    // dayLabel is "Oct 26"; timeLabel adds the time, "Oct 26 14:00", at the
    // snapshot's offset.
    TL.dayLabel = function (t, env) { var d = TL.dayOf(t, env); return MONTHS[+d.slice(5, 7) - 1] + " " + (+d.slice(8, 10)); };
    TL.timeLabel = function (t, env) { return TL.dayLabel(t, env) + " " + new Date(t + ((env && env.offsetMs) || 0)).toISOString().slice(11, 16); };
    // ticks places axis ticks about every 80 px: at local midnights for day
    // steps, at local hours below a day; labels name the day, or the time.
    TL.ticks = function (dom, width, env) {
      var span = dom.b - dom.a, max = Math.max(2, Math.floor(width / 80)), off = (env && env.offsetMs) || 0;
      var step = STEPS.filter(function (s) { return span / s <= max; })[0] || STEPS[STEPS.length - 1];
      var unit = step < DAY ? step : DAY, n = step < DAY ? 1 : step / DAY, out = [];
      var t = Math.ceil((dom.a + off) / unit) * unit - off;
      for (var guard = 0; t <= dom.b && guard < 400; t += unit, guard++) {
        var k = Math.round((t + off) / unit);
        if (k % n) continue;
        var midnight = (t + off) % DAY === 0;
        out.push({ t: t, x: (t - dom.a) / span * width, label: step < DAY && !midnight ? new Date(t + off).toISOString().slice(11, 16) : TL.dayLabel(t, env) });
      }
      return out;
    };

    // ---------- a PR's history ----------
    // segments reads a PR's timeline as [{state, a, b, reviewers, last}] in
    // ms, each ending where the next starts and the last at now; null when
    // the PR has no timeline.
    TL.segments = function (pr, nowMs) {
      var segs = pr && pr.timeline && Array.isArray(pr.timeline.segments) ? pr.timeline.segments : null;
      if (!segs || !segs.length) return null;
      var out = [];
      segs.forEach(function (s, i) {
        var a = ms(s && s.start);
        if (a == null) return;
        var nx = i + 1 < segs.length ? ms(segs[i + 1].start) : null;
        out.push({ state: String(s.state || ""), a: a, b: nx != null ? nx : Math.max(a, nowMs), reviewers: Array.isArray(s.reviewers) ? s.reviewers.filter(function (r) { return typeof r === "string"; }) : [], last: i === segs.length - 1 });
      });
      return out.length ? out : null;
    };
    // fallback is the one bar of a PR without a history: from created_at to
    // its merge or close when known, else to now; null without created_at.
    TL.fallback = function (pr, nowMs) {
      var a = ms(pr && pr.created_at);
      if (a == null) return null;
      var end = pr.state === "merged" ? ms(pr.merged_at) : pr.state === "closed" ? ms(pr.closed_at) : null;
      return { a: a, b: Math.max(a, end != null ? end : nowMs), end: end != null ? pr.state : "" };
    };
    // reviews are the review ticks: [{t, who, ok}], ok for an approval.
    TL.reviews = function (pr) {
      var rs = pr && pr.timeline && Array.isArray(pr.timeline.reviews) ? pr.timeline.reviews : [];
      return rs.map(function (r) { return { t: ms(r && r.at), who: String((r && r.reviewer) || ""), ok: r && r.state === "APPROVED" }; }).filter(function (r) { return r.t != null; });
    };
    // lanes are the reviewer lanes of an expanded row: [{who, waits: [{a, b}]}].
    TL.lanes = function (pr, nowMs) {
      var ls = pr && pr.timeline && Array.isArray(pr.timeline.lanes) ? pr.timeline.lanes : [];
      return ls.map(function (l) {
        var waits = (Array.isArray(l && l.waits) ? l.waits : []).map(function (w) { var a = ms(w && w.start), b = w && w.end ? ms(w.end) : nowMs; return { a: a, b: b, open: !(w && w.end) }; })
          .filter(function (w) { return w.a != null && w.b != null && w.b > w.a; });
        return { who: String((l && l.reviewer) || ""), waits: waits };
      }).filter(function (l) { return l.who; });
    };

    // ---------- the operator ----------
    function requestsOf(pr) {
      if (Array.isArray(pr.review_requests)) return pr.review_requests.map(function (r) { return r && r.reviewer; }).filter(function (s) { return typeof s === "string"; });
      if (pr.reviewers_known && Array.isArray(pr.requested_reviewers)) return pr.requested_reviewers.filter(function (s) { return typeof s === "string"; });
      return [];
    }
    // operator is who the accent is for: @me's login (lowercased) and the
    // teams the snapshot shows them in — the only team requested on a PR
    // whose review_requested involvement names @me with no direct request.
    TL.operator = function (ix) {
      var me = String((ix.env && ix.env.me) || "").toLowerCase(), teams = {};
      if (me) ix.nodes.forEach(function (n) {
        var pr = n && n.pr;
        if (!pr || !Array.isArray(pr.involvement)) return;
        if (!pr.involvement.some(function (v) { return v && String(v.person).toLowerCase() === me && v.role === "review_requested"; })) return;
        var reqs = requestsOf(pr), tm = reqs.filter(function (r) { return /^team:/i.test(r); });
        if (tm.length === 1 && !reqs.some(function (r) { return r.toLowerCase() === me; })) teams[tm[0].toLowerCase()] = true;
      });
      return { me: me, teams: teams };
    };
    TL.isMine = function (who, op) { var w = String(who || "").toLowerCase(); return !!op.me && (w === op.me || op.teams[w] === true); };
    function mergeSpans(list) {
      list.sort(function (x, y) { return x.a - y.a; });
      var out = [];
      list.forEach(function (s) { var p = out[out.length - 1]; if (p && s.a <= p.b) p.b = Math.max(p.b, s.b); else out.push({ a: s.a, b: s.b }); });
      return out;
    }
    // mineSpans are the stretches a PR waited on the operator: their lanes'
    // waits, directly or through one of their teams, within its waiting
    // segments. The accent marks these and nothing else.
    TL.mineSpans = function (pr, segs, op, nowMs) {
      if (!segs || !op.me) return [];
      var waits = [], out = [];
      TL.lanes(pr, nowMs).forEach(function (l) { if (TL.isMine(l.who, op)) waits = waits.concat(l.waits); });
      segs.forEach(function (s) {
        if (s.state !== "waiting") return;
        waits.forEach(function (w) { var a = Math.max(s.a, w.a), b = Math.min(s.b, w.b); if (b > a) out.push({ a: a, b: b }); });
      });
      return mergeSpans(out);
    };

    // ---------- words ----------
    // dur is a duration as the table writes days: "12d", "5h", "<1h".
    TL.dur = function (d) {
      if (!(d >= 0)) return "";
      if (d < HOUR) return "<1h";
      if (d < DAY) return Math.round(d / HOUR) + "h";
      return Math.round(d / DAY) + "d";
    };
    // names joins reviewers, the operator as "you" and their teams marked.
    function names(list, op) {
      return joinNames(list.map(function (r) {
        var w = String(r).toLowerCase();
        return op && op.me && w === op.me ? "you" : op && op.teams && op.teams[w] === true ? r + " (you)" : r;
      }));
    }
    // segWords is a segment in words: "waiting on bob and you", "changes
    // requested by bob", "approved by carol" (short: "approved").
    TL.segWords = function (s, op, short) {
      var who = names(s.reviewers || [], op);
      switch (s.state) {
        case "waiting": return "waiting" + (who ? " on " + who : "");
        case "changes_requested": return "changes requested" + (who ? " by " + who : "");
        case "approved": return "approved" + (who && !short ? " by " + who : "");
        case "unattended": return short ? "unattended" : "unattended: ready, nobody asked, no approval";
      }
      return WORD[s.state] || s.state || "unknown";
    };
    // label is the text inside a segment when it is wide enough.
    TL.label = function (s, mine, op) {
      if (s.state === "waiting" && mine) return "waiting on you";
      return s.state === "waiting" || s.state === "changes_requested" ? TL.segWords(s, op, true) : WORD[s.state] || s.state;
    };
    // labels are where a laid-out segment's words go, in time: a wait's
    // accent spans read "waiting on you", anchored on the accent, and the
    // stretch before the first of them names the others waited on; any other
    // segment has one label from its start.
    TL.labels = function (s, mineSpans, op) {
      var mine = (mineSpans || []).filter(function (m) { return m.a < s.b && m.b > s.a; });
      if (s.state !== "waiting" || !mine.length) return [{ a: s.a, b: s.b, text: TL.label(s, false, op), short: TL.label({ state: s.state, reviewers: [] }, false, op), mine: false }];
      var others = s.reviewers.filter(function (r) { return !TL.isMine(r, op); }), out = [];
      if (mine[0].a > s.a) out.push({ a: s.a, b: mine[0].a, text: TL.segWords({ state: "waiting", reviewers: others }, op, true), short: "waiting", mine: false });
      mine.forEach(function (m) { out.push({ a: Math.max(s.a, m.a), b: Math.min(s.b, m.b), text: "waiting on you", short: "you", mine: true }); });
      return out;
    };
    // fits says whether text fits in w px at the view's 11 px font.
    TL.fits = function (text, w) { return w >= text.length * 6.3 + 8; };
    // summary is a row's text for screen readers: "draft 12d, waiting on bob
    // 30d, approved 2d"; a PR's final merge or close has no duration.
    TL.summary = function (pr, segs, op, nowMs) {
      if (!segs) {
        var f = TL.fallback(pr, nowMs);
        if (!f) return "history not fetched yet; created at an unknown time";
        return "history not fetched yet; " + TL.dur(f.b - f.a) + (f.end === "merged" ? " from creation to merge" : f.end === "closed" ? " from creation to close" : " since creation");
      }
      return segs.map(function (s) { var w = TL.segWords(s, op, true); return s.last && TL.TERMINAL[s.state] ? w : w + " " + TL.dur(s.b - s.a); }).join(", ");
    };
    // stateText is the label right of a row: its current state, for how long
    // or since when. A wait names whoever is waited on now, not everyone
    // the segment waited on.
    // currentWait is the PR's current state as gh-99's waiting_since reads
    // it: for a wait, whoever is waited on now and since when — the earliest
    // wait still open on their lanes, not the start of a segment that began
    // with someone since answered; otherwise the last segment as it is.
    TL.currentWait = function (pr, segs, nowMs) {
      var s = segs[segs.length - 1], out = { state: s.state, reviewers: s.reviewers, a: s.a };
      if (s.state !== "waiting") return out;
      var open = TL.lanes(pr, nowMs).filter(function (l) { return l.waits.length && l.waits[l.waits.length - 1].open; });
      if (!open.length) return out;
      out.reviewers = open.map(function (l) { return l.who; });
      out.a = Math.min.apply(null, open.map(function (l) { return l.waits[l.waits.length - 1].a; }));
      return out;
    };
    TL.stateText = function (pr, segs, op, nowMs, env) {
      if (segs) {
        var s = segs[segs.length - 1];
        if (TL.TERMINAL[s.state]) return s.state + " " + TL.dayLabel(s.a, env);
        var w = TL.currentWait(pr, segs, nowMs);
        return TL.segWords(w, op, true) + " " + TL.dur(nowMs - w.a);
      }
      var st = pr && pr.state === "open" && pr.is_draft ? "draft" : (pr && pr.state) || "state unknown";
      return st + " · no history";
    };
    // tip is a segment's details on demand: what, who, from, to, how long.
    TL.tip = function (s, mineSpans, op, env) {
      var lines = [TL.segWords(s, op, false)], mine = 0;
      (mineSpans || []).forEach(function (m) { var a = Math.max(s.a, m.a), b = Math.min(s.b, m.b); if (b > a) mine += b - a; });
      if (s.last && TL.TERMINAL[s.state]) lines.push(TL.timeLabel(s.a, env));
      else lines.push(TL.timeLabel(s.a, env) + " – " + (s.last ? "now" : TL.timeLabel(s.b, env)) + " · " + TL.dur(s.b - s.a));
      if (mine) lines.push("waiting on you for " + TL.dur(mine));
      return lines;
    };
    // layout places segments on [0, width] px: clipped to the domain, cut
    // marking one that runs on beyond an edge. A final merge or close is a
    // cap at its time, not a bar.
    TL.layout = function (segs, dom, width) {
      var k = width / (dom.b - dom.a), out = [];
      segs.forEach(function (s, i) {
        if (s.last && TL.TERMINAL[s.state]) {
          if (s.a >= dom.a && s.a <= dom.b) out.push({ i: i, s: s, cap: true, x: (s.a - dom.a) * k, w: 0 });
          return;
        }
        var a = Math.max(s.a, dom.a), b = Math.min(s.b, dom.b);
        if (b <= a) return;
        out.push({ i: i, s: s, x: (a - dom.a) * k, w: (b - a) * k, cutL: s.a < dom.a, cutR: s.b > dom.b });
      });
      return out;
    };
    // at is the laid-out segment under x px, or the nearest within 4 px.
    TL.at = function (laid, x) {
      var best = null, dist = 5;
      laid.forEach(function (p) {
        var d = x < p.x ? p.x - x : x > p.x + p.w ? x - p.x - p.w : 0;
        if (d < dist) { dist = d; best = p; }
      });
      return best;
    };
    return TL;
  }

  if (typeof module !== "undefined" && module.exports) module.exports = logic;
  if (typeof window === "undefined" || !window.WGOLive || typeof document === "undefined") return;

  // ---------- the view ----------
  var L = window.WGOLive, T = L.tableLogic || null, TL = logic(L, T);
  var ROW_H = 26, LANE_H = 18, BAR_Y = 10, BAR_H = 12, LEFT = 150, RIGHT = 200, VIRTUAL_MIN = 120, MARGIN = 12;
  var cur = null, V = null;

  function build(ctx) {
    var dom = ctx.dom, el = dom.el, host = ctx.container, button = dom.button;
    host.textContent = "";
    host.classList.add("tl-host");
    V = { host: host, focus: 0, focusId: null, seg: -1, model: null, win: [0, 0], raf: 0, brush: null, cache: null };
    V.bar = el("div", "tl-bar", null, host);
    V.sum = el("span", "tl-sum", null, V.bar);
    V.earlier = button(V.bar, "◂", null, function () { zoom(TL.pan(V.model.dom, -0.5, V.model.env)); }, "Earlier: move the axis back by half its span");
    V.later = button(V.bar, "▸", null, function () { zoom(TL.pan(V.model.dom, 0.5, V.model.env)); }, "Later: move the axis on by half its span");
    V.out = button(V.bar, "Zoom out", null, function () { zoom(TL.zoomOut(V.model.dom, V.model.env)); });
    V.reset = button(V.bar, "Reset zoom", null, function () { cur.setState({ t: "" }, { commit: true }); });
    V.reset.title = "Back to the default span: the last " + TL.windowDays(0) + " days or more";
    V.when = button(V.bar, "", null, function () {
      var m = V.model, range = TL.formatRange(m.dom.a, m.dom.b, m.env);
      cur.setQuery(TL.applyWhen(cur.getState("q"), cur.registry, range), { commit: true });
    });
    V.legend = el("div", "tl-legend", null, host);
    V.legend.setAttribute("aria-label", "Legend");
    legend(ctx);
    V.head = el("div", "tl-row tl-head", null, host);
    el("span", "tl-ref", "PR", V.head);
    V.axis = svg("svg", { class: "tl-axis", height: 22 }, V.head);
    V.axis.setAttribute("role", "img");
    V.nowTxt = el("span", "tl-state", null, V.head);
    V.scroll = el("div", "tl-scroll", null, host);
    V.list = el("div", "tl-list", null, V.scroll);
    V.list.setAttribute("role", "list");
    V.list.setAttribute("aria-label", "PR timelines. Arrow keys move between rows and, left and right, between a row's segments; Enter or Space selects a row, which shows one lane per reviewer.");
    V.tip = el("div", "tip tl-tip", null, host); V.tip.hidden = true;
    V.live = el("div", "sr", null, host); V.live.setAttribute("aria-live", "polite");

    V.axis.addEventListener("pointerdown", function (ev) {
      if (ev.button !== 0) return;
      var x = ev.clientX - V.axis.getBoundingClientRect().left;
      V.brush = { x0: x, x1: x, el: svg("rect", { class: "tl-brush", x: x, y: 0, width: 0, height: 22 }, V.axis) };
      V.axis.setPointerCapture(ev.pointerId);
      ev.preventDefault();
    });
    V.axis.addEventListener("pointermove", function (ev) {
      if (!V.brush) return;
      V.brush.x1 = ev.clientX - V.axis.getBoundingClientRect().left;
      var lo = Math.max(0, Math.min(V.brush.x0, V.brush.x1)), hi = Math.min(V.model.laneW, Math.max(V.brush.x0, V.brush.x1));
      V.brush.el.setAttribute("x", lo); V.brush.el.setAttribute("width", Math.max(0, hi - lo));
    });
    function endBrush(ev, apply) {
      if (!V.brush) return;
      var b = V.brush; V.brush = null;
      if (b.el.parentNode) b.el.parentNode.removeChild(b.el);
      if (apply) { var r = TL.brushRange(b.x0, ev.clientX - V.axis.getBoundingClientRect().left, V.model.dom, V.model.laneW, V.model.env); if (r) zoom(r); }
    }
    V.axis.addEventListener("pointerup", function (ev) { endBrush(ev, true); });
    V.axis.addEventListener("pointercancel", function (ev) { endBrush(ev, false); });

    V.list.addEventListener("click", function (ev) {
      if (ev.target.closest("a")) return;
      var row = ev.target.closest(".tl-row[data-p]"); if (!row) return;
      setFocus(+row.dataset.p); rove();
      cur.select([rowId(V.focus)], ev.shiftKey || ev.metaKey);
    });
    V.list.addEventListener("keydown", onKey);
    V.list.addEventListener("focusin", function (ev) {
      var row = ev.target.closest && ev.target.closest(".tl-row[data-p]");
      if (row && +row.dataset.p !== V.focus) { setFocus(+row.dataset.p); V.seg = -1; }
    });
    V.list.addEventListener("focusout", function () { if (!V.host.contains(document.activeElement)) hideTip(); });
    V.list.addEventListener("mousemove", function (ev) {
      var lane = ev.target.closest("svg.tl-lane"), row = ev.target.closest(".tl-row[data-p]");
      hover(row ? rowId(+row.dataset.p) : null);
      if (!lane || !row) { hideTip(); return; }
      var r = V.model.rows[+row.dataset.p], x = ev.clientX - lane.getBoundingClientRect().left;
      showTip(r, lane.dataset.who ? laneAt(r, lane.dataset.who, x) : TL.at(r.laid || [], x), ev.clientX, ev.clientY);
    });
    V.list.addEventListener("mouseleave", function () { hideTip(); hover(null); });
    V.scroll.addEventListener("scroll", function () {
      hideTip();
      if (!V.model || !V.model.virtual || V.raf) return;
      V.raf = requestAnimationFrame(function () { V.raf = 0; var w = windowFor(); if (w[0] !== V.win[0] || w[1] !== V.win[1]) paintRows(); });
    });
  }

  function svg(tag, attrs, parent) { return cur.dom.svgEl(tag, attrs, parent); }
  // segAttrs are a segment rect's attributes; a draft is filled with the
  // hatch pattern by attribute, since the addon stylesheet may not name a url.
  function segAttrs(state, x, y, w, h) {
    var a = { class: "seg s-" + state, x: x, y: y, width: w, height: h };
    if (state === "draft") a.fill = "url(#tl-hatch)";
    return a;
  }
  function zoom(range) { cur.setState({ t: range }, { commit: true }); }
  function rowId(p) { return V.model.rows[p].id; }
  function setFocus(p) { V.focus = p; V.focusId = p >= 0 && p < V.model.rows.length ? rowId(p) : null; }
  function rove() { Array.prototype.forEach.call(V.list.querySelectorAll(".tl-row[data-p]"), function (r) { r.tabIndex = +r.dataset.p === V.focus ? 0 : -1; }); }
  function hover(id) { if (cur.hover) cur.hover(id); }

  // legend is the key: each state's swatch and words, the accent, the ticks,
  // the fallback bar and the now line. Colour is never the only cue.
  function legend(ctx) {
    var el = ctx.dom.el;
    function item(text, draw) {
      var li = el("span", "tl-key", null, V.legend), s = svg("svg", { width: 22, height: 12, "aria-hidden": "true" }, li);
      draw(s);
      el("span", null, text, li);
    }
    TL.STATES.forEach(function (st) {
      if (TL.TERMINAL[st.state]) return;
      item(st.text, function (s) { svg("rect", segAttrs(st.state, 1, 1, 20, 10), s); });
    });
    item("waiting on you", function (s) { svg("rect", { class: "seg s-mine", x: 1, y: 1, width: 20, height: 10 }, s); });
    item("merged or closed", function (s) { svg("line", { class: "cap", x1: 11, x2: 11, y1: 0, y2: 12 }, s); });
    item("review: ✓ approved, ✕ changes requested", function (s) { var t = svg("text", { class: "tick", x: 11, y: 10, "text-anchor": "middle" }, s); t.textContent = "✓"; });
    item("history not fetched yet", function (s) { svg("line", { class: "none", x1: 1, x2: 21, y1: 6, y2: 6 }, s); });
    item("now", function (s) { svg("line", { class: "now", x1: 11, x2: 11, y1: 0, y2: 12 }, s); });
    var defs = svg("svg", { class: "tl-defs", width: 0, height: 0, "aria-hidden": "true", focusable: "false" }, V.legend);
    var p = svg("pattern", { id: "tl-hatch", width: 5, height: 5, patternUnits: "userSpaceOnUse", patternTransform: "rotate(45)" }, svg("defs", {}, defs));
    svg("line", { class: "hatch", x1: 0, y1: 0, x2: 0, y2: 5 }, p);
  }

  // model works out the rows, the axis and each row's layout for ctx.
  function model(ctx) {
    var ix = ctx.queryIndex, env = ix.env, nowMs = env.nowMs != null ? env.nowMs : Date.now();
    if (!V.cache || V.cache.ix !== ix) V.cache = { ix: ix, op: TL.operator(ix), segs: new Map() };
    var r = TL.rows(ix, ctx.query, ctx.getState("sort")), op = V.cache.op;
    var laneW = Math.max(240, (V.host.clientWidth || 1200) - LEFT - RIGHT - 40);
    var m = { ix: ix, env: env, nowMs: nowMs, op: op, sort: r.sort, laneW: laneW, dom: TL.domain(ctx.getState("t"), nowMs, ctx.snapshot && ctx.snapshot.days, env), rows: [], withHistory: 0, height: 0 };
    m.ticks = TL.ticks(m.dom, laneW, env);
    var sel = ctx.store.selection;
    r.rows.forEach(function (i) {
      var n = ix.nodes[i], id = ix.ids[i], pr = n.pr || {}, c = V.cache.segs.get(id);
      if (!c) {
        var segs = TL.segments(pr, nowMs);
        c = { segs: segs, mine: TL.mineSpans(pr, segs, op, nowMs), lanes: TL.lanes(pr, nowMs), reviews: TL.reviews(pr), fb: segs ? null : TL.fallback(pr, nowMs) };
        c.summary = TL.summary(pr, segs, op, nowMs);
        c.state = TL.stateText(pr, segs, op, nowMs, env);
        V.cache.segs.set(id, c);
      }
      if (c.segs) m.withHistory++;
      var expanded = sel.has(id) && c.lanes.length > 0;
      var row = { i: i, id: id, n: n, pr: pr, c: c, sel: sel.has(id), top: m.height, h: ROW_H + (expanded ? c.lanes.length * LANE_H : 0), expanded: expanded };
      m.height += row.h;
      m.rows.push(row);
    });
    m.virtual = m.rows.length > VIRTUAL_MIN;
    return m;
  }

  function render(ctx) {
    cur = ctx;
    if (!V || V.host !== ctx.container) build(ctx);
    if (!ctx.queryIndex) return;
    if (ctx.reason === "hover") return;
    var hadFocus = V.list.contains(document.activeElement);
    V.model = model(ctx);
    var at = V.focusId == null ? -1 : V.model.rows.findIndex(function (r) { return r.id === V.focusId; });
    setFocus(at >= 0 ? at : Math.min(Math.max(V.focus, 0), V.model.rows.length - 1));
    hideTip();
    paintBar(); paintAxis(); paintRows();
    if (hadFocus) focusRow(V.focus);
  }

  function sortText(m) {
    if (!m.sort.length) return "in the table's default order";
    return "sorted as the table: " + m.sort.map(function (s) { return T.colInfo(m.ix.reg, s.key).label + (s.desc ? " ▼" : " ▲"); }).join(", then ");
  }
  function paintBar() {
    var m = V.model, plural = cur.dom.plural, span = TL.dayLabel(m.dom.a, m.env) + " – " + TL.dayLabel(m.dom.zoomed ? m.dom.b - 1 : m.nowMs, m.env);
    V.sum.textContent = plural(m.rows.length, "PR") + " · " + sortText(m) + " · history cached for " + m.withHistory + " of " + m.rows.length + " · " + span +
      (m.dom.zoomed ? " (zoomed; drag the axis to zoom again)" : " (the last " + TL.windowDays(cur.snapshot && cur.snapshot.days) + " days; drag the axis to zoom)");
    var range = TL.formatRange(m.dom.a, m.dom.b, m.env);
    V.when.textContent = "Filter to these days";
    V.when.title = "Apply the axis range as a filter: when:" + range + " (merged, closed or last updated in it)";
    V.when.setAttribute("aria-label", "Filter the rows to when:" + range);
    V.when.disabled = !m.dom.zoomed; V.reset.disabled = !m.dom.zoomed;
  }

  function paintAxis() {
    var m = V.model, w = m.laneW;
    V.axis.textContent = "";
    V.axis.setAttribute("width", w);
    V.axis.setAttribute("aria-label", "Time axis from " + TL.dayLabel(m.dom.a, m.env) + " to " + TL.dayLabel(m.dom.b - 1, m.env) + ". Drag across it to zoom.");
    svg("line", { class: "base", x1: 0, x2: w, y1: 21.5, y2: 21.5 }, V.axis);
    var nx = (m.nowMs - m.dom.a) / (m.dom.b - m.dom.a) * w;
    m.ticks.forEach(function (t) {
      svg("line", { class: "grid", x1: t.x, x2: t.x, y1: 15, y2: 22 }, V.axis);
      // The now label wins over a tick label it would collide with.
      if (t.x + 3 + t.label.length * 6.3 > nx - 30 && t.x < nx + 2) return;
      var tx = svg("text", { x: t.x + 3, y: 12 }, V.axis); tx.textContent = t.label;
    });
    if (nx >= 0 && nx <= w) {
      svg("line", { class: "now", x1: nx, x2: nx, y1: 0, y2: 22 }, V.axis);
      var t = svg("text", { class: "nowlbl", x: nx - 3, y: 12, "text-anchor": "end" }, V.axis); t.textContent = "now";
    }
    V.nowTxt.textContent = "Now: " + TL.timeLabel(m.nowMs, m.env);
    V.nowTxt.title = "The snapshot's time, which every duration counts to";
  }

  function windowFor() {
    var m = V.model, rows = m.rows;
    if (!m.virtual) return [0, rows.length];
    var top = V.scroll.scrollTop, h = V.scroll.clientHeight || 600, lo = 0, hi = rows.length;
    while (lo < hi) { var mid = (lo + hi) >> 1; if (rows[mid].top + rows[mid].h < top) lo = mid + 1; else hi = mid; }
    var end = lo;
    while (end < rows.length && rows[end].top < top + h) end++;
    return [Math.max(0, lo - MARGIN), Math.min(rows.length, end + MARGIN)];
  }
  function spacer(px) { var d = cur.dom.el("div", "tl-spacer", null, null); d.style.height = px + "px"; d.setAttribute("aria-hidden", "true"); return d; }
  function paintRows() {
    var m = V.model, el = cur.dom.el, w = windowFor(), frag = document.createDocumentFragment();
    var hadFocus = V.list.contains(document.activeElement);
    V.win = w;
    V.list.textContent = "";
    if (!m.rows.length) { el("div", "tl-empty", cur.query ? "No PRs match the query." : "No PRs in the snapshot.", V.list); return; }
    if (w[0] > 0) frag.appendChild(spacer(m.rows[w[0]].top));
    var tab = V.focus >= w[0] && V.focus < w[1] ? V.focus : w[0];
    for (var p = w[0]; p < w[1]; p++) frag.appendChild(rowEl(p, p === tab));
    if (w[1] < m.rows.length) frag.appendChild(spacer(m.height - m.rows[w[1]].top));
    V.list.appendChild(frag);
    if (hadFocus) {
      var f = V.list.querySelector(".tl-row[data-p=\"" + V.focus + "\"]") || V.list.querySelector(".tl-row[data-p]");
      if (f) { setFocus(+f.dataset.p); rove(); f.focus({ preventScroll: true }); }
    }
  }

  function x(m, t) { return (t - m.dom.a) / (m.dom.b - m.dom.a) * m.laneW; }

  // rowEl draws row p: the PR's ref, its lane, its current state, and the
  // summary sentence for screen readers; an expanded row adds a lane per
  // reviewer.
  function rowEl(p, tabbable) {
    var m = V.model, r = m.rows[p], el = cur.dom.el, c = r.c, pr = r.pr;
    var box = el("div", "tl-row" + (r.sel ? " sel" : "") + (c.segs ? "" : " nohist") + (pr.timeline && pr.timeline.freshness === "stale" ? " stale" : ""), null, null);
    box.dataset.p = String(p); box.tabIndex = tabbable ? 0 : -1;
    box.setAttribute("role", "listitem"); box.setAttribute("aria-selected", String(r.sel));
    if (r.expanded) box.setAttribute("aria-expanded", "true");
    var ref = el("span", "tl-ref", null, box), label = r.n.label || r.id;
    if (L.safeURL(pr.url)) cur.dom.link(ref, label, pr.url); else ref.textContent = label;
    ref.title = label + (pr.title ? " — " + pr.title : "");
    var lane = svg("svg", { class: "tl-lane", width: m.laneW, height: ROW_H, "aria-hidden": "true" }, box);
    drawLane(lane, r);
    var stateTxt = c.state + (pr.timeline && pr.timeline.freshness === "stale" ? " · history as of " + TL.dayLabel(TL.ms(pr.timeline.fetched_at) || m.nowMs, m.env) : "");
    el("span", "tl-state", stateTxt, box).title = stateTxt;
    el("span", "sr", label + ": " + c.summary + (r.expanded ? ". Reviewer lanes follow." : ""), box);
    if (r.expanded) c.lanes.forEach(function (l) { laneRow(box, r, l); });
    return box;
  }

  function drawLane(s, r) {
    var m = V.model, c = r.c;
    m.ticks.forEach(function (t) { svg("line", { class: "grid", x1: t.x, x2: t.x, y1: 0, y2: ROW_H }, s); });
    if (!c.segs) {
      if (c.fb) {
        var a = Math.max(0, x(m, c.fb.a)), b = Math.min(m.laneW, x(m, c.fb.b)), text = "history not fetched yet";
        if (b > a) svg("line", { class: "none", x1: a, x2: b, y1: BAR_Y + BAR_H / 2, y2: BAR_Y + BAR_H / 2 }, s);
        if (b > a && TL.fits(text, b - a)) { var t1 = svg("text", { class: "lbl lbl-none", x: a + 4, y: BAR_Y + 2 }, s); t1.textContent = text; }
      } else { var t2 = svg("text", { class: "lbl lbl-none", x: 4, y: BAR_Y + 9 }, s); t2.textContent = "history not fetched yet; created at an unknown time"; }
    } else {
      r.laid = TL.layout(c.segs, m.dom, m.laneW);
      r.laid.forEach(function (q) {
        var st = q.s.state;
        if (q.cap) { svg("line", { class: "cap", x1: q.x, x2: q.x, y1: BAR_Y - 2, y2: BAR_Y + BAR_H + 2 }, s); return; }
        // A 2 px surface gap parts touching segments.
        var gx = q.x + (q.cutL ? 0 : 1), gw = Math.max(0.5, q.w - (q.cutL ? 0 : 1) - (q.cutR ? 0 : 1));
        if (TL.TERMINAL[st]) svg("line", { class: "s-closed", x1: gx, x2: gx + gw, y1: BAR_Y + BAR_H / 2, y2: BAR_Y + BAR_H / 2 }, s);
        else svg("rect", segAttrs(st, gx, BAR_Y, gw, BAR_H), s);
      });
      c.mine.forEach(function (sp) { mineRect(s, m, sp, BAR_Y, BAR_H); });
      r.laid.forEach(function (q) {
        if (q.cap) return;
        if (q.cutL) { var g = svg("text", { class: "lbl cut", x: 2, y: BAR_Y + 9 }, s); g.textContent = "◂"; }
        TL.labels(q.s, c.mine, m.op).forEach(function (lb) {
          var a = Math.max(0, x(m, lb.a)), b = Math.min(m.laneW, x(m, lb.b)), pad = a === 0 && q.cutL ? 12 : 5;
          var text = TL.fits(lb.text, b - a - pad + 5) ? lb.text : TL.fits(lb.short, b - a - pad + 5) ? lb.short : "";
          if (!text) return;
          var t = svg("text", { class: "lbl l-" + q.s.state + (lb.mine ? " l-mine" : ""), x: a + pad, y: BAR_Y + 9 }, s);
          t.textContent = text;
        });
      });
      c.reviews.forEach(function (rv) { tick(s, m, rv, 0); });
    }
    nowLine(s, m, ROW_H);
  }
  function tick(s, m, rv, y0) {
    var tx = x(m, rv.t);
    if (tx < 0 || tx > m.laneW) return;
    svg("line", { class: "tickline", x1: tx, x2: tx, y1: y0 + 1, y2: y0 + BAR_Y + BAR_H }, s);
    var t = svg("text", { class: "tick" + (rv.ok ? " ok" : " no"), x: tx + 2, y: y0 + 8 }, s);
    t.textContent = rv.ok ? "✓" : "✕";
  }
  // mineRect draws an accent span, its times kept for checks.
  function mineRect(s, m, sp, y, h) {
    var a = Math.max(0, x(m, sp.a)), b = Math.min(m.laneW, x(m, sp.b));
    if (b > a) svg("rect", { class: "seg s-mine", x: a + 1, y: y, width: Math.max(0.5, b - a - 2), height: h, "data-a": sp.a, "data-b": sp.b }, s);
  }
  function nowLine(s, m, h) {
    var nx = x(m, m.nowMs);
    if (nx >= 0 && nx <= m.laneW) svg("line", { class: "now", x1: nx, x2: nx, y1: 0, y2: h }, s);
  }
  // laneRow is one reviewer's lane under an expanded row: their waits, the
  // accent where the reviewer is the operator and the PR was waiting (as on
  // the row: not while it was, say, with changes requested), and their
  // review ticks.
  function laneRow(box, r, l) {
    var m = V.model, el = cur.dom.el, sub = el("div", "tl-sub", null, box), mine = TL.isMine(l.who, m.op), total = 0;
    el("span", "tl-ref", "↳ " + (mine && l.who.toLowerCase() === m.op.me ? l.who + " (you)" : l.who), sub);
    var s = svg("svg", { class: "tl-lane", width: m.laneW, height: LANE_H, "aria-hidden": "true" }, sub);
    s.dataset.who = l.who;
    m.ticks.forEach(function (t) { svg("line", { class: "grid", x1: t.x, x2: t.x, y1: 0, y2: LANE_H }, s); });
    l.waits.forEach(function (w) {
      total += w.b - w.a;
      var a = Math.max(0, x(m, w.a)), b = Math.min(m.laneW, x(m, w.b));
      if (b > a) svg("rect", { class: "seg s-waiting", x: a + 1, y: 4, width: Math.max(0.5, b - a - 2), height: 9 }, s);
      if (mine) r.c.mine.forEach(function (sp) {
        var lo = Math.max(sp.a, w.a), hi = Math.min(sp.b, w.b);
        if (hi > lo) mineRect(s, m, { a: lo, b: hi }, 4, 9);
      });
    });
    r.c.reviews.forEach(function (rv) { if (rv.who === l.who) { var tx = x(m, rv.t); if (tx >= 0 && tx <= m.laneW) { var t = svg("text", { class: "tick" + (rv.ok ? " ok" : " no"), x: tx + 2, y: 13 }, s); t.textContent = rv.ok ? "✓" : "✕"; } } });
    nowLine(s, m, LANE_H);
    var txt = "waited " + TL.dur(total) + (l.waits.some(function (w) { return w.open; }) ? ", still waiting" : "");
    el("span", "tl-state", txt, sub);
    el("span", "sr", l.who + (mine ? " (you)" : "") + ": " + txt, sub);
  }

  // laneAt is the wait under x px on a reviewer's lane, as a segment.
  function laneAt(r, who, px) {
    var m = V.model, l = r.c.lanes.filter(function (q) { return q.who === who; })[0];
    if (!l) return null;
    var t = m.dom.a + px / m.laneW * (m.dom.b - m.dom.a), w = l.waits.filter(function (q) { return t >= q.a - HOUR && t <= q.b + HOUR; })[0];
    return w ? { s: { state: "waiting", a: w.a, b: w.b, reviewers: [who], last: w.open } } : null;
  }
  function showTip(r, q, cx, cy) {
    if (!q) { hideTip(); return; }
    var m = V.model, lines = TL.tip(q.s, r.c.mine, m.op, m.env), el = cur.dom.el;
    V.tip.textContent = "";
    el("b", null, (r.n.label || r.id) + ": " + lines[0], V.tip);
    lines.slice(1).forEach(function (t) { el("div", "dim", t, V.tip); });
    V.tip.hidden = false;
    var hb = V.host.getBoundingClientRect(), tw = V.tip.offsetWidth || 260;
    V.tip.style.left = Math.min(Math.max(4, cx - hb.left + 12), hb.width - tw - 4) + "px";
    V.tip.style.top = (cy - hb.top + 14) + "px";
    return lines;
  }
  function hideTip() { if (V && V.tip) V.tip.hidden = true; }

  function focusRow(p) {
    var m = V.model;
    if (p < 0 || !m.rows.length) return;
    if (m.virtual) {
      var r = m.rows[p], s = V.scroll;
      if (r.top < s.scrollTop) s.scrollTop = r.top;
      else if (r.top + r.h > s.scrollTop + s.clientHeight) s.scrollTop = r.top + r.h - s.clientHeight;
      var w = windowFor();
      if (w[0] !== V.win[0] || w[1] !== V.win[1]) paintRows();
    }
    setFocus(p); rove();
    var row = V.list.querySelector(".tl-row[data-p=\"" + p + "\"]");
    if (row) { row.focus({ preventScroll: m.virtual }); if (!m.virtual && row.scrollIntoView) row.scrollIntoView({ block: "nearest" }); }
  }
  // onKey: Up and Down move between rows, Left and Right between the focused
  // row's segments (their details shown and announced), Enter or Space
  // selects, Escape hides the details.
  function onKey(ev) {
    var row = ev.target.closest(".tl-row[data-p]"); if (!row || ev.target !== row) return;
    var p = +row.dataset.p, r = V.model.rows[p];
    if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setFocus(p); cur.select([rowId(p)], ev.shiftKey); return; }
    if (ev.key === "Escape") { hideTip(); V.seg = -1; return; }
    if (ev.key === "ArrowLeft" || ev.key === "ArrowRight") {
      var laid = (r.laid || []).filter(function (q) { return !q.cap; });
      if (!laid.length) { V.live.textContent = r.c.summary; return; }
      ev.preventDefault();
      V.seg = ev.key === "ArrowRight" ? Math.min(laid.length - 1, V.seg + 1) : Math.max(0, V.seg < 0 ? laid.length - 1 : V.seg - 1);
      var q = laid[V.seg], lane = row.querySelector("svg.tl-lane"), b = lane.getBoundingClientRect();
      var lines = showTip(r, q, b.left + q.x + Math.min(q.w, 40) / 2, b.top + BAR_Y);
      if (lines) V.live.textContent = lines.join(". ");
      return;
    }
    var nx = L.nextIndex(V.model.rows.length, p, ev.key);
    if (nx < 0) return;
    ev.preventDefault();
    V.seg = -1; hideTip();
    focusRow(nx);
  }

  // extract is what Copy as Markdown asks for: the timeline's PR rows, with
  // the table's PR columns, in its sort.
  function extract(ctx) {
    var reg = ctx.registry, q = L.parseQuery(ctx.getState("q") || "", reg), expr = ctx.getState("q") || "";
    if (!q.error && !q.clauses.some(function (c) { return c.name === "kind"; })) expr = expr ? expr + ";kind:pr" : "kind:pr";
    var out = { q: expr };
    if (T) {
      out.cols = T.defaultColumns(reg, ["pr"]);
      out.sort = T.formatSort(T.parseSort(ctx.getState("sort")).filter(function (s) { return !!reg.field(s.key) && !T.LOCAL[s.key]; }));
    }
    return out;
  }

  window.addEventListener("resize", function () {
    if (!V || !cur || V.host.hidden || V.raf) return;
    V.raf = requestAnimationFrame(function () { V.raf = 0; if (cur && !V.host.hidden) render(Object.assign({}, cur, { reason: "view" })); });
  });

  window.WGOLive.registerView({ id: "timeline", label: "Timeline", question: "Where has each PR's time gone — in draft, waiting on whom, or with changes requested?",
    render: render, extract: extract,
    destroy: function () { if (cur) hover(null); hideTip(); } });
})();
