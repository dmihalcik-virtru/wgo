#!/usr/bin/env python3
"""Collect a developer's GitHub + Jira contributions for a period into a ledger.

Read-only: GitHub REST GETs and GraphQL queries through `gh`, Jira searches and
views through `acli`, plus `wgo ls` / `wgo contrib`. Raw searches are cached per
calendar month under ~/.wgo/cache/review/raw so closed months are never
re-fetched; the assembled, period-filtered output lands in
~/.wgo/cache/review/runs/<label>/:

  ledger.jsonl   one normalized record per contribution (see RECORD KINDS)
  people.json    counterpart interaction counts + team membership
  coverage.json  per-month source counts, truncation, errors, gaps
  local.json     wgo discovery + contrib heatmap (optional)

RECORD KINDS: pr_authored, pr_reviewed, pr_commented, issue_commented,
issue_filed, commit_direct, release, jira.

Usage: collect.py --period 2026            (Jan 1 .. today, or Dec 31 if past)
       collect.py --period 2026-H1|2026-Q3|2026-03-01..2026-06-30
       collect.py --since 2026-01-01 [--until 2026-09-30] [--label NAME]
"""

from __future__ import annotations

import argparse
import calendar
import concurrent.futures as cf
import datetime as dt
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path

CACHE = Path(os.environ.get("YIR_CACHE", Path.home() / ".wgo" / "cache" / "review"))
SEARCH_PAUSE = 2.1  # GitHub search API allows 30 req/min
SEARCH_CAP = 1000  # GitHub search never returns more than this per query
GQL_BATCH = 40
TEAM_MAX = 40  # teams larger than this ("everyone", "developers") say nothing
TEAM_TTL = 7 * 86400
BODY_EXCERPT = 800
TICKET_RE = re.compile(r"\b([A-Z][A-Z0-9]{1,9}-\d+)\b")
GH_ISSUE_RE = re.compile(r"\bgh-(\d+)\b", re.I)
DEFAULT_BRANCHES = {"main", "master", "develop", "trunk"}
# keep in step with IsBot in internal/review/graph.go
BOT_RE = re.compile(
    r"(\[bot\]$|-bot$|-automation$|copilot|^coderabbit|^github-actions|^renovate|^dependabot"
    r"|^(gemini-code-assist|github-advanced-security|virtru-internal|virtru-contents-and-pr-rw)$)",
    re.I,
)
DEP_BUMP_RE = re.compile(r"(chore\(deps|\bbump\b|renovate|dependabot|update dependenc)", re.I)

errors: list[str] = []
truncated: list[str] = []  # lists cut short outside the month searches (GraphQL connections)
REFRESH = False  # --refresh: bypass every cache, not just the month searches


def is_bot(login: str | None) -> bool:
    return bool(login) and bool(BOT_RE.search(login))


def log(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)


# ---------------------------------------------------------------- shell helpers


def describe(args: list[str]) -> str:
    """The command for an error message: endpoint and query, not GraphQL bodies."""
    return " ".join(a if len(a) < 160 else a[:40] + "…" for a in args)


def run_raw(args: list[str], retries: int = 5) -> subprocess.CompletedProcess:
    """Run a command, retrying on rate limits; the caller judges the exit code."""
    for attempt in range(retries):
        p = subprocess.run(args, capture_output=True, text=True)
        # stdout carries the payload (PR titles, bodies), so only stderr can say "rate limit"
        if p.returncode != 0 and "rate limit" in p.stderr.lower() and attempt < retries - 1:
            wait = 30 * (attempt + 1)
            log(f"  rate limited; sleeping {wait}s")
            time.sleep(wait)
            continue
        return p
    raise RuntimeError("unreachable")


def run(args: list[str], retries: int = 5) -> str:
    p = run_raw(args, retries)
    if p.returncode != 0:
        raise RuntimeError(f"{describe(args)}: {(p.stderr or p.stdout).strip()[:300]}")
    return p.stdout


def gh_json(path: str, **params: str) -> dict:
    args = ["gh", "api", "-X", "GET", path]
    for k, v in params.items():
        args += ["-f", f"{k}={v}"]
    return json.loads(run(args))


def gh_graphql(query: str) -> tuple[dict, list[dict]]:
    """Run a GraphQL query; returns (data, errors).

    gh exits 1 when *any* node fails (SAML, deleted repo), even though stdout
    still holds the good nodes, so partial data is kept rather than discarded.
    """
    args = ["gh", "api", "graphql", "-f", f"query={query}"]
    p = run_raw(args)
    try:
        out = json.loads(p.stdout or "null") or {}
    except ValueError:
        out = {}
    if not out.get("data"):
        msg = out.get("errors") or (p.stderr or p.stdout).strip()[:300]
        raise RuntimeError(f"graphql: {str(msg)[:300]}")
    return out["data"], out.get("errors") or []


def read_json(path: Path, default=None):
    try:
        return json.loads(path.read_text())
    except (OSError, ValueError):
        return default


def write_json(path: Path, data) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(data, indent=1, default=str))
    tmp.replace(path)


# ---------------------------------------------------------------- periods


def months(since: dt.date, until: dt.date):
    y, m = since.year, since.month
    while (y, m) <= (until.year, until.month):
        first = dt.date(y, m, 1)
        last = dt.date(y, m, calendar.monthrange(y, m)[1])
        yield f"{y:04d}-{m:02d}", first, last
        y, m = (y + 1, 1) if m == 12 else (y, m + 1)


def now_utc() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def month_is_closed(last: dt.date, fetched_at: str | None) -> bool:
    """A month's cache is final once fetched a full UTC day after it ended.

    GitHub and Jira evaluate date ranges in UTC, and search indexing lags, so
    a fetch just after local midnight on the 1st can still miss late work.
    """
    if not fetched_at:
        return False
    t = dt.datetime.fromisoformat(fetched_at)
    if t.tzinfo is None:  # caches written before fetched_at carried a zone
        return t.date() > last + dt.timedelta(days=1)
    return t.astimezone(dt.timezone.utc).date() > last + dt.timedelta(days=1)


def parse_period(spec: str, today: dt.date) -> tuple[dt.date, dt.date, str]:
    """2026 | 2026-H1 | 2026-Q3 | A..B -> (since, until, label); until never passes today."""
    if ".." in spec:
        a, b = spec.split("..", 1)
        since, until = dt.date.fromisoformat(a), dt.date.fromisoformat(b) if b else today
    elif m := re.fullmatch(r"(\d{4})(?:-([HQ])([1-4]))?", spec):
        y = int(m.group(1))
        if not m.group(2):
            since, until = dt.date(y, 1, 1), dt.date(y, 12, 31)
        else:
            n, per = int(m.group(3)), 6 if m.group(2) == "H" else 3
            if m.group(2) == "H" and n > 2:
                raise ValueError(f"bad half: {spec}")
            sm = (n - 1) * per + 1
            em = sm + per - 1
            since, until = dt.date(y, sm, 1), dt.date(y, em, calendar.monthrange(y, em)[1])
    else:
        raise ValueError(f"unrecognised period {spec!r}; use 2026, 2026-H1, 2026-Q3 or A..B")
    label = spec.replace("..", "_")
    if until > today:
        until, label = today, f"{label}-to-{today}"
    return since, until, label


def in_period(ts: str | None, since: dt.date, until: dt.date) -> bool:
    if not ts:
        return False
    d = dt.date.fromisoformat(ts[:10])
    return since <= d <= until


# ---------------------------------------------------------------- GitHub search


def search_range(kind: str, q: str, first: dt.date, last: dt.date, trunc: list) -> list[dict]:
    """Search one date range; bisect when GitHub's 1000-result cap would truncate."""
    endpoint = "search/commits" if kind == "commits" else "search/issues"
    full_q = q.format(range=f"{first}..{last}")
    items: list[dict] = []
    page, total = 1, None
    while True:
        time.sleep(SEARCH_PAUSE)
        res = gh_json(endpoint, q=full_q, per_page="100", page=str(page))
        total = res.get("total_count", 0)
        if total > SEARCH_CAP and first < last:
            mid = first + (last - first) // 2
            return search_range(kind, q, first, mid, trunc) + search_range(
                kind, q, mid + dt.timedelta(days=1), last, trunc
            )
        if res.get("incomplete_results"):  # a search timeout: transient, so the month is not final
            trunc.append(f"{kind} {first}..{last}: GitHub reported incomplete_results")
        items += res.get("items", [])
        if len(items) >= min(total, SEARCH_CAP) or not res.get("items"):
            break
        page += 1
    if total > SEARCH_CAP:
        trunc.append(f"{kind} {first}..{last}: {total} results, only {SEARCH_CAP} retrievable")
    return items


def slim_issue(it: dict) -> dict:
    return {
        "url": it["html_url"],
        "node_id": it["node_id"],
        "repo": it["repository_url"].split("/repos/", 1)[1],
        "number": it["number"],
        "title": it["title"],
        "state": it["state"],
        "author": (it.get("user") or {}).get("login"),
        "created": it["created_at"],
        "closed": it.get("closed_at"),
        "merged": (it.get("pull_request") or {}).get("merged_at"),
        "comments": it.get("comments", 0),
        "labels": [l["name"] for l in it.get("labels", [])],
    }


def slim_commit(it: dict) -> dict:
    c = it["commit"]
    return {
        "url": it["html_url"],
        "sha": it["sha"],
        "repo": it["repository"]["full_name"],
        "message": c["message"].split("\n", 1)[0][:200],
        "date": c["author"]["date"],
    }


def github_month(me: str, key: str, first: dt.date, last: dt.date, refresh: bool) -> dict:
    path = CACHE / "raw" / key / "github.json"
    cached = read_json(path)
    if cached and not refresh and month_is_closed(last, cached.get("fetched_at")):
        return cached
    log(f"  github {key}")
    trunc: list[str] = []
    failed: list[str] = []
    queries = {
        "authored": ("issues", f"author:{me} is:pr created:{{range}}"),
        "reviewed": ("issues", f"reviewed-by:{me} -author:{me} is:pr updated:{{range}}"),
        "commented": ("issues", f"commenter:{me} -author:{me} -reviewed-by:{me} updated:{{range}}"),
        "issues": ("issues", f"author:{me} is:issue created:{{range}}"),
        "commits": ("commits", f"author:{me} author-date:{{range}}"),
    }
    data: dict = {"fetched_at": now_utc(), "truncation": trunc, "failed": failed}
    for name, (kind, q) in queries.items():
        try:
            items = search_range(kind, q, first, last, trunc)
            data[name] = [slim_commit(i) if kind == "commits" else slim_issue(i) for i in items]
        except (RuntimeError, ValueError) as e:
            errors.append(f"github {name} {key}: {e}")
            data[name] = []
            failed.append(f"github_{name}")
    if failed or any("incomplete_results" in t for t in trunc):
        data["fetched_at"] = None  # never treat a partial month as final
    write_json(path, data)
    return data


# ---------------------------------------------------------------- GitHub enrichment

# Settles once the PR closes, so closed PRs are cached for good.
AUTHORED_FIELDS = """
... on PullRequest { id url state isDraft createdAt mergedAt closedAt additions deletions
  changedFiles headRefName baseRefName body
  commits { totalCount }
  reviewThreads(first: 100) { totalCount nodes { comments(first: 1) { nodes { author { login } } } } }
  reviews(first: 100) { totalCount nodes { author { login } state submittedAt } }
  closingIssuesReferences(first: 5) { nodes { url } }
}"""

# Reverts, follow-up fixes and other people building on a PR all arrive after
# it closes, so cross-references are fetched fresh on every run.
TIMELINE_FIELDS = """
... on PullRequest { id
  timelineItems(first: 100, itemTypes: [CROSS_REFERENCED_EVENT]) { filteredCount nodes {
    ... on CrossReferencedEvent { createdAt source { __typename
      ... on PullRequest { url title state mergedAt author { login } }
      ... on Issue { url title author { login } } } } } }
}"""

REVIEWED_FIELDS = """
... on PullRequest { id url state createdAt mergedAt closedAt additions deletions changedFiles
  author { login }
  reviews(first: 50, author: "%s") { totalCount nodes { state submittedAt comments { totalCount } } }
}"""


def enrich(node_ids: list[str], fields: str, bucket: str, cache: bool = True) -> dict[str, dict]:
    """Fetch GraphQL details for PR node ids. With cache, nodes of closed PRs
    (judged from the fetched node, not the search snapshot) are kept for good.
    Ids missing from the result could not be fetched; each failure is in errors."""
    cdir = CACHE / "raw" / "gql" / bucket
    out: dict[str, dict] = {}
    todo = []
    for nid in node_ids:
        c = read_json(cdir / f"{nid}.json") if cache and not REFRESH else None
        if c is not None:
            out[nid] = c
        else:
            todo.append(nid)
    if todo:
        log(f"  graphql {bucket}: {len(todo)} PRs")
    for i in range(0, len(todo), GQL_BATCH):
        batch = todo[i : i + GQL_BATCH]
        q = "{ nodes(ids: %s) { %s } }" % (json.dumps(batch), fields)
        try:
            data, errs = gh_graphql(q)
        except RuntimeError as e:
            errors.append(f"graphql {bucket}: {len(batch)} PRs not enriched: {e}")
            continue
        for e in errs:
            path = e.get("path") or []
            idx = path[1] if len(path) > 1 and isinstance(path[1], int) else None
            who = batch[idx] if idx is not None and idx < len(batch) else "?"
            errors.append(f"graphql {bucket} {who}: {e.get('type') or ''} {str(e.get('message', ''))[:200]}")
        for n in data.get("nodes") or []:
            if not n:
                continue
            out[n["id"]] = n
            if cache and n.get("state") not in (None, "OPEN"):
                write_json(cdir / f"{n['id']}.json", n)
    return out


def note_truncation(g: dict, url: str) -> None:
    for conn in ("reviews", "reviewThreads", "timelineItems"):
        c = g.get(conn) or {}
        # timelineItems' totalCount ignores the itemTypes filter; filteredCount honours it
        n, total = len(c.get("nodes") or []), c.get("filteredCount", c.get("totalCount"))
        if total is not None and total > n:
            truncated.append(f"{url}: {conn} has {total}, only {n} read")


def live_state(p: dict, g: dict | None) -> dict:
    """state/closed/merged from the GraphQL node when we have one: the month
    search cache holds whatever was true when that month was fetched."""
    if not g:
        return {k: p[k] for k in ("state", "closed", "merged")}
    return {"state": "open" if g["state"] == "OPEN" else "closed", "closed": g.get("closedAt"), "merged": g.get("mergedAt")}


# ---------------------------------------------------------------- Jira via acli

JIRA_FIELDS = (
    "key,summary,issuetype,status,resolution,priority,created,resolutiondate,"
    "parent,components,labels,assignee,reporter,description,{sp},{sprint}"
)


def jira_site() -> str | None:
    try:
        m = re.search(r"Site:\s*(\S+)", run(["acli", "jira", "auth", "status"]))
    except (RuntimeError, FileNotFoundError) as e:
        errors.append(f"jira site: {e}; Jira records carry bare keys instead of URLs")
        return None
    if not m:
        errors.append("jira site: `acli jira auth status` names no Site; Jira records carry bare keys instead of URLs")
    return m.group(1) if m else None


def jira_keys(jql: str) -> list[str]:
    # `--fields key` alone yields a list of nulls; acli needs a second field.
    out = run(["acli", "jira", "workitem", "search", "--jql", jql, "--fields", "key,summary", "--paginate", "--json"])
    items = json.loads(out or "[]") or []
    if items and not any(items):
        raise RuntimeError(f"acli returned {len(items)} null results for: {jql}")
    return [i["key"] for i in items if i]


def adf_text(node, limit: int = 600) -> str:
    """Flatten Atlassian Document Format to plain text."""
    parts: list[str] = []

    def walk(n):
        if isinstance(n, dict):
            if n.get("type") == "text":
                parts.append(n.get("text", ""))
            for c in n.get("content", []) or []:
                walk(c)
            if n.get("type") in ("paragraph", "heading", "listItem"):
                parts.append(" ")
        elif isinstance(n, list):
            for c in n:
                walk(c)

    if isinstance(node, str):
        return node[:limit]
    walk(node)
    return re.sub(r"\s+", " ", "".join(parts)).strip()[:limit]


def jira_issue(key: str, sp: str, sprint: str) -> dict | None:
    path = CACHE / "raw" / "jira" / f"{key}.json"
    c = read_json(path)
    if c and not REFRESH and (c.get("resolved") or time.time() - c.get("_fetched", 0) < 86400):
        return c
    fields = JIRA_FIELDS.format(sp=sp, sprint=sprint)
    try:
        raw = json.loads(run(["acli", "jira", "workitem", "view", key, "--fields", fields, "--json"]))
    except (RuntimeError, ValueError, FileNotFoundError) as e:
        errors.append(f"jira view {key}: {e}")
        return c
    f = raw.get("fields", {})
    parent = f.get("parent") or {}
    sprints = f.get(sprint) or []
    rec = {
        "key": key,
        "summary": f.get("summary"),
        "type": (f.get("issuetype") or {}).get("name"),
        "status": (f.get("status") or {}).get("name"),
        "status_category": ((f.get("status") or {}).get("statusCategory") or {}).get("key"),
        "resolution": (f.get("resolution") or {}).get("name"),
        "priority": (f.get("priority") or {}).get("name"),
        "created": f.get("created"),
        "resolved": f.get("resolutiondate"),
        "parent": parent.get("key"),
        "parent_summary": (parent.get("fields") or {}).get("summary"),
        "parent_type": ((parent.get("fields") or {}).get("issuetype") or {}).get("name"),
        "components": [c["name"] for c in f.get("components") or []],
        "labels": f.get("labels") or [],
        "assignee": (f.get("assignee") or {}).get("displayName"),
        "reporter": (f.get("reporter") or {}).get("displayName"),
        "story_points": f.get(sp),
        "sprints": [s.get("name") for s in sprints if isinstance(s, dict)],
        "description": adf_text(f.get("description")),
        "_fetched": time.time(),
    }
    write_json(path, rec)
    return rec


def jira_month(key: str, first: dt.date, last: dt.date, refresh: bool) -> dict:
    path = CACHE / "raw" / key / "jira.json"
    cached = read_json(path)
    if cached and not refresh and month_is_closed(last, cached.get("fetched_at")):
        return cached
    log(f"  jira {key}")
    s, u = first.isoformat(), last.isoformat()
    queries = {
        "resolved": f'assignee = currentUser() AND resolved >= "{s}" AND resolved <= "{u} 23:59"',
        "reported": f'reporter = currentUser() AND created >= "{s}" AND created <= "{u} 23:59"',
        "transitioned": f'status changed BY currentUser() DURING ("{s}", "{u} 23:59")',
    }
    data: dict = {"fetched_at": now_utc(), "failed": []}
    for name, jql in queries.items():
        try:
            data[name] = jira_keys(jql)
        except (RuntimeError, FileNotFoundError, ValueError) as e:
            errors.append(f"jira {name} {key}: {e}")
            data[name] = []
            data["failed"].append(f"jira_{name}")
            data["fetched_at"] = None
    write_json(path, data)
    return data


# ---------------------------------------------------------------- teams


def team_map(me: str, orgs: set[str]) -> tuple[dict[str, list[str]], list[str]]:
    path = CACHE / "raw" / "teams.json"
    c = read_json(path)
    if c and not REFRESH and time.time() - c.get("_fetched", 0) < TEAM_TTL and set(c.get("orgs", [])) >= orgs:
        return c["members"], c["mine"]
    log("  github teams")
    members: dict[str, list[str]] = {}
    mine: list[str] = []
    ok = True
    try:
        for t in json.loads(run(["gh", "api", "user/teams", "--paginate"])):
            mine.append(f"{t['organization']['login']}/{t['slug']}")
    except (RuntimeError, ValueError) as e:
        errors.append(f"teams mine: {e} (try `gh auth refresh -s read:org`)")
        ok = False
    for org in sorted(orgs):
        try:
            teams = json.loads(run(["gh", "api", f"orgs/{org}/teams", "--paginate"]))
        except (RuntimeError, ValueError) as e:
            if "Not Found" in str(e) or "HTTP 404" in str(e):
                continue  # a user namespace, not an org: no teams to read
            errors.append(f"teams {org}: not readable: {e} (try `gh auth refresh -s read:org`)")
            ok = False
            continue
        for t in teams:
            try:
                ms = json.loads(run(["gh", "api", f"orgs/{org}/teams/{t['slug']}/members", "--paginate"]))
            except (RuntimeError, ValueError) as e:
                errors.append(f"teams {org}/{t['slug']}: members not readable: {e}")
                ok = False
                continue
            if len(ms) > TEAM_MAX:  # deliberate: "everyone"-sized teams say nothing
                continue
            for m in ms:
                members.setdefault(m["login"], []).append(f"{org}/{t['slug']}")
    if ok:  # don't pin a failed lookup in the cache for a week
        write_json(path, {"_fetched": time.time(), "orgs": sorted(orgs), "members": members, "mine": mine})
    return members, mine


# ---------------------------------------------------------------- scoring


def tickets_in(*texts: str) -> list[str]:
    found: list[str] = []
    for t in texts:
        for m in TICKET_RE.findall(t or ""):
            if m not in found:
                found.append(m)
    return found


def complexity(pr: dict, sp: float | None = None) -> tuple[str, int, list[str]]:
    """Deterministic complexity score; see rubric.md. Returns (band, score, drivers)."""
    score, drivers = 0, []
    churn = (pr.get("additions") or 0) + (pr.get("deletions") or 0)
    files = pr.get("changedFiles") or 0
    threads = (pr.get("review_threads") or 0) + 2 * (pr.get("changes_requested") or 0)
    for val, cuts, label in (
        (churn, (50, 250, 1000), f"{churn} lines"),
        (files, (5, 15, 40), f"{files} files"),
        (threads, (3, 10), f"{threads} review threads/requests"),
    ):
        pts = sum(val >= c for c in cuts)
        if pts:
            score += pts
            drivers.append(label)
    if (pr.get("commits") or 0) > 10:
        score += 1
        drivers.append(f"{pr['commits']} commits")
    if pr.get("base") and pr["base"] not in DEFAULT_BRANCHES:
        score += 1
        drivers.append(f"stacked on {pr['base']}")
    if sp:
        pts = 2 if sp >= 8 else 1 if sp >= 5 else 0
        if pts:
            score += pts
            drivers.append(f"{sp} story points")
    if DEP_BUMP_RE.search(pr.get("title", "")):
        score = min(score, 2)
        drivers.append("dependency bump (capped)")
    band = "L" if score <= 2 else "M" if score <= 4 else "H" if score <= 6 else "XL"
    return band, score, drivers


def hours_between(a: str | None, b: str | None) -> float | None:
    if not a or not b:
        return None
    ta = dt.datetime.fromisoformat(a.replace("Z", "+00:00"))
    tb = dt.datetime.fromisoformat(b.replace("Z", "+00:00"))
    return round((tb - ta).total_seconds() / 3600, 1)


def outcome(pr: dict, until: dt.date) -> str:
    if pr.get("reverted"):
        return "reverted"
    if pr.get("merged"):
        return "shipped-with-rework" if pr.get("followup_fixes") else "shipped"
    if pr["state"] == "closed":
        return "abandoned"
    age = (until - dt.date.fromisoformat(pr["created"][:10])).days
    return "stalled" if age >= 30 else "in-flight"


# ---------------------------------------------------------------- assembly


def build_authored(me: str, prs: list[dict], until: dt.date) -> list[dict]:
    ids = [p["node_id"] for p in prs]
    gql = enrich(ids, AUTHORED_FIELDS, "authored")
    timeline = enrich(ids, TIMELINE_FIELDS, "timeline", cache=False)
    recs = []
    for p in prs:
        # caches from before the timeline split hold a stale timelineItems; the fresh one wins
        g = {**gql.get(p["node_id"], {}), **timeline.get(p["node_id"], {})}
        live = live_state(p, gql.get(p["node_id"]))
        note_truncation(g, p["url"])
        reviews = (g.get("reviews") or {}).get("nodes") or []
        reviewers: dict[str, dict[str, int]] = {}
        for r in reviews:
            login = (r.get("author") or {}).get("login")
            if login and login != me and not is_bot(login):
                reviewers.setdefault(login, {}).setdefault(r["state"], 0)
                reviewers[login][r["state"]] += 1
        refs = []
        for n in (g.get("timelineItems") or {}).get("nodes") or []:
            src = (n or {}).get("source") or {}
            if src.get("url") and src["url"] != p["url"]:
                refs.append(
                    {
                        "url": src["url"],
                        "title": src.get("title", "")[:120],
                        "author": (src.get("author") or {}).get("login"),
                        "type": src.get("__typename"),
                        "merged": bool(src.get("mergedAt")),
                        "at": n.get("createdAt"),
                    }
                )
        full_body = g.get("body") or ""
        body = full_body[:BODY_EXCERPT] + (" …[truncated]" if len(full_body) > BODY_EXCERPT else "")
        rec = {
            "kind": "pr_authored",
            **{k: p[k] for k in ("url", "repo", "number", "title", "created", "labels")},
            **live,
            "enriched": p["node_id"] in gql,
            "draft": g.get("isDraft"),
            "head": g.get("headRefName"),
            "base": g.get("baseRefName"),
            "additions": g.get("additions"),
            "deletions": g.get("deletions"),
            "changedFiles": g.get("changedFiles"),
            "commits": (g.get("commits") or {}).get("totalCount"),
            "review_threads": sum(
                1
                for t in (g.get("reviewThreads") or {}).get("nodes") or []
                if not is_bot(((((t.get("comments") or {}).get("nodes") or [{}])[0] or {}).get("author") or {}).get("login"))
            ),
            "bot_reviewers": sorted(
                {(r.get("author") or {}).get("login") for r in reviews if is_bot((r.get("author") or {}).get("login"))}
            ),
            "changes_requested": sum(r.get("CHANGES_REQUESTED", 0) for r in reviewers.values()),
            "reviewers": reviewers,
            "cycle_hours": hours_between(p["created"], live["merged"]),
            "closing_issues": [n["url"] for n in (g.get("closingIssuesReferences") or {}).get("nodes") or []],
            "cross_refs": refs,
            "downstream_others": sorted(
                {r["author"] for r in refs if r["author"] and r["author"] != me and not is_bot(r["author"])}
            ),
            "reverted": any(r["merged"] and r["title"].lower().startswith("revert") for r in refs),
            # a fix of mine that points back here *after* merge; earlier refs are stack siblings
            "followup_fixes": [
                r["url"]
                for r in refs
                if r["author"] == me
                and r["merged"]
                and live["merged"]
                and (r["at"] or "") > live["merged"]
                and re.match(r"^\W*fix", r["title"], re.I)
            ],
            "tickets": tickets_in(p["title"], g.get("headRefName") or "", full_body[:400]),
            "gh_issue_refs": GH_ISSUE_RE.findall(g.get("headRefName") or ""),
            "body_excerpt": body,
        }
        if rec["enriched"]:
            rec["complexity"], rec["complexity_score"], rec["complexity_drivers"] = complexity(rec)
        else:  # no size, reviews or base: an L would be a guess, not a measurement
            rec["complexity"], rec["complexity_score"], rec["complexity_drivers"] = None, None, ["not enriched"]
        rec["outcome"] = outcome(rec, until)
        recs.append(rec)
    return recs


def build_reviewed(me: str, prs: list[dict], since: dt.date, until: dt.date) -> tuple[list[dict], list[str]]:
    """Records for PRs I reviewed *during the period*, judged by my review dates
    (the search can only match on the PR's updated date). Returns (records,
    URLs that could not be enriched and so could not be dated)."""
    gql = enrich([p["node_id"] for p in prs], REVIEWED_FIELDS % me, "reviewed")
    recs, unenriched = [], []
    for p in prs:
        g = gql.get(p["node_id"])
        if not g:
            unenriched.append(p["url"])
            continue
        note_truncation(g, p["url"])
        mine = [r for r in (g.get("reviews") or {}).get("nodes") or [] if in_period(r.get("submittedAt"), since, until)]
        if not mine:
            continue
        states: dict[str, int] = {}
        for r in mine:
            states[r["state"]] = states.get(r["state"], 0) + 1
        recs.append(
            {
                "kind": "pr_reviewed",
                **{k: p[k] for k in ("url", "repo", "number", "title", "author", "created")},
                "state": live_state(p, g)["state"],
                "merged": g.get("mergedAt"),
                "additions": g.get("additions"),
                "deletions": g.get("deletions"),
                "changedFiles": g.get("changedFiles"),
                "my_review_states": states,
                "my_review_comments": sum((r.get("comments") or {}).get("totalCount", 0) for r in mine),
                "first_review": min((r["submittedAt"] for r in mine if r.get("submittedAt")), default=None),
                "tickets": tickets_in(p["title"]),
            }
        )
    return recs, unenriched


def releases(me: str, repos: set[str], since: dt.date, until: dt.date) -> list[dict]:
    recs = []
    for repo in sorted(repos):
        try:
            rels = json.loads(run(["gh", "api", f"repos/{repo}/releases?per_page=100", "--paginate"]))
        except (RuntimeError, ValueError) as e:
            errors.append(f"releases {repo}: {e}")
            continue
        for r in rels:
            if (r.get("author") or {}).get("login") == me and in_period(r.get("published_at"), since, until):
                recs.append(
                    {
                        "kind": "release",
                        "url": r["html_url"],
                        "repo": repo,
                        "title": r.get("name") or r["tag_name"],
                        "created": r["published_at"],
                    }
                )
    return recs


def local_context(since: dt.date, until: dt.date) -> dict:
    out: dict = {}
    try:
        out["repos"] = [r.get("repo_url") for r in json.loads(run(["wgo", "ls", "--format=json"])) if r.get("repo_url")]
    except (RuntimeError, FileNotFoundError, ValueError) as e:
        errors.append(f"wgo ls: {e}")
    # wgo contrib always ends today, so for a past period the heatmap covers
    # since..today, capped at a year; say so rather than mislabel it
    weeks = min(53, max(1, (dt.date.today() - since).days // 7 + 1))
    out["contrib_window"] = f"last {weeks} weeks to {dt.date.today()} (period {since}..{until})"
    try:
        out["contrib_heatmap"] = run(["wgo", "contrib", "--weeks", str(weeks)])
    except (RuntimeError, FileNotFoundError) as e:
        errors.append(f"wgo contrib: {e}")
    return out


def main() -> None:
    global REFRESH
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--period", help="2026 | 2026-H1 | 2026-Q3 | 2026-01-01..2026-06-30")
    ap.add_argument("--since", type=dt.date.fromisoformat)
    ap.add_argument("--until", default=dt.date.today(), type=dt.date.fromisoformat)
    ap.add_argument("--label", help="run directory name (default: the period label, or SINCE_UNTIL)")
    ap.add_argument("--sources", default="github,jira,teams,releases,local")
    ap.add_argument("--refresh", action="store_true", help="ignore every cache: month searches, PR details, Jira issues, teams")
    ap.add_argument("--sp-field", default="customfield_10004", help="Jira story points field")
    ap.add_argument("--sprint-field", default="customfield_10007", help="Jira sprint field")
    ap.add_argument("--jira-workers", type=int, default=8)
    ap.add_argument(
        "--jira-projects", default="", help="extra Jira project keys to recognise in PR titles, branches, bodies and commits"
    )
    a = ap.parse_args()
    if a.period:
        a.since, a.until, default_label = parse_period(a.period, dt.date.today())
    elif a.since:
        default_label = f"{a.since}_{a.until}"
    else:
        ap.error("need --period or --since")
    if a.since > a.until:
        ap.error(f"period starts {a.since}, after {a.until}")
    REFRESH = a.refresh
    sources = set(a.sources.split(","))
    label = a.label or default_label
    outdir = CACHE / "runs" / label
    me = run(["gh", "api", "user", "--jq", ".login"]).strip()
    log(f"collecting {a.since}..{a.until} for {me} -> {outdir}")

    ledger: list[dict] = []
    coverage: dict = {"period": [str(a.since), str(a.until)], "me": me, "months": {}, "truncation": []}
    gh_raw = {k: [] for k in ("authored", "reviewed", "commented", "issues", "commits")}
    jira_signals: dict[str, set[str]] = {}
    jira_ok = False  # at least one Jira month query succeeded

    for key, first, last in months(a.since, a.until):
        mc: dict = {}
        failed: list[str] = []
        if "github" in sources:
            g = github_month(me, key, first, last, a.refresh)
            coverage["truncation"] += g.get("truncation", [])
            failed += g.get("failed", [])
            for k in gh_raw:
                gh_raw[k] += g.get(k, [])
                mc[f"gh_{k}"] = len(g.get(k, []))
        if "jira" in sources:
            j = jira_month(key, first, last, a.refresh)
            failed += j.get("failed", [])
            jira_ok = jira_ok or len(j.get("failed", [])) < 3
            for sig in ("resolved", "reported", "transitioned"):
                mc[f"jira_{sig}"] = len(j.get(sig, []))
                for k in j.get(sig, []):
                    jira_signals.setdefault(k, set()).add(sig)
        if failed:
            mc["failed"] = failed  # these counts are 0 because the query failed, not because the month was quiet
        coverage["months"][key] = mc

    # The reviewed search can only match a PR's *updated* date, which is on or
    # after my review. A PR reviewed in the period but touched since would be
    # missed, so search on through the current month and date reviews myself.
    if "github" in sources:
        today = dt.date.today()
        for key, first, last in months(a.until + dt.timedelta(days=1), today):
            if (first.year, first.month) == (a.until.year, a.until.month):
                continue  # already searched as part of the period
            g = github_month(me, key, first, last, a.refresh)
            gh_raw["reviewed"] += g.get("reviewed", [])
            if "github_reviewed" in g.get("failed", []):
                coverage["truncation"].append(f"reviewed {key}: search failed, so reviews from the period on PRs updated then are missing")

    def dedupe(items: list[dict], date_key: str | None) -> list[dict]:
        """Drop month-overlap duplicates; date_key=None keeps everything the search matched."""
        seen: dict[str, dict] = {}
        for i in items:
            if date_key is None or in_period(i.get(date_key), a.since, a.until):
                seen[i["url"]] = i
        return list(seen.values())

    if "github" in sources:
        authored = dedupe(gh_raw["authored"], "created")
        ledger += build_authored(me, authored, a.until)
        reviewed, coverage["reviewed_unenriched"] = build_reviewed(me, dedupe(gh_raw["reviewed"], None), a.since, a.until)
        ledger += reviewed
        # matched on the item's updated date only: approximate, see rubric
        for it in dedupe(gh_raw["commented"], None):
            ledger.append({"kind": "pr_commented" if "/pull/" in it["url"] else "issue_commented", **it})
        for it in dedupe(gh_raw["issues"], "created"):
            ledger.append({"kind": "issue_filed", **it, "tickets": tickets_in(it["title"])})
        pr_repos = {r["repo"] for r in ledger if r["kind"] == "pr_authored"}
        direct: dict[str, list[dict]] = {}
        for c in {c["sha"]: c for c in gh_raw["commits"]}.values():
            if in_period(c["date"], a.since, a.until) and c["repo"] not in pr_repos:
                direct.setdefault(c["repo"], []).append(c)
        for repo, cs in direct.items():
            ledger.append(
                {
                    "kind": "commit_direct",
                    "url": f"https://github.com/{repo}/commits?author={me}",
                    "repo": repo,
                    "title": f"{len(cs)} commits outside any authored PR",
                    "created": min(c["date"] for c in cs),
                    "count": len(cs),
                    "samples": [c["message"] for c in cs[:8]],
                    "tickets": tickets_in(*(c["message"] for c in cs)),
                }
            )
        if "releases" in sources:
            ledger += releases(me, pr_repos, a.since, a.until)

    prefix_counts: dict[str, int] = {}
    for r in ledger:
        for t in r.get("tickets", []):
            prefix_counts[t.split("-")[0]] = prefix_counts.get(t.split("-")[0], 0) + 1
    projects = {k.split("-")[0] for k in jira_signals} | set(filter(None, a.jira_projects.split(",")))
    if not jira_ok:
        # Without Jira the known projects are unknowable, and filtering against an
        # empty set would strip every ticket and break work-item grouping.
        projects |= {k for k, n in prefix_counts.items() if n >= 3}
        coverage["jira_projects_inferred"] = "Jira skipped or failed: projects are ticket prefixes cited 3+ times"

    # The primary ticket each of my PRs cites, when no Jira query surfaced it
    # (assigned to someone else, resolved outside the period): fetch it too so
    # the PR gets its epic. Only the primary: slice.py groups on nothing else.
    if "jira" in sources:
        cited = {r["tickets"][0] for r in ledger if r["kind"] == "pr_authored" and r.get("tickets")}
        for t in sorted(cited - set(jira_signals)):
            if t.split("-")[0] in projects:
                jira_signals.setdefault(t, set()).add("linked")

    if "jira" in sources and jira_signals:
        site = jira_site()
        log(f"  jira view: {len(jira_signals)} issues")
        with cf.ThreadPoolExecutor(a.jira_workers) as ex:
            issues = list(ex.map(lambda k: jira_issue(k, a.sp_field, a.sprint_field), sorted(jira_signals)))
        parents = {i["parent"] for i in issues if i and i.get("parent")} - set(jira_signals)
        with cf.ThreadPoolExecutor(a.jira_workers) as ex:
            epics = {e["key"]: e for e in ex.map(lambda k: jira_issue(k, a.sp_field, a.sprint_field), sorted(parents)) if e}
        for i in issues:
            if not i:
                continue
            rec = {k: v for k, v in i.items() if not k.startswith("_")}
            rec.update(
                kind="jira",
                url=f"https://{site}/browse/{i['key']}" if site else i["key"],
                title=i["summary"],
                signals=sorted(jira_signals[i["key"]]),
                parent_status=(epics.get(i.get("parent")) or {}).get("status"),
            )
            ledger.append(rec)
        resolved = [r for r in ledger if r["kind"] == "jira" and r.get("resolved")]
        if resolved and not any(r.get("story_points") is not None for r in resolved):
            errors.append(f"jira story points: none of {len(resolved)} resolved issues has {a.sp_field}; wrong --sp-field?")

    people: dict = {"counterparts": {}, "teams": {}, "my_teams": []}
    cp = people["counterparts"]

    def bump(login: str | None, field: str, n: int = 1) -> None:
        if login and login != me and not is_bot(login):
            cp.setdefault(login, {}).setdefault(field, 0)
            cp[login][field] += n

    for r in ledger:
        if r["kind"] == "pr_authored":
            for login in r["reviewers"]:
                bump(login, "reviewed_my_prs")
            for login in r["downstream_others"]:
                bump(login, "built_on_my_prs")
        elif r["kind"] == "pr_reviewed":
            bump(r.get("author"), "i_reviewed")
        elif r["kind"] in ("pr_commented", "issue_commented"):
            bump(r.get("author"), "discussed")
    if "teams" in sources and "github" in sources:
        orgs = {r["repo"].split("/")[0] for r in ledger if r.get("repo")}
        members, people["my_teams"] = team_map(me, orgs)
        people["teams"] = {login: members.get(login, []) for login in cp}

    jira_keys_seen = {r["key"] for r in ledger if r["kind"] == "jira"}
    dropped: dict[str, int] = {}
    for r in ledger:  # drop look-alikes such as ML-KEM-768 -> KEM-768
        if "tickets" in r:
            for t in r["tickets"]:
                if t.split("-")[0] not in projects:
                    dropped[t.split("-")[0]] = dropped.get(t.split("-")[0], 0) + 1
            r["tickets"] = [t for t in r["tickets"] if t.split("-")[0] in projects]
    coverage["dropped_ticket_prefixes"] = dropped  # real projects? re-run with --jira-projects
    coverage["jira_projects"] = sorted(projects)
    tix = {t for r in ledger for t in r.get("tickets", [])}
    coverage["truncation"] += truncated
    coverage["errors"] = errors
    coverage["counts"] = {}
    for r in ledger:
        coverage["counts"][r["kind"]] = coverage["counts"].get(r["kind"], 0) + 1
    coverage["prs_authored"] = coverage["counts"].get("pr_authored", 0)
    coverage["prs_without_ticket"] = sum(1 for r in ledger if r["kind"] == "pr_authored" and not r["tickets"])
    coverage["unenriched"] = sorted(r["url"] for r in ledger if r["kind"] == "pr_authored" and not r["enriched"])
    # resolved-by-me tickets that no record (PR title/branch/first 400 chars of body, commit, issue) cites
    coverage["resolved_tickets_without_pr"] = sorted(
        r["key"] for r in ledger if r["kind"] == "jira" and "resolved" in r["signals"] and r["key"] not in tix
    )
    # primary tickets of my PRs that were looked up but could not be fetched (deleted, no permission, other site)
    unfetched = sorted(k for k, sig in jira_signals.items() if sig == {"linked"} and k not in jira_keys_seen)
    coverage["cited_tickets_unfetched_count"] = len(unfetched)
    coverage["cited_tickets_unfetched"] = unfetched[:200]
    # months whose PR+Jira activity is under 40% of the median of months with no failed source
    activity = {m: c.get("gh_authored", 0) + c.get("gh_reviewed", 0) + c.get("jira_resolved", 0)
                for m, c in coverage["months"].items()}
    measured = {m: n for m, n in activity.items() if not coverage["months"][m].get("failed")}
    if len(measured) >= 3:
        med = sorted(measured.values())[len(measured) // 2]
        coverage["quiet_months"] = {m: n for m, n in measured.items() if n < 0.4 * med}
    else:
        coverage["quiet_months"] = "n/a: fewer than 3 fully fetched months in period"
    coverage["failed_months"] = {m: c["failed"] for m, c in coverage["months"].items() if c.get("failed")}
    coverage["monthly_activity"] = activity

    outdir.mkdir(parents=True, exist_ok=True)
    with (outdir / "ledger.jsonl").open("w") as f:
        for r in sorted(ledger, key=lambda r: r.get("created") or r.get("resolved") or ""):
            f.write(json.dumps(r, default=str) + "\n")
    write_json(outdir / "people.json", people)
    if "local" in sources:
        loc = local_context(a.since, a.until)
        gh_repos = {f"https://github.com/{r['repo']}" for r in ledger if r.get("repo")}
        my_orgs = {r["repo"].split("/")[0] for r in ledger if r.get("repo")}
        coverage["local_repos_without_github_activity"] = sorted({
            u for u in loc.get("repos", []) if u.split("/")[3:4] and u.split("/")[3] in my_orgs and u not in gh_repos
        })
        write_json(outdir / "local.json", loc)
    else:
        (outdir / "local.json").unlink(missing_ok=True)  # don't leave an earlier run's
    write_json(outdir / "coverage.json", coverage)
    print(json.dumps({"run_dir": str(outdir), "counts": coverage["counts"], "errors": len(errors),
                      "truncation": len(coverage["truncation"]), "unenriched": len(coverage["unenriched"])}, indent=1))


if __name__ == "__main__":
    main()
