/* wgo dash triage view (spec/gh-133.md). An addon view, registered as
 * "triage" through window.WGOLive: "Which of my open PRs should I land, put
 * back to draft, or close?"
 *
 * The open PRs the operator authored that the query keeps, sorted into
 * buckets by the triage rules (boot.triage: [[pr.triage.rules]] or the
 * defaults), the first match winning. A rule whose fields are unknown for a
 * PR does not match it, and the PR's row names those fields. Each bucket is
 * a section with its count and note; each row has the PR, its draft state,
 * last update, tickets, human reviews, title, the PRs stacked on it and the
 * unknown fields. It redraws with every snapshot, so a PR closed on GitHub
 * drops out; one closed since the page loaded stays, struck through, until
 * the next Mark seen. Copy as Markdown asks /api/extract for view=triage,
 * which internal/dash/triage.go and worklist.go build by the same rules;
 * keep the two in step.
 *
 * The pure logic is logic(WGOLive), also module.exports under node, so
 * internal/review/testdata/live_logic.js tests it without a page. Like
 * live.js it writes no markup from strings and issues no requests. */
(function () {
  "use strict";

  // ---------- pure logic ----------
  function logic(L) {
    var T = {};
    function lower(s) { return String(s || "").toLowerCase(); }
    function isObj(v) { return v && typeof v === "object" && !Array.isArray(v); }
    function hasData(f) { return f === "fresh" || f === "stale"; }
    function cmp(a, b) { return a < b ? -1 : a > b ? 1 : 0; }

    // compile parses the rules against reg; a rule that does not parse
    // (the server checked them, so only a stale page) is left out.
    T.compile = function (rules, reg) {
      var out = [];
      (Array.isArray(rules) ? rules : []).forEach(function (r) {
        if (!isObj(r) || typeof r.bucket !== "string" || !r.bucket) return;
        var q = L.parseQuery(typeof r.match === "string" ? r.match : "", reg);
        if (!q.error) out.push({ bucket: r.bucket, note: typeof r.note === "string" ? r.note : "", q: q });
      });
      return out;
    };

    // sources mirrors triage.go triageSources: the open-authored inventory
    // set's freshness and what makes it incomplete, and Jira's.
    T.sources = function (snap, me) {
      var inv = (snap && snap.sources && snap.sources.github_inventory) || {}, name = lower(me + "/open-authored");
      var srcs = [], incomplete = [], found = false;
      (Array.isArray(inv.parts) ? inv.parts : []).forEach(function (p) {
        if (!isObj(p) || lower(p.name) !== name) return;
        found = true;
        srcs.push({ name: p.name, state: p.state, fetched_at: p.fetched_at || "" });
        if (!hasData(p.state)) incomplete.push(p.name + ": " + (p.error || "not fetched"));
        else if (p.truncated) incomplete.push(p.name + ": cut short by GitHub's search limit");
        else if (p.incomplete) incomplete.push(p.name + ": the last fetch missed some PRs" + (p.error ? " (" + p.error + ")" : ""));
      });
      if (!found) incomplete.push(me + "/open-authored: not fetched");
      var jira = (snap && snap.sources && snap.sources.jira) || {};
      srcs.push({ name: "jira", state: jira.state || "unknown", fetched_at: jira.oldest_fetch || "" });
      return { sources: srcs, incomplete: incomplete };
    };

    // build sorts the operator's open PRs in ix (rows, when set, the
    // query's rows) into the rules' buckets, as triage.go BuildTriage does.
    T.build = function (snap, ix, rules, rows) {
      var me = ix && ix.env && ix.env.me;
      if (!me) return { error: "The snapshot names no authenticated GitHub user, so wgo cannot tell which PRs are yours; check gh auth status." };
      var src = T.sources(snap, me), out = { me: me, buckets: [], total: 0, dropped: [], sources: src.sources, incomplete: src.incomplete };
      var listedAt = src.sources.length > 1 && hasData(src.sources[0].state) ? Date.parse(src.sources[0].fetched_at) : null;
      var index = new Map();
      rules.forEach(function (r) {
        if (!index.has(r.bucket)) { index.set(r.bucket, out.buckets.length); out.buckets.push({ name: r.bucket, note: "", notes: [], items: [] }); }
        var b = out.buckets[index.get(r.bucket)];
        if (r.note && b.notes.indexOf(r.note) < 0) { b.notes.push(r.note); b.note = b.notes.join(" "); }
      });
      if (!out.buckets.length) return out;
      var tris = rules.map(function (r) { return L.clauseTris(r.q, ix); });
      var tickets = new Map();
      ((snap && snap.edges) || []).forEach(function (e) {
        if (!e || e.kind !== "ticket") return;
        if (!tickets.has(e.source)) tickets.set(e.source, []);
        tickets.get(e.source).push(e.target);
      });
      var unfetched = [];
      for (var i = 0; i < ix.nodes.length; i++) {
        var n = ix.nodes[i], pr = n && n.kind === "pr" && isObj(n.pr) ? n.pr : null;
        if (!pr || pr.state !== "open" || lower(pr.author) !== lower(me) || (rows && !rows.has(n.id))) continue;
        // A complete open-authored set can rule out an older bookmark's
        // open state. Older snapshots lack the explicit membership marker;
        // their inventory-only PRs use fetch times as a fallback.
        var oldInventoryOnly = listedAt != null && Array.isArray(pr.sources) && pr.sources.length === 1 && pr.sources[0] === "inventory" && !(Date.parse(pr.fetched_at) >= listedAt);
        if (!src.incomplete.length && (typeof pr.open_authored_listed === "boolean" ? !pr.open_authored_listed : oldInventoryOnly)) {
          out.dropped.push(pr.url || n.id);
          continue;
        }
        // The server made the last rule match every PR.
        var item = T.item(n, ix, tickets.get(n.id) || []), dest = index.get(rules[rules.length - 1].bucket);
        item.tickets.forEach(function (t) { if (!t.status && unfetched.indexOf(t.key) < 0) unfetched.push(t.key); });
        for (var k = 0; k < rules.length; k++) {
          var holds = 1, unk = [];
          for (var c = 0; c < tris[k].length && holds !== 0; c++) {
            var t = tris[k][c].tris[i];
            if (t === 0) holds = 0;
            else if (t === 2) { holds = 2; unk.push(tris[k][c].name); }
          }
          if (holds === 1) { dest = index.get(rules[k].bucket); item.rule = rules[k].note; break; }
          if (holds === 2) unk.forEach(function (u) { if (item.unknown.indexOf(u) < 0) item.unknown.push(u); });
        }
        out.buckets[dest].items.push(item);
        out.total++;
      }
      out.buckets.forEach(function (b) { b.items.sort(T.compareItems); });
      if (unfetched.length) { unfetched.sort(); out.incomplete.push("no status for " + T.joinNames(unfetched, 10)); }
      return out;
    };

    // item is one PR's row: its tickets (in edge order), human reviews and
    // stack children.
    T.item = function (n, ix, ticketIds) {
      var pr = n.pr, f = isObj(pr.facts) ? pr.facts : {};
      var it = { id: n.id, node: n, ref: (pr.repo || "") + "#" + pr.number, url: pr.url || "", title: pr.title || "", draft: !!pr.is_draft,
        updated: pr.updated_at || "", tickets: [], reviews: Array.isArray(f.human_reviews) ? f.human_reviews : null, children: [], unknown: [], rule: "" };
      ticketIds.forEach(function (id) {
        var j = ix.byId.get(id), tn = j == null ? null : ix.nodes[j], ti = tn && isObj(tn.ticket) ? tn.ticket : null;
        if (!ti) { it.tickets.push({ key: id.slice(id.lastIndexOf(":") + 1), url: "", status: "" }); return; }
        it.tickets.push({ key: ti.key || tn.label || id, url: ti.url || "", status: hasData(ti.freshness) ? ti.status || "" : "" });
      });
      (Array.isArray(f.stack_children) ? f.stack_children : []).forEach(function (id) {
        var j = ix.byId.get(id), c = j == null ? null : ix.nodes[j];
        it.children.push({ id: id, number: c && c.pr ? c.pr.number : null, url: c && c.pr ? c.pr.url || "" : "" });
      });
      return it;
    };
    // splitRef mirrors triage.go splitRef: the lower-cased repository and
    // number of "owner/repo#N".
    function splitRef(ref) {
      ref = String(ref || "");
      var i = ref.lastIndexOf("#");
      return i < 0 ? [lower(ref), 0] : [lower(ref.slice(0, i)), parseInt(ref.slice(i + 1), 10) || 0];
    }
    T.compareItems = function (a, b) {
      var ka = a.tickets.length ? a.tickets[0].key : "~", kb = b.tickets.length ? b.tickets[0].key : "~";
      var ra = splitRef(a.ref), rb = splitRef(b.ref);
      return cmp(ka, kb) || cmp(ra[0], rb[0]) || ra[1] - rb[1] || cmp(a.id, b.id);
    };
    T.joinNames = function (items, max) {
      if (max > 0 && items.length > max) return items.slice(0, max).join(", ") + " and " + (items.length - max) + " more";
      if (items.length < 2) return items.join("");
      return items.slice(0, -1).join(", ") + " and " + items[items.length - 1];
    };
    T.reviewText = function (r) { return r.author + " " + lower(r.state).replace(/_/g, " "); };
    // shortRef drops the owner: api#41, as the worklist labels it.
    T.shortRef = function (ref) { var i = ref.indexOf("/"); return i >= 0 ? ref.slice(i + 1) : ref; };
    // closed lists the items of before, a Map of id to {item, bucket},
    // whose PR is no longer open in ix, or dropped out of the open PR list
    // (built.dropped): closed since they were seen.
    T.closed = function (before, ix, built) {
      var dropped = new Set(built.dropped), out = [];
      before.forEach(function (v, id) {
        var j = ix.byId.get(id), n = j == null ? null : ix.nodes[j], pr = n && isObj(n.pr) ? n.pr : null;
        if (!pr || pr.state !== "open" || dropped.has(pr.url || id)) out.push(v);
      });
      return out.sort(function (a, b) { return T.compareItems(a.item, b.item); });
    };
    return T;
  }

  if (typeof module !== "undefined" && module.exports) { module.exports = logic; return; }
  var L = window.WGOLive, T = logic(L);

  // ---------- view ----------
  var V = null, cur = null;
  function build(ctx) {
    V = { host: ctx.container, rows: [], rowEls: [], focusId: null, seen: null, baseline: null };
    V.host.classList.add("tv-host");
    V.root = ctx.dom.el("div", "tv", null, V.host);
    V.root.addEventListener("click", onClick);
    V.root.addEventListener("keydown", onKey);
  }

  function render(ctx) {
    cur = ctx;
    if (!V || V.host !== ctx.container) build(ctx);
    if (!ctx.snapshot || !ctx.queryIndex) return;
    if (ctx.reason === "selection" && V.m && V.snap === ctx.snapshot) { paintSelection(); return; }
    var a = document.activeElement, key = a && V.root.contains(a) && a.dataset ? a.dataset.k : null, top = V.host.scrollTop;
    V.snap = ctx.snapshot;
    var q = ctx.getState("q") || "", rows = q.trim() && ctx.query ? ctx.query.rowSet : null;
    V.m = T.build(ctx.snapshot, ctx.queryIndex, T.compile(ctx.boot.triage, ctx.registry), rows);
    track(ctx);
    V.root.textContent = "";
    V.rows = []; V.rowEls = [];
    if (V.m.error) paintError(V.m.error);
    else { paintHead(); paintClosed(); V.m.buckets.forEach(paintBucket); }
    V.host.scrollTop = top;
    if (V.focusId == null || V.rows.indexOf(V.focusId) < 0) V.focusId = V.rows[0] || null;
    rove();
    if (key) {
      var back = Array.prototype.filter.call(V.root.querySelectorAll("[data-k]"), function (e) { return e.dataset.k === key; })[0];
      if (back) back.focus({ preventScroll: true });
    }
  }

  // track remembers every open PR shown since the page load or the last
  // Mark seen, so one closed since shows struck through until the next.
  function track(ctx) {
    if (V.m.error) return;
    var d = ctx.store && ctx.store.payload && ctx.store.payload.delta, base = d ? String(d.baseline_generation || "") : "";
    if (!V.seen || V.baseline !== base) { V.seen = new Map(); V.baseline = base; V.closed = []; }
    V.closed = T.closed(V.seen, ctx.queryIndex, V.m);
    V.m.buckets.forEach(function (b) { b.items.forEach(function (it) { V.seen.set(it.id, { item: it, bucket: b.name }); }); });
  }

  function paintError(msg) {
    var box = cur.dom.el("div", "tv-gap", null, V.root);
    box.setAttribute("role", "status");
    cur.dom.el("h2", null, "No triage to show", box);
    cur.dom.el("p", null, msg, box);
  }

  function paintHead() {
    var m = V.m, el = cur.dom.el, head = el("div", "tv-head", null, V.root);
    el("h2", null, "Triage", head);
    var q = (cur.getState("q") || "").trim();
    el("span", "tv-meta", cur.dom.plural(m.total, "open PR") + " by " + m.me + (q ? " that the query keeps" : ""), head);
    var src = m.sources.map(function (s) { return s.name + " " + (s.state || "unknown") + (s.fetched_at ? ", fetched " + new Date(Date.parse(s.fetched_at)).toLocaleString() : ""); });
    el("p", "tv-note", "Sources: " + src.join(" · "), V.root);
    if (m.incomplete.length) {
      var inc = el("p", "tv-inc", null, V.root);
      inc.setAttribute("role", "note");
      el("b", null, "Incomplete: ", inc);
      inc.appendChild(document.createTextNode(m.incomplete.join("; ") + ". Re-run before acting on what they cover."));
    }
    // One button per bucket, its count beside its name, that moves to its
    // section.
    var nav = el("nav", "tv-nav", null, V.root);
    nav.setAttribute("aria-label", "Buckets");
    m.buckets.forEach(function (b, i) {
      var btn = cur.dom.button(nav, null, b.items.length ? null : "empty", null);
      el("b", null, b.name, btn);
      el("span", "n", String(b.items.length), btn);
      btn.dataset.act = "jump"; btn.dataset.sec = String(i); btn.dataset.k = "jump:" + i;
      btn.setAttribute("aria-label", b.name + ": " + cur.dom.plural(b.items.length, "PR") + ". Go to the section");
    });
  }

  function paintClosed() {
    if (!V.closed.length) return;
    var el = cur.dom.el, sec = el("section", "tv-sec tv-closed", null, V.root);
    var h = el("h3", null, "Closed since you looked", sec);
    el("span", "n", " (" + V.closed.length + ")", h);
    el("p", "tv-note", "No longer open on GitHub. They stay here, struck through, until the next Mark seen.", sec);
    var ul = el("ul", null, null, sec);
    V.closed.forEach(function (c) {
      var li = el("li", null, null, ul);
      var s = el("s", null, null, li);
      cur.dom.link(s, T.shortRef(c.item.ref), c.item.url);
      s.appendChild(document.createTextNode(" " + c.item.title));
      el("span", "tv-tag", "closed · was in " + c.bucket, li);
    });
  }

  var COLS = ["PR", "Title", "State", "Updated", "Tickets", "Human reviews", "Notes"];
  function paintBucket(b, i) {
    var el = cur.dom.el, sec = el("section", "tv-sec", null, V.root);
    sec.dataset.sec = String(i);
    var h = el("h3", null, b.name, sec);
    h.tabIndex = -1; h.id = "tv-sec-" + i;
    el("span", "n", " (" + b.items.length + ")", h);
    // An empty bucket is its heading alone; its note is the heading's tip.
    if (!b.items.length) { sec.classList.add("empty"); if (b.note) h.title = b.note; return; }
    if (b.note) el("p", "tv-note", b.note, sec);
    var wrap = el("div", "tv-scroll", null, sec), table = el("table", "tv-table", null, wrap);
    table.setAttribute("role", "grid");
    table.setAttribute("aria-labelledby", h.id);
    var hr = el("tr", null, null, el("thead", null, null, table));
    COLS.forEach(function (c) { el("th", null, c, hr).scope = "col"; });
    var tb = el("tbody", null, null, table);
    b.items.forEach(function (it) { tb.appendChild(rowEl(it)); });
  }

  function rowEl(it) {
    var el = cur.dom.el, sel = cur.store.selection.has(it.id), tr = el("tr", sel ? "sel" : null, null, null);
    tr.dataset.id = it.id; tr.dataset.k = "row:" + it.id; tr.dataset.hover = it.id;
    tr.setAttribute("aria-selected", String(sel));
    V.rows.push(it.id); V.rowEls.push(tr);
    var td = el("td", "nm", null, tr), a = cur.dom.link(td, T.shortRef(it.ref), it.url);
    if (a) { a.dataset.k = "link:" + it.id; a.title = it.ref; }
    el("td", "ttl", it.title, tr);
    el("td", it.draft ? "muted" : null, it.draft ? "draft" : "ready", tr);
    var day = it.updated && cur.queryIndex.env.dayOf ? cur.queryIndex.env.dayOf(Date.parse(it.updated)) : "";
    el("td", "num", day || "unknown", tr);
    var tk = el("td", "tk", null, tr);
    if (!it.tickets.length) { tk.classList.add("muted"); tk.textContent = "no ticket"; }
    it.tickets.forEach(function (t, j) {
      if (j) tk.appendChild(document.createTextNode(", "));
      var l = cur.dom.link(tk, t.key, t.url);
      if (l) l.dataset.k = "tk:" + it.id + ":" + t.key;
      el("span", t.status ? "st" : "st unk", " " + (t.status || "status unknown"), tk);
    });
    var rv = el("td", null, null, tr);
    if (it.reviews == null) { rv.classList.add("unk"); rv.textContent = "unknown"; }
    else if (!it.reviews.length) { rv.classList.add("muted"); rv.textContent = "none"; }
    else rv.textContent = it.reviews.map(T.reviewText).join(", ");
    var nt = el("td", "notes", null, tr);
    if (it.children.length) {
      var st = el("span", "stack", "base of ", nt);
      it.children.forEach(function (c, j) {
        if (j) st.appendChild(document.createTextNode(", "));
        var l = cur.dom.link(st, c.number != null ? "#" + c.number : c.id, c.url);
        if (l) l.dataset.k = "child:" + it.id + ":" + c.id;
      });
    }
    if (it.unknown.length) {
      var u = el("span", "unk", "unknown: " + it.unknown.join(", "), nt);
      u.title = "These fields are unknown for this PR, so the rules that need them did not match it.";
    }
    return tr;
  }

  // rove gives the keyboard's row, and the links in it, the only tab stops
  // in the tables.
  function rove() {
    V.rowEls.forEach(function (tr) {
      var on = tr.dataset.id === V.focusId;
      tr.tabIndex = on ? 0 : -1;
      Array.prototype.forEach.call(tr.querySelectorAll("a"), function (x) { x.tabIndex = on ? 0 : -1; });
    });
  }
  function paintSelection() {
    var sel = cur.store.selection;
    V.rowEls.forEach(function (tr) { var on = sel.has(tr.dataset.id); tr.classList.toggle("sel", on); tr.setAttribute("aria-selected", String(on)); });
  }

  function onClick(ev) {
    var t = ev.target.closest("[data-act]");
    if (t && t.dataset.act === "jump") {
      var h = V.root.querySelector("#tv-sec-" + t.dataset.sec);
      if (h) { h.focus({ preventScroll: true }); if (h.scrollIntoView) h.scrollIntoView({ block: "start" }); }
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
    if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); cur.select([tr.dataset.id], ev.shiftKey); return; }
    var nx = L.nextIndex(V.rows.length, V.rows.indexOf(tr.dataset.id), ev.key);
    if (nx < 0) return;
    ev.preventDefault();
    V.focusId = V.rows[nx]; rove();
    var row = V.rowEls[nx];
    row.focus();
    if (row.scrollIntoView) row.scrollIntoView({ block: "nearest" });
    if (!ev.shiftKey) cur.select([V.focusId], false, "replace");
  }

  window.WGOLive.registerView({ id: "triage", label: "Triage", question: "Which of my open PRs should I land, put back to draft, or close?",
    render: render,
    extract: function () { return { view: "triage", cols: "" }; } });
})();
