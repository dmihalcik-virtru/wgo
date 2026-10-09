#!/usr/bin/env python3
"""Group a collect.py ledger into work items and analyst-sized slices.

Work item: the connected component of records linked by a shared *primary*
ticket key (the first cited in title, then branch, then body; secondary
mentions like "related to X" would chain unrelated work together) or a
stacked-PR base chain (PR B based on PR A's head branch). One Jira ticket plus
the four PRs that implemented it is one item.

Slice: a set of work items one analyst agent can read in a single pass. Items
go to their Jira epic, else their Jira component, else their GitHub repo;
reviews of other people's work become one digest per org (for the collaboration
lens, not the slice analysts). Slices under MIN_ITEMS are folded into an
`<org>-misc` slice (unless they carry MIN_PRS_TO_KEEP PRs: a lone 20-PR stack
keeps its epic context) and slices over MAX_ITEMS or MAX_BYTES are split by quarter,
then month.

Writes into <run_dir>/:
  slices/<id>.json   the slice with its items (records trimmed for reading)
  slices/index.json  per-slice summaries, heaviest first
  reviews/<org>.json digest of reviews and discussions on others' work
  stats.json         headline numbers for the whole period

Usage: slice.py <run_dir> [--focus "term, synonym, TICKET-1" [--focus-limit 15]]
  --focus writes slices/focus-<slug>.json holding every item matching any
  comma-separated term (all of a term's words, as case-insensitive substrings)
  in a title, body, description, ticket, parent or epic key, epic summary,
  repo, component or label, ranked by terms matched then complexity (for
  yir-deep-dive).
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import statistics
import sys
from collections import Counter, defaultdict
from pathlib import Path

from collect import is_bot

MIN_ITEMS = 3
MIN_PRS_TO_KEEP = 5
MAX_ITEMS = 40
MAX_BYTES = 120_000  # roughly 30k tokens: one comfortable analyst read
BANDS = ["L", "M", "H", "XL"]
OUTCOME_RANK = ["shipped", "shipped-with-rework", "in-flight", "stalled", "reverted", "abandoned"]
DONE = {"Done", "Fixed", "Resolved", "Complete", "Completed"}
TRUNKS = {"main", "master", "develop", "trunk"}
TRIM = {"node_id", "body_excerpt", "cross_refs", "description"}


class DSU:
    def __init__(self) -> None:
        self.p: dict[str, str] = {}

    def find(self, x: str) -> str:
        self.p.setdefault(x, x)
        while self.p[x] != x:
            self.p[x] = self.p[self.p[x]]
            x = self.p[x]
        return x

    def union(self, a: str, b: str) -> None:
        self.p[self.find(a)] = self.find(b)


def rid(r: dict) -> str:
    return r["key"] if r["kind"] == "jira" else r["url"]


def trim(r: dict) -> dict:
    out = {k: v for k, v in r.items() if k not in TRIM and v not in (None, [], {}, "")}
    for src, dst, n in (("body_excerpt", "body", 400), ("description", "description", 400)):
        if r.get(src):
            out[dst] = r[src][:n] + (" …[truncated]" if len(r[src]) > n else "")
    if r.get("cross_refs"):
        out["cross_refs"] = [{k: c[k] for k in ("url", "author", "title")} for c in r["cross_refs"][:6]]
    return out


def is_trunk(branch: str) -> bool:
    return branch in TRUNKS or branch.startswith("release/")


def quarter(ts: str) -> str:
    return f"{ts[:4]}-Q{(int(ts[5:7]) - 1) // 3 + 1}"


def build_items(ledger: list[dict]) -> list[dict]:
    dsu = DSU()
    by_id = {rid(r): r for r in ledger}
    heads: dict[tuple[str, str], str] = {}
    for r in ledger:
        dsu.find(rid(r))
        # a release PR (develop -> main) or a fork's main is not a stack parent:
        # every PR based on that trunk would chain into one item
        if r["kind"] == "pr_authored" and r.get("head") and not is_trunk(r["head"]):
            heads[(r["repo"], r["head"])] = rid(r)
    for r in ledger:
        if r["kind"] in ("pr_reviewed", "pr_commented", "issue_commented"):
            continue  # other people's work never merges into my items
        for t in r.get("tickets", [])[:1]:
            dsu.union(rid(r), t if t in by_id else f"ticket:{t}")
        if r["kind"] == "pr_authored" and (r["repo"], r.get("base")) in heads:
            dsu.union(rid(r), heads[(r["repo"], r["base"])])

    groups: dict[str, list[dict]] = defaultdict(list)
    for r in ledger:
        groups[dsu.find(rid(r))].append(r)

    items = []
    for members in groups.values():
        items.append(summarize_item(members))
    return items


def summarize_item(members: list[dict]) -> dict:
    prs = [m for m in members if m["kind"] == "pr_authored"]
    jira = [m for m in members if m["kind"] == "jira"]
    reviews = [m for m in members if m["kind"] in ("pr_reviewed", "pr_commented", "issue_commented")]
    primary = Counter(p["tickets"][0] for p in prs if p.get("tickets"))
    jira.sort(key=lambda j: (-primary.get(j["key"], 0), j.get("signals") == ["linked"]))
    anchor = (jira or sorted(prs, key=lambda p: -(p.get("complexity_score") or 0)) or members)[0]
    dates = sorted(filter(None, (m.get("created") for m in members)))
    ends = sorted(filter(None, (m.get("merged") or m.get("resolved") for m in members)))

    banded = [p for p in prs if p.get("complexity")]  # unenriched PRs have no measured band
    band = max((BANDS.index(p["complexity"]) for p in banded), default=0)
    drivers = [d for p in banded for d in p.get("complexity_drivers", [])]
    if len(banded) < len(prs):
        drivers.append(f"{len(prs) - len(banded)} PRs not enriched")
    sp = sum(j.get("story_points") or 0 for j in jira)
    repos = sorted({m["repo"] for m in members if m.get("repo")})
    if len(prs) >= 4 or len(repos) >= 2:  # breadth the per-PR score cannot see
        band = min(band + 1, 3)
        drivers.append(f"{len(prs)} PRs across {len(repos)} repos")
    if sp >= 8 and band < 2:
        band = 2
        drivers.append(f"{sp} story points")

    # A ticket is my work if I resolved it or moved it. One I only reported or
    # only cited counts as my delivery only if one of my PRs on it merged.
    def worked(j: dict) -> bool:
        return bool({"resolved", "transitioned"} & set(j.get("signals", [])))

    mine_done = any(j.get("resolution") in DONE and worked(j) for j in jira)
    linked_done = any(j.get("resolution") in DONE for j in jira) and any(p.get("merged") for p in prs)
    rework = any(p["outcome"] in ("shipped-with-rework", "reverted") for p in prs)
    if mine_done or linked_done:
        outcome = "shipped-with-rework" if rework else "shipped"
    elif prs:
        outcome = min((p["outcome"] for p in prs), key=OUTCOME_RANK.index)
        if outcome == "shipped" and rework:  # one shipped PR must not hide a reverted sibling
            outcome = "shipped-with-rework"
    elif jira and not any(worked(j) for j in jira):
        outcome = "filed"  # reported for someone else (or later) to pick up, whoever resolved it
    elif any(j.get("resolution") for j in jira if worked(j)):
        outcome = "abandoned"  # I resolved it, but as Won't Do / Duplicate / etc.
    elif jira:
        outcome = "in-flight"
    elif reviews:
        outcome = "review"
    else:
        outcome = "shipped" if members[0]["kind"] == "release" else "n/a"

    epics = Counter(j["key"] if j.get("type") == "Epic" else j.get("parent") for j in jira)
    epics.pop(None, None)
    epic = epics.most_common(1)[0][0] if epics else None
    return {
        "id": rid(anchor),
        "title": anchor.get("title") or anchor.get("summary"),
        "kinds": dict(Counter(m["kind"] for m in members)),
        "repos": repos,
        "tickets": sorted({t for m in members for t in m.get("tickets", [])} | {j["key"] for j in jira}),
        "epic": epic,
        "epic_summary": next(
            (j["summary"] if j["key"] == epic else j.get("parent_summary") for j in jira if epic in (j["key"], j.get("parent"))),
            None,
        ),
        "components": sorted({c for j in jira for c in j.get("components", [])}),
        "labels": sorted({l for m in members for l in m.get("labels", [])}),
        "start": dates[0] if dates else None,
        "end": ends[-1] if ends else None,
        "complexity": BANDS[band],
        "complexity_drivers": drivers[:8],
        "churn": sum((p.get("additions") or 0) + (p.get("deletions") or 0) for p in prs),
        "story_points": sp or None,
        "outcome": outcome,
        "reviewers": sorted({r for p in prs for r in p.get("reviewers", {})}),
        "downstream_others": sorted({o for p in prs for o in p.get("downstream_others", [])}),
        "members": [trim(m) for m in members],
    }


def slice_key(item: dict) -> tuple[str, str, str]:
    """(slice id, basis, org) for an item."""
    org = item["repos"][0].split("/")[0] if item["repos"] else "jira"
    if item["epic"]:
        return f"epic-{item['epic']}", "epic", org
    if item["components"]:
        return f"component-{slug(item['components'][0])}", "component", org
    if item["repos"]:
        return f"repo-{slug(item['repos'][0])}", "repo", org
    return "jira-unparented", "jira", org


def slug(s: str) -> str:
    return re.sub(r"[^a-z0-9]+", "-", s.lower()).strip("-")[:48]


def build_slices(items: list[dict]) -> dict[str, dict]:
    slices: dict[str, dict] = {}
    for it in items:
        if it["outcome"] == "review":
            continue
        sid, basis, org = slice_key(it)
        s = slices.setdefault(sid, {"id": sid, "basis": basis, "org": org, "items": []})
        s["items"].append(it)
        if basis == "epic" and it.get("epic_summary"):
            s["epic_summary"] = it["epic_summary"]

    small = [
        k for k, s in slices.items()
        if len(s["items"]) < MIN_ITEMS and sum(i["kinds"].get("pr_authored", 0) for i in s["items"]) < MIN_PRS_TO_KEEP
    ]
    for sid in small:
        s = slices.pop(sid)
        misc = slices.setdefault(f"{s['org']}-misc", {"id": f"{s['org']}-misc", "basis": "misc", "org": s["org"], "items": []})
        misc["items"] += s["items"]

    def too_big(its: list) -> bool:
        return len(its) > MAX_ITEMS or len(json.dumps(its, default=str)) > MAX_BYTES

    for sid in [k for k, s in slices.items() if too_big(s["items"])]:
        s = slices.pop(sid)
        for bucket_fn in (quarter, lambda ts: ts[:7]):
            parts: dict[str, list] = defaultdict(list)
            for it in s["items"]:
                parts[bucket_fn(it["start"] or "0000-01")].append(it)
            if not any(too_big(v) for v in parts.values()) or bucket_fn is not quarter:
                break
        for b, its in parts.items():
            slices[f"{sid}-{b}"] = {**s, "id": f"{sid}-{b}", "items": its}
            if too_big(its):
                print(f"warning: slice {sid}-{b} is still over {MAX_ITEMS} items or {MAX_BYTES} bytes", file=sys.stderr)
    return slices


def summarize_slice(s: dict) -> dict:
    its = s["items"]
    starts = sorted(filter(None, (i["start"] for i in its)))
    return {
        "items": len(its),
        "prs": sum(i["kinds"].get("pr_authored", 0) for i in its),
        "jira": sum(i["kinds"].get("jira", 0) for i in its),
        "repos": sorted({r for i in its for r in i["repos"]}),
        "span": [starts[0][:10], starts[-1][:10]] if starts else None,
        "complexity": dict(Counter(i["complexity"] for i in its)),
        "outcomes": dict(Counter(i["outcome"] for i in its)),
        "churn": sum(i["churn"] for i in its),
        "story_points": sum(i["story_points"] or 0 for i in its) or None,
        "weight": sum(BANDS.index(i["complexity"]) + 1 for i in its),
    }


def review_digests(ledger: list[dict], people: dict) -> dict[str, dict]:
    """Per-org digest of work on other people's PRs: who, where, and the heaviest."""
    teams = people.get("teams", {})
    by_org: dict[str, list[dict]] = defaultdict(list)
    for r in ledger:
        if r["kind"] in ("pr_reviewed", "pr_commented", "issue_commented"):
            by_org[r["repo"].split("/")[0]].append(r)
    out = {}
    for org, rs in by_org.items():
        reviews = [r for r in rs if r["kind"] == "pr_reviewed"]
        authors = Counter(r.get("author") for r in rs if r.get("author"))
        size = lambda r: (r.get("additions") or 0) + (r.get("deletions") or 0)
        heaviest = sorted(reviews, key=lambda r: (-(r.get("my_review_comments") or 0), -size(r)))[:15]
        out[org] = {
            "org": org,
            "reviews": len(reviews),
            "discussions": len(rs) - len(reviews),
            "review_comments": sum(r.get("my_review_comments") or 0 for r in reviews),
            "states": dict(sum((Counter(r.get("my_review_states") or {}) for r in reviews), Counter())),
            "by_repo": dict(Counter(r["repo"] for r in rs).most_common(15)),
            "by_author": [
                {"login": l, "n": n, "teams": teams.get(l, []), "bot": is_bot(l)}
                for l, n in authors.most_common(20)
            ],
            "by_month": dict(sorted(Counter((r.get("first_review") or r.get("created") or "")[:7] for r in rs).items())),
            "heaviest": [
                {k: r.get(k) for k in ("url", "title", "author", "my_review_comments", "my_review_states")} | {"lines": size(r)}
                for r in heaviest
            ],
            "discussed": [{k: r.get(k) for k in ("url", "title", "author")} for r in rs if r["kind"] != "pr_reviewed"][:20],
        }
    return out


def stats(ledger: list[dict], items: list[dict], people: dict, coverage: dict) -> dict:
    prs = [r for r in ledger if r["kind"] == "pr_authored"]
    merged = [p for p in prs if p.get("merged")]
    cycles = [p["cycle_hours"] for p in merged if p.get("cycle_hours") is not None]
    jira_done = [r for r in ledger if r["kind"] == "jira" and "resolved" in r.get("signals", []) and r.get("resolution") in DONE]
    cp = people.get("counterparts", {})
    teams = people.get("teams", {})
    my_teams = set(people.get("my_teams", []))
    work = [i for i in items if i["outcome"] != "review"]
    return {
        "prs_authored": len(prs),
        "prs_merged": len(merged),
        "prs_open": sum(1 for p in prs if p["state"] == "open"),
        "prs_closed_unmerged": sum(1 for p in prs if p["state"] == "closed" and not p.get("merged")),
        "median_cycle_hours": round(statistics.median(cycles), 1) if cycles else None,
        "p90_cycle_hours": round(statistics.quantiles(cycles, n=10)[-1], 1) if len(cycles) >= 10 else None,
        "lines_changed_merged": sum((p.get("additions") or 0) + (p.get("deletions") or 0) for p in merged),
        "repos": len({p["repo"] for p in prs}),
        "orgs": sorted({p["repo"].split("/")[0] for p in prs}),
        "reviews_given": sum(1 for r in ledger if r["kind"] == "pr_reviewed"),
        "review_comments_given": sum(r.get("my_review_comments") or 0 for r in ledger if r["kind"] == "pr_reviewed"),
        "discussions": sum(1 for r in ledger if r["kind"] in ("pr_commented", "issue_commented")),
        "issues_filed": sum(1 for r in ledger if r["kind"] == "issue_filed"),
        "releases": sum(1 for r in ledger if r["kind"] == "release"),
        "jira_resolved_done": len(jira_done),
        "story_points_resolved": sum(j.get("story_points") or 0 for j in jira_done),
        "jira_reported": sum(1 for r in ledger if "reported" in r.get("signals", [])),
        "prs_reverted": sum(1 for p in prs if p.get("reverted")),
        "prs_with_followup_fixes": sum(1 for p in prs if p.get("followup_fixes")),
        "work_items": len(work),
        "work_items_by_complexity": dict(Counter(i["complexity"] for i in work)),
        "work_items_by_outcome": dict(Counter(i["outcome"] for i in work)),
        "shipped_by_complexity": dict(
            Counter(i["complexity"] for i in work if i["outcome"] in ("shipped", "shipped-with-rework"))
        ),
        "collaborators": len(cp),
        "collaborators_outside_my_teams": sum(1 for l in cp if teams.get(l) and not set(teams[l]) & my_teams),
        "prs_built_on_by_others": sum(1 for p in prs if p.get("downstream_others")),
        # how far to trust the numbers above; details in coverage.json
        "data_errors": len(coverage.get("errors", [])),
        "truncated": len(coverage.get("truncation", [])),
        "unenriched_prs": len(coverage.get("unenriched", [])),
        "failed_months": sorted(coverage.get("failed_months", {})),
    }


def focus_score(item: dict, needle: str) -> int:
    """How many comma-separated terms of needle the item matches (a term = all its words)."""
    hay = " ".join(
        [item["title"] or "", item.get("epic") or "", item.get("epic_summary") or "",
         *item["tickets"], *item["repos"], *item["components"], *item["labels"]]
        + [m.get("parent") or "" for m in item["members"]]
        + [f"{m.get('title') or ''} {m.get('body') or ''} {m.get('description') or ''}" for m in item["members"]]
    ).lower()
    terms = [t.split() for t in needle.lower().split(",") if t.strip()]
    return sum(all(w in hay for w in t) for t in terms)


def read_optional(path: Path) -> dict:
    if not path.exists():
        print(f"warning: {path} missing; numbers that depend on it read as 0", file=sys.stderr)
        return {}
    return json.loads(path.read_text())


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("run_dir", type=Path)
    ap.add_argument("--focus")
    ap.add_argument("--focus-limit", type=int, default=15)
    a = ap.parse_args()

    ledger = [json.loads(l) for l in (a.run_dir / "ledger.jsonl").read_text().splitlines() if l.strip()]
    people = read_optional(a.run_dir / "people.json")
    coverage = read_optional(a.run_dir / "coverage.json")
    items = build_items(ledger)
    out = a.run_dir / "slices"
    out.mkdir(exist_ok=True)

    if a.focus:
        scored = [(focus_score(i, a.focus), i) for i in items if i["outcome"] != "review"]
        hits = [i for sc, i in sorted(scored, key=lambda t: (-t[0], -BANDS.index(t[1]["complexity"]), t[1]["start"] or "")) if sc]
        path = out / f"focus-{slug(a.focus.split(',')[0])}.json"
        path.write_text(json.dumps({
            "focus": a.focus,
            "deep": hits[: a.focus_limit],
            "also": [{k: i[k] for k in ("id", "title", "complexity", "outcome", "start")} for i in hits[a.focus_limit :]],
        }, indent=1))
        print(json.dumps({"focus_file": str(path), "matched": len(hits)}))
        return

    for old in out.glob("*.json"):
        if not old.name.startswith("focus-"):
            old.unlink()
    rdir = a.run_dir / "reviews"
    rdir.mkdir(exist_ok=True)
    for old in rdir.glob("*.json"):  # an org with no reviews this run must not keep last run's digest
        old.unlink()
    for org, d in review_digests(ledger, people).items():
        (rdir / f"{org}.json").write_text(json.dumps(d, indent=1))
    slices = build_slices(items)
    index = []
    for sid, s in slices.items():
        s["items"].sort(key=lambda i: i["start"] or "")
        s["summary"] = summarize_slice(s)
        body = json.dumps(s["items"], sort_keys=True, default=str)
        s["hash"] = hashlib.sha256(body.encode()).hexdigest()[:16]
        (out / f"{sid}.json").write_text(json.dumps(s, indent=1, default=str))
        index.append({k: s.get(k) for k in ("id", "basis", "org", "epic_summary", "hash")} | {"summary": s["summary"]})
    index.sort(key=lambda s: -s["summary"]["weight"])
    (out / "index.json").write_text(json.dumps(index, indent=1))
    st = stats(ledger, items, people, coverage)
    (a.run_dir / "stats.json").write_text(json.dumps(st, indent=1))
    print(json.dumps({"slices": len(index), "work_items": st["work_items"], "index": str(out / "index.json"),
                      "review_digests": sorted(p.name for p in rdir.glob("*.json"))}))
    for s in index:
        sm = s["summary"]
        print(f"  {s['id']:<48} {sm['items']:>3} items  {sm['prs']:>3} PRs  {sm['jira']:>3} jira  w={sm['weight']}",
              file=sys.stderr)


if __name__ == "__main__":
    main()
