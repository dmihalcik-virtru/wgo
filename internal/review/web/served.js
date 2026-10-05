/* Additions for a review page served by `wgo dash` (never part of the offline
 * `wgo review graph` export). It links historical PRs and tickets forward to
 * the live dashboard and honours #focus=<review node IDs> from live links.
 * It only reads: links are plain GET navigations to the serving dashboard. */
(function () {
  "use strict";
  var bootEl = document.getElementById("wgo-boot"), boot;
  try { boot = JSON.parse(bootEl ? bootEl.textContent : "null"); } catch (e) { boot = null; }
  var store = window.__wgoStore;
  if (!boot || !boot.served || !store) return;
  var byId = new Map();
  try {
    var data = JSON.parse(document.getElementById("graph-data").textContent);
    ((data && data.nodes) || []).forEach(function (n) { byId.set(n.id, n); });
  } catch (e) { /* the app already reports bad data */ }

  function el(tag, cls, text, parent) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    if (parent) parent.appendChild(e);
    return e;
  }
  // The dashboard serves this page, so its own origin is the active
  // http://127.0.0.1:<port>.
  function lookupURL(id) { return location.origin + (boot.lookup || "/lookup") + "?entity=" + encodeURIComponent(id); }

  var hdr = document.querySelector(".hdr");
  var note = hdr ? el("div", "note wgo-served", null, hdr) : null;
  if (note) {
    var back = el("a", null, "Back to wgo dash (live work)", note);
    back.href = boot.live || "/";
    note.appendChild(document.createTextNode(" · review run " + (boot.run || "")));
  }

  function forwardLinks() {
    var box = document.querySelector(".side .detail");
    if (!box) return;
    var old = box.querySelector(".wgo-forward");
    if (old) old.parentNode.removeChild(old);
    var ids = Array.from(store.selection).filter(function (id) { return /^(pr|ticket):/.test(id); }).slice(0, 12);
    if (!ids.length) return;
    var sec = el("div", "wgo-forward", null, box);
    el("h4", null, "Live work", sec);
    var ul = el("ul", null, null, sec);
    ids.forEach(function (id) {
      var n = byId.get(id), li = el("li", null, null, ul);
      var a = el("a", null, "Is " + (n ? n.label : id) + " active now? Look it up in wgo dash", li);
      a.href = lookupURL(id);
    });
  }
  // The app subscribed first, so its detail panel is rebuilt before this runs.
  store.subscribe(function (reason) { if (reason === "selection") forwardLinks(); });

  function focusFromHash() {
    var m = /(?:^#|&)focus=([^&]*)/.exec(location.hash);
    if (!m) return;
    var ids = m[1].split(",").map(function (s) { try { return decodeURIComponent(s); } catch (e) { return ""; } })
      .filter(function (id) { return byId.has(id); });
    if (!ids.length) {
      if (note) note.appendChild(document.createTextNode(" · the linked node is not in this run"));
      return;
    }
    if (store.opts.collapse && ids.some(function (id) { return id.indexOf("pr:") === 0; })) {
      // PR nodes are hidden while PRs are collapsed; flip the app's own
      // checkbox so its state and the view agree.
      Array.prototype.forEach.call(document.querySelectorAll(".bar label"), function (lab) {
        var cb = lab.querySelector("input[type=checkbox]");
        if (cb && cb.checked && /^Collapse PRs/.test(lab.textContent)) cb.click();
      });
    }
    store.select(ids, false);
  }
  window.addEventListener("hashchange", focusFromHash);
  focusFromHash();
})();
