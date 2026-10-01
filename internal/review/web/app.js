/* wgo review graph explorer. Offline, no eval, no innerHTML: all data goes through textContent. */
(function () {
  "use strict";
  var root = document.getElementById("app");
  var dataEl = document.getElementById("graph-data");
  if (!root || !dataEl) return;
  var DATA;
  try { DATA = JSON.parse(dataEl.textContent); } catch (e) { root.textContent = "Could not parse graph data: " + e.message; return; }
  var d3 = window.d3force;
  var reduceMotion = window.matchMedia && matchMedia("(prefers-reduced-motion: reduce)").matches;

  // ---------- helpers ----------
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
  function link(parent, text, url) {
    var u = safeURL(url);
    if (!u) { parent.appendChild(document.createTextNode(text)); return null; }
    var a = el("a", null, text, parent);
    a.href = u; a.target = "_blank"; a.rel = "noopener noreferrer";
    return a;
  }
  function clamp(v, lo, hi) { return Math.max(lo, Math.min(hi, v)); }
  function plural(n, s) { return n.toLocaleString() + " " + s + (n === 1 ? "" : "s"); }

  // ---------- model ----------
  var NODES = DATA.nodes || [], EDGES = DATA.edges || [];
  var byId = new Map();
  NODES.forEach(function (n) { byId.set(n.id, n); });
  var fullAdj = new Map(); // id -> [{o: otherId, kind, w, out}]
  function addAdj(a, b, kind, w, out) {
    var l = fullAdj.get(a); if (!l) fullAdj.set(a, l = []);
    l.push({ o: b, kind: kind, w: w, out: out });
  }
  EDGES.forEach(function (e) {
    if (!byId.has(e.source) || !byId.has(e.target)) return;
    addAdj(e.source, e.target, e.kind, e.weight || 1, true);
    addAdj(e.target, e.source, e.kind, e.weight || 1, false);
  });
  var counts = { person: 0, pr: 0, ticket: 0, repo: 0, bot: 0 };
  NODES.forEach(function (n) { counts[n.kind] = (counts[n.kind] || 0) + 1; if (n.bot) counts.bot++; });
  var meNode = byId.get(DATA.me && "person:" + DATA.me) || NODES.filter(function (n) { return n.me; })[0] || null;

  // ---------- store (shared selection/filter state; views subscribe) ----------
  var store = {
    selection: new Set(),
    hover: null,
    opts: { collapse: true, table: false, edgeKinds: {} },
    hidden: {}, // name -> predicate(node) => true to hide. Later PRs add facet filters here.
    subs: [],
    subscribe: function (fn) { this.subs.push(fn); },
    notify: function (reason) { for (var i = 0; i < this.subs.length; i++) this.subs[i](reason); },
    isHidden: function (n) { for (var k in this.hidden) if (this.hidden[k](n)) return true; return false; },
    setHidden: function (name, fn) { if (fn) this.hidden[name] = fn; else delete this.hidden[name]; this.notify("structure"); },
    select: function (ids, add) {
      if (!add) this.selection.clear();
      ids.forEach(function (id) { if (add && store.selection.has(id)) store.selection.delete(id); else store.selection.add(id); });
      this.notify("selection");
    },
    clear: function () { if (this.selection.size) { this.selection.clear(); this.notify("selection"); } }
  };
  store.hidden.bots = function (n) { return !!n.bot; };

  // ---------- encoding ----------
  var KINDS = [
    { k: "person", label: "Person", cv: "--k-person" },
    { k: "pr", label: "Pull request", cv: "--k-pr" },
    { k: "ticket", label: "Ticket / issue", cv: "--k-ticket" },
    { k: "repo", label: "Repository", cv: "--k-repo" }
  ];
  var OUTCOMES = [
    { k: "shipped", label: "Shipped", cv: "--s-good", dash: [] },
    { k: "shipped-with-rework", label: "Shipped with rework", cv: "--s-warn", dash: [5, 2.5] },
    { k: "abandoned", label: "Abandoned", cv: "--s-critical", dash: [2, 2] },
    { k: "stalled", label: "Stalled", cv: "--s-serious", dash: [0.6, 2.6] },
    { k: "in-flight", label: "In flight", cv: "--muted", dash: [] }
  ];
  var OUTCOME = {}; OUTCOMES.forEach(function (o) { OUTCOME[o.k] = o; });
  // edge kinds: hue family (blue = people, orange = PR, green = tickets, grey = repo), dash and weight
  var EDGEKINDS = [
    { k: "reviewed", label: "reviewed my PR", cv: "--k-person", dash: [], a: 0.5 },
    { k: "built_on", label: "built on my PR", cv: "--k-person", dash: [6, 3], a: 0.55 },
    { k: "i_reviewed", label: "I reviewed their PR", cv: "--k-person", dash: [1.5, 3], a: 0.5 },
    { k: "discussed", label: "discussed with", cv: "--k-person", dash: [6, 2, 1.5, 2], a: 0.6 },
    { k: "authored", label: "I authored", cv: "--k-pr", dash: [], a: 0.35 },
    { k: "wrote", label: "wrote (author)", cv: "--k-pr", dash: [], a: 0.3 },
    { k: "refs", label: "PR references PR", cv: "--k-pr", dash: [3, 3], a: 0.3 },
    { k: "ticket", label: "linked to ticket", cv: "--k-ticket", dash: [], a: 0.35 },
    { k: "parent", label: "ticket parent", cv: "--k-ticket", dash: [4, 3], a: 0.5 },
    { k: "in_repo", label: "in repository", cv: "--muted", dash: [], a: 0.3 }
  ];
  var EK = {}; EDGEKINDS.forEach(function (e) { EK[e.k] = e; store.opts.edgeKinds[e.k] = true; });
  store.opts.edgeKinds.refs = false;

  var theme = {};
  function readTheme() {
    var cs = getComputedStyle(document.documentElement), keys = ["--surface", "--ink", "--ink2", "--muted", "--grid",
      "--k-person", "--k-pr", "--k-ticket", "--k-repo", "--s-good", "--s-warn", "--s-serious", "--s-critical", "--accent"];
    keys.forEach(function (k) { theme[k] = cs.getPropertyValue(k).trim(); });
  }
  readTheme();
  function kindColor(kind) { return theme["--k-" + kind] || theme["--muted"]; }

  // ---------- view graph (derived from data + store) ----------
  var view = null, posCache = new Map();
  function buildView() {
    var collapse = store.opts.collapse, nodes = [], idx = new Map(), links = [], linkKey = new Map();
    NODES.forEach(function (n) {
      if (collapse && n.kind === "pr") return;
      if (!n.me && store.isHidden(n)) return;
      var o = { id: n.id, d: n, deg: 0 };
      var p = posCache.get(n.id);
      if (p) { o.x = p.x; o.y = p.y; o.fx = p.fx; o.fy = p.fy; }
      idx.set(n.id, o); nodes.push(o);
    });
    function addLink(a, b, kind, w) {
      if (a === b || !idx.has(a) || !idx.has(b)) return;
      var key = a < b ? a + "\n" + b + "\n" + kind : b + "\n" + a + "\n" + kind, l = linkKey.get(key);
      if (l) { l.weight += w; return; }
      l = { source: a, target: b, kind: kind, weight: w };
      linkKey.set(key, l); links.push(l);
    }
    if (!collapse) {
      EDGES.forEach(function (e) { addLink(e.source, e.target, e.kind, e.weight || 1); });
    } else {
      NODES.forEach(function (n) {
        if (n.kind === "person" && n.counterpart && meNode) {
          var c = n.counterpart;
          addLink(n.id, meNode.id, "reviewed", c.reviewed_my_prs || 0);
          addLink(n.id, meNode.id, "built_on", c.built_on_my_prs || 0);
          addLink(n.id, meNode.id, "i_reviewed", c.i_reviewed || 0);
          addLink(n.id, meNode.id, "discussed", c.discussed || 0);
        }
        if (n.kind !== "pr") return;
        var authors = [], others = [];
        (fullAdj.get(n.id) || []).forEach(function (a) {
          if (a.kind === "authored" || a.kind === "wrote") authors.push(a.o);
          else if (a.kind === "in_repo" || a.kind === "ticket") others.push(a);
        });
        authors.forEach(function (au) { others.forEach(function (o) { addLink(au, o.o, o.kind, 1); }); });
      });
      EDGES.forEach(function (e) { if (e.kind === "parent" || e.kind === "discussed") addLink(e.source, e.target, e.kind, e.weight || 1); });
      links = links.filter(function (l) { return l.weight > 0; });
    }
    var adj = new Map();
    links.forEach(function (l) {
      [[l.source, l.target], [l.target, l.source]].forEach(function (p) {
        var m = adj.get(p[0]); if (!m) adj.set(p[0], m = []);
        m.push({ o: p[1], kind: l.kind, w: l.weight });
      });
      idx.get(l.source).deg++; idx.get(l.target).deg++;
    });
    nodes.forEach(function (o) {
      var d = o.d, g = Math.sqrt(o.deg);
      if (d.me) o.r = 15;
      else if (d.kind === "person") o.r = clamp(3.5 + 1.5 * g, 4, 15);
      else if (d.kind === "pr") o.r = clamp(2.5 + 0.55 * Math.log10((d.churn || 0) + 10) * 2, 3, 8);
      else if (d.kind === "repo") o.r = clamp(5 + 0.9 * g, 5, 14);
      else o.r = clamp(3 + 0.5 * g, 3, 7);
      if (d.me) { o.fx = 0; o.fy = 0; o.x = 0; o.y = 0; }
    });
    // links must reference node objects for rendering
    links.forEach(function (l) { l.source = idx.get(l.source); l.target = idx.get(l.target); });
    return { nodes: nodes, links: links, idx: idx, adj: adj, linkCount: links.length };
  }

  // ---------- DOM skeleton ----------
  var hdr = el("div", "hdr", null, root);
  el("h1", null, "Influence graph: " + (DATA.label || "review period"), hdr);
  var countsEl = el("div", "counts", null, hdr);
  el("div", "note", "WGO-n refs are GitHub issues, not Jira; people shown by login only.", hdr);

  var bar = el("div", "bar", null, root);
  var search = el("input", null, null, bar);
  search.type = "search"; search.placeholder = "Search by label (press /)"; search.setAttribute("aria-label", "Search nodes by label");
  function toggle(text, checked, onchange) {
    var lab = el("label", null, null, bar), cb = el("input", null, null, lab);
    cb.type = "checkbox"; cb.checked = checked; cb.addEventListener("change", function () { onchange(cb.checked); });
    el("span", null, text, lab);
    return cb;
  }
  var collapseCb = toggle("Collapse PRs into person weights", true, function (v) { store.opts.collapse = v; store.notify("structure"); });
  var botsCb = toggle("Show bots (" + counts.bot + ")", false, function (v) {
    store.setHidden("bots", v ? null : function (n) { return !!n.bot; });
  });
  var tableCb = toggle("Table view", false, function (v) { store.opts.table = v; store.notify("table"); });
  var fitBtn = el("button", null, "Fit to view", bar); fitBtn.type = "button";
  var unpinBtn = el("button", null, "Release pins", bar); unpinBtn.type = "button";
  var statusEl = el("span", "status", "", bar); statusEl.setAttribute("role", "status"); statusEl.setAttribute("aria-live", "polite");

  var main = el("div", "main", null, root);
  var stage = el("div", "stage", null, main);
  var canvas = el("canvas", null, null, stage);
  canvas.tabIndex = 0;
  canvas.setAttribute("role", "img");
  canvas.setAttribute("aria-label", "Interactive influence graph. Use the Table view toggle for an accessible list of the same nodes.");
  var tip = el("div", "tip", null, stage); tip.hidden = true;
  var tableWrap = el("div", "tablewrap", null, stage); tableWrap.hidden = true;
  var side = el("div", "side", null, main);
  var ctx = canvas.getContext("2d");

  // ---------- legend ----------
  function shapePath(kind, x, y, r) {
    if (kind === "person") return "M" + (x + r) + " " + y + "A" + r + " " + r + " 0 1 1 " + (x - r) + " " + y + "A" + r + " " + r + " 0 1 1 " + (x + r) + " " + y;
    if (kind === "pr") { var s = r * 0.9; return "M" + (x - s) + " " + (y - s) + "h" + 2 * s + "v" + 2 * s + "h" + (-2 * s) + "z"; }
    if (kind === "ticket") { var t = r * 1.25; return "M" + x + " " + (y - t) + "L" + (x + t) + " " + y + "L" + x + " " + (y + t) + "L" + (x - t) + " " + y + "z"; }
    var p = []; for (var i = 0; i < 6; i++) { var a = Math.PI / 3 * i; p.push((x + r * 1.1 * Math.cos(a)) + " " + (y + r * 1.1 * Math.sin(a))); }
    return "M" + p.join("L") + "z";
  }
  var legendBox = el("div", null, null, side);
  var legendRefs = [];
  function buildLegend() {
    legendBox.textContent = "";
    legendRefs = [];
    el("h2", null, "Legend", legendBox);
    var ul = el("ul", "legend", null, legendBox);
    KINDS.forEach(function (K) {
      var li = el("li", null, null, ul), s = svgEl("svg", { width: 22, height: 18, viewBox: "0 0 22 18", "aria-hidden": "true" }, li);
      svgEl("path", { d: shapePath(K.k, 11, 9, 6), fill: kindColor(K.k) }, s);
      el("span", null, K.label + " (" + (counts[K.k] || 0).toLocaleString() + ")", li);
    });
    var li = el("li", null, null, ul), s = svgEl("svg", { width: 22, height: 18, viewBox: "0 0 22 18", "aria-hidden": "true" }, li);
    svgEl("path", { d: shapePath("person", 11, 9, 6), fill: kindColor("person"), stroke: theme["--ink"], "stroke-width": 1.4, "stroke-dasharray": "2.5 2" }, s);
    el("span", null, "Dashed outline: outside my teams", li);
    el("h2", null, "PR outcome (ring)", legendBox);
    ul = el("ul", "legend", null, legendBox);
    OUTCOMES.forEach(function (O) {
      var li2 = el("li", null, null, ul), s2 = svgEl("svg", { width: 22, height: 18, viewBox: "0 0 22 18", "aria-hidden": "true" }, li2);
      svgEl("path", { d: shapePath("pr", 11, 9, 4.5), fill: kindColor("pr") }, s2);
      svgEl("circle", { cx: 11, cy: 9, r: 8, fill: "none", stroke: theme[O.cv], "stroke-width": 1.8, "stroke-dasharray": O.dash.join(" ") || "none", "stroke-linecap": "round" }, s2);
      el("span", null, O.label, li2);
    });
    el("h2", null, "Edges (toggle)", legendBox);
    ul = el("ul", "legend", null, legendBox);
    EDGEKINDS.forEach(function (E) {
      var li3 = el("li", null, null, ul), lab = el("label", null, null, li3), cb = el("input", null, null, lab);
      cb.type = "checkbox"; cb.checked = store.opts.edgeKinds[E.k];
      cb.addEventListener("change", function () { store.opts.edgeKinds[E.k] = cb.checked; store.notify("edges"); });
      var s3 = svgEl("svg", { width: 26, height: 10, viewBox: "0 0 26 10", "aria-hidden": "true" }, lab);
      svgEl("line", { x1: 1, y1: 5, x2: 25, y2: 5, stroke: theme[E.cv], "stroke-width": 2, "stroke-dasharray": E.dash.join(" ") || "none", opacity: Math.min(1, E.a + 0.35) }, s3);
      var t = el("span", null, E.label, lab); legendRefs.push({ k: E.k, li: li3, t: t });
    });
    refreshLegend();
  }
  function refreshLegend() {
    legendRefs.forEach(function (r) {
      var present = view && view.links.some(function (l) { return l.kind === r.k; });
      r.li.classList.toggle("off", !present);
      r.t.textContent = EK[r.k].label + (present ? "" : " (not in this view)");
    });
  }
  var detailBox = el("div", "detail", null, side);

  // ---------- canvas view ----------
  var W = 0, H = 0, dpr = 1, T = { k: 1, x: 0, y: 0 }, dirty = true, sim = null, quad = null, quadDirty = true, maxR = 15;
  var topPeople = new Set();
  function resize() {
    dpr = window.devicePixelRatio || 1;
    W = stage.clientWidth; H = stage.clientHeight;
    canvas.width = Math.max(1, Math.round(W * dpr)); canvas.height = Math.max(1, Math.round(H * dpr));
    dirty = true;
  }
  new ResizeObserver(resize).observe(stage);
  function s2w(px, py) { return [(px - T.x) / T.k, (py - T.y) / T.k]; }
  function fitTo(list, pad) {
    if (!list.length) return;
    var x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    list.forEach(function (n) { x0 = Math.min(x0, n.x - n.r); y0 = Math.min(y0, n.y - n.r); x1 = Math.max(x1, n.x + n.r); y1 = Math.max(y1, n.y + n.r); });
    var bw = Math.max(x1 - x0, 40), bh = Math.max(y1 - y0, 40);
    var k = clamp(Math.min((W - 2 * pad) / bw, (H - 2 * pad) / bh), 0.05, 6);
    T = { k: k, x: W / 2 - k * (x0 + x1) / 2, y: H / 2 - k * (y0 + y1) / 2 };
    dirty = true;
  }
  function robustFit() {
    // ignore far outliers: fit the central 97% of nodes on each axis
    var ns = view.nodes.filter(function (n) { return isFinite(n.x); });
    if (ns.length < 30) return fitTo(ns, 40);
    var xs = ns.map(function (n) { return n.x; }).sort(function (a, b) { return a - b; });
    var ys = ns.map(function (n) { return n.y; }).sort(function (a, b) { return a - b; });
    var lo = Math.floor(ns.length * 0.015), hi = ns.length - 1 - lo;
    var inl = ns.filter(function (n) { return n.x >= xs[lo] && n.x <= xs[hi] && n.y >= ys[lo] && n.y <= ys[hi]; });
    fitTo(inl, 40);
  }
  function distFor(l) {
    if (l.kind === "in_repo") return 55;
    if (l.kind === "refs") return 50;
    if (l.kind === "ticket") return 45;
    return clamp(80 - 7 * Math.sqrt(l.weight), 28, 80);
  }
  function startSim(first) {
    if (sim) sim.stop();
    view = buildView();
    maxR = view.nodes.reduce(function (m, n) { return Math.max(m, n.r); }, 1);
    var ranked = view.nodes.filter(function (n) { return n.d.kind === "person" && !n.d.me; }).sort(function (a, b) { return b.deg - a.deg; });
    topPeople = new Set(ranked.slice(0, 15).map(function (n) { return n.id; }));
    // prune selection of nodes that vanished
    var changed = false;
    store.selection.forEach(function (id) { if (!view.idx.has(id)) { store.selection.delete(id); changed = true; } });
    sim = d3.forceSimulation(view.nodes)
      .force("link", d3.forceLink(view.links).id(function (d) { return d.id; }).distance(distFor).strength(function (l) { return 1 / Math.min(l.source.deg, l.target.deg, 8); }))
      .force("charge", d3.forceManyBody().strength(function (d) { return -14 - d.r * 2.2; }).theta(0.95).distanceMax(350))
      .force("collide", d3.forceCollide(function (d) { return d.r + 1.5; }).iterations(1))
      .force("x", d3.forceX().strength(0.035)).force("y", d3.forceY().strength(0.035))
      .alphaDecay(0.028).velocityDecay(0.4).stop();
    // pre-tick so the first frame is already roughly settled (and reduced-motion users see no animation)
    var pre = reduceMotion ? 300 : (first ? 90 : 40);
    for (var i = 0; i < pre; i++) sim.tick();
    view.nodes.forEach(function (n) { posCache.set(n.id, n); });
    quadDirty = true;
    if (first || true) robustFit();
    if (!reduceMotion) { sim.alpha(first ? 0.5 : 0.35); sim.on("tick", function () { quadDirty = true; dirty = true; }).restart(); }
    else sim.on("tick", null);
    refreshLegend(); renderHeader(); renderDetail(); renderTable();
    dirty = true;
    if (changed) store.notify("selection");
  }
  function rebuildQuad() {
    quad = d3.quadtree(view.nodes, function (d) { return d.x; }, function (d) { return d.y; });
    quadDirty = false;
  }
  function hit(px, py) {
    if (quadDirty) rebuildQuad();
    var w = s2w(px, py), best = null, bd = Infinity, tol = 3 / T.k, reach = maxR + tol;
    quad.visit(function (node, x0, y0, x1, y1) {
      if (!node.length) {
        do {
          var d = node.data, dx = d.x - w[0], dy = d.y - w[1], dist = Math.sqrt(dx * dx + dy * dy) - d.r;
          if (dist <= tol && dist < bd) { bd = dist; best = d; }
        } while ((node = node.next));
      }
      return x0 > w[0] + reach || x1 < w[0] - reach || y0 > w[1] + reach || y1 < w[1] - reach;
    });
    return best;
  }

  // neighborhood of the selection (in the current view, honoring edge-kind toggles)
  var nbr = null;
  function computeNbr() {
    if (!store.selection.size || !view) { nbr = null; return; }
    nbr = new Set();
    store.selection.forEach(function (id) {
      if (!view.idx.has(id)) return;
      nbr.add(id);
      (view.adj.get(id) || []).forEach(function (a) { if (store.opts.edgeKinds[a.kind]) nbr.add(a.o); });
    });
  }

  function drawShape(n, extra) {
    var r = n.r + (extra || 0), x = n.x, y = n.y;
    ctx.beginPath();
    switch (n.d.kind) {
      case "person": ctx.arc(x, y, r, 0, 6.2832); break;
      case "pr": { var s = r * 0.9; ctx.rect(x - s, y - s, 2 * s, 2 * s); break; }
      case "ticket": { var t = r * 1.25; ctx.moveTo(x, y - t); ctx.lineTo(x + t, y); ctx.lineTo(x, y + t); ctx.lineTo(x - t, y); ctx.closePath(); break; }
      default: for (var i = 0; i < 6; i++) { var a = Math.PI / 3 * i; var px = x + r * 1.1 * Math.cos(a), py = y + r * 1.1 * Math.sin(a); if (i) ctx.lineTo(px, py); else ctx.moveTo(px, py); } ctx.closePath();
    }
  }
  function drawNode(n, alpha) {
    var d = n.d, k = T.k;
    ctx.globalAlpha = alpha;
    drawShape(n, 0);
    ctx.fillStyle = kindColor(d.kind); ctx.fill();
    ctx.lineWidth = 1.1 / k; ctx.strokeStyle = theme["--surface"]; ctx.stroke(); // surface ring separates overlaps
    if (d.me) { ctx.lineWidth = 2.2 / k; ctx.strokeStyle = theme["--ink"]; ctx.stroke(); }
    if (d.outside) { drawShape(n, 1.6); ctx.setLineDash([2.5 / k, 2 / k]); ctx.lineWidth = 1.3 / k; ctx.strokeStyle = theme["--ink"]; ctx.stroke(); ctx.setLineDash([]); }
    if (d.kind === "pr" && d.outcome && OUTCOME[d.outcome]) {
      var o = OUTCOME[d.outcome];
      ctx.beginPath(); ctx.arc(n.x, n.y, n.r * 1.2 + 2.4, 0, 6.2832);
      ctx.setLineDash(o.dash.map(function (v) { return v / k * 1.2; })); ctx.lineCap = "round";
      ctx.lineWidth = 1.9 / k; ctx.strokeStyle = theme[o.cv]; ctx.stroke();
      ctx.setLineDash([]); ctx.lineCap = "butt";
    }
    ctx.globalAlpha = 1;
  }
  function draw() {
    dirty = false;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = theme["--surface"]; ctx.fillRect(0, 0, W, H);
    if (!view) return;
    computeNbr();
    ctx.setTransform(dpr * T.k, 0, 0, dpr * T.k, dpr * T.x, dpr * T.y);
    var k = T.k, dim = !!nbr, sel = store.selection;
    // edges, batched by (kind, dim, weight bucket)
    var vis = 24 / k, x0 = -T.x / k - vis, y0 = -T.y / k - vis, x1 = (W - T.x) / k + vis, y1 = (H - T.y) / k + vis;
    function edgePass(strong) {
      EDGEKINDS.forEach(function (E) {
        if (!store.opts.edgeKinds[E.k]) return;
        for (var b = 0; b < 3; b++) {
          ctx.beginPath(); var any = false;
          for (var i = 0; i < view.links.length; i++) {
            var l = view.links[i]; if (l.kind !== E.k) continue;
            var wb = l.weight >= 6 ? 2 : l.weight >= 2 ? 1 : 0; if (wb !== b) continue;
            var on = dim && (sel.has(l.source.id) || sel.has(l.target.id));
            if (dim && on !== strong) continue;
            if (!dim && strong) continue;
            var a = l.source, c = l.target;
            if ((a.x < x0 && c.x < x0) || (a.x > x1 && c.x > x1) || (a.y < y0 && c.y < y0) || (a.y > y1 && c.y > y1)) continue;
            ctx.moveTo(a.x, a.y); ctx.lineTo(c.x, c.y); any = true;
          }
          if (!any) continue;
          ctx.setLineDash(E.dash.map(function (v) { return v / k; }));
          ctx.lineWidth = (0.6 + 0.7 * b) / k * (strong && dim ? 1.6 : 1);
          ctx.strokeStyle = theme[E.cv];
          ctx.globalAlpha = dim ? (strong ? 0.85 : 0.05) : E.a * (0.7 + 0.15 * b);
          ctx.stroke();
        }
      });
      ctx.setLineDash([]); ctx.globalAlpha = 1;
    }
    if (dim) { edgePass(false); edgePass(true); } else { dim = false; edgePass(false); }
    // nodes: dimmed first, then neighborhood, then selected
    var nodes = view.nodes, i, n;
    var hov = store.hover ? view.idx.get(store.hover) : null;
    for (i = 0; i < nodes.length; i++) {
      n = nodes[i]; if (nbr && nbr.has(n.id)) continue;
      if (n.x < x0 || n.x > x1 || n.y < y0 || n.y > y1) continue;
      drawNode(n, nbr ? 0.13 : 1);
    }
    if (nbr) for (i = 0; i < nodes.length; i++) { n = nodes[i]; if (nbr.has(n.id) && !sel.has(n.id)) drawNode(n, 1); }
    sel.forEach(function (id) {
      var s = view.idx.get(id); if (!s) return; drawNode(s, 1);
      drawShape(s, 3.5); ctx.lineWidth = 2 / k; ctx.strokeStyle = theme["--accent"]; ctx.stroke();
    });
    if (hov) { drawShape(hov, 2.5); ctx.lineWidth = 1.6 / k; ctx.strokeStyle = theme["--ink"]; ctx.stroke(); }
    // labels in screen space, greedy collision avoidance
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.font = "12px system-ui, -apple-system, 'Segoe UI', sans-serif"; ctx.textBaseline = "middle";
    var placed = [], cand = [];
    function addCand(nn, pri) { if (nn) cand.push({ n: nn, p: pri }); }
    sel.forEach(function (id) { addCand(view.idx.get(id), 0); });
    addCand(hov, 0);
    if (nbr && sel.size === 1) nbr.forEach(function (id) { var nn = view.idx.get(id); if (nn && nn.d.kind === "person" && !sel.has(id)) addCand(nn, 2); });
    if (meNode) addCand(view.idx.get(meNode.id), 1);
    topPeople.forEach(function (id) { addCand(view.idx.get(id), 3 + (nbr && !nbr.has(id) ? 5 : 0)); });
    cand.sort(function (a, b) { return a.p - b.p; });
    var seen = new Set();
    cand.forEach(function (c) {
      if (seen.has(c.n.id)) return; seen.add(c.n.id);
      var sx = c.n.x * k + T.x, sy = c.n.y * k + T.y, rr = c.n.r * k;
      if (sx < -50 || sx > W + 50 || sy < -20 || sy > H + 20) return;
      var text = c.n.d.label, tw = ctx.measureText(text).width, bx = sx + rr + 4, by = sy - 8;
      if (bx + tw > W - 4) bx = sx - rr - 4 - tw;
      var box = { x: bx, y: by, w: tw, h: 16 }, ok = true;
      if (c.p > 1) for (var j = 0; j < placed.length; j++) { var q = placed[j]; if (box.x < q.x + q.w + 3 && box.x + box.w + 3 > q.x && box.y < q.y + q.h && box.y + box.h > q.y) { ok = false; break; } }
      if (!ok) return;
      placed.push(box);
      ctx.lineWidth = 3.5; ctx.lineJoin = "round"; ctx.strokeStyle = theme["--surface"]; ctx.strokeText(text, bx, by + 8);
      ctx.fillStyle = theme["--ink"]; ctx.fillText(text, bx, by + 8);
    });
  }
  function frame() { if (dirty) draw(); requestAnimationFrame(frame); }
  requestAnimationFrame(frame);

  // ---------- tooltip ----------
  function nodeStats(d) {
    var s = [];
    if (d.kind === "person") {
      if (d.me) s.push("this is me");
      if (d.bot) s.push("bot");
      if (d.outside) s.push("outside my teams");
      if (d.counterpart) { var c = d.counterpart; s.push("reviewed my PRs " + c.reviewed_my_prs + ", built on my PRs " + c.built_on_my_prs + ", I reviewed " + c.i_reviewed + ", discussed " + c.discussed); }
    } else if (d.kind === "pr") {
      if (d.repo) s.push(d.repo);
      s.push([d.state, d.outcome, d.mine ? "mine" : "", d.month].filter(Boolean).join(" · "));
      var m = [];
      if (d.complexity) m.push("complexity " + d.complexity);
      if (d.churn != null) m.push("churn " + d.churn.toLocaleString() + " lines");
      if (d.cycle_hours != null) m.push("cycle " + Math.round(d.cycle_hours) + "h");
      if (d.points != null) m.push(d.points + " pts");
      if (d.ticketless) m.push("no ticket");
      if (m.length) s.push(m.join(", "));
    }
    return s;
  }
  function showTip(n, px, py) {
    if (!n) { tip.hidden = true; return; }
    tip.textContent = "";
    el("b", null, n.d.label, tip);
    el("div", "dim", n.d.kind + " · " + plural(n.deg, "link"), tip);
    nodeStats(n.d).forEach(function (t) { el("div", null, t, tip); });
    el("div", "dim", store.selection.has(n.id) ? "click again (or Esc) to clear · shift-click to add" : "click to pin selection · shift-click to add", tip);
    tip.hidden = false;
    var tw = tip.offsetWidth, th = tip.offsetHeight;
    tip.style.left = clamp(px + 14, 4, Math.max(4, W - tw - 4)) + "px";
    tip.style.top = clamp(py + 14, 4, Math.max(4, H - th - 4)) + "px";
  }

  // ---------- pointer interaction ----------
  var drag = null;
  function pos(ev) { var r = canvas.getBoundingClientRect(); return [ev.clientX - r.left, ev.clientY - r.top]; }
  canvas.addEventListener("pointerdown", function (ev) {
    if (ev.button !== 0) return;
    var p = pos(ev), n = hit(p[0], p[1]);
    drag = { sx: p[0], sy: p[1], n: n, moved: false, tx: T.x, ty: T.y, shift: ev.shiftKey };
    canvas.setPointerCapture(ev.pointerId);
    canvas.classList.add("dragging");
  });
  canvas.addEventListener("pointermove", function (ev) {
    var p = pos(ev);
    if (drag) {
      if (!drag.moved && Math.hypot(p[0] - drag.sx, p[1] - drag.sy) > 4) { drag.moved = true; tip.hidden = true; if (drag.n && sim) sim.alphaTarget(reduceMotion ? 0 : 0.25).restart(); }
      if (drag.moved) {
        if (drag.n) { var w = s2w(p[0], p[1]); drag.n.fx = drag.n.x = w[0]; drag.n.fy = drag.n.y = w[1]; drag.n.pinned = true; quadDirty = true; if (reduceMotion) { sim.tick(); } }
        else { T.x = drag.tx + p[0] - drag.sx; T.y = drag.ty + p[1] - drag.sy; }
        dirty = true;
      }
      return;
    }
    var n = hit(p[0], p[1]), id = n ? n.id : null;
    canvas.classList.toggle("hot", !!n);
    if (id !== store.hover) { store.hover = id; store.notify("hover"); }
    showTip(n, p[0], p[1]);
  });
  function endDrag(ev) {
    if (!drag) return;
    var d = drag; drag = null; canvas.classList.remove("dragging");
    if (d.moved) { if (d.n && sim) sim.alphaTarget(0); return; }
    if (d.n) store.select([d.n.id], ev.shiftKey || d.shift);
    else store.clear();
  }
  canvas.addEventListener("pointerup", endDrag);
  canvas.addEventListener("pointercancel", function () { drag = null; canvas.classList.remove("dragging"); });
  canvas.addEventListener("pointerleave", function () { if (!drag && store.hover) { store.hover = null; tip.hidden = true; store.notify("hover"); } });
  canvas.addEventListener("dblclick", function (ev) {
    var p = pos(ev), n = hit(p[0], p[1]);
    if (n && !n.d.me) { n.fx = n.fy = null; n.pinned = false; posCache.set(n.id, n); if (sim && !reduceMotion) sim.alpha(0.3).restart(); dirty = true; }
  });
  canvas.addEventListener("wheel", function (ev) {
    ev.preventDefault();
    var p = pos(ev), f = Math.exp(-ev.deltaY * (ev.ctrlKey ? 0.01 : 0.0015)), k2 = clamp(T.k * f, 0.04, 12);
    f = k2 / T.k; T.x = p[0] - (p[0] - T.x) * f; T.y = p[1] - (p[1] - T.y) * f; T.k = k2; dirty = true;
  }, { passive: false });
  document.addEventListener("keydown", function (ev) {
    if (ev.key === "Escape") { if (document.activeElement === search && search.value) { search.value = ""; statusEl.textContent = ""; } store.clear(); }
    else if (ev.key === "/" && document.activeElement !== search && !/^(INPUT|TEXTAREA)$/.test((document.activeElement || {}).tagName || "")) { ev.preventDefault(); search.focus(); }
  });
  fitBtn.addEventListener("click", function () { robustFit(); });
  unpinBtn.addEventListener("click", function () {
    view.nodes.forEach(function (n) { if (!n.d.me) { n.fx = n.fy = null; n.pinned = false; } });
    if (sim && !reduceMotion) sim.alpha(0.3).restart();
  });

  // ---------- search ----------
  var searchTimer = 0;
  function runSearch() {
    var q = search.value.trim().toLowerCase();
    if (!q) { statusEl.textContent = ""; return; }
    var m = view.nodes.filter(function (n) { return n.d.label.toLowerCase().indexOf(q) >= 0; });
    statusEl.textContent = m.length ? plural(m.length, "match") + (m.length > 60 ? " (selecting first 60)" : "") : "no matches in the current view";
    if (!m.length) return;
    m = m.slice(0, 60);
    store.select(m.map(function (n) { return n.id; }), false);
    if (!store.opts.table) fitTo(m, 120), T.k = Math.min(T.k, 3), recenter(m);
  }
  function recenter(m) {
    var cx = 0, cy = 0; m.forEach(function (n) { cx += n.x; cy += n.y; }); cx /= m.length; cy /= m.length;
    T.x = W / 2 - T.k * cx; T.y = H / 2 - T.k * cy; dirty = true;
  }
  search.addEventListener("input", function () { clearTimeout(searchTimer); searchTimer = setTimeout(runSearch, 200); });
  search.addEventListener("keydown", function (ev) { if (ev.key === "Enter") { clearTimeout(searchTimer); runSearch(); } });

  // ---------- header, details, table ----------
  function renderHeader() {
    var shown = view ? view.nodes.length : 0, se = view ? view.links.length : 0;
    countsEl.textContent = plural(counts.person, "person") + " (" + counts.bot + " bots) · " + plural(counts.pr, "PR") + " · " +
      plural(counts.ticket, "ticket") + " · " + plural(counts.repo, "repo") + " · " + plural(EDGES.length, "edge") +
      "   |   showing " + plural(shown, "node") + ", " + plural(se, "edge") + (store.opts.collapse ? " (PRs collapsed)" : "");
  }
  var KIND_ORDER = ["authored", "wrote", "reviewed", "i_reviewed", "built_on", "discussed", "ticket", "parent", "in_repo", "refs"];
  function phrase(a, selKind) {
    var e = EK[a.kind] ? EK[a.kind].label : a.kind;
    return e + (a.out ? " →" : " ←");
  }
  var MAXLIST = 40, expanded = new Set();
  function renderDetail() {
    detailBox.textContent = "";
    el("h2", null, "Selection", detailBox);
    if (!store.selection.size) { el("p", "empty", "Click a node to pin its neighborhood. Shift-click adds, Esc or a background click clears.", detailBox); return; }
    var ids = Array.from(store.selection).slice(0, 12);
    ids.forEach(function (id) {
      var d = byId.get(id); if (!d) return;
      var h = el("h3", null, null, detailBox); link(h, d.label, d.url);
      el("div", "kv", d.kind + (d.bot ? " · bot" : "") + (d.outside ? " · outside my teams" : ""), detailBox);
      nodeStats(d).forEach(function (t) { el("div", "kv", t, detailBox); });
      var groups = {};
      (fullAdj.get(id) || []).forEach(function (a) { (groups[a.kind + (a.out ? ">" : "<")] = groups[a.kind + (a.out ? ">" : "<")] || []).push(a); });
      var keys = Object.keys(groups).sort(function (a, b) { return KIND_ORDER.indexOf(a.slice(0, -1)) - KIND_ORDER.indexOf(b.slice(0, -1)); });
      keys.forEach(function (gk) {
        var list = groups[gk].slice().sort(function (a, b) { return b.w - a.w; });
        el("h4", null, phrase(list[0]) + "  (" + list.length + ")", detailBox);
        var ul = el("ul", null, null, detailBox), ek = id + gk, lim = expanded.has(ek) ? list.length : MAXLIST;
        list.slice(0, lim).forEach(function (a) {
          var o = byId.get(a.o); if (!o) return;
          var li = el("li", null, null, ul);
          link(li, o.label, o.url);
          if (a.w > 1) li.appendChild(document.createTextNode(" ×" + a.w));
          if (view && view.idx.has(o.id)) {
            var b = el("button", null, "select", li); b.type = "button"; b.setAttribute("aria-label", "Select " + o.label);
            b.addEventListener("click", function () { store.select([o.id], false); });
          }
        });
        if (list.length > lim) {
          var more = el("button", null, "show " + (list.length - lim) + " more", detailBox); more.type = "button";
          more.addEventListener("click", function () { expanded.add(ek); renderDetail(); });
        }
      });
    });
    if (store.selection.size > ids.length) el("p", "empty", "+" + (store.selection.size - ids.length) + " more selected", detailBox);
  }

  var sortKey = "deg", sortDir = -1;
  function renderTable() {
    tableWrap.hidden = !store.opts.table; canvas.style.visibility = store.opts.table ? "hidden" : "";
    if (!store.opts.table || !view) return;
    tableWrap.textContent = "";
    var tbl = el("table", null, null, tableWrap), cap = el("caption", "sr", "Currently visible nodes", tbl);
    var thead = el("thead", null, null, tbl), tr = el("tr", null, null, thead);
    [["label", "Name"], ["kind", "Kind"], ["deg", "Links"], ["info", "Details"]].forEach(function (c) {
      var th = el("th", null, null, tr); th.scope = "col";
      th.setAttribute("aria-sort", sortKey === c[0] ? (sortDir > 0 ? "ascending" : "descending") : "none");
      var b = el("button", null, c[1] + (sortKey === c[0] ? (sortDir > 0 ? " ▲" : " ▼") : ""), th); b.type = "button";
      b.addEventListener("click", function () { if (sortKey === c[0]) sortDir = -sortDir; else { sortKey = c[0]; sortDir = c[0] === "deg" ? -1 : 1; } renderTable(); });
    });
    function val(n) { return sortKey === "label" ? n.d.label.toLowerCase() : sortKey === "kind" ? n.d.kind : sortKey === "deg" ? n.deg : nodeStats(n.d).join("; "); }
    var rows = view.nodes.slice().sort(function (a, b) { var x = val(a), y = val(b); return (x < y ? -1 : x > y ? 1 : 0) * sortDir; });
    var tb = el("tbody", null, null, tbl), frag = document.createDocumentFragment();
    rows.forEach(function (n) {
      var r = el("tr", store.selection.has(n.id) ? "sel" : null, null, frag);
      link(el("td", null, null, r), n.d.label, n.d.url);
      el("td", null, n.d.kind, r); el("td", null, String(n.deg), r); el("td", null, nodeStats(n.d).join("; "), r);
    });
    tb.appendChild(frag);
  }

  // ---------- wiring ----------
  store.subscribe(function (reason) {
    if (reason === "structure") startSim(false);
    else if (reason === "selection") { renderDetail(); if (store.opts.table) renderTable(); dirty = true; }
    else if (reason === "edges") dirty = true;
    else if (reason === "table") { renderTable(); dirty = true; }
    else if (reason === "hover") dirty = true;
    else if (reason === "theme") { buildLegend(); dirty = true; }
  });
  if (window.matchMedia) {
    var mq = matchMedia("(prefers-color-scheme: dark)"), onTheme = function () { readTheme(); store.notify("theme"); };
    if (mq.addEventListener) mq.addEventListener("change", onTheme);
  }
  resize();
  buildLegend();
  startSim(true);
  window.__wgoStore = store; // handle for later views / debugging
})();
