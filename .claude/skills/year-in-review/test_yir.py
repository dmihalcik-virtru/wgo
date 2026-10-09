"""Unit tests for the pure logic in collect.py and slice.py.

Run from this directory: python3 -m unittest test_yir
"""

from __future__ import annotations

import datetime as dt
import json
import os
import subprocess
import tempfile
import unittest
from unittest import mock

os.environ.setdefault("YIR_CACHE", tempfile.mkdtemp(prefix="yir-test-"))

import collect  # noqa: E402
import slice as yslice  # noqa: E402

D = dt.date


def pr(url="https://github.com/o/r/pull/1", **kw) -> dict:
    rec = {
        "kind": "pr_authored",
        "url": url,
        "repo": "o/r",
        "title": "work",
        "created": "2026-03-01T00:00:00Z",
        "state": "closed",
        "merged": "2026-03-02T00:00:00Z",
        "tickets": [],
        "complexity": "L",
        "complexity_drivers": [],
        "outcome": "shipped",
    }
    rec.update(kw)
    return rec


def jira(key="ABC-1", **kw) -> dict:
    rec = {"kind": "jira", "key": key, "summary": key, "signals": ["resolved"], "resolution": "Done", "url": key}
    rec.update(kw)
    return rec


class MonthIsClosed(unittest.TestCase):
    def test_needs_a_full_utc_day_after_month_end(self):
        last = D(2026, 3, 31)
        self.assertFalse(collect.month_is_closed(last, None))
        self.assertFalse(collect.month_is_closed(last, "2026-04-01T08:00:00+00:00"))
        self.assertTrue(collect.month_is_closed(last, "2026-04-02T00:30:00+00:00"))

    def test_converts_local_offsets_to_utc(self):
        # 08:00 on Apr 2 in UTC+10 is still Apr 1 in UTC
        self.assertFalse(collect.month_is_closed(D(2026, 3, 31), "2026-04-02T08:00:00+10:00"))

    def test_naive_timestamps_from_old_caches(self):
        self.assertFalse(collect.month_is_closed(D(2026, 3, 31), "2026-04-01T23:00:00"))
        self.assertTrue(collect.month_is_closed(D(2026, 3, 31), "2026-04-02T00:00:00"))


class Outcome(unittest.TestCase):
    def test_stalled_from_thirty_days(self):
        p = {"state": "open", "created": "2026-03-01T00:00:00Z"}
        self.assertEqual(collect.outcome(p, D(2026, 3, 30)), "in-flight")
        self.assertEqual(collect.outcome(p, D(2026, 3, 31)), "stalled")


class LiveState(unittest.TestCase):
    def test_graphql_state_wins_over_search_snapshot(self):
        snap = {"state": "open", "closed": None, "merged": None}
        g = {"state": "MERGED", "closedAt": "2026-10-03T00:00:00Z", "mergedAt": "2026-10-03T00:00:00Z"}
        self.assertEqual(
            collect.live_state(snap, g),
            {"state": "closed", "closed": "2026-10-03T00:00:00Z", "merged": "2026-10-03T00:00:00Z"},
        )
        self.assertEqual(collect.live_state(snap, None), snap)


class IsBot(unittest.TestCase):
    def test_matches_go_isbot(self):
        for login in ("dependabot[bot]", "foo-bot", "ci-automation", "copilot-swe-agent", "gemini-code-assist"):
            self.assertTrue(collect.is_bot(login), login)
        for login in ("talbot", "abbot", "automationist", None):
            self.assertFalse(collect.is_bot(login), login)


class GhGraphql(unittest.TestCase):
    def fake(self, returncode, stdout, stderr=""):
        return mock.patch.object(
            collect.subprocess, "run", return_value=subprocess.CompletedProcess([], returncode, stdout, stderr)
        )

    def test_keeps_good_nodes_when_one_fails(self):
        body = {
            "data": {"nodes": [{"id": "A", "state": "MERGED"}, None]},
            "errors": [{"type": "NOT_FOUND", "path": ["nodes", 1], "message": "gone"}],
        }
        with self.fake(1, json.dumps(body), "gh: Could not resolve"):
            data, errs = collect.gh_graphql("{}")
        self.assertEqual(data["nodes"][0]["id"], "A")
        self.assertEqual(errs[0]["path"], ["nodes", 1])

    def test_enrich_names_the_failed_node(self):
        body = {
            "data": {"nodes": [{"id": "A", "state": "OPEN"}, None]},
            "errors": [{"type": "FORBIDDEN", "path": ["nodes", 1], "message": "SAML"}],
        }
        collect.errors.clear()
        with self.fake(1, json.dumps(body)):
            out = collect.enrich(["A", "B"], "id", "test", cache=False)
        self.assertEqual(set(out), {"A"})
        self.assertTrue(any(" B: FORBIDDEN" in e for e in collect.errors), collect.errors)

    def test_raises_when_there_is_no_data(self):
        with self.fake(1, "", "HTTP 502"):
            with self.assertRaises(RuntimeError):
                collect.gh_graphql("{}")

    def test_rate_limit_text_in_stdout_is_not_retried(self):
        body = {"data": {"nodes": [{"id": "A", "title": "handle rate limit errors"}]}}
        with self.fake(0, json.dumps(body)) as run:
            collect.gh_graphql("{}")
        self.assertEqual(run.call_count, 1)


class BuildReviewed(unittest.TestCase):
    def test_keeps_only_reviews_submitted_in_the_period(self):
        snap = {"url": "u", "repo": "o/r", "number": 1, "title": "t", "author": "x", "created": "2024-01-01T00:00:00Z",
                "state": "closed", "closed": None, "merged": None}
        prs = [dict(snap, node_id="OLD", url="old"), dict(snap, node_id="NEW", url="new"), dict(snap, node_id="LOST", url="lost")]
        nodes = {
            "OLD": {"state": "MERGED", "reviews": {"nodes": [{"state": "APPROVED", "submittedAt": "2024-02-01T00:00:00Z"}]}},
            "NEW": {"state": "OPEN", "reviews": {"nodes": [
                {"state": "COMMENTED", "submittedAt": "2024-02-01T00:00:00Z"},
                {"state": "APPROVED", "submittedAt": "2026-03-05T00:00:00Z"},
            ]}},
        }
        with mock.patch.object(collect, "enrich", return_value=nodes):
            recs, unenriched = collect.build_reviewed("me", prs, D(2026, 1, 1), D(2026, 12, 31))
        self.assertEqual([r["url"] for r in recs], ["new"])
        self.assertEqual(recs[0]["my_review_states"], {"APPROVED": 1})
        self.assertEqual(recs[0]["state"], "open")
        self.assertEqual(unenriched, ["lost"])


class BuildItems(unittest.TestCase):
    def test_trunk_head_does_not_chain_prs(self):
        release = pr("https://github.com/o/r/pull/1", head="develop", base="main")
        a = pr("https://github.com/o/r/pull/2", head="feat-a", base="develop")
        b = pr("https://github.com/o/r/pull/3", head="feat-b", base="develop")
        self.assertEqual(len(yslice.build_items([release, a, b])), 3)

    def test_stack_chains_on_feature_heads(self):
        a = pr("https://github.com/o/r/pull/1", head="feat-a", base="main")
        b = pr("https://github.com/o/r/pull/2", head="feat-b", base="feat-a")
        self.assertEqual(len(yslice.build_items([a, b])), 1)


class SummarizeItem(unittest.TestCase):
    def test_reported_ticket_resolved_by_someone_else_is_filed(self):
        it = yslice.summarize_item([jira(signals=["reported"])])
        self.assertEqual(it["outcome"], "filed")

    def test_ticket_i_resolved_is_shipped(self):
        self.assertEqual(yslice.summarize_item([jira()])["outcome"], "shipped")

    def test_my_wont_do_is_abandoned(self):
        self.assertEqual(yslice.summarize_item([jira(resolution="Won't Do")])["outcome"], "abandoned")

    def test_reverted_sibling_is_not_hidden_without_a_ticket(self):
        items = [pr(), pr("https://github.com/o/r/pull/2", outcome="reverted")]
        self.assertEqual(yslice.summarize_item(items)["outcome"], "shipped-with-rework")

    def test_lone_reverted_pr_stays_reverted(self):
        self.assertEqual(yslice.summarize_item([pr(outcome="reverted")])["outcome"], "reverted")

    def test_unenriched_prs_do_not_set_the_band(self):
        items = [pr(complexity="M"), pr("https://github.com/o/r/pull/2", complexity=None, complexity_drivers=["not enriched"])]
        it = yslice.summarize_item(items)
        self.assertEqual(it["complexity"], "M")
        self.assertIn("1 PRs not enriched", it["complexity_drivers"])


class FocusScore(unittest.TestCase):
    def test_matches_parent_epic_key(self):
        it = yslice.summarize_item([jira("ABC-2", parent="ABC-100", summary="zip work")])
        self.assertEqual(yslice.focus_score(it, "abc-100"), 1)
        self.assertEqual(yslice.focus_score(it, "zip, nothing"), 1)


if __name__ == "__main__":
    unittest.main()
