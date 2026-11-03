/* wgo dash linked filters (spec/gh-102.md). An addon that turns the live
 * page's filters into dynamic queries (Ahlberg & Shneiderman): a facet
 * panel left of the views, each facet a small histogram that is also its
 * own filter and shows, over a light ghost of the whole snapshot, what the
 * *other* facets' clauses leave (Spence & Tweedie's Attribute Explorer);
 * an attention summary leading the overview; and the overview's charts — a
 * brushable when chart, PRs by state, agent sessions — rebuilt on the same
 * counts and gestures:
 *
 *   click, Enter or Space            only this value (field:v); again clears
 *   Shift- or ⌘-click, Shift+Enter   add or remove v in the OR list
 *   Alt-click, Alt+Enter             exclude (!field:v)
 *   drag a brush or a handle, arrows or Shift+arrows   set a range
 *
 * A drag is live (the history entry is replaced) and its release commits,
 * so one Back undoes it. Coupled counts AND one bitset per clause
 * (WGOLive.clauseMasks) with one bitset per facet value, built once per
 * snapshot, so no bar re-scans the nodes. Chosen values are outlined and
 * marked ✓, excluded ones struck through and marked ✕: never colour alone;
 * the accent marks only the selection's share and "your move".
 *
 * The pure logic is logic(WGOLive), also module.exports under node, so
 * internal/review/testdata/live_logic.js tests it without a page. Like
 * live.js it writes no markup from strings and issues no requests. */
(function () {
  "use strict";

  // ---------- pure logic ----------
  function logic(L) {
    var F = {};
    function cmp(a, b) { return a < b ? -1 : a > b ? 1 : 0; }

    // DEFAULT_FACETS is the panel's order without [dash] facets
    // (dash.DefaultFacets; TestFacetDefaultsMatchTheAddon keeps them equal).
    F.DEFAULT_FACETS = ["move", "ci", "prstate", "repo", "author", "reviewer", "waiting", "age", "size", "ticket_status", "sprint", "mismatch"];
    // PEOPLE fields list the configured people first, by display name.
    var PEOPLE = { author: true, reviewer: true, requested_from: true, reviewed_by: true, move_people: true, waiting_on: true };
    F.PEOPLE = PEOPLE;
    // TOP is how many values a long list shows before its type-ahead.
    F.TOP = 8;

    // ---------- bitsets: bit i is node i of a query index ----------
    F.words = function (n) { return (n + 31) >>> 5; };
    // full is every node of n.
    F.full = function (n) {
      var b = new Uint32Array(F.words(n));
      for (var i = 0; i < b.length; i++) b[i] = 0xffffffff;
      if (n & 31) b[b.length - 1] = 0xffffffff >>> (32 - (n & 31));
      return b;
    };
    function setBit(b, i) { b[i >>> 5] |= 1 << (i & 31); }
    F.has = function (b, i) { return ((b[i >>> 5] >>> (i & 31)) & 1) === 1; };
    function pop(x) {
      x -= (x >>> 1) & 0x55555555;
      x = (x & 0x33333333) + ((x >>> 2) & 0x33333333);
      return Math.imul((x + (x >>> 4)) & 0x0f0f0f0f, 0x01010101) >>> 24;
    }
    // count is the size of a ∩ b ∩ c (b and c optional).
    F.count = function (a, b, c) {
      var n = 0, i;
      if (c) for (i = 0; i < a.length; i++) n += pop(a[i] & b[i] & c[i]);
      else if (b) for (i = 0; i < a.length; i++) n += pop(a[i] & b[i]);
      else for (i = 0; i < a.length; i++) n += pop(a[i]);
      return n;
    };
    // fromIds is the bitset of the node IDs ids that ix holds.
    F.fromIds = function (ix, ids) {
      var b = new Uint32Array(F.words(ix.nodes.length));
      ids.forEach(function (id) { var i = ix.byId.get(id); if (i != null) setBit(b, i); });
      return b;
    };

    // Per query index: field indexes, clause masks and saved-filter counts,
    // built once and dropped with the snapshot.
    var cache = new WeakMap();
    function memo(ix) {
      var m = cache.get(ix);
      if (!m) cache.set(ix, m = { fields: new Map(), masks: new Map(), summary: null, filters: null, efforts: null });
      return m;
    }

    // ---------- field indexes ----------
    // facetType is how a field draws: "bars" (enum, set, bool), "hist"
    // (number, duration) or "days" (date); "" for text, a substring match
    // with no values to count.
    F.facetType = function (f) {
      if (!f || f.type === "text") return "";
      return f.type === "number" || f.type === "duration" ? "hist" : f.type === "date" ? "days" : "bars";
    };
    // fieldIndex decodes field name over ix once: applies (the field applies
    // to the node), unknown, none (known and empty), and one bitset per
    // value (bars), bin (hist) or day of the window (days), each with its
    // count over the whole snapshot (ghost).
    F.fieldIndex = function (ix, name) {
      var m = memo(ix), fi = m.fields.get(name);
      if (fi) return fi;
      var f = ix.reg.field(name), n = ix.nodes.length, w = F.words(n), vals = ix.values.get(name) || [];
      fi = { name: name, f: f, type: F.facetType(f), n: n, applies: new Uint32Array(w), unknown: new Uint32Array(w), none: new Uint32Array(w), known: 0 };
      var per = []; // per node: its values (bars), numbers (hist) or days (days)
      for (var i = 0; i < n; i++) {
        var v = vals[i];
        if (!v || v.na) continue;
        setBit(fi.applies, i);
        if (v.unknown) { setBit(fi.unknown, i); continue; }
        fi.known++;
        var xs = fi.type === "hist" ? v.nums : v.norm;
        if (!xs.length) { setBit(fi.none, i); continue; }
        per.push([i, xs, v.disp]);
      }
      if (fi.type === "bars") {
        fi.values = new Map();
        per.forEach(function (p) {
          p[1].forEach(function (k, j) {
            var e = fi.values.get(k);
            if (!e) fi.values.set(k, e = { key: k, disp: p[2][j], bits: new Uint32Array(w), ghost: 0 });
            setBit(e.bits, p[0]); e.ghost++;
          });
        });
      } else if (fi.type === "hist") {
        var all = [];
        fi.frac = false;
        per.forEach(function (p) { p[1].forEach(function (x) { all.push(x); if (x !== Math.floor(x)) fi.frac = true; }); });
        fi.bins = F.numberBins(f, all);
        fi.bins.forEach(function (b) { b.bits = new Uint32Array(w); b.ghost = 0; });
        per.forEach(function (p) {
          p[1].forEach(function (x) { var b = fi.bins[F.binOf(fi.bins, x)]; if (!F.has(b.bits, p[0])) { setBit(b.bits, p[0]); b.ghost++; } });
        });
      } else if (fi.type === "days") {
        fi.days = F.dayWindow(ix);
        var pos = new Map(), first = fi.days[0] || "", last = fi.days[fi.days.length - 1] || "";
        fi.days.forEach(function (d, k) { pos.set(d, k); });
        fi.bins = fi.days.map(function (d) { return { day: d, bits: new Uint32Array(w), ghost: 0 }; });
        fi.earlier = { bits: new Uint32Array(w), ghost: 0 };
        fi.later = { bits: new Uint32Array(w), ghost: 0 };
        per.forEach(function (p) {
          p[1].forEach(function (d) {
            var k = pos.get(d), b = k != null ? fi.bins[k] : !first || d < first ? fi.earlier : fi.later;
            if (!F.has(b.bits, p[0])) { setBit(b.bits, p[0]); b.ghost++; }
          });
        });
        if (!last) fi.later.ghost = 0;
      }
      fi.unknownN = F.count(fi.unknown);
      fi.noneN = F.count(fi.none);
      m.fields.set(name, fi);
      return fi;
    };

    // ---------- bins ----------
    // DURATION_LOG are the day edges of a log-scaled duration: a day, two,
    // half a week, a week, two, a month, two, a quarter, half a year, a year.
    var DURATION_LOG = [0, 1, 2, 4, 7, 14, 30, 60, 90, 180, 365];
    function seq(a, b) { var out = []; for (var i = a; i <= b; i++) out.push(i); return out; }
    function niceStep(x) { var p = Math.pow(10, Math.floor(Math.log10(x))), r = x / p; return (r <= 1 ? 1 : r <= 2 ? 2 : r <= 5 ? 5 : 10) * p; }
    // numberBins bins a number or duration field's values: durations in
    // whole days, with an open last bin starting at the largest edge the
    // data reaches (log-spaced days for a log scale, else one per day up to
    // two weeks); log-scaled numbers in 1-3-10 steps from 0; other numbers
    // one bin per integer when that makes at most 20, else about 10 bins of
    // a round width. Each bin is [lo, hi), or [lo, ∞) when open.
    F.numberBins = function (f, nums) {
      var max = 0, min = 0, edges, open = false, i;
      for (i = 0; i < nums.length; i++) { if (i === 0 || nums[i] > max) max = nums[i]; if (i === 0 || nums[i] < min) min = nums[i]; }
      if (f.type === "duration") {
        var base = f.scale === "log" ? DURATION_LOG : seq(0, 14), k = 2;
        while (k + 1 < base.length && base[k + 1] <= max) k++;
        edges = base.slice(0, k + 1); open = true;
      } else if (f.scale === "log") {
        edges = [0, 1];
        while (edges.length < 4 || edges[edges.length - 1] <= max) { var e = edges[edges.length - 1]; edges.push(String(e).charAt(0) === "1" ? e * 3 : e * 10 / 3); }
      } else {
        var lo = Math.min(0, Math.floor(min)), hi = Math.floor(max) + 1;
        if (hi - lo <= 20) edges = seq(lo, hi);
        else {
          var step = niceStep((hi - lo) / 10), s = Math.floor(lo / step) * step;
          edges = [];
          for (var x = s; edges.length < 2 || edges[edges.length - 1] <= max; x += step) edges.push(x);
        }
      }
      var bins = [];
      for (i = 0; i + 1 < edges.length; i++) bins.push({ lo: edges[i], hi: edges[i + 1], open: false });
      if (open) bins.push({ lo: edges[edges.length - 1], hi: null, open: true });
      bins.forEach(function (b) { b.label = shortNum(b.lo) + (b.open ? "+" : ""); b.text = F.binText(f, b); });
      return bins;
    };
    function shortNum(x) { return x >= 1000 && x % 1000 === 0 ? x / 1000 + "k" : String(x); }
    // binText is a bin in words: "2–3 days", "≥ 14 days", "100–299 lines".
    F.binText = function (f, b) {
      var unit = f.unit ? " " + f.unit : "";
      if (b.open) return "≥ " + b.lo + unit;
      if (b.hi - b.lo === 1 && b.lo === Math.floor(b.lo)) return b.lo + (b.lo === 1 ? unit.replace(/s$/, "") : unit);
      return b.lo + "–" + (Number.isInteger(b.hi) ? b.hi - 1 : "<" + b.hi) + unit;
    };
    F.binOf = function (bins, x) {
      for (var k = bins.length - 1; k > 0; k--) if (x >= bins[k].lo) return k;
      return 0;
    };
    // dayWindow is the days a date facet draws, oldest first: twice the
    // snapshot's activity window, and at least four weeks — the PR
    // inventory's closed-recent window (inventory.ClosedWindowDays) — so a
    // week can be compared with the one before it. Empty without a
    // generated_at to count from.
    F.dayWindow = function (ix) {
      var n = Math.max(2 * (ix.days.length || 14), 28), out = [];
      if (!ix.today) return out;
      for (var k = n - 1; k >= 0; k--) out.push(k ? L.resolveDay(ix, "-" + k + "d") : ix.today);
      return out;
    };

    // ---------- the query's clauses ----------
    // queryMasks parses expr over ix and evaluates each clause on its own
    // (cached per snapshot and expression).
    F.queryMasks = function (ix, expr) {
      var m = memo(ix), qm = m.masks.get(expr);
      if (qm) return qm;
      var q = L.parseQuery(expr, ix.reg);
      qm = { expr: expr, q: q.error ? { expr: expr, clauses: [] } : q, error: q.error || null, masks: q.error ? [] : L.clauseMasks(q, ix), n: ix.nodes.length };
      if (m.masks.size > 64) m.masks.clear();
      m.masks.set(expr, qm);
      return qm;
    };
    // others is the rows under every clause not on field name: facet name's
    // coupled population (kind: clauses included, unless name is kind). A
    // null name gives the query's own rows.
    F.others = function (qm, name) {
      var out = F.full(qm.n);
      qm.masks.forEach(function (x) { if (x.c.name !== name) for (var i = 0; i < out.length; i++) out[i] &= x.bits[i]; });
      return out;
    };
    // facetCounts is facet fi's counts for each value (bars) or bin (hist,
    // days), and for unknown and none: ghost, over the whole snapshot;
    // coupled, among others (the rows every other facet's clauses leave);
    // part, in the selection sel when given.
    F.facetCounts = function (fi, others, sel) {
      function c(bits, ghost) { return { ghost: ghost, coupled: F.count(bits, others), part: sel ? F.count(bits, sel) : 0 }; }
      var out = { unknown: c(fi.unknown, fi.unknownN), none: c(fi.none, fi.noneN), values: [], bins: [] };
      if (fi.values) fi.values.forEach(function (e) { var r = c(e.bits, e.ghost); r.key = e.key; r.disp = e.disp; out.values.push(r); });
      if (fi.bins) fi.bins.forEach(function (b) { out.bins.push(c(b.bits, b.ghost)); });
      if (fi.earlier) { out.earlier = c(fi.earlier.bits, fi.earlier.ghost); out.later = c(fi.later.bits, fi.later.ghost); }
      return out;
    };

    // fieldState is what a query asks of field name: positive and negative
    // list values (lowercased; macros as written), whether name:* or
    // !name:* is there, its positive range bounds, its negated ranges and
    // the clauses themselves.
    F.fieldState = function (q, name) {
      var st = { pos: [], neg: [], any: false, notAny: false, los: [], his: [], negRanges: [], clauses: [] };
      q.clauses.forEach(function (c) {
        if (c.name !== name) return;
        st.clauses.push(c);
        if (c.op === "any") { if (c.neg) st.notAny = true; else st.any = true; }
        else if (c.op === "list") c.vals.forEach(function (v) { (c.neg ? st.neg : st.pos).push(v.macro ? v.text : v.text.toLowerCase()); });
        else if (c.neg) st.negRanges.push(c);
        else { if (c.lo) st.los.push({ text: c.lo.text, incl: c.loIncl }); if (c.hi) st.his.push({ text: c.hi.text, incl: c.hiIncl }); }
      });
      st.range = st.los.length > 0 || st.his.length > 0;
      st.active = st.clauses.length > 0;
      return st;
    };

    // ---------- gestures ----------
    function valueText(v) { return v.macro ? v.text : L.quoteValue(v.text); }
    function listClause(neg, name, vals) { return (neg ? "!" : "") + name + ":" + vals.map(valueText).join(","); }
    // gesture applies a facet gesture to expr: mode "only" (click: name:v,
    // again clears — gh-100's toggleClause), "or" (Shift- or ⌘-click: add
    // or remove v in the first positive list, dropping it from exclusions)
    // or "exclude" (Alt-click: add v to !name:…, dropping it from positive
    // lists; again removes it). Other clauses are left as they are.
    F.gesture = function (expr, reg, name, value, mode) {
      if (mode !== "or" && mode !== "exclude") return L.toggleClause(expr, reg, name, value);
      var q = L.parseQuery(expr, reg), want = String(value).toLowerCase(), out = [], done = false, add = { text: String(value) };
      if (q.error) return (mode === "exclude" ? "!" : "") + name + ":" + L.quoteValue(value);
      function rest(c) { return c.vals.filter(function (v) { return v.macro || v.text.toLowerCase() !== want; }); }
      var excluded = mode === "exclude" && q.clauses.some(function (c) { return c.name === name && c.neg && c.op === "list" && rest(c).length !== c.vals.length; });
      q.clauses.forEach(function (c) {
        if (c.name !== name || c.op !== "list") { out.push(c.raw); return; }
        var vs = rest(c), had = vs.length !== c.vals.length;
        // The clause on the other side of the gesture loses v.
        if (c.neg === (mode === "or") || excluded) { if (!had) out.push(c.raw); else if (vs.length) out.push(listClause(c.neg, name, vs)); return; }
        if (done) { out.push(c.raw); return; }
        done = true;
        if (!had) out.push(listClause(c.neg, name, c.vals.concat([add])));
        else if (vs.length) out.push(listClause(c.neg, name, vs));
      });
      if (!done && !excluded) out.push((mode === "exclude" ? "!" : "") + name + ":" + L.quoteValue(value));
      return out.join(";");
    };
    // noneGesture is a gesture on a set field's "none" (known, no values):
    // only and or choose !name:*, exclude chooses name:* (has any); again
    // clears. It replaces the field's other clauses.
    F.noneGesture = function (expr, reg, name, mode) {
      var q = L.parseQuery(expr, reg), neg = mode !== "exclude";
      if (q.error) return (neg ? "!" : "") + name + ":*";
      var on = q.clauses.filter(function (c) { return c.name === name; });
      var keep = q.clauses.filter(function (c) { return c.name !== name; }).map(function (c) { return c.raw; });
      if (!(on.length === 1 && on[0].op === "any" && on[0].neg === neg)) keep.push((neg ? "!" : "") + name + ":*");
      return keep.join(";");
    };
    // setClauses puts texts in place of field name's positive clauses
    // (lists, ranges, *), where the first of them was, keeping its
    // exclusions and every other clause: what a brush or a handle sets.
    // Empty texts clear the positive clauses.
    F.setClauses = function (expr, reg, name, texts) {
      var q = L.parseQuery(expr, reg), out = [], placed = false;
      if (q.error) return texts.join(";");
      q.clauses.forEach(function (c) {
        if (c.name === name && !c.neg) { if (!placed) { placed = true; out = out.concat(texts); } return; }
        out.push(c.raw);
      });
      if (!placed) out = out.concat(texts);
      return out.join(";");
    };
    // toggleText adds text as a clause, or removes it when expr has it.
    F.toggleText = function (expr, reg, text) {
      var q = L.parseQuery(expr, reg), raws = q.error ? [] : q.clauses.map(function (c) { return c.raw; }), i = raws.indexOf(text);
      if (i >= 0) raws.splice(i, 1); else raws.push(text);
      return raws.join(";");
    };
    function numText(x) { return String(Math.round(x * 1e6) / 1e6); }
    // rangeTexts are the clauses for numbers in [lo, hi) (null: open): one
    // inclusive range lo..hi-1 for whole numbers, or >=lo and <hi when the
    // field holds fractions.
    F.rangeTexts = function (name, lo, hi, frac) {
      if (lo == null && hi == null) return [];
      if (frac) { var t = []; if (lo != null) t.push(name + ":>=" + numText(lo)); if (hi != null) t.push(name + ":<" + numText(hi)); return t; }
      return [name + ":" + (lo != null ? numText(lo) : "") + ".." + (hi != null ? numText(hi - 1) : "")];
    };
    // edgeRange is the range of the bins between edges a and b (bin a to
    // bin b-1); the outer edges leave that side open.
    F.edgeRange = function (fi, a, b) {
      var B = fi.bins.length;
      return F.rangeTexts(fi.name, a > 0 ? fi.bins[a].lo : null, b < B ? fi.bins[b].lo : null, fi.frac);
    };
    // binExclusion is the clause excluding bin k: !name:lo..hi-1.
    F.binExclusion = function (fi, k) {
      var b = fi.bins[k];
      return "!" + fi.name + ":" + numText(b.lo) + ".." + (b.open ? "" : numText(fi.frac ? b.hi : b.hi - 1));
    };
    function boundNum(f, s) {
      if (f.type !== "duration") return parseFloat(s);
      var last = s.charAt(s.length - 1);
      return parseFloat(last === "w" || last === "d" ? s.slice(0, -1) : s) * (last === "w" ? 7 : 1);
    }
    // numRange is the inclusive [lo, hi] (null: open) the positive range
    // clauses of a number or duration field make, exclusive bounds moved in
    // by one (or a hair, for fractions).
    F.numRange = function (fi, st) {
      if (!st.range) return null;
      var eps = fi.frac ? 1e-9 : 1, lo = null, hi = null;
      st.los.forEach(function (b) { var x = boundNum(fi.f, b.text) + (b.incl ? 0 : eps); if (lo == null || x > lo) lo = x; });
      st.his.forEach(function (b) { var x = boundNum(fi.f, b.text) - (b.incl ? 0 : eps); if (hi == null || x < hi) hi = x; });
      return { lo: lo, hi: hi };
    };
    // rangeEdges places a slider's handles for the query: the edges around
    // the bins the range reaches, or the whole axis.
    F.rangeEdges = function (fi, st) {
      var B = fi.bins.length, r = F.numRange(fi, st), a = 0, b = B, k;
      if (!r) return { a: 0, b: B, active: false };
      if (r.lo != null) { a = B - 1; for (k = 0; k < B; k++) if (fi.bins[k].open || fi.bins[k].hi > r.lo) { a = k; break; } }
      if (r.hi != null) { b = 1; for (k = B - 1; k >= 0; k--) if (fi.bins[k].lo <= r.hi) { b = k + 1; break; } }
      if (b <= a) b = Math.min(B, a + 1);
      return { a: a, b: b, active: true };
    };
    // binExcluded reports whether a negated range clause covers bin k.
    F.binExcluded = function (fi, st, k) {
      var raw = F.binExclusion(fi, k);
      return st.negRanges.some(function (c) { return c.raw === raw; });
    };

    // ---------- days ----------
    function dayNum(d) { return Date.UTC(+d.slice(0, 4), +d.slice(5, 7) - 1, +d.slice(8, 10)) / 86400000; }
    F.addDays = function (d, n) { return new Date((dayNum(d) + n) * 86400000).toISOString().slice(0, 10); };
    F.dayCount = function (a, b) { return dayNum(b) - dayNum(a) + 1; };
    // dayRange is the inclusive [lo, hi] (null: open) of a date field's
    // positive range clauses, or a single day chosen alone (a click), or
    // null when there is neither.
    F.dayRange = function (ix, st) {
      if (!st.range) {
        if (st.pos.length !== 1 || st.any) return null;
        var d = L.resolveDay(ix, st.pos[0]);
        return d ? { lo: d, hi: d } : null;
      }
      var lo = null, hi = null;
      st.los.forEach(function (b) { var d = L.resolveDay(ix, b.text); if (d) { if (!b.incl) d = F.addDays(d, 1); if (lo == null || d > lo) lo = d; } });
      st.his.forEach(function (b) { var d = L.resolveDay(ix, b.text); if (d) { if (!b.incl) d = F.addDays(d, -1); if (hi == null || d < hi) hi = d; } });
      return lo == null && hi == null ? null : { lo: lo, hi: hi };
    };
    // dayTexts are the clauses for days lo..hi (null: open).
    F.dayTexts = function (name, lo, hi) { return lo == null && hi == null ? [] : [name + ":" + (lo || "") + ".." + (hi || "")]; };
    // dayStates marks each day of a date facet chosen (in the positive list
    // or range) or excluded (in a negative list or range).
    F.dayStates = function (ix, fi, st) {
      var r = F.dayRange(ix, st), pos = {}, neg = {};
      st.pos.forEach(function (s) { var d = L.resolveDay(ix, s); if (d) pos[d] = true; });
      st.neg.forEach(function (s) { var d = L.resolveDay(ix, s); if (d) neg[d] = true; });
      var negR = st.negRanges.map(function (c) {
        var lo = c.lo ? L.resolveDay(ix, c.lo.text) : null, hi = c.hi ? L.resolveDay(ix, c.hi.text) : null;
        if (lo && !c.loIncl) lo = F.addDays(lo, 1);
        if (hi && !c.hiIncl) hi = F.addDays(hi, -1);
        return { lo: lo, hi: hi };
      });
      function inR(d, x) { return !!x && (x.lo == null || d >= x.lo) && (x.hi == null || d <= x.hi); }
      return fi.days.map(function (d) {
        return { sel: !!pos[d] || (st.range && inR(d, r)), exc: !!neg[d] || negR.some(function (x) { return inR(d, x); }) };
      });
    };

    // ---------- time (requirements 8 and 9) ----------
    // timeTarget is the field the when chart shows and brushes: merged while
    // the query has a merged range and no when clause (the shipped filter),
    // so a brush replaces merged:-7d.. with merged:a..b; otherwise when.
    F.timeTarget = function (q) {
      if (q.clauses.some(function (c) { return c.name === "when"; })) return "when";
      return q.clauses.some(function (c) { return c.name === "merged" && !c.neg && c.op === "range"; }) ? "merged" : "when";
    };
    // coverDay is the first local day the PR inventory knows every followed
    // person's merges from (github_inventory's covered_since), or "".
    F.coverDay = function (ix, snap) {
      var inv = snap && snap.sources && snap.sources.github_inventory, ms = inv ? Date.parse(inv.covered_since) : NaN;
      if (!isFinite(ms) || !ix.env.dayOf) return "";
      return ix.env.dayOf(ms + 86400000 - 1);
    };
    // period compares the merged PRs among the query's rows with those the
    // same query finds in the period of equal length just before, while a
    // when or merged range (or a single day) is active; null otherwise.
    // prevCovered says the PR inventory covers the whole previous period;
    // without it the previous count is never shown.
    F.period = function (ix, expr, snap) {
      var qm = F.queryMasks(ix, expr);
      if (qm.error) return null;
      var target = F.timeTarget(qm.q), st = F.fieldState(qm.q, target), r = F.dayRange(ix, st);
      if (!r) return null;
      var pr = F.fieldIndex(ix, "prstate"), merged = pr.values && pr.values.get("merged");
      var out = { target: target, lo: r.lo, hi: r.hi || ix.today, cur: merged ? F.count(F.others(qm, null), merged.bits) : 0, coverDay: F.coverDay(ix, snap) };
      out.curCovered = !!out.coverDay && r.lo != null && r.lo >= out.coverDay;
      if (r.lo == null || !out.hi) { out.open = true; return out; }
      out.len = F.dayCount(r.lo, out.hi);
      out.prevLo = F.addDays(r.lo, -out.len); out.prevHi = F.addDays(r.lo, -1);
      var pq = F.queryMasks(ix, F.setClauses(expr, ix.reg, target, F.dayTexts(target, out.prevLo, out.prevHi)));
      out.prev = pq.error || !merged ? null : F.count(F.others(pq, null), merged.bits);
      out.prevCovered = !!out.coverDay && out.prev != null && out.prevLo >= out.coverDay;
      return out;
    };
    function days(n) { return n + (n === 1 ? " day" : " days"); }
    // periodText is the comparison in words: "3 merged · previous 0", or
    // why the previous period is not shown.
    F.periodText = function (p) {
      if (!p) return "";
      var cur = (p.curCovered ? "" : "at least ") + p.cur + " merged";
      if (p.open) return cur + " · no previous period: the range has no start";
      if (p.prevCovered) return cur + " · previous " + p.prev;
      return cur + " · previous " + days(p.len) + (p.coverDay ? " not covered: merges are known from " + p.coverDay : " not known: the PR inventory does not say from when merges are known");
    };
    F.periodTitle = function (p) {
      if (!p) return "";
      if (p.open) return "Merged PRs up to " + p.hi + " among the rows";
      return "Merged PRs among the rows, " + p.lo + " to " + p.hi + " (" + days(p.len) + "), against the same query " + p.prevLo + " to " + p.prevHi +
        (p.coverDay ? "; the PR inventory knows merges from " + p.coverDay : "") + (p.curCovered ? "" : "; this period starts before that, so its count is a lower bound");
    };

    // ---------- attention summary (requirement 11) ----------
    F.SUMMARY = [
      { name: "my-move", label: "your move", accent: true },
      { name: "waiting-reviewers", label: "waiting on reviewers" },
      { name: "nobody", label: "nobody's move" }
    ];
    // summary counts the rows of the three saved filters over ix: count is
    // null, with why, when a filter is missing, hidden, or cannot resolve
    // (@me with nobody marked me), never a misleading zero.
    F.summary = function (ix, filters) {
      var m = memo(ix);
      if (m.summary && m.filters === filters) return m.summary;
      m.filters = filters;
      m.summary = F.SUMMARY.map(function (s) {
        var f = (filters || []).filter(function (x) { return x.name === s.name; })[0];
        var r = { name: s.name, label: s.label, accent: !!s.accent, expr: f ? f.expr : "", count: null, why: "" };
        if (!f) r.why = "no saved filter " + s.name;
        else if (f.hidden) r.why = f.reason || "the saved filter is hidden";
        else {
          var run = L.runQuery(f.expr, ix);
          if (run.error) r.why = run.error.error;
          else if (run.res.warnings.length) r.why = run.res.warnings.join("; ");
          else r.count = run.res.rows.length;
        }
        return r;
      });
      return m.summary;
    };

    // ---------- facet lists ----------
    // facetNames is the panel's facets: base ([dash] facets or the
    // defaults), then those added with More filters (extra), then any other
    // field the query has a clause on, so the whole query stays visible —
    // less skip (the field the when chart shows) and text.
    F.facetNames = function (reg, base, extra, q, skip) {
      var out = [];
      function add(n) { var f = reg.field(n); if (f && F.facetType(f) && n !== skip && out.indexOf(n) < 0) out.push(n); }
      base.forEach(add); extra.forEach(add);
      if (q) q.clauses.forEach(function (c) { add(c.name); });
      return out;
    };
    // effortLabels maps effort keys to their nodes' labels.
    function effortLabels(ix) {
      var m = memo(ix);
      if (!m.efforts) {
        m.efforts = new Map();
        ix.nodes.forEach(function (n, i) {
          if (ix.kinds[i] !== "effort") return;
          var k = n.effort && typeof n.effort.key === "string" && n.effort.key ? n.effort.key : ix.ids[i].replace(/^effort:/, "");
          m.efforts.set(k.toLowerCase(), n.label || k);
        });
      }
      return m.efforts;
    }
    F.effortLabel = function (ix, key) { return effortLabels(ix).get(String(key).toLowerCase()) || key; };
    // shadowed reports whether value key is a bare ID (all digits) that
    // selects exactly the nodes another value of the field does — a sprint's
    // ID beside its name — so the facet lists the name alone.
    function shadowed(fi, key) {
      if (!/^[0-9]+$/.test(key) || fi.f.type !== "set") return false;
      var bits = fi.values.get(key).bits, found = false;
      fi.values.forEach(function (e) {
        if (found || e.key === key || /^[0-9]+$/.test(e.key) || e.ghost !== fi.values.get(key).ghost) return;
        for (var i = 0; i < bits.length; i++) if (bits[i] !== e.bits[i]) return;
        found = true;
      });
      return found;
    }
    // valueRows lists a bars facet's rows in order: closed enums in their
    // order (short ones with every value, even at zero), people fields with
    // the configured people first by display name, everything else by
    // count; then unknown and, for sets, none (known, no value), when there
    // are any or the query names them. Each row says whether the query
    // chooses (sel) or excludes (exc) it.
    F.valueRows = function (ix, fi, counts, st) {
      var f = fi.f, byKey = new Map(), rows = [], zero = { ghost: 0, coupled: 0, part: 0 };
      counts.values.forEach(function (v) { byKey.set(v.key, v); });
      function row(key, disp, c, label) {
        return { key: key, disp: disp, label: label || disp, ghost: c.ghost, coupled: c.coupled, part: c.part, kind: "value",
          sel: st.pos.indexOf(key) >= 0, exc: st.neg.indexOf(key) >= 0 };
      }
      var closed = (f.values || []).filter(function (v) { return v !== "unknown"; }), keys = [], people = [];
      if (closed.length) {
        closed.forEach(function (k) { if (byKey.has(k) || closed.length <= 6 || st.pos.indexOf(k) >= 0 || st.neg.indexOf(k) >= 0) keys.push(k); });
      } else if (f.type === "bool") {
        keys = ["true", "false"].filter(function (k) { return byKey.has(k) || st.pos.indexOf(k) >= 0 || st.neg.indexOf(k) >= 0; });
      } else if (PEOPLE[fi.name] && ix.env.people.length) {
        people = ix.env.people.slice().sort(function (a, b) { return cmp((a.name || a.login).toLowerCase(), (b.name || b.login).toLowerCase()) || cmp(a.login, b.login); });
        keys = people.map(function (p) { return p.login.toLowerCase(); });
      }
      var rest = counts.values.filter(function (v) { return keys.indexOf(v.key) < 0 && !shadowed(fi, v.key); }).sort(function (a, b) { return b.ghost - a.ghost || cmp(a.key, b.key); });
      keys.forEach(function (k, i) {
        var c = byKey.get(k) || zero, p = people[i];
        rows.push(row(k, c.disp || (p ? p.login : k), c, p ? (p.name || p.login) + (p.me ? " (you)" : "") : null));
        if (p) rows[rows.length - 1].person = p.login;
      });
      rest.forEach(function (v) {
        // st.pos holds a macro as written: @me chooses the person marked me.
        var r = row(v.key, v.disp, v, fi.name === "effort" ? F.effortLabel(ix, v.disp) : null);
        rows.push(r);
      });
      // A value the query names always shows: at zero when the snapshot lacks it.
      st.pos.concat(st.neg).forEach(function (k) {
        if (k !== "unknown" && k.charAt(0) !== "@" && !rows.some(function (r) { return r.key === k; })) { var c = byKey.get(k) || zero; rows.push(row(k, c.disp || k, c)); }
      });
      var u = counts.unknown;
      if (u.ghost || st.pos.indexOf("unknown") >= 0 || st.neg.indexOf("unknown") >= 0)
        rows.push({ key: "\u0000unknown", disp: "unknown", label: "unknown", ghost: u.ghost, coupled: u.coupled, part: u.part, kind: "unknown",
          sel: st.pos.indexOf("unknown") >= 0, exc: st.neg.indexOf("unknown") >= 0 });
      var n = counts.none;
      if (f.type === "set" && (n.ghost || st.any || st.notAny))
        rows.push({ key: "\u0000none", disp: "none", label: "none", ghost: n.ghost, coupled: n.coupled, part: n.part, kind: "none", sel: st.notAny, exc: st.any });
      return rows;
    };
    // shownRows is the rows a facet lists: the first TOP values (of those
    // whose label or value holds find, when given), every value the query
    // names, then unknown and none; hidden counts the values left out.
    F.shownRows = function (rows, find, top) {
      var vals = rows.filter(function (r) { return r.kind === "value"; }), q = String(find || "").trim().toLowerCase();
      var pool = q ? vals.filter(function (r) { return r.label.toLowerCase().indexOf(q) >= 0 || r.key.indexOf(q) >= 0; }) : vals;
      var shown = pool.slice(0, top);
      vals.forEach(function (r) { if ((r.sel || r.exc) && shown.indexOf(r) < 0) shown.push(r); });
      return { rows: shown.concat(rows.filter(function (r) { return r.kind !== "value"; })), hidden: vals.length - shown.length, matched: pool.length };
    };

    return F;
  }

  if (typeof module !== "undefined" && module.exports) module.exports = logic;
  if (typeof window === "undefined" || !window.WGOLive || typeof document === "undefined") return;

  // ---------- page ----------
  var L = window.WGOLive, F = logic(L);
  // S is the addon's own state: a drag in progress (drag), a keyboard range
  // (kb), the type-ahead text per facet (find), the element to focus after
  // the next render (wantFocus), and each panel's latest ctx.
  var S = { drag: null, press: null, kb: null, skipClick: false, find: {}, wantFocus: null, ctx: {} };

  // M is what every panel draws from, once per snapshot, query and selection.
  var M = null;
  function model(ctx) {
    var ix = ctx.queryIndex;
    if (!ix) return null;
    var expr = ctx.getState("q"), sel = ctx.selection;
    if (M && M.ix === ix && M.expr === expr && M.selRef === sel && M.snap === ctx.snapshot) return M;
    var qm = F.queryMasks(ix, expr);
    M = { ix: ix, expr: expr, selRef: sel, snap: ctx.snapshot, qm: qm, q: qm.q, others: new Map(), target: F.timeTarget(qm.q),
      sel: sel && sel.size ? F.fromIds(ix, Array.from(sel)) : null };
    return M;
  }
  function othersFor(m, name) {
    var o = m.others.get(name);
    if (!o) m.others.set(name, o = F.others(m.qm, name));
    return o;
  }
  function apply(ctx, expr, commit) { return ctx.setQuery(expr, { commit: commit !== false, show: commit !== false }); }
  function modeOf(ev) { return ev.altKey ? "exclude" : ev.shiftKey || ev.metaKey || ev.ctrlKey ? "or" : "only"; }
  function plural(n, s) { return n.toLocaleString() + " " + s + (n === 1 ? "" : "s"); }
  function pct(n, max) { return (max > 0 ? Math.min(100, 100 * n / max) : 0) + "%"; }
  var HELP = "Click: only this · Shift- or ⌘-click: add or remove · Alt-click: exclude";

  // Focus survives a re-render: focusable marks carry data-fk, and the one
  // focused before is focused again after — or the one a gesture asked for
  // (want), whichever panel draws it within the next moment.
  function want(fk) { S.wantFocus = fk == null ? null : { fk: fk, t: Date.now() }; }
  function focusKey(box) {
    var a = document.activeElement;
    return a && box.contains(a) && a !== box ? a.getAttribute("data-fk") : null;
  }
  function focusIn(box, fk) {
    var all = box.querySelectorAll("[data-fk]");
    for (var i = 0; i < all.length; i++) {
      if (all[i].getAttribute("data-fk") !== fk) continue;
      var g = all[i].getAttribute("data-group");
      if (g) Array.prototype.forEach.call(box.querySelectorAll("[data-group]"), function (x) { if (x.getAttribute("data-group") === g) x.setAttribute("tabindex", "-1"); });
      if (all[i].getAttribute("tabindex") !== null || g) all[i].setAttribute("tabindex", "0");
      all[i].focus();
      return true;
    }
    return false;
  }
  function refocus(box, fk) {
    var w = S.wantFocus;
    if (w && Date.now() - w.t < 1000 && focusIn(box, w.fk)) { S.wantFocus = null; return; }
    if (fk != null) focusIn(box, fk);
  }
  // roving moves focus within items (one tab stop per facet) for the
  // arrow, Home and End keys; it reports whether it handled the key.
  function roving(items, i, ev, vertical) {
    var next = -1, back = vertical ? "ArrowUp" : "ArrowLeft", fwd = vertical ? "ArrowDown" : "ArrowRight";
    if (ev.key === back || (!vertical && ev.key === "ArrowUp") || (vertical && ev.key === "ArrowLeft")) next = Math.max(0, i - 1);
    else if (ev.key === fwd || (!vertical && ev.key === "ArrowDown") || (vertical && ev.key === "ArrowRight")) next = Math.min(items.length - 1, i + 1);
    else if (ev.key === "Home") next = 0;
    else if (ev.key === "End") next = items.length - 1;
    if (next < 0) return false;
    ev.preventDefault();
    items[i].setAttribute("tabindex", "-1"); items[next].setAttribute("tabindex", "0"); items[next].focus();
    return true;
  }
  function firstTabStop(items, isOn) {
    var k = items.findIndex(isOn);
    if (items.length) items[k < 0 ? 0 : k].setAttribute("tabindex", "0");
  }
  // keyMode maps Enter or Space, with Shift or Alt, to a gesture mode.
  function keyMode(ev) { return ev.key === "Enter" || ev.key === " " ? (ev.altKey ? "exclude" : ev.shiftKey ? "or" : "only") : ""; }

  // ---------- pointer drags ----------
  // A press on a brushable chart or a handle becomes a drag once it moves:
  // window listeners follow it (so re-rendering never loses it), each
  // change applies live, and the release commits. A press that never moved
  // is left to the click handler, which a drag's own click skips.
  function press(ev, spec) {
    if (ev.button !== 0) return;
    S.press = { x: ev.clientX, spec: spec, moved: false };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onUp);
  }
  function onMove(ev) {
    var p = S.press;
    if (!p) return;
    if (!p.moved && Math.abs(ev.clientX - p.x) < 4) return;
    if (!p.moved) { p.moved = true; S.drag = p.spec; p.spec.start(ev); }
    p.spec.move(ev);
  }
  function onUp() {
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", onUp);
    window.removeEventListener("pointercancel", onUp);
    var p = S.press; S.press = null;
    if (!p || !p.moved) return;
    S.skipClick = true; setTimeout(function () { S.skipClick = false; }, 0);
    var spec = S.drag; S.drag = null;
    spec.end();
  }

  // ---------- bars: enum, set and bool facets ----------
  function drawBars(ctx, box, m, name, fi) {
    var el = ctx.dom.el, st = F.fieldState(m.q, name), counts = F.facetCounts(fi, othersFor(m, name), m.sel);
    var rows = F.valueRows(m.ix, fi, counts, st), max = rows.reduce(function (a, r) { return Math.max(a, r.ghost); }, 0);
    var list = el("div", "fx-list", null, box);
    list.setAttribute("role", "group"); list.setAttribute("aria-label", fi.f.label + ": " + HELP);
    var find = null;
    function draw() {
      list.textContent = "";
      var shown = F.shownRows(rows, S.find[name], F.TOP), items = [];
      shown.rows.forEach(function (r) {
        var cls = "fx-row" + (r.sel ? " sel" : "") + (r.exc ? " exc" : "") + (r.kind !== "value" ? " " + r.kind : "") + (r.ghost === 0 ? " zero" : "");
        var row = el("div", cls, null, list);
        row.setAttribute("role", "button"); row.setAttribute("tabindex", "-1");
        row.setAttribute("data-fk", name + "\u0001" + r.key); row.setAttribute("data-group", name);
        row.setAttribute("aria-pressed", String(r.sel));
        row.setAttribute("aria-label", r.label + ": " + r.coupled + " under the other filters, " + r.ghost + " in the snapshot" +
          (r.part ? ", " + r.part + " selected" : "") + (r.sel ? "; chosen" : r.exc ? "; excluded" : ""));
        row.title = r.label + (r.person ? " (" + r.person + ")" : "") + "\n" + HELP;
        el("span", "fx-g", r.sel ? "✓" : r.exc ? "✕" : "", row).setAttribute("aria-hidden", "true");
        el("span", "fx-l", r.label, row);
        var bar = el("span", "fx-bar", null, row);
        bar.setAttribute("aria-hidden", "true");
        el("span", "fx-ghost", null, bar).style.width = pct(r.ghost, max);
        el("span", "fx-solid", null, bar).style.width = pct(r.coupled, max);
        if (r.part) el("span", "fx-part", null, bar).style.width = pct(r.part, max);
        var n = el("span", "fx-n", String(r.coupled), row);
        if (r.ghost !== r.coupled) el("span", "fx-of", "/" + r.ghost, n);
        function go(mode) {
          var expr = r.kind === "none" ? F.noneGesture(m.expr, m.ix.reg, name, mode) : F.gesture(m.expr, m.ix.reg, name, r.kind === "unknown" ? "unknown" : r.disp, mode);
          want(row.getAttribute("data-fk"));
          apply(ctx, expr);
        }
        row.addEventListener("click", function (ev) { go(modeOf(ev)); });
        row.addEventListener("keydown", function (ev) {
          var mode = keyMode(ev);
          if (mode) { ev.preventDefault(); go(mode); return; }
          roving(items, items.indexOf(row), ev, true);
        });
        items.push(row);
      });
      firstTabStop(items, function (x) { return x.getAttribute("aria-pressed") === "true"; });
      if (find) find.placeholder = shown.hidden > 0 || S.find[name] ? "Find among " + plural(rows.filter(function (r) { return r.kind === "value"; }).length, "value") + "…" : "";
    }
    var values = rows.filter(function (r) { return r.kind === "value"; }).length;
    if (values > F.TOP || S.find[name]) {
      // The type-ahead redraws only this list, so it keeps the keyboard.
      find = el("input", "fx-find", null, null);
      find.type = "search"; find.value = S.find[name] || ""; find.setAttribute("data-fk", name + "\u0001find");
      find.setAttribute("aria-label", "Find a value of " + fi.f.label);
      find.addEventListener("input", function () { S.find[name] = find.value; draw(); });
      box.insertBefore(find, list);
    }
    draw();
  }

  // ---------- histograms: number and duration facets ----------
  function drawHist(ctx, box, m, name, fi) {
    var el = ctx.dom.el, svgEl = ctx.dom.svgEl, st = F.fieldState(m.q, name), counts = F.facetCounts(fi, othersFor(m, name), m.sel);
    var B = fi.bins.length;
    var W = Math.max(180, (box.clientWidth || 236) - 4), H = 58, top = 11, base = H - 13;
    var slot = W / B, plotW = slot * B, bw = Math.max(2, Math.min(22, slot - 2));
    var all = counts.bins, max = all.reduce(function (a, c) { return Math.max(a, c.ghost); }, 0) || 1;
    var edges = F.rangeEdges(fi, st);
    var svg = svgEl("svg", { viewBox: "0 0 " + W + " " + H, width: W, height: H, role: "group", "class": "fx-hist",
      "aria-label": fi.f.label + " histogram" + (fi.f.scale === "log" ? " (log scale bins)" : "") + ": " + HELP }, box);
    var band = svgEl("rect", { "class": "fx-band", x: edges.a * slot, y: 0, width: (edges.b - edges.a) * slot, height: base, rx: 2 }, svg);
    band.style.display = edges.active ? "" : "none";
    svgEl("line", { x1: 0, x2: plotW, y1: base + 0.5, y2: base + 0.5, "class": "fx-axis" }, svg);
    var items = [];
    all.forEach(function (c, k) {
      var b = fi.bins[k], x = k * slot + (slot - bw) / 2;
      var sel = edges.active && k >= edges.a && k < edges.b, exc = F.binExcluded(fi, st, k), label = b.text;
      var g = svgEl("g", { "class": "hb" + (sel ? " sel" : "") + (exc ? " exc" : ""), tabindex: -1, role: "button", "aria-pressed": String(sel),
        "data-fk": name + "\u0001b" + k, "data-group": name + "\u0001bins",
        "aria-label": label + ": " + c.coupled + " under the other filters, " + c.ghost + " in the snapshot" + (c.part ? ", " + c.part + " selected" : "") + (sel ? "; in the range" : exc ? "; excluded" : "") }, svg);
      svgEl("title", {}, g).textContent = label + ": " + c.coupled + " of " + c.ghost + "\n" + HELP + " · drag the handles for a range";
      svgEl("rect", { "class": "hit", x: k * slot, y: 0, width: slot, height: H }, g);
      var gh = (base - top) * c.ghost / max, sh = (base - top) * c.coupled / max, ph = (base - top) * c.part / max;
      if (c.ghost) svgEl("rect", { "class": "fx-ghost", x: x, y: base - gh, width: bw, height: gh }, g);
      if (c.coupled) svgEl("rect", { "class": "fx-solid", x: x, y: base - sh, width: bw, height: sh }, g);
      if (c.part) svgEl("rect", { "class": "part", x: x + bw * 0.25, y: base - ph, width: bw * 0.5, height: ph }, g);
      if (slot >= 15 && c.coupled) svgEl("text", { x: x + bw / 2, y: base - sh - 2, "text-anchor": "middle", "class": "fx-cnt" }, g).textContent = String(c.coupled);
      if (slot >= 20 || k % 2 === 0) svgEl("text", { x: x + bw / 2, y: H - 2, "text-anchor": "middle", "class": "fx-tick" + (exc ? " exc" : "") }, g).textContent = (exc ? "✕" : "") + b.label;
      function go(mode) {
        var expr;
        if (mode === "exclude") expr = F.toggleText(m.expr, m.ix.reg, F.binExclusion(fi, k));
        else if (mode === "or" && edges.active) expr = F.setClauses(m.expr, m.ix.reg, name, F.edgeRange(fi, Math.min(edges.a, k), Math.max(edges.b, k + 1)));
        else if (sel && edges.a === k && edges.b === k + 1) expr = F.setClauses(m.expr, m.ix.reg, name, []);
        else expr = F.setClauses(m.expr, m.ix.reg, name, F.edgeRange(fi, k, k + 1));
        want(g.getAttribute("data-fk"));
        apply(ctx, expr);
      }
      g.addEventListener("click", function (ev) { if (!S.skipClick) go(modeOf(ev)); });
      g.addEventListener("keydown", function (ev) {
        var mode = keyMode(ev);
        if (mode) { ev.preventDefault(); go(mode); return; }
        if (ev.shiftKey && (ev.key === "ArrowLeft" || ev.key === "ArrowRight")) { ev.preventDefault(); kbRange(ctx, m, name, k, ev.key === "ArrowRight" ? 1 : -1, B, function (a, b) { return F.edgeRange(fi, a, b + 1); }, name + "\u0001b"); return; }
        roving(items, items.indexOf(g), ev, false);
      });
      items.push(g);
    });
    firstTabStop(items, function (x) { return x.getAttribute("aria-pressed") === "true"; });
    drawSlider(ctx, box, m, name, fi, edges, plotW, band, slot);
    unknownRow(ctx, box, m, name, counts.unknown, st);
  }
  // unknownRow is a histogram's unknown values as a row of text, with the
  // facet gestures (unknown:…): no bar, since the known values' scale must
  // not shrink under a mass of unknowns, and a bar off that scale would lie.
  function unknownRow(ctx, box, m, name, c, st) {
    var sel = st.pos.indexOf("unknown") >= 0, exc = st.neg.indexOf("unknown") >= 0, el = ctx.dom.el;
    if (!c.ghost && !sel && !exc) return;
    var row = el("div", "fx-row unknown fx-unk" + (sel ? " sel" : "") + (exc ? " exc" : ""), null, box);
    row.setAttribute("role", "button"); row.setAttribute("tabindex", "0"); row.setAttribute("aria-pressed", String(sel));
    row.setAttribute("data-fk", name + "\u0001unknown");
    row.setAttribute("aria-label", "unknown: " + c.coupled + " under the other filters, " + c.ghost + " in the snapshot" + (sel ? "; chosen" : exc ? "; excluded" : ""));
    row.title = HELP;
    el("span", "fx-g", sel ? "✓" : exc ? "✕" : "", row).setAttribute("aria-hidden", "true");
    el("span", "fx-l", "unknown", row);
    var n = el("span", "fx-n", String(c.coupled), row);
    if (c.ghost !== c.coupled) el("span", "fx-of", "/" + c.ghost, n);
    function go(mode) { want(row.getAttribute("data-fk")); apply(ctx, F.gesture(m.expr, m.ix.reg, name, "unknown", mode)); }
    row.addEventListener("click", function (ev) { go(modeOf(ev)); });
    row.addEventListener("keydown", function (ev) { var mode = keyMode(ev); if (mode) { ev.preventDefault(); go(mode); } });
  }
  // drawSlider is the two-handle range slider under a histogram, its edges
  // the bins' edges, with inputs for exact bounds.
  function drawSlider(ctx, box, m, name, fi, edges, plotW, band, slot) {
    var el = ctx.dom.el, B = fi.bins.length, st = F.fieldState(m.q, name);
    var sl = el("div", "fx-slider", null, box);
    sl.style.width = plotW + "px";
    el("div", "fx-track", null, sl);
    var fill = el("div", "fx-fill", null, sl), cur = { a: edges.a, b: edges.b };
    function edgeText(e, hi) {
      if (hi && e >= B) return "no upper bound";
      if (!hi && e <= 0) return "no lower bound";
      return (hi ? "below " : "from ") + fi.bins[e].lo + (fi.f.unit ? " " + fi.f.unit : "");
    }
    function place() {
      fill.style.left = pct(cur.a, B); fill.style.width = pct(cur.b - cur.a, B);
      hs[0].style.left = pct(cur.a, B); hs[1].style.left = pct(cur.b, B);
      hs[0].setAttribute("aria-valuenow", String(cur.a)); hs[0].setAttribute("aria-valuetext", edgeText(cur.a, false));
      hs[1].setAttribute("aria-valuenow", String(cur.b)); hs[1].setAttribute("aria-valuetext", edgeText(cur.b, true));
      band.style.display = cur.a > 0 || cur.b < B ? "" : "none";
      band.setAttribute("x", String(cur.a * slot)); band.setAttribute("width", String((cur.b - cur.a) * slot));
    }
    function exprFor() { return F.setClauses(m.expr, m.ix.reg, name, cur.a > 0 || cur.b < B ? F.edgeRange(fi, cur.a, cur.b) : []); }
    var hs = ["lo", "hi"].map(function (which, j) {
      var h = el("div", "fx-handle fx-handle-" + which, null, sl);
      h.setAttribute("role", "slider"); h.setAttribute("tabindex", "0");
      h.setAttribute("aria-label", fi.f.label + (j ? ": upper bound" : ": lower bound"));
      h.setAttribute("aria-valuemin", "0"); h.setAttribute("aria-valuemax", String(B));
      h.setAttribute("data-fk", name + "\u0001" + which);
      function set(e) {
        e = j ? Math.max(cur.a + 1, Math.min(B, e)) : Math.max(0, Math.min(cur.b - 1, e));
        if (e === (j ? cur.b : cur.a)) return false;
        if (j) cur.b = e; else cur.a = e;
        place();
        return true;
      }
      h.addEventListener("pointerdown", function (ev) {
        ev.preventDefault(); h.focus();
        press(ev, {
          panel: "facets", facet: name, section: box,
          start: function () {},
          move: function (e) { var r = sl.getBoundingClientRect(); if (set(Math.round((e.clientX - r.left) / r.width * B))) ctx.setQuery(exprFor(), {}); },
          end: function () { want(h.getAttribute("data-fk")); apply(ctx, exprFor()); rerender("facets"); }
        });
      });
      h.addEventListener("keydown", function (ev) {
        var d = { ArrowLeft: -1, ArrowDown: -1, ArrowRight: 1, ArrowUp: 1, PageDown: -5, PageUp: 5 }[ev.key], e;
        if (ev.key === "Home") e = 0; else if (ev.key === "End") e = B; else if (d) e = (j ? cur.b : cur.a) + d * (ev.shiftKey ? 5 : 1);
        if (e == null) return;
        ev.preventDefault();
        if (set(e)) { want(h.getAttribute("data-fk")); ctx.setQuery(exprFor(), {}); }
      });
      // Each key press is one history entry: its release commits it.
      h.addEventListener("keyup", function (ev) { if (/^(Arrow|Page|Home|End)/.test(ev.key)) ctx.setQuery(exprFor(), { commit: true }); });
      return h;
    });
    place();
    // Exact bounds: the range's own text, or empty for an open side.
    var row = el("div", "fx-inputs", null, box), r0 = F.numRange(fi, st);
    var unit = fi.f.unit || "";
    var ins = ["from", "to"].map(function (w, j) {
      el("span", null, j ? "to" : "from", row);
      var i = el("input", null, null, row);
      i.type = "text"; i.inputMode = "decimal"; i.size = 5; i.setAttribute("data-fk", name + "\u0001in" + j);
      i.setAttribute("aria-label", fi.f.label + (j ? ", at most" : ", at least") + (unit ? " (" + unit + ")" : ""));
      var b = j ? st.his : st.los;
      i.value = b.length === 1 ? b[0].text : r0 && (j ? r0.hi : r0.lo) != null ? numText(j ? r0.hi : r0.lo) : "";
      i.placeholder = j ? "max" : "min";
      i.addEventListener("keydown", function (ev) { if (ev.key === "Enter") { ev.preventDefault(); exact(); } });
      i.addEventListener("change", exact);
      return i;
    });
    if (unit) el("span", "fx-unit", unit, row);
    var last = null;
    function exact() {
      var lo = ins[0].value.trim(), hi = ins[1].value.trim(), key = lo + "\u0001" + hi;
      if (key === last) return;
      last = key;
      want(document.activeElement && document.activeElement.getAttribute("data-fk"));
      apply(ctx, F.setClauses(m.expr, m.ix.reg, name, lo || hi ? [name + ":" + lo + ".." + hi] : []));
    }
  }
  function numText(x) { return String(Math.round(x * 100) / 100); } // an exact bound as the inputs show it

  // kbRange extends a range by keyboard (Shift+arrows) from the bar where
  // it started: live while Shift is held, committed when it is released.
  function kbRange(ctx, m, key, k, d, n, texts, fkPrefix) {
    if (!S.kb || S.kb.key !== key) S.kb = { key: key, anchor: k, at: k, ctx: ctx };
    S.kb.at = Math.max(0, Math.min(n - 1, S.kb.at + d));
    var a = Math.min(S.kb.anchor, S.kb.at), b = Math.max(S.kb.anchor, S.kb.at);
    want(fkPrefix + S.kb.at);
    S.kb.expr = F.setClauses(m.expr, m.ix.reg, key === "\u0001when" ? m.target : key, texts(a, b));
    ctx.setQuery(S.kb.expr, {});
  }
  document.addEventListener("keyup", function (ev) {
    if (ev.key !== "Shift" || !S.kb) return;
    var kb = S.kb; S.kb = null;
    want(document.activeElement && document.activeElement.getAttribute("data-fk"));
    kb.ctx.setQuery(kb.expr, { commit: true });
  });

  // ---------- day histograms: the when chart and date facets ----------
  // drawDays draws a per-day histogram over the window, with earlier and
  // later bins for days outside it and an unknown bin, a brush over the
  // days, and the facet gestures on each day. field is what it counts
  // (when, merged, or a date facet); opts.H is its height.
  function drawDays(ctx, box, m, field, fi, opts) {
    var svgEl = ctx.dom.svgEl, st = F.fieldState(m.q, field), counts = F.facetCounts(fi, othersFor(m, field), m.sel);
    var days = fi.days, nd = days.length, states = F.dayStates(m.ix, fi, st), r = F.dayRange(m.ix, st);
    var slots = [];
    if (counts.earlier && (counts.earlier.ghost || (r && r.lo == null))) slots.push({ kind: "earlier", c: counts.earlier });
    days.forEach(function (d, k) { slots.push({ kind: "day", day: d, k: k, c: counts.bins[k], sel: states[k].sel, exc: states[k].exc }); });
    if (counts.later && counts.later.ghost) slots.push({ kind: "later", c: counts.later });
    var off = slots.length && slots[0].kind === "earlier" ? 1 : 0;
    var W = Math.max(160, Math.round(box.clientWidth || 300)), H = opts.H, top = 12, base = H - 16;
    var slot = W / Math.max(slots.length, 1), bw = Math.max(2, Math.min(28, slot - 2));
    var max = slots.reduce(function (a, s) { return Math.max(a, s.c.ghost); }, 0) || 1;
    var svg = svgEl("svg", { viewBox: "0 0 " + W + " " + H, width: W, height: H, role: "group", "class": "fx-days", "aria-label": opts.aria }, box);
    var band = svgEl("rect", { "class": "fx-band", y: 0, height: base, rx: 2 }, svg);
    function showBand(a, b) {
      // a and b are day indexes; -1 is the earlier bin, nd the later one.
      if (a == null) { band.style.display = "none"; return; }
      band.style.display = "";
      band.setAttribute("x", String((a + off) * slot)); band.setAttribute("width", String((b - a + 1) * slot));
    }
    var rA = null, rB = null, later = slots.some(function (s) { return s.kind === "later"; });
    if (st.range && r) {
      rA = r.lo == null || r.lo < days[0] ? (off ? -1 : 0) : r.lo > days[nd - 1] ? nd : days.indexOf(r.lo);
      rB = r.hi == null ? nd - 1 : r.hi > days[nd - 1] ? (later ? nd : nd - 1) : r.hi < days[0] ? -1 : days.indexOf(r.hi);
      if (rB < rA) rA = rB = null;
    }
    showBand(rA, rB);
    svgEl("line", { x1: 0, x2: W, y1: base + 0.5, y2: base + 0.5, "class": "fx-axis" }, svg);
    var items = [];
    slots.forEach(function (s, i) {
      var c = s.c, x = i * slot + (slot - bw) / 2, d = s.day;
      var name = s.kind === "day" ? d : s.kind === "earlier" ? "before " + days[0] : "after " + days[nd - 1];
      var g = svgEl("g", { "class": "mbar fx-d" + (s.sel ? " on sel" : "") + (s.exc ? " exc" : ""), tabindex: -1, role: "button", "aria-pressed": String(!!s.sel),
        "data-fk": field + "\u0001d" + (s.kind === "day" ? s.k : s.kind), "data-group": field + "\u0001days",
        "aria-label": name + ": " + c.coupled + " under the other filters, " + c.ghost + " in the snapshot" + (c.part ? ", " + c.part + " selected" : "") + (s.sel ? "; chosen" : s.exc ? "; excluded" : "") }, svg);
      svgEl("title", {}, g).textContent = name + ": " + c.coupled + " of " + c.ghost + "\n" + HELP + " · drag across days for a range";
      svgEl("rect", { "class": "hit", x: i * slot, y: 0, width: slot, height: H }, g);
      var gh = (base - top) * c.ghost / max, sh = (base - top) * c.coupled / max, ph = (base - top) * c.part / max;
      if (c.ghost) svgEl("rect", { "class": "fx-ghost", x: x, y: base - gh, width: bw, height: gh, rx: Math.min(2, bw / 4) }, g);
      if (c.coupled) svgEl("rect", { "class": "fx-solid", x: x, y: base - sh, width: bw, height: sh, rx: Math.min(2, bw / 4) }, g);
      if (c.part) svgEl("rect", { "class": "part", x: x + bw * 0.25, y: base - ph, width: bw * 0.5, height: ph }, g);
      // Day labels every week back from the last day; the first starts at
      // its bar and the last ends at its, so neither runs into a neighbour.
      var lab = s.kind === "day" ? (s.k === 0 || s.k === nd - 1 || ((nd - 1 - s.k) % 7 === 0 && s.k > 3) ? d.slice(5) : "") : s.kind === "earlier" ? "<" : ">";
      var glyph = s.exc ? "✕" : s.sel && !st.range ? "✓" : "", anchor = s.kind === "day" && s.k === 0 ? "start" : s.kind === "day" && s.k === nd - 1 ? "end" : "middle";
      if (glyph || lab) svgEl("text", { x: anchor === "start" ? i * slot : anchor === "end" ? (i + 1) * slot : i * slot + slot / 2, y: H - 3, "text-anchor": anchor, "class": "fx-tick" + (s.exc ? " exc" : "") }, g).textContent = glyph + lab;
      function go(mode) {
        var e;
        if (s.kind === "day") e = F.gesture(m.expr, m.ix.reg, field, d, mode);
        else {
          var t = s.kind === "earlier" ? field + ":.." + F.addDays(days[0], -1) : field + ":" + F.addDays(days[nd - 1], 1) + "..";
          e = mode === "exclude" ? F.toggleText(m.expr, m.ix.reg, "!" + t) : F.setClauses(m.expr, m.ix.reg, field, [t]);
        }
        // Shift-click on a day with a range active stretches the range to it.
        if (mode === "or" && s.kind === "day" && st.range && r) {
          var lo = r.lo == null ? null : d < r.lo ? d : r.lo, hi = r.hi == null ? null : d > r.hi ? d : r.hi;
          e = F.setClauses(m.expr, m.ix.reg, field, F.dayTexts(field, lo, hi));
        }
        want(g.getAttribute("data-fk"));
        apply(ctx, e);
      }
      g.addEventListener("click", function (ev) { if (!S.skipClick) go(modeOf(ev)); });
      g.addEventListener("keydown", function (ev) {
        var mode = keyMode(ev);
        if (mode) { ev.preventDefault(); go(mode); return; }
        if (ev.shiftKey && s.kind === "day" && (ev.key === "ArrowLeft" || ev.key === "ArrowRight")) {
          ev.preventDefault();
          kbRange(ctx, m, opts.kbKey || field, s.k, ev.key === "ArrowRight" ? 1 : -1, nd, function (a, b) { return F.dayTexts(field, days[a], days[b]); }, field + "\u0001d");
          return;
        }
        roving(items, items.indexOf(g), ev, false);
      });
      items.push(g);
    });
    firstTabStop(items, function (x) { return x.getAttribute("aria-pressed") === "true"; });
    // The brush: a press on the chart that moves chooses the days between.
    function dayAt(clientX) {
      var rect = svg.getBoundingClientRect(), i = Math.floor((clientX - rect.left) / rect.width * slots.length);
      i = Math.max(0, Math.min(slots.length - 1, i));
      var s = slots[i];
      return s.kind === "day" ? s.k : s.kind === "earlier" ? -1 : nd;
    }
    svg.addEventListener("pointerdown", function (ev) {
      var a = dayAt(ev.clientX), b = a;
      press(ev, {
        panel: opts.panel, facet: field, section: box,
        start: function () {},
        move: function (e) {
          var nb = dayAt(e.clientX);
          if (nb === b) return;
          b = nb;
          var lo = Math.min(a, b), hi = Math.max(a, b);
          showBand(lo, Math.min(hi, nd - 1));
          ctx.setQuery(F.setClauses(m.expr, m.ix.reg, field, F.dayTexts(field, lo < 0 ? null : days[lo], hi >= nd ? null : days[hi])), {});
        },
        end: function () {
          var lo = Math.min(a, b), hi = Math.max(a, b);
          apply(ctx, F.setClauses(m.expr, m.ix.reg, field, F.dayTexts(field, lo < 0 ? null : days[lo], hi >= nd ? null : days[hi])));
          rerender(opts.panel);
        }
      });
    });
    unknownRow(ctx, box, m, field, counts.unknown, st);
    return { range: st.range && r ? r : null };
  }

  // rerender draws a panel again with its latest ctx (after a drag, whose
  // committed query may equal the last live one, so nothing re-renders it).
  function rerender(id) { var c = S.ctx[id]; if (c && PANELS[id]) PANELS[id](c); }

  // ---------- the facet panel (requirements 1 to 7, 10) ----------
  function facetSection(ctx, m, name, extra) {
    var el = ctx.dom.el, fi = F.fieldIndex(m.ix, name), st = F.fieldState(m.q, name);
    if (!fi.known && !st.active) return null; // no known value in the snapshot: hidden
    var sec = el("section", "fx" + (st.active ? " active" : ""), null, null);
    sec.setAttribute("data-facet", name);
    var head = el("div", "fx-hd", null, sec);
    var h = el("h3", "fx-t", fi.f.label, head);
    if (name === "reviewer") h.title = "Review load: how the reviewers' PRs spread under the current filter";
    // Log-spaced bins are labelled so; bar lengths stay linear from zero.
    if (fi.type === "hist" && fi.f.scale === "log") el("span", "fx-scale", "log scale", head);
    if (st.active) {
      ctx.dom.button(head, "Clear", "fx-clear", function () {
        var q = L.parseQuery(m.expr, m.ix.reg);
        want("\u0001more");
        apply(ctx, q.error ? "" : q.clauses.filter(function (c) { return c.name !== name; }).map(function (c) { return c.raw; }).join(";"));
      }, "Clear the " + fi.f.label + " filter");
    }
    if (extra) {
      ctx.dom.button(head, "✕", "fx-x", function () {
        var ex = extraFacets(ctx).filter(function (x) { return x !== name; });
        ctx.setState({ facets: ex.join(",") }, { commit: true });
      }, "Remove the " + fi.f.label + " facet");
    }
    if (st.active) el("code", "fx-q", st.clauses.map(function (c) { return c.raw; }).join(";"), sec);
    if (fi.type === "bars") drawBars(ctx, sec, m, name, fi);
    else if (fi.type === "hist") drawHist(ctx, sec, m, name, fi);
    else drawDays(ctx, sec, m, name, fi, { H: 54, panel: "facets", aria: fi.f.label + " per day: " + HELP + "; drag across days for a range" });
    return sec;
  }
  function extraFacets(ctx) { return String(ctx.getState("facets") || "").split(",").map(function (s) { return s.trim(); }).filter(Boolean); }
  function renderFacets(ctx) {
    S.ctx.facets = ctx;
    var box = ctx.container, el = ctx.dom.el, m = model(ctx);
    box.classList.add("fx-panel");
    var fk = focusKey(box);
    var keep = S.drag && S.drag.panel === "facets" ? S.drag.section : null;
    var parts = [];
    var head = el("div", "fx-top", null, null);
    parts.push(head);
    el("h2", null, "Filters", head);
    if (!m) { el("p", "empty", "Waiting for data…", head); return place(box, parts, keep, fk); }
    var res = ctx.query, total = m.ix.nodes.length;
    el("span", "fx-sum", res ? plural(res.rows.length, "row") + " of " + total.toLocaleString() : plural(total, "node") + ", no filter", head);
    var clear = ctx.dom.button(head, "Clear all", "fx-clearall", function () { want("\u0001more"); apply(ctx, ""); }, "Clear every filter");
    clear.disabled = !m.expr;
    parts.push(el("p", "fx-help", HELP + "; drag a range. Grey bars: the whole snapshot; dark: what the other filters leave.", null));
    ((ctx.boot && ctx.boot.facet_notes) || []).forEach(function (n) { parts.push(el("p", "fx-note", n, null)); });
    var base = (ctx.boot && ctx.boot.facets && ctx.boot.facets.length ? ctx.boot.facets : F.DEFAULT_FACETS), extra = extraFacets(ctx);
    var names = F.facetNames(m.ix.reg, base, extra, m.q, m.target);
    names.forEach(function (name) {
      if (keep && S.drag.facet === name) { parts.push(keep); return; }
      var sec = facetSection(ctx, m, name, extra.indexOf(name) >= 0 && base.indexOf(name) < 0);
      if (sec) parts.push(sec);
    });
    // More filters: any other field, added to the URL's facets= key.
    var more = el("select", "fx-more", null, null);
    more.setAttribute("aria-label", "More filters: add a facet for another field"); more.setAttribute("data-fk", "\u0001more");
    el("option", null, "More filters…", more).value = "";
    m.ix.reg.fields.forEach(function (f) {
      if (!F.facetType(f) || names.indexOf(f.name) >= 0 || f.name === m.target) return;
      el("option", null, f.label + " (" + f.name + ")", more).value = f.name;
    });
    more.addEventListener("change", function () {
      if (!more.value) return;
      want(more.value + "\u0001find");
      ctx.setState({ facets: extra.concat([more.value]).join(",") }, { commit: true });
    });
    parts.push(more);
    place(box, parts, keep, fk);
  }
  // place puts parts in box in order, keeping keep (a facet being dragged)
  // where it is, and focuses fk again.
  function place(box, parts, keep, fk) {
    Array.prototype.slice.call(box.childNodes).forEach(function (n) { if (n !== keep) box.removeChild(n); });
    var after = false;
    parts.forEach(function (p) {
      if (p === keep) { after = true; return; }
      if (keep && !after) box.insertBefore(p, keep); else box.appendChild(p);
    });
    refocus(box, fk);
  }

  // ---------- the attention summary (requirement 11) ----------
  function renderAttention(ctx) {
    S.ctx.attention = ctx;
    var box = ctx.container, el = ctx.dom.el, m = model(ctx), fk = focusKey(box);
    box.classList.add("fx-att");
    box.textContent = "";
    el("h2", null, "Attention", box);
    if (!m) { el("p", "empty", "Waiting for data…", box); return; }
    var ul = el("ul", "att-list", null, box);
    F.summary(m.ix, ctx.boot && ctx.boot.filters).forEach(function (s) {
      var li = el("li", null, null, ul), on = !!s.expr && s.expr === m.expr;
      var a = el("a", "att-link" + (s.accent ? " mine" : "") + (on ? " on" : ""), null, li);
      a.setAttribute("data-fk", "\u0001att" + s.name);
      if (s.expr) {
        a.href = L.buildHash(L.parseHash(location.hash), { view: ctx.getState("view"), q: s.expr, sel: ctx.getState("sel"), values: {} });
        a.addEventListener("click", function (ev) {
          if (ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return; // a new tab or window keeps the link
          ev.preventDefault();
          want(a.getAttribute("data-fk"));
          apply(ctx, s.expr);
        });
      }
      if (on) a.setAttribute("aria-current", "true");
      el("span", "att-n", s.count == null ? "?" : s.count.toLocaleString(), a);
      el("span", "att-l", (on ? "✓ " : "") + s.label, a);
      a.title = (s.count == null ? "Unknown: " + s.why : plural(s.count, "row") + " of the saved filter " + s.name) + (s.expr ? "\n" + s.expr : "");
    });
    var p = F.period(m.ix, m.expr, m.snap);
    if (p) { var pe = el("p", "att-period", F.periodText(p), box); pe.title = F.periodTitle(p); }
    refocus(box, fk);
  }

  // ---------- overview charts ----------
  function chartHead(ctx, box, title, sub) {
    var h = ctx.dom.el("h2", null, title, box);
    if (sub) ctx.dom.el("span", "sub", " " + sub, h);
  }
  // renderWhen is the brushable when chart (requirements 4 and 8): items
  // per day by when, or by merge day while the query is shipped-like.
  function renderWhen(ctx) {
    S.ctx.activity = ctx;
    var box = ctx.container, m = model(ctx);
    if (S.drag && S.drag.panel === "activity") return; // the brush draws itself until released
    var fk = focusKey(box);
    box.classList.add("fx-chart");
    box.textContent = "";
    if (!m) { chartHead(ctx, box, "When"); ctx.dom.el("p", "empty", "Waiting for data…", box); return; }
    var field = m.target, fi = F.fieldIndex(m.ix, field), merged = field === "merged";
    var st = F.fieldState(m.q, field), r = F.dayRange(m.ix, st);
    var sub = merged ? "PRs by merge day" : "activity, merges, updates";
    chartHead(ctx, box, merged ? "Merged per day" : "When", "(" + sub + (r ? " · " + (r.lo || "…") + " to " + (r.hi || m.ix.today) : " · drag for a range") + ")");
    box.title = merged ? "Merged PRs per day of merge. Drag across days to set merged:a..b." :
      "Items per day: efforts and workspaces by local activity; PRs by merge, close or else update; tickets by resolution or else update. Drag across days to set when:a..b.";
    if (!fi.days.length) { ctx.dom.el("p", "empty", "No days: the snapshot has no generation time.", box); return; }
    drawDays(ctx, box, m, field, fi, { H: 92, panel: "activity", kbKey: "\u0001when",
      aria: (merged ? "Merged PRs per day" : "Items per day by when") + ". " + HELP + "; drag across days to set " + field + ":a..b." });
    refocus(box, fk);
  }
  // barsChart draws a vertical bar chart of a facet's values (PRs by state,
  // agent sessions by effort) with ghost, coupled and selection bars.
  function barsChart(ctx, box, m, name, rows, aria) {
    var svgEl = ctx.dom.svgEl, W = Math.max(160, Math.round(box.clientWidth || 300)), H = 92, top = 12, base = H - 16, n = rows.length;
    var svg = svgEl("svg", { viewBox: "0 0 " + W + " " + H, width: W, height: H, role: "group", "aria-label": aria }, box);
    var max = rows.reduce(function (a, r) { return Math.max(a, r.ghost); }, 0) || 1, slot = W / Math.max(n, 1), bw = Math.max(2, Math.min(28, slot - 2));
    svgEl("line", { x1: 0, x2: W, y1: base + 0.5, y2: base + 0.5, "class": "fx-axis" }, svg);
    var items = [];
    rows.forEach(function (r, i) {
      var x = i * slot + (slot - bw) / 2;
      var g = svgEl("g", { "class": "mbar" + (r.sel ? " on sel" : "") + (r.exc ? " exc" : ""), tabindex: -1, role: "button", "aria-pressed": String(r.sel),
        "data-fk": name + "\u0001" + r.key, "data-group": name + "\u0001chart",
        "aria-label": r.label + ": " + r.coupled + " under the other filters, " + r.ghost + " in the snapshot" + (r.part ? ", " + r.part + " selected" : "") + (r.sel ? "; chosen" : r.exc ? "; excluded" : "") }, svg);
      svgEl("title", {}, g).textContent = (r.title || r.label) + ": " + r.coupled + " of " + r.ghost + "\n" + HELP;
      svgEl("rect", { "class": "hit", x: i * slot, y: 0, width: slot, height: H }, g);
      var gh = (base - top) * r.ghost / max, sh = (base - top) * r.coupled / max, ph = (base - top) * r.part / max;
      if (r.ghost) svgEl("rect", { "class": "fx-ghost", x: x, y: base - gh, width: bw, height: gh, rx: Math.min(2, bw / 4) }, g);
      if (r.coupled) svgEl("rect", { "class": "fx-solid", x: x, y: base - sh, width: bw, height: sh, rx: Math.min(2, bw / 4) }, g);
      if (r.part) svgEl("rect", { "class": "part", x: x + bw * 0.25, y: base - ph, width: bw * 0.5, height: ph }, g);
      if (r.ghost) svgEl("text", { x: x + bw / 2, y: base - gh - 2, "text-anchor": "middle", "class": "fx-cnt" }, g).textContent = r.coupled + (r.coupled !== r.ghost ? "/" + r.ghost : "");
      var lab = r.short || r.label;
      svgEl("text", { x: i * slot + slot / 2, y: H - 3, "text-anchor": "middle", "class": "fx-tick" + (r.exc ? " exc" : "") }, g).textContent = (r.sel ? "✓" : r.exc ? "✕" : "") + lab;
      function go(mode) {
        want(g.getAttribute("data-fk"));
        apply(ctx, r.kind === "unknown" ? F.gesture(m.expr, m.ix.reg, name, "unknown", mode) : F.gesture(m.expr, m.ix.reg, name, r.disp, mode));
      }
      g.addEventListener("click", function (ev) { go(modeOf(ev)); });
      g.addEventListener("keydown", function (ev) {
        var mode = keyMode(ev);
        if (mode) { ev.preventDefault(); go(mode); return; }
        roving(items, items.indexOf(g), ev, false);
      });
      items.push(g);
    });
    firstTabStop(items, function (x) { return x.getAttribute("aria-pressed") === "true"; });
  }
  // renderPRState is PRs by state on the prstate facet's counts, with the
  // previous period while a time range is active (requirement 9).
  function renderPRState(ctx) {
    S.ctx.prstate = ctx;
    var box = ctx.container, m = model(ctx), fk = focusKey(box);
    box.classList.add("fx-chart");
    box.textContent = "";
    chartHead(ctx, box, "PRs by state", "(bookmarks without a known PR lookup count as unknown, not none)");
    if (!m) { ctx.dom.el("p", "empty", "Waiting for data…", box); return; }
    var fi = F.fieldIndex(m.ix, "prstate"), st = F.fieldState(m.q, "prstate");
    var rows = F.valueRows(m.ix, fi, F.facetCounts(fi, othersFor(m, "prstate"), m.sel), st);
    barsChart(ctx, box, m, "prstate", rows, "PRs by state. " + HELP + ".");
    var p = F.period(m.ix, m.expr, m.snap);
    if (p) { var pe = ctx.dom.el("p", "fx-period", F.periodText(p), box); pe.title = F.periodTitle(p); }
    refocus(box, fk);
  }
  // renderAgents is agent sessions by effort, shown only when there are any.
  function renderAgents(ctx) {
    S.ctx.agents = ctx;
    var box = ctx.container, m = model(ctx), fk = focusKey(box);
    box.classList.add("fx-chart");
    box.textContent = "";
    var kinds = m && F.fieldIndex(m.ix, "kind"), agents = kinds && kinds.values.get("agent");
    box.hidden = !!m && !agents;
    if (!m || !agents) { if (!m) { chartHead(ctx, box, "Agent sessions by effort"); ctx.dom.el("p", "empty", "Waiting for data…", box); } return; }
    var fi = F.fieldIndex(m.ix, "effort"), others = othersFor(m, "effort"), st = F.fieldState(m.q, "effort");
    var rows = [];
    fi.values.forEach(function (e) {
      var ghost = F.count(e.bits, agents.bits);
      if (!ghost) return;
      var label = F.effortLabel(m.ix, e.disp);
      rows.push({ key: e.key, disp: e.disp, label: label, short: label.length > 10 ? label.slice(0, 9) + "…" : label, title: label,
        ghost: ghost, coupled: F.count(e.bits, agents.bits, others), part: m.sel ? F.count(e.bits, agents.bits, m.sel) : 0,
        sel: st.pos.indexOf(e.key) >= 0, exc: st.neg.indexOf(e.key) >= 0, kind: "value" });
    });
    rows.sort(function (a, b) { return b.ghost - a.ghost || (a.label < b.label ? -1 : a.label > b.label ? 1 : 0); });
    chartHead(ctx, box, "Agent sessions by effort");
    barsChart(ctx, box, m, "effort", rows.slice(0, 12), "Agent sessions per effort. " + HELP + ".");
    refocus(box, fk);
  }

  var PANELS = { facets: renderFacets, attention: renderAttention, activity: renderWhen, prstate: renderPRState, agents: renderAgents };
  // The attention summary leads the overview; the when chart, PRs by state
  // and agent sessions replace the built-in charts in place; the facet
  // panel goes left of the views.
  L.registerPanel({ id: "attention", label: "Attention", before: "activity", render: renderAttention });
  L.registerPanel({ id: "activity", label: "When", render: renderWhen });
  L.registerPanel({ id: "prstate", label: "PRs by state", render: renderPRState });
  L.registerPanel({ id: "agents", label: "Agent sessions by effort", render: renderAgents });
  window.WGOLive.registerPanel({ id: "facets", label: "Filters", slot: "left", render: renderFacets });
})();
