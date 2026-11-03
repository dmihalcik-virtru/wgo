/* wgo dash scatterplot (spec/gh-103.md): "Which PRs are small and have waited
 * long — push them — and which are big — split them?" An addon view,
 * registered as "scatter" through window.WGOLive, after Ahlberg and
 * Shneiderman's FilmFinder starfield: every PR row of the current query is a
 * mark in a two-measure field, filtered live by the query, with details on
 * demand.
 *
 * x defaults to waiting and y to size; either can be any registered number
 * or duration field, kept in the URL as x= and y=. Log axes (a symmetric log,
 * so zero has a place) for fields whose registry scale is log and for
 * durations, labelled "log scale", with light gridlines at 1, 10, 100, 1,000
 * lines or 1, 7, 30, 90 days. Shape says whose move it is; the accent (with a
 * dot in the mark, for grey-scale readers) only that it is the operator's; a
 * small × that CI fails on the PR's own checks; everything else is neutral.
 * A mark whose value on an axis is unknown, or does not apply (a PR waiting on
 * no one), is never drawn at zero: it sits in a labelled strip along that
 * axis's low edge. Direct labels for the selection and at most five outliers
 * (largest rank sum on the two axes); a tooltip for every other mark.
 * Optional median lines (median=on). Drag a rectangle to select (Shift adds),
 * wheel or pinch to zoom, click to select; the selection is the page's.
 *
 * Marks are reachable by keyboard in the table's sort order (sort=, the
 * table's own comparator), each with an aria-label. Under 1,000 marks they
 * are SVG elements; from 1,000 they are drawn on a canvas, hit-tested with a
 * quadtree (window.d3force.quadtree, as the graph), and the keyboard moves
 * one focusable proxy from mark to mark.
 *
 * The pure logic is logic(WGOLive, tableLogic), also module.exports under
 * node, so internal/review/testdata/live_logic.js tests it without a page.
 * Like live.js it writes no markup from strings and issues no requests. */
(function () {
  "use strict";

  // ---------- pure logic ----------
  function logic(L, T) {
    var S = {};
    var hasOwn = Object.prototype.hasOwnProperty;
    function cmp(a, b) { return a < b ? -1 : a > b ? 1 : 0; }
    var NATURAL = new Intl.Collator("en", { numeric: true });

    S.DEFAULT_X = "waiting";
    S.DEFAULT_Y = "size";
    // CANVAS_MIN marks or more are drawn on a canvas (requirement 9).
    S.CANVAS_MIN = 1000;
    S.MAX_OUTLIERS = 5;

    // ---------- axes (requirement 2) ----------
    // fwd and inv are the symmetric log: log10(1 + |v|), signed, so 0 maps
    // to 0 and 9, 99 and 999 to 1, 2 and 3.
    S.fwd = function (v) { return v < 0 ? -Math.log10(1 - v) : Math.log10(1 + v); };
    S.inv = function (t) { return t < 0 ? 1 - Math.pow(10, -t) : Math.pow(10, t) - 1; };
    function same(v) { return v; }
    // axisFields lists the fields an axis can show: registered number and
    // duration fields that apply to PRs.
    S.axisFields = function (reg) {
      return reg.fields.filter(function (f) { return (f.type === "number" || f.type === "duration") && (f.kinds || []).indexOf("pr") >= 0; });
    };
    // axisField reads x= or y= (raw): that field when it can be an axis,
    // else the default, else the first field that can (before gh-99 has
    // registered waiting and size); null when none can.
    S.axisField = function (reg, raw, def) {
      var ok = S.axisFields(reg), pick = function (n) { return ok.filter(function (f) { return f.name === n; })[0] || null; };
      return (raw && pick(raw)) || pick(def) || ok[0] || null;
    };
    // isLog: log for the registry's log scale and for every duration.
    S.isLog = function (f) { return !!f && (f.scale === "log" || f.type === "duration"); };
    S.scale = function (f) { var log = S.isLog(f); return { log: log, fwd: log ? S.fwd : same, inv: log ? S.inv : same }; };
    // axisTitle is the label with its unit, unless the label names it,
    // and "log scale": "Waiting (days, log scale)", "Size (lines changed,
    // log scale)".
    S.axisTitle = function (f) {
      var unit = f.type === "duration" ? "days" : f.unit || "", label = f.label;
      if (unit && label.toLowerCase().indexOf(unit.toLowerCase()) >= 0) unit = "";
      var bits = [unit, S.isLog(f) ? "log scale" : ""].filter(Boolean);
      if (!bits.length) return label;
      return /\)$/.test(label) ? label.slice(0, -1) + ", " + bits.join(", ") + ")" : label + " (" + bits.join(", ") + ")";
    };
    // shortLabel drops a label's parenthesis: "Size (lines changed)" says
    // "Size" before a value that names its unit.
    S.shortLabel = function (f) { return f.label.replace(/\s*\(.*\)$/, ""); };
    // Gridlines at readable steps: lines (and other counts) by powers of
    // ten, days by day, week, month and quarter.
    // Zoomed in between two of them, the denser steps take over.
    var LOG_STEPS = [0, 1, 10, 100, 1000, 10000, 100000, 1000000, 10000000];
    var DAY_STEPS = [0, 1, 7, 30, 90, 365, 730, 1825, 3650, 36500];
    var LOG_DENSE = [0, 1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000, 50000, 100000, 1000000, 10000000];
    var DAY_DENSE = [0, 1, 2, 3, 5, 7, 14, 21, 30, 60, 90, 180, 365, 730, 1825, 3650, 36500];
    function signed(base) { return base.slice(1).map(function (v) { return -v; }).reverse().concat(base); }
    // steps lists the gridline values for f covering [lo, hi] (raw values);
    // dense asks for the denser log steps.
    S.steps = function (f, lo, hi, dense) {
      if (S.isLog(f)) return signed(f.type === "duration" ? (dense ? DAY_DENSE : DAY_STEPS) : (dense ? LOG_DENSE : LOG_STEPS));
      var span = Math.max(hi - lo, 1e-9), mag = Math.pow(10, Math.floor(Math.log10(span / 5))), step = mag;
      [1, 2, 5, 10].some(function (m) { step = m * mag; return span / step <= 6; });
      var out = [];
      for (var v = Math.floor(lo / step) * step; v <= hi + step * 1.0001 && out.length < 40; v += step) out.push(Math.round(v / step) * step);
      if (out[0] > lo) out.unshift(out[0] - step);
      return out;
    };
    // niceDomain is the axis range for values (raw), in transformed units,
    // from the gridline at or below the least to the one at or above the
    // greatest, so the axis ends on labelled lines. [0, 1] with no values.
    S.niceDomain = function (f, vals) {
      var sc = S.scale(f);
      if (!vals.length) return [sc.fwd(0), sc.fwd(f.type === "duration" ? 7 : 10)];
      var lo = Math.min.apply(null, vals), hi = Math.max.apply(null, vals), st = S.steps(f, lo, hi), a = null, b = null;
      var snap = function () {
        a = null; b = null;
        st.forEach(function (v) { if (v <= lo) a = v; if (b == null && v >= hi) b = v; });
        if (a == null) a = lo;
        if (b == null) b = hi;
      };
      snap();
      // A linear axis's step follows its span: snap again with the step the
      // widened range takes, so its ends are lines too.
      if (!sc.log && a !== b) { var st2 = S.steps(f, a, b); if (st2[1] - st2[0] !== st[1] - st[0]) { st = st2; snap(); } }
      if (a === b) {
        var i = st.indexOf(a);
        if (i >= 0 && i + 1 < st.length && a >= 0) b = st[i + 1]; else if (i > 0) a = st[i - 1]; else b = a + 1;
      }
      return [sc.fwd(a), sc.fwd(b)];
    };
    // ticks lists the gridlines inside a transformed domain [t0, t1] as
    // {v (raw), t (transformed), label}.
    // When fewer than three lines show (a narrow range, or zoomed in), it
    // uses denser log steps, then linear ones.
    S.ticks = function (f, dom) {
      var sc = S.scale(f), lo = sc.inv(dom[0]), hi = sc.inv(dom[1]), eps = (dom[1] - dom[0]) * 1e-6;
      var within = function (vals) { return vals.filter(function (v) { var t = sc.fwd(v); return t >= dom[0] - eps && t <= dom[1] + eps; }); };
      var vals = within(S.steps(f, lo, hi));
      if (vals.length < 3 && sc.log) vals = within(S.steps(f, lo, hi, true));
      if (vals.length < 2 && sc.log) vals = within(S.steps({ type: "number" }, lo, hi));
      return vals.map(function (v) { return { v: v, t: sc.fwd(v), label: S.tickText(f, v) }; });
    };
    S.tickText = function (f, v) {
      if (f.type === "duration") return v === 0 ? "0" : v + "d";
      return Math.abs(v) >= 1000 ? v.toLocaleString("en") : String(Math.round(v * 100) / 100);
    };
    // zoomDomain scales dom by factor about at (all transformed units).
    S.zoomDomain = function (dom, at, factor) { return [at + (dom[0] - at) * factor, at + (dom[1] - at) * factor]; };

    // ---------- values and unknowns (requirement 5) ----------
    // value reads field f on node i of the query index: {k: "v", v, lower}
    // for a number, {k: "unknown"}, or {k: "na", tag} when it does not
    // apply (gh-101: a PR whose move is its author's, or that is closed,
    // waits on no reviewer: "not waiting").
    S.value = function (ix, f, i) {
      var vals = ix.values.get(f.name), v = vals && vals[i], facts = ix.nodes[i] && ix.nodes[i].pr && ix.nodes[i].pr.facts;
      if (!v || v.na) return { k: "na", tag: "n/a" };
      if (v.unknown) {
        if (f.name === "waiting" && T && T.waitingNA(facts)) return { k: "na", tag: "not waiting" };
        return { k: "unknown" };
      }
      if (!v.nums.length) return { k: "unknown" };
      return { k: "v", v: v.nums[0], lower: f.name === "waiting" && !!facts && facts.waiting_lower_bound === true };
    };
    // valueText is a value as the tooltip and aria-label say it.
    S.valueText = function (f, val) {
      if (val.k === "unknown") return "unknown";
      if (val.k === "na") return val.tag === "n/a" ? "n/a" : "n/a (" + val.tag + ")";
      if (f.type === "duration") { var d = Math.floor(val.v); return (val.lower ? "at least " : "") + d + (d === 1 ? " day" : " days"); }
      var n = Math.round(val.v * 100) / 100;
      return n.toLocaleString("en") + (f.unit ? " " + (n === 1 && /s$/.test(f.unit) ? f.unit.slice(0, -1) : f.unit) : "");
    };
    S.shortText = function (f, val) {
      if (val.k !== "v") return val.k === "unknown" ? "unknown" : "n/a";
      return f.type === "duration" ? (val.lower ? "≥" : "") + Math.floor(val.v) + "d" : (Math.round(val.v * 100) / 100).toLocaleString("en");
    };
    // ZONES: where a mark sits. plot: both values known; xstrip: the strip
    // along the x axis's low edge (left), for an unknown x, placed by y;
    // ystrip: along the y axis's low edge (bottom), placed by x; corner:
    // neither known.
    S.zone = function (xv, yv) { return xv.k === "v" ? (yv.k === "v" ? "plot" : "ystrip") : yv.k === "v" ? "xstrip" : "corner"; };
    // stripText names what a strip holds: "unknown: 2 · not waiting: 12".
    S.stripText = function (vals) {
      var by = {}, order = [];
      vals.forEach(function (v) {
        var k = v.k === "na" ? v.tag : "unknown";
        if (!hasOwn.call(by, k)) { by[k] = 0; order.push(k); }
        by[k]++;
      });
      order.sort(function (a, b) { return a === "unknown" ? -1 : b === "unknown" ? 1 : cmp(a, b); });
      return order.map(function (k) { return k + ": " + by[k]; }).join(" · ");
    };

    // ---------- encoding (requirement 3) ----------
    S.SHAPES = { reviewer: "circle", author: "triangle", nobody: "square", unknown: "ring", none: "diamond" };
    // KEY is the one compact key, in the order it reads.
    S.KEY = [
      { shape: "circle", text: "reviewer's move" }, { shape: "triangle", text: "author's" }, { shape: "square", text: "nobody's" },
      { shape: "ring", text: "unknown" }, { shape: "diamond", text: "none (merged or closed)" }
    ];
    S.shapeOf = function (move) { return hasOwn.call(S.SHAPES, move) ? S.SHAPES[move] : "ring"; };
    // shapePath is a shape's SVG path data centred on 0,0 for radius r; the
    // canvas draws the same data through Path2D.
    S.shapePath = function (shape, r) {
      var f = function (v) { return Math.round(v * 100) / 100; };
      switch (shape) {
        case "triangle": { var t = r * 1.3; return "M0 " + f(-t) + "L" + f(t * 0.95) + " " + f(t * 0.72) + "L" + f(-t * 0.95) + " " + f(t * 0.72) + "Z"; }
        case "square": { var s = r * 0.88; return "M" + f(-s) + " " + f(-s) + "H" + f(s) + "V" + f(s) + "H" + f(-s) + "Z"; }
        case "diamond": { var d = r * 1.15; return "M0 " + f(-d) + "L" + f(d) + " 0L0 " + f(d) + "L" + f(-d) + " 0Z"; }
      }
      return "M" + f(r) + " 0A" + f(r) + " " + f(r) + " 0 1 1 " + f(-r) + " 0A" + f(r) + " " + f(r) + " 0 1 1 " + f(r) + " 0Z";
    };
    // CROSS is the × badge, up and to the right of the mark.
    S.CROSS = "M3.5 -8.5L8.5 -3.5M8.5 -8.5L3.5 -3.5";
    // markInfo is a PR's encoding: shape by move, mine (the accent) when
    // move_people holds @me, failing (the ×) when CI fails on its own checks.
    S.markInfo = function (pr, me) {
      var facts = pr && pr.facts, move = facts && typeof facts.move === "string" ? facts.move : "unknown";
      var w = T ? T.moveWord(facts, me) : { mine: false };
      return { move: move, shape: S.shapeOf(move), mine: !!w.mine, failing: !!facts && facts.ci === "failing" };
    };
    var MOVE_TEXT = { reviewer: "reviewer's move", author: "author's move", nobody: "nobody's move", none: "no move (merged or closed)" };
    S.moveText = function (info) { return (MOVE_TEXT[info.move] || "move unknown") + (info.mine ? " (yours)" : ""); };
    // ariaLabel says a mark in one sentence: ref, both values, move, CI.
    S.ariaLabel = function (ref, fx, xv, fy, yv, info) {
      return ref + ": " + S.shortLabel(fx) + " " + S.valueText(fx, xv) + ", " + S.shortLabel(fy) + " " + S.valueText(fy, yv) + ", " + S.moveText(info) + (info.failing ? ", CI failing" : "");
    };

    // marks lays out the PR rows (node indices) for the two axes.
    S.marks = function (ix, rows, fx, fy) {
      var me = ix.env.me, out = [];
      rows.forEach(function (i) {
        if (ix.kinds[i] !== "pr") return;
        var n = ix.nodes[i], xv = S.value(ix, fx, i), yv = S.value(ix, fy, i), info = S.markInfo(n.pr, me);
        out.push({ i: i, id: ix.ids[i], ref: n.label || ix.ids[i], xv: xv, yv: yv, zone: S.zone(xv, yv), info: info, label: S.ariaLabel(n.label || ix.ids[i], fx, xv, fy, yv, info) });
      });
      return out;
    };
    // partition groups marks by zone.
    S.partition = function (marks) {
      var p = { plot: [], xstrip: [], ystrip: [], corner: [] };
      marks.forEach(function (m) { p[m.zone].push(m); });
      return p;
    };

    // ---------- outliers (requirement 4) ----------
    // ranks gives each value its rank among vals, 1 for the least; ties
    // share their average rank.
    S.ranks = function (vals) {
      var ord = vals.map(function (v, i) { return i; }).sort(function (a, b) { return vals[a] - vals[b]; }), r = new Array(vals.length);
      for (var s = 0; s < ord.length;) {
        var e = s;
        while (e + 1 < ord.length && vals[ord[e + 1]] === vals[ord[s]]) e++;
        for (var k = s; k <= e; k++) r[ord[k]] = (s + e) / 2 + 1;
        s = e + 1;
      }
      return r;
    };
    // outliers picks at most max plotted marks by the largest sum of their
    // ranks on the two axes (ties: natural order of the ref), largest first,
    // one per point: a mark on the same values as one already picked would
    // only stack a second label there.
    S.outliers = function (plotted, max) {
      if (max == null) max = S.MAX_OUTLIERS;
      var rx = S.ranks(plotted.map(function (m) { return m.xv.v; })), ry = S.ranks(plotted.map(function (m) { return m.yv.v; })), at = {}, out = [];
      plotted.map(function (m, k) { return { m: m, s: rx[k] + ry[k] }; })
        .sort(function (a, b) { return b.s - a.s || NATURAL.compare(a.m.ref, b.m.ref) || cmp(a.m.id, b.m.id); })
        .forEach(function (x) {
          var key = x.m.xv.v + "," + x.m.yv.v;
          if (out.length >= max || hasOwn.call(at, key)) return;
          at[key] = true; out.push(x.m);
        });
      return out;
    };
    S.median = function (vals) {
      if (!vals.length) return null;
      var s = vals.slice().sort(function (a, b) { return a - b; }), h = s.length >> 1;
      return s.length % 2 ? s[h] : (s[h - 1] + s[h]) / 2;
    };

    // ---------- keyboard order (requirement 8) ----------
    // order sorts marks as the table sorts its rows: sort= through the
    // table's own keys and comparator; by ref without the table's logic.
    S.order = function (ix, reg, marks, rawSort) {
      if (!T) return marks.slice().sort(function (a, b) { return NATURAL.compare(a.ref, b.ref) || cmp(a.id, b.id); });
      var at = new Map(marks.map(function (m) { return [m.i, m]; }));
      return T.sortRows(ix, marks.map(function (m) { return m.i; }), T.sortKeys(reg, rawSort)).map(function (i) { return at.get(i); });
    };

    // ---------- labels ----------
    // placeLabels places labels greedily, in the given order: to the right
    // of the mark, else left, above or below (slid along the edge to stay
    // in box), skipping one that would overlap a placed label or leave box.
    // cands: [{key, x, y, w, h, r}].
    S.placeLabels = function (cands, box) {
      var placed = [], out = [];
      var cy = function (y, h) { return Math.max(box.y0, Math.min(box.y1 - h, y)); }, cx = function (x, w) { return Math.max(box.x0, Math.min(box.x1 - w, x)); };
      cands.forEach(function (c) {
        var pad = c.r + 4, opts = [[c.x + pad, cy(c.y - c.h / 2, c.h)], [c.x - pad - c.w, cy(c.y - c.h / 2, c.h)], [cx(c.x - c.w / 2, c.w), c.y - pad - c.h], [cx(c.x - c.w / 2, c.w), c.y + pad]];
        for (var k = 0; k < opts.length; k++) {
          var b = { x: opts[k][0], y: opts[k][1], w: c.w, h: c.h };
          if (b.x < box.x0 || b.y < box.y0 || b.x + b.w > box.x1 || b.y + b.h > box.y1) continue;
          if (placed.some(function (q) { return b.x < q.x + q.w + 2 && b.x + b.w + 2 > q.x && b.y < q.y + q.h && b.y + b.h > q.y; })) continue;
          placed.push(b); out.push({ key: c.key, x: b.x, y: b.y, w: b.w, h: b.h });
          return;
        }
      });
      return out;
    };
    // inRect lists the marks whose position (px, py) lies in the rectangle.
    S.inRect = function (marks, r) {
      var x0 = Math.min(r.x0, r.x1), x1 = Math.max(r.x0, r.x1), y0 = Math.min(r.y0, r.y1), y1 = Math.max(r.y0, r.y1);
      return marks.filter(function (m) { return m.vis && m.px >= x0 && m.px <= x1 && m.py >= y0 && m.py <= y1; });
    };
    // jitter spreads marks across a strip's width, the same way each time.
    S.jitter = function (id) {
      var h = 2166136261;
      for (var k = 0; k < id.length; k++) { h ^= id.charCodeAt(k); h = Math.imul(h, 16777619); }
      return ((h >>> 0) % 1000) / 999;
    };
    return S;
  }

  if (typeof module !== "undefined" && module.exports) module.exports = logic;
  if (typeof window === "undefined" || !window.WGOLive || typeof document === "undefined") return;

  // ---------- the view ----------
  var L = window.WGOLive, S = null;
  // R is a mark's radius (10 px across) and DOT the radius of the dot in a
  // "your move" mark; HIT how near the pointer must come to a mark;
  // STRIP the unknown strips' width, its label in the outer 12 px and its
  // marks in the inner part; PAD keeps marks off the axis lines.
  var R = 5, DOT = 2, HIT = 12, STRIP = 30, GAP = 8, PAD = 8, M = { l: 62, r: 18, t: 14, b: 44 };
  var cur = null; // the latest ctx: handlers read the live store through it
  var V = null; // the view's DOM and model, built once per container

  // WGOLive.scatterOptions.canvasMin, when a number, moves the canvas
  // threshold, so a test can draw the same marks both ways.
  function opts() { var o = L.scatterOptions || {}; return { canvasMin: typeof o.canvasMin === "number" ? o.canvasMin : S.CANVAS_MIN }; }
  function svgEl(tag, attrs, parent) { return cur.dom.svgEl(tag, attrs || {}, parent); }

  function build(ctx) {
    var dom = ctx.dom, el = dom.el, host = ctx.container;
    host.textContent = "";
    host.classList.add("scatter-host");
    V = { host: host, focusId: null, zoom: null, axisKey: "", model: null, raf: 0, hoverId: null, drag: null, pointers: new Map(), pinch: null };
    V.bar = el("div", "sc-bar", null, host);
    var xl = el("label", null, "x ", V.bar); V.xSel = el("select", null, null, xl); V.xSel.setAttribute("aria-label", "x axis field");
    var yl = el("label", null, "y ", V.bar); V.ySel = el("select", null, null, yl); V.ySel.setAttribute("aria-label", "y axis field");
    var ml = el("label", null, null, V.bar); V.medBox = el("input", null, null, ml); V.medBox.type = "checkbox"; ml.appendChild(document.createTextNode(" Median lines"));
    V.resetBtn = dom.button(V.bar, "Reset zoom", null, function () { V.zoom = null; layout(); paintAll(); });
    V.resetBtn.title = "Back to every mark (or press 0 in the chart)";
    V.summary = el("span", "sc-sum", null, V.bar);
    V.key = el("div", "sc-key", null, host); V.key.setAttribute("aria-label", "Key");
    V.chart = el("div", "sc-chart", null, host);
    V.base = svgEl("svg", { "class": "sc-base", "aria-hidden": "true" }, V.chart);
    V.canvas = el("canvas", "sc-canvas", null, V.chart); V.canvas.hidden = true; V.canvas.setAttribute("aria-hidden", "true");
    V.over = svgEl("svg", { "class": "sc-over", role: "listbox", "aria-multiselectable": "true" }, V.chart);
    V.tip = el("div", "tip sc-tip", null, V.chart); V.tip.hidden = true;
    V.live = el("div", "sr", null, V.chart); V.live.setAttribute("role", "status"); V.live.setAttribute("aria-live", "polite");
    // status says what a drag selected. It is for screen readers only: text
    // in the bar could wrap it and resize the chart under the pointer.
    V.status = el("div", "sr", null, V.chart); V.status.setAttribute("role", "status");
    V.empty = el("p", "empty sc-empty", null, V.chart); V.empty.hidden = true;
    V.measure = document.createElement("canvas").getContext("2d");

    V.xSel.addEventListener("change", function () { setAxis("x", V.xSel.value, S.DEFAULT_X); });
    V.ySel.addEventListener("change", function () { setAxis("y", V.ySel.value, S.DEFAULT_Y); });
    V.medBox.addEventListener("change", function () { cur.setState({ median: V.medBox.checked ? "on" : "" }, { commit: true }); });
    V.over.addEventListener("pointerdown", onDown);
    V.over.addEventListener("pointermove", onMove);
    V.over.addEventListener("pointerup", onUp);
    V.over.addEventListener("pointercancel", function (ev) { if (!V) return; V.pointers.delete(ev.pointerId); V.pinch = null; endDrag(); });
    V.over.addEventListener("pointerleave", function () { if (V && !V.drag) { hideTip(); setHover(null); } });
    V.over.addEventListener("wheel", onWheel, { passive: false });
    V.over.addEventListener("keydown", onKey);
    V.over.addEventListener("focusin", function (ev) { var g = V && ev.target.closest && ev.target.closest(".sc-mk"); if (g) onFocusMark(g); });
    V.over.addEventListener("focusout", function () { if (V && !V.hoverId) hideTip(); });
    if (window.ResizeObserver) {
      V.ro = new ResizeObserver(function () {
        if (!V || V.raf) return;
        V.raf = requestAnimationFrame(function () { if (!V) return; V.raf = 0; if (V.model) { layout(); paintAll(); } });
      });
      V.ro.observe(V.chart);
    }
  }
  function setAxis(which, name, def) {
    var o = {}; o[which] = name === def ? "" : name;
    cur.setState(o, { commit: true });
  }

  function render(ctx) {
    cur = ctx;
    if (!S) S = logic(L, L.tableLogic || null);
    if (!V || V.host !== ctx.container) build(ctx);
    if (!V.subscribed) { V.subscribed = true; ctx.subscribe(function (reason) { if (reason === "hover" && V && V.model) paintHover(); }); }
    if (!ctx.queryIndex) return;
    if (ctx.reason === "selection" && V.model && V.model.ix === ctx.queryIndex) { paintSelection(); return; }
    var hadFocus = V.over.contains(document.activeElement);
    V.model = model(ctx);
    if (V.model.axisKey !== V.axisKey) { V.zoom = null; V.axisKey = V.model.axisKey; }
    paintBar();
    layout();
    paintAll();
    if (hadFocus) focusMark(V.focusId, true);
  }

  // model works out the marks, their order and the axes for ctx.
  function model(ctx) {
    var ix = ctx.queryIndex, reg = ctx.registry, res = ctx.query, rows;
    if (res) rows = res.rowIdx; else { rows = []; for (var i = 0; i < ix.nodes.length; i++) rows.push(i); }
    var fx = S.axisField(reg, ctx.getState("x"), S.DEFAULT_X), fy = S.axisField(reg, ctx.getState("y"), S.DEFAULT_Y);
    var m = { ix: ix, reg: reg, fx: fx, fy: fy, marks: [], order: [], axisKey: (fx && fx.name) + "|" + (fy && fy.name), median: ctx.getState("median") === "on" };
    if (!fx || !fy) return m;
    m.sx = S.scale(fx); m.sy = S.scale(fy);
    m.marks = S.marks(ix, rows, fx, fy);
    m.zones = S.partition(m.marks);
    m.order = S.order(ix, reg, m.marks, ctx.getState("sort"));
    m.order.forEach(function (mk, k) { mk.k = k; });
    m.byId = new Map(m.marks.map(function (mk) { return [mk.id, mk]; }));
    var xs = m.marks.filter(function (mk) { return mk.xv.k === "v"; }).map(function (mk) { return mk.xv.v; });
    var ys = m.marks.filter(function (mk) { return mk.yv.k === "v"; }).map(function (mk) { return mk.yv.v; });
    m.dom = { x: S.niceDomain(fx, xs), y: S.niceDomain(fy, ys) };
    m.med = { x: S.median(xs), y: S.median(ys) };
    m.outliers = new Set(S.outliers(m.zones.plot).map(function (mk) { return mk.id; }));
    m.canvas = m.marks.length >= opts().canvasMin;
    return m;
  }

  // ---------- bar and key ----------
  function paintBar() {
    var m = V.model, el = cur.dom.el;
    [[V.xSel, m.fx], [V.ySel, m.fy]].forEach(function (p) {
      var sel = p[0]; sel.textContent = "";
      S.axisFields(m.reg).forEach(function (f) { var o = el("option", null, f.label + (S.isLog(f) ? " (log)" : ""), sel); o.value = f.name; });
      if (p[1]) sel.value = p[1].name;
    });
    V.medBox.checked = m.median;
    var n = m.marks.length, z = m.zones || { plot: [] }, strips = n - z.plot.length;
    V.summary.textContent = n ? cur.dom.plural(n, "PR") + " · " + z.plot.length.toLocaleString() + " plotted" + (strips ? " · " + strips.toLocaleString() + " in the unknown strips" : "") + (m.canvas ? " · drawn on a canvas" : "") : "";
    if (V.keyBuilt) return;
    V.keyBuilt = true;
    S.KEY.forEach(function (k) { keyItem(k.shape, k.text, ""); });
    keyItem("circle", "your move", "mine");
    keyItem("cross", "CI failing (its own checks)", "");
  }
  function keyItem(shape, text, cls) {
    var span = cur.dom.el("span", "sc-ki", null, V.key), s = svgEl("svg", { width: 16, height: 16, viewBox: "-8 -8 16 16", "aria-hidden": "true" }, span);
    if (shape === "cross") svgEl("path", { "class": "x", d: "M-3 -3L3 3M3 -3L-3 3" }, s);
    else {
      var g = svgEl("g", { "class": "sc-mk s-" + shape + (cls ? " " + cls : "") }, s);
      svgEl("path", { "class": "b", d: S.shapePath(shape, R) }, g);
      if (cls === "mine") svgEl("circle", { "class": "dot", r: DOT }, g);
    }
    span.appendChild(document.createTextNode(text));
  }

  // ---------- layout ----------
  // layout sizes the chart and places every mark (px, py, vis).
  function layout() {
    var m = V.model, W = Math.max(320, V.chart.clientWidth || 600), H = Math.max(240, V.chart.clientHeight || 400);
    var g = { W: W, H: H };
    g.px0 = M.l + STRIP + GAP; g.px1 = W - M.r; g.py0 = M.t; g.py1 = H - M.b - STRIP - GAP;
    g.sx0 = M.l; g.sx1 = M.l + STRIP; g.sy0 = g.py1 + GAP; g.sy1 = g.sy0 + STRIP;
    V.geo = g;
    if (!m.fx || !m.fy) return;
    var d = V.zoom || m.dom;
    g.dx = d.x; g.dy = d.y;
    var X = function (t) { return g.px0 + PAD + (t - d.x[0]) / ((d.x[1] - d.x[0]) || 1) * (g.px1 - g.px0 - 2 * PAD); };
    var Y = function (t) { return g.py1 - PAD - (t - d.y[0]) / ((d.y[1] - d.y[0]) || 1) * (g.py1 - g.py0 - 2 * PAD); };
    g.X = X; g.Y = Y;
    var inX = function (x) { return x >= g.px0 - 0.5 && x <= g.px1 + 0.5; }, inY = function (y) { return y >= g.py0 - 0.5 && y <= g.py1 + 0.5; };
    m.marks.forEach(function (mk) {
      var j = S.jitter(mk.id);
      var x = mk.xv.k === "v" ? X(m.sx.fwd(mk.xv.v)) : g.sx0 + 16 + j * (STRIP - 21);
      var y = mk.yv.k === "v" ? Y(m.sy.fwd(mk.yv.v)) : g.sy0 + 16 + S.jitter(mk.id + "y") * (STRIP - 21);
      mk.px = x; mk.py = y;
      mk.vis = (mk.xv.k !== "v" || inX(x)) && (mk.yv.k !== "v" || inY(y));
    });
    var vis = m.marks.filter(function (mk) { return mk.vis; });
    V.quad = window.d3force && window.d3force.quadtree ? window.d3force.quadtree(vis, function (mk) { return mk.px; }, function (mk) { return mk.py; }) : null;
    V.visible = vis;
  }
  // nearest is the visible mark nearest (x, y) within HIT pixels, or null.
  function nearest(x, y) {
    if (V.quad) return V.quad.find(x, y, HIT) || null;
    var best = null, bd = HIT;
    (V.visible || []).forEach(function (mk) { var dd = Math.hypot(mk.px - x, mk.py - y); if (dd <= bd) { bd = dd; best = mk; } });
    return best;
  }

  // ---------- painting ----------
  function paintAll() {
    var m = V.model, g = V.geo;
    V.base.setAttribute("width", g.W); V.base.setAttribute("height", g.H); V.base.setAttribute("viewBox", "0 0 " + g.W + " " + g.H);
    V.over.setAttribute("width", g.W); V.over.setAttribute("height", g.H); V.over.setAttribute("viewBox", "0 0 " + g.W + " " + g.H);
    V.base.textContent = ""; V.over.textContent = "";
    V.resetBtn.disabled = !V.zoom;
    V.empty.hidden = true;
    if (!m.fx || !m.fy) { showEmpty("No number or duration fields are registered for PRs, so there is nothing to plot."); return; }
    if (!m.marks.length) { showEmpty(cur.query ? "No PRs in the query: the scatterplot plots PR rows (try kind:pr)." : "No PRs in this snapshot."); }
    V.over.setAttribute("aria-label", "PRs plotted by " + m.fx.label + " and " + m.fy.label + ". Arrow keys move through them in the table's order; Enter selects, Shift+Enter adds; + and − zoom, 0 resets.");
    paintAxes();
    if (m.median) paintMedians();
    V.marksG = svgEl("g", { "class": "sc-marks" }, V.over);
    V.ringsG = svgEl("g", { "class": "sc-rings", "aria-hidden": "true" }, V.over);
    V.labelsG = svgEl("g", { "class": "sc-labels", "aria-hidden": "true" }, V.over);
    V.brush = svgEl("rect", { "class": "sc-brush", visibility: "hidden" }, V.over);
    V.brushText = svgEl("text", { "class": "sc-brushl", visibility: "hidden" }, V.over);
    V.canvas.hidden = !m.canvas;
    if (m.canvas) { paintCanvas(); paintProxy(); } else paintSVGMarks();
    paintSelection();
  }
  function showEmpty(text) { V.empty.textContent = text; V.empty.hidden = false; }

  function paintAxes() {
    var m = V.model, g = V.geo, b = V.base;
    var tx = S.ticks(m.fx, g.dx), ty = S.ticks(m.fy, g.dy);
    tx.forEach(function (t) {
      var x = Math.round(g.X(t.t)) + 0.5;
      svgEl("line", { "class": "sc-grid", x1: x, x2: x, y1: g.py0, y2: g.py1 }, b);
      svgEl("text", { "class": "sc-tick", x: x, y: g.sy1 + 13, "text-anchor": "middle" }, b).textContent = t.label;
    });
    ty.forEach(function (t) {
      var y = Math.round(g.Y(t.t)) + 0.5;
      svgEl("line", { "class": "sc-grid", x1: g.px0, x2: g.px1, y1: y, y2: y }, b);
      svgEl("text", { "class": "sc-tick", x: g.sx0 - 6, y: y + 3.5, "text-anchor": "end" }, b).textContent = t.label;
    });
    svgEl("line", { "class": "sc-axis", x1: g.px0, x2: g.px1, y1: g.py1 + 0.5, y2: g.py1 + 0.5 }, b);
    svgEl("line", { "class": "sc-axis", x1: g.px0 - 0.5, x2: g.px0 - 0.5, y1: g.py0, y2: g.py1 }, b);
    svgEl("text", { "class": "sc-title", x: (g.px0 + g.px1) / 2, y: g.H - 6, "text-anchor": "middle" }, b).textContent = S.axisTitle(m.fx);
    svgEl("text", { "class": "sc-title", x: 0, y: 0, "text-anchor": "middle", transform: "translate(13 " + (g.py0 + g.py1) / 2 + ") rotate(-90)" }, b).textContent = S.axisTitle(m.fy);
    // The unknown strips, drawn only when they hold marks.
    var z = m.zones;
    if (z.xstrip.length) {
      svgEl("rect", { "class": "sc-strip", x: g.sx0, y: g.py0, width: STRIP, height: g.py1 - g.py0 }, b);
      var t1 = svgEl("text", { "class": "sc-stripl", x: 0, y: 0, "text-anchor": "end", transform: "translate(" + (g.sx0 + 10) + " " + (g.py0 + 4) + ") rotate(-90)" }, b);
      t1.textContent = S.shortLabel(m.fx) + " " + S.stripText(z.xstrip.map(function (mk) { return mk.xv; }));
    }
    if (z.ystrip.length) {
      svgEl("rect", { "class": "sc-strip", x: g.px0, y: g.sy0, width: g.px1 - g.px0, height: STRIP }, b);
      svgEl("text", { "class": "sc-stripl", x: g.px1 - 4, y: g.sy0 + 10, "text-anchor": "end" }, b).textContent = S.shortLabel(m.fy) + " " + S.stripText(z.ystrip.map(function (mk) { return mk.yv; }));
    }
    if (z.corner.length) {
      svgEl("rect", { "class": "sc-strip", x: g.sx0, y: g.sy0, width: STRIP, height: STRIP }, b);
      var cl = svgEl("text", { "class": "sc-stripl", x: g.sx0 + STRIP / 2, y: g.sy0 + 10, "text-anchor": "middle" }, b);
      cl.textContent = "both";
      svgEl("title", {}, cl).textContent = "Both values unknown or not applicable: " + z.corner.map(function (mk) { return mk.ref; }).join(", ");
    }
  }
  function paintMedians() {
    var m = V.model, g = V.geo, b = V.base, f = function (fld, v) { return S.shortText(fld, { k: "v", v: v }); };
    if (m.med.x != null) {
      var x = g.X(m.sx.fwd(m.med.x));
      if (x >= g.px0 && x <= g.px1) {
        svgEl("line", { "class": "sc-med", x1: x, x2: x, y1: g.py0, y2: g.py1 }, b);
        svgEl("text", { "class": "sc-medl", x: x + 3, y: g.py0 + 9 }, b).textContent = "median " + f(m.fx, m.med.x);
      }
    }
    if (m.med.y != null) {
      var y = g.Y(m.sy.fwd(m.med.y));
      if (y >= g.py0 && y <= g.py1) {
        svgEl("line", { "class": "sc-med", x1: g.px0, x2: g.px1, y1: y, y2: y }, b);
        svgEl("text", { "class": "sc-medl", x: g.px1 - 3, y: y - 3, "text-anchor": "end" }, b).textContent = "median " + f(m.fy, m.med.y);
      }
    }
  }

  // markEl draws one mark as an SVG group: the shape, the dot of "your
  // move" and the × of a failing CI.
  function markEl(mk, parent, attrs) {
    var info = mk.info, g = svgEl("g", Object.assign({ "class": "sc-mk s-" + info.shape + (info.mine ? " mine" : "") + (info.failing ? " fail" : "") }, attrs || {}), parent);
    svgEl("path", { "class": "b", d: S.shapePath(info.shape, R) }, g);
    if (info.mine) svgEl("circle", { "class": "dot", r: DOT }, g);
    if (info.failing) { svgEl("path", { "class": "xh", d: S.CROSS }, g); svgEl("path", { "class": "x", d: S.CROSS }, g); }
    return g;
  }
  function paintSVGMarks() {
    var m = V.model, frag = document.createDocumentFragment(), tab = tabMark();
    V.markEls = new Map();
    m.order.forEach(function (mk) {
      if (!mk.vis) return;
      var g = markEl(mk, frag, { transform: "translate(" + mk.px.toFixed(1) + " " + mk.py.toFixed(1) + ")", role: "option", tabindex: mk === tab ? "0" : "-1",
        "aria-label": mk.label, "aria-selected": "false", "data-id": mk.id, "data-zone": mk.zone });
      V.markEls.set(mk.id, g);
    });
    V.marksG.appendChild(frag);
  }
  // tabMark is the mark the keyboard enters at: the one it left, else the
  // first selected, else the first in order.
  function tabMark() {
    var m = V.model, vis = m.order.filter(function (mk) { return mk.vis; });
    var at = V.focusId && m.byId.get(V.focusId);
    if (at && at.vis) return at;
    var sel = cur.store.selection;
    return vis.filter(function (mk) { return sel.has(mk.id); })[0] || vis[0] || null;
  }

  // ---------- canvas (1,000 marks or more) ----------
  var PATHS = {};
  function path2d(shape) { return PATHS[shape] || (PATHS[shape] = new Path2D(S.shapePath(shape, R))); }
  function paintCanvas() {
    var m = V.model, g = V.geo, c = V.canvas, dpr = window.devicePixelRatio || 1, th = cur.theme;
    c.width = Math.round(g.W * dpr); c.height = Math.round(g.H * dpr);
    c.style.width = g.W + "px"; c.style.height = g.H + "px";
    var x = c.getContext("2d");
    x.setTransform(dpr, 0, 0, dpr, 0, 0);
    x.clearRect(0, 0, g.W, g.H);
    var cross = new Path2D(S.CROSS);
    x.lineJoin = "round"; x.lineCap = "round";
    m.marks.forEach(function (mk) {
      if (!mk.vis) return;
      var info = mk.info, p = path2d(info.shape);
      x.setTransform(dpr, 0, 0, dpr, mk.px * dpr, mk.py * dpr);
      x.lineWidth = info.shape === "ring" ? 1.6 : 1.5;
      x.strokeStyle = info.shape === "ring" ? th["--ink2"] : th["--surface"];
      x.fillStyle = info.shape === "ring" ? th["--surface"] : info.mine ? th["--accent"] : th["--muted"];
      x.fill(p); x.stroke(p);
      if (info.mine) { x.beginPath(); x.arc(0, 0, DOT, 0, 6.2832); x.fillStyle = th["--surface"]; x.fill(); }
      if (info.failing) { x.lineWidth = 4; x.strokeStyle = th["--surface"]; x.stroke(cross); x.lineWidth = 1.8; x.strokeStyle = th["--s-critical"]; x.stroke(cross); }
    });
    x.setTransform(1, 0, 0, 1, 0, 0);
  }
  // paintProxy places the one focusable element of the canvas: it stands
  // for the mark the keyboard is on and carries its aria-label.
  function paintProxy() {
    var mk = tabMark();
    V.proxy = svgEl("g", { "class": "sc-mk sc-proxy", role: "option", tabindex: "0", "aria-selected": "false" }, V.marksG);
    V.proxyRing = svgEl("circle", { "class": "pr", r: R + 5 }, V.proxy);
    proxyTo(mk);
  }
  function proxyTo(mk) {
    if (!V.proxy) return;
    var m = V.model, n = m.order.length;
    if (!mk) { V.proxy.setAttribute("aria-label", "No PRs to plot"); V.proxy.setAttribute("transform", "translate(-50 -50)"); return; }
    V.proxy.dataset.id = mk.id;
    V.proxy.setAttribute("transform", "translate(" + mk.px.toFixed(1) + " " + mk.py.toFixed(1) + ")");
    V.proxy.setAttribute("aria-label", mk.label);
    V.proxy.setAttribute("aria-posinset", String(mk.k + 1)); V.proxy.setAttribute("aria-setsize", String(n));
    V.proxy.setAttribute("aria-selected", String(cur.store.selection.has(mk.id)));
  }

  // ---------- selection, labels, hover ----------
  function paintSelection() {
    var m = V.model, sel = cur.store.selection;
    if (!m || !m.fx || !V.ringsG) return;
    V.ringsG.textContent = ""; V.labelsG.textContent = "";
    if (V.markEls) V.markEls.forEach(function (g, id) { g.setAttribute("aria-selected", String(sel.has(id))); });
    if (V.proxy && V.proxy.dataset.id) V.proxy.setAttribute("aria-selected", String(sel.has(V.proxy.dataset.id)));
    var chosen = [], outl = [];
    m.marks.forEach(function (mk) {
      if (!mk.vis) return;
      if (sel.has(mk.id)) { chosen.push(mk); svgEl("circle", { "class": "sc-selring", cx: mk.px, cy: mk.py, r: R + 4.5 }, V.ringsG); }
      else if (m.outliers.has(mk.id)) outl.push(mk);
    });
    paintHover();
    // Direct labels: the selection first, then the outliers by rank sum.
    var order = chosen.concat(m.order.filter(function (mk) { return outl.indexOf(mk) >= 0; }).sort(function (a, b) { return outlierRank(a) - outlierRank(b); }));
    V.measure.font = "11px system-ui, -apple-system, 'Segoe UI', sans-serif";
    var g = V.geo, cands = order.slice(0, 60).map(function (mk) { return { key: mk.id, x: mk.px, y: mk.py, w: V.measure.measureText(mk.ref).width, h: 13, r: R + (sel.has(mk.id) ? 4 : 1) }; });
    var placed = S.placeLabels(cands, { x0: 2, y0: 2, x1: g.W - 2, y1: g.H - 2 });
    placed.forEach(function (p) {
      var mk = m.byId.get(p.key), t = svgEl("text", { "class": "sc-lbl" + (sel.has(mk.id) ? " sel" : " out"), x: p.x, y: p.y + 10, "data-id": mk.id }, V.labelsG);
      t.textContent = mk.ref;
    });
  }
  function outlierRank(mk) { var k = 0, r = -1; V.model.outliers.forEach(function (id) { if (id === mk.id) r = k; k++; }); return r; }
  function paintHover() {
    if (!V.ringsG) return;
    var old = V.ringsG.querySelector(".sc-hovring");
    if (old) old.remove();
    var id = cur.store.hover, mk = id && V.model.byId && V.model.byId.get(id);
    if (mk && mk.vis) svgEl("circle", { "class": "sc-hovring", cx: mk.px, cy: mk.py, r: R + 3 }, V.ringsG);
  }
  function setHover(id) { if (V.hoverId === id) return; V.hoverId = id; cur.hover(id); }

  // ---------- tooltip (details on demand) ----------
  function showTip(mk) {
    var m = V.model, el = cur.dom.el, tip = V.tip, n = m.ix.nodes[mk.i], pr = n.pr || {}, T = L.tableLogic;
    tip.textContent = "";
    el("b", null, mk.ref, tip);
    if (pr.title) el("div", null, pr.title, tip);
    el("div", null, S.shortLabel(m.fx) + ": " + S.valueText(m.fx, mk.xv), tip);
    el("div", null, S.shortLabel(m.fy) + ": " + S.valueText(m.fy, mk.yv) + (m.fy.name === "size" && T && mk.yv.k === "v" ? " (" + T.sizeText(pr, mk.yv.v) + ")" : ""), tip);
    el("div", null, S.moveText(mk.info) + (mk.info.failing ? " · CI failing" : ""), tip);
    var why = T ? T.moveTitle(pr.facts) : "";
    why.split("\n").filter(Boolean).forEach(function (line) { el("div", "dim", line, tip); });
    tip.hidden = false;
    var g = V.geo, w = tip.offsetWidth, h = tip.offsetHeight;
    tip.style.left = Math.max(4, Math.min(mk.px + 14, g.W - w - 4)) + "px";
    tip.style.top = Math.max(4, Math.min(mk.py + 14, g.H - h - 4)) + "px";
  }
  function hideTip() { V.tip.hidden = true; }

  // ---------- pointer: hover, click, brush, wheel and pinch ----------
  function pos(ev) { var r = V.over.getBoundingClientRect(); return [ev.clientX - r.left, ev.clientY - r.top]; }
  // The handlers outlive a destroyed view (V is null then) on its old DOM.
  function onDown(ev) {
    if (!V || ev.button !== 0 || !V.model || !V.model.fx) return;
    var p = pos(ev);
    V.pointers.set(ev.pointerId, p);
    if (V.pointers.size === 2) { startPinch(); V.drag = null; return; }
    V.drag = { x0: p[0], y0: p[1], x1: p[0], y1: p[1], shift: ev.shiftKey, moved: false, id: ev.pointerId };
    V.over.setPointerCapture(ev.pointerId);
  }
  function onMove(ev) {
    if (!V || !V.model || !V.model.fx) return;
    var p = pos(ev);
    if (V.pointers.has(ev.pointerId)) V.pointers.set(ev.pointerId, p);
    if (V.pinch) { movePinch(); return; }
    var d = V.drag;
    if (d && d.id === ev.pointerId) {
      d.x1 = p[0]; d.y1 = p[1];
      if (!d.moved && Math.hypot(d.x1 - d.x0, d.y1 - d.y0) > 4) { d.moved = true; hideTip(); }
      if (d.moved) {
        var x = Math.min(d.x0, d.x1), y = Math.min(d.y0, d.y1);
        V.brush.setAttribute("x", x); V.brush.setAttribute("y", y);
        V.brush.setAttribute("width", Math.abs(d.x1 - d.x0)); V.brush.setAttribute("height", Math.abs(d.y1 - d.y0));
        V.brush.setAttribute("visibility", "visible");
        // The count rides the rectangle, inside the chart.
        var n = S.inRect(V.model.marks, d).length;
        V.brushText.textContent = cur.dom.plural(n, "PR") + (d.shift ? " to add" : "");
        V.brushText.setAttribute("x", x + 2); V.brushText.setAttribute("y", y > 14 ? y - 4 : y + Math.abs(d.y1 - d.y0) + 12);
        V.brushText.setAttribute("visibility", "visible");
      }
      return;
    }
    var mk = nearest(p[0], p[1]);
    V.over.classList.toggle("hot", !!mk);
    setHover(mk ? mk.id : null);
    if (mk) showTip(mk); else hideTip();
  }
  function onUp(ev) {
    if (!V) return;
    V.pointers.delete(ev.pointerId);
    if (V.pinch) { if (V.pointers.size < 2) V.pinch = null; return; }
    var d = V.drag;
    if (!d || d.id !== ev.pointerId) return;
    endDrag();
    var sel = cur.store.selection;
    if (d.moved) {
      // One committed change for the whole drag: one Back undoes it.
      var ids = S.inRect(V.model.marks, d).map(function (mk) { return mk.id; });
      V.status.textContent = cur.dom.plural(ids.length, "PR") + (d.shift ? " added to the selection" : " selected");
      if (d.shift) { var u = new Set(sel); ids.forEach(function (id) { u.add(id); }); ids = Array.from(u); }
      cur.setState({ sel: ids }, { commit: true });
      return;
    }
    var mk = nearest(d.x0, d.y0);
    if (mk) { V.focusId = mk.id; cur.select([mk.id], d.shift || ev.metaKey); focusMark(mk.id, false); }
    else if (sel.size && !d.shift) cur.setState({ sel: [] }, { commit: true });
  }
  function endDrag() { V.drag = null; if (V.brush) { V.brush.setAttribute("visibility", "hidden"); V.brushText.setAttribute("visibility", "hidden"); } }
  // zoomAt scales both axes by factor about the screen point (x, y).
  function zoomAt(x, y, factor) {
    var m = V.model, g = V.geo, d = V.zoom || m.dom;
    // The transformed values under (x, y): layout's X and Y inverted.
    var tx = d.x[0] + (Math.max(g.px0, Math.min(g.px1, x)) - g.px0 - PAD) / (g.px1 - g.px0 - 2 * PAD) * (d.x[1] - d.x[0]);
    var ty = d.y[0] + (g.py1 - PAD - Math.max(g.py0, Math.min(g.py1, y))) / (g.py1 - g.py0 - 2 * PAD) * (d.y[1] - d.y[0]);
    var nx = S.zoomDomain(d.x, tx, factor), ny = S.zoomDomain(d.y, ty, factor);
    // Never wider than every mark, never narrower than a twentieth of it.
    var spanX = m.dom.x[1] - m.dom.x[0], spanY = m.dom.y[1] - m.dom.y[0];
    if (nx[1] - nx[0] >= spanX && ny[1] - ny[0] >= spanY) { V.zoom = null; }
    else if (nx[1] - nx[0] < spanX / 20 || ny[1] - ny[0] < spanY / 20) return;
    else V.zoom = { x: nx, y: ny };
    hideTip();
    layout(); paintAll();
  }
  function onWheel(ev) {
    if (!V || !V.model || !V.model.fx) return;
    ev.preventDefault();
    var p = pos(ev);
    // A trackpad pinch arrives as a wheel event with ctrlKey.
    zoomAt(p[0], p[1], Math.exp(ev.deltaY * (ev.ctrlKey ? 0.01 : 0.0015)));
  }
  function pinchState() {
    var ps = Array.from(V.pointers.values());
    return { d: Math.hypot(ps[0][0] - ps[1][0], ps[0][1] - ps[1][1]) || 1, x: (ps[0][0] + ps[1][0]) / 2, y: (ps[0][1] + ps[1][1]) / 2 };
  }
  function startPinch() { endDrag(); V.pinch = pinchState(); }
  function movePinch() {
    if (V.pointers.size < 2) return;
    var now = pinchState(), f = V.pinch.d / now.d;
    if (Math.abs(f - 1) < 0.02) return;
    V.pinch = now;
    zoomAt(now.x, now.y, f);
  }

  // ---------- keyboard (requirement 8) ----------
  function visOrder() { return V.model.order.filter(function (mk) { return mk.vis; }); }
  function onKey(ev) {
    var m = V && V.model;
    if (!m || !m.fx) return;
    var k = ev.key, g = V.geo;
    if (k === "+" || k === "=" || k === "-" || k === "0") {
      ev.preventDefault();
      if (k === "0") { V.zoom = null; layout(); paintAll(); }
      else zoomAt((g.px0 + g.px1) / 2, (g.py0 + g.py1) / 2, k === "-" ? 1.25 : 0.8);
      focusMark(V.focusId, true);
      return;
    }
    var list = visOrder(), at = list.findIndex(function (mk) { return mk.id === V.focusId; });
    if (k === "Enter" || k === " ") {
      if (at < 0) return;
      ev.preventDefault();
      cur.select([list[at].id], ev.shiftKey);
      return;
    }
    var key = k === "ArrowRight" ? "ArrowDown" : k === "ArrowLeft" ? "ArrowUp" : k;
    var nx = L.nextIndex(list.length, at, key);
    if (nx < 0) return;
    ev.preventDefault();
    focusMark(list[nx].id, true);
    if (!ev.shiftKey) cur.select([list[nx].id], false, "replace");
  }
  // focusMark moves the keyboard to the mark id (or where it can enter).
  function focusMark(id, takeFocus) {
    var m = V.model, mk = id && m.byId && m.byId.get(id);
    if (!mk || !mk.vis) mk = tabMark();
    if (!mk) return;
    V.focusId = mk.id;
    if (m.canvas) {
      proxyTo(mk);
      if (takeFocus) V.proxy.focus({ preventScroll: true });
      V.live.textContent = mk.label;
      showTip(mk);
      return;
    }
    if (V.markEls) V.markEls.forEach(function (g, gid) { g.setAttribute("tabindex", gid === mk.id ? "0" : "-1"); });
    var el = V.markEls && V.markEls.get(mk.id);
    if (el && takeFocus) el.focus({ preventScroll: true });
  }
  function onFocusMark(g) {
    var id = g.dataset.id, mk = id && V.model.byId.get(id);
    if (!mk) return;
    V.focusId = id;
    showTip(mk);
  }

  // extract: Copy as Markdown lists the rows with the two plotted fields and
  // whose move it is.
  function extract(ctx) {
    var s = S || logic(L, L.tableLogic || null), reg = ctx.registry;
    var fx = s.axisField(reg, ctx.getState("x"), s.DEFAULT_X), fy = s.axisField(reg, ctx.getState("y"), s.DEFAULT_Y);
    return { cols: [fx && fx.name, fy && fy.name, "move"].filter(function (c, k, all) { return c && reg.field(c) && all.indexOf(c) === k; }) };
  }

  window.WGOLive.registerView({ id: "scatter", label: "Scatter", question: "Which PRs are small and have waited long — push them — and which are big — split them?",
    render: render, extract: extract,
    destroy: function () {
      if (!V) return;
      if (V.ro) V.ro.disconnect();
      if (V.hoverId && cur) cur.hover(null);
      V = null;
    } });
})();
