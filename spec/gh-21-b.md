# gh-21-b: jj-vcs migration — cmd-layer rewires and `internal/git` deletion

**Status:** Pending (continues from [`spec/gh-21.md`](gh-21.md))
**Issue:** https://github.com/dmihalcik-virtru/wgo/issues/21
**Predecessor PR:** https://github.com/dmihalcik-virtru/wgo/pull/23 (draft, branch `jj2-vcs-update`)

## Context

[`spec/gh-21.md`](gh-21.md) landed the foundational packages for replacing git with jj:
`internal/jj`, `internal/github` (HTTP), `internal/sync`, slimmed `internal/store`,
deleted `internal/hooks`, `wgo doctor`/`wgo sync` commands, and `.jj/` discovery.

This spec finishes the migration: rewire every remaining caller of `internal/git`
to `internal/jj`, route the last `gh` shell-outs through `internal/github`'s
HTTP client, then delete `internal/git/` entirely and verify a clean build.

The branch must compile cleanly throughout. After every task in this spec,
`go build ./...` and `go vet ./...` succeed, and `go test ./...` passes (the
pre-existing `internal/pilot` failures are addressed in Task 10).

## Constraints (unchanged from gh-21)

- No `git` CLI dependency anywhere after Task 11. The final `exec.Command("git", ...)`
  call leaves the tree in this PR. **Superseded (narrowly):** `internal/lfs`
  reintroduces optional `git`/`git-lfs` shell-outs for `wgo lfs sync`/`wgo lfs
  status`, gated on `lfs.Available()` — jj has no git-lfs support to fall
  back on. This is scoped to LFS hydration only; nothing else in wgo shells
  out to `git`.
- No `gh` CLI shell-out except `gh auth token` in `internal/github/auth.go`
  (credential bootstrap only).
- Pure jj only — never `--colocate`. `jj.GitInit` and `jj.GitClone` already
  enforce this. **Superseded:** colocation is now the default for main
  checkouts (`jj.GitInit`/`GitClone` pass `--colocate`; `jj.EnsureColocated`
  retrofits pre-existing repos before a workspace is added). Secondary
  workspaces remain plain jj — jj only allows colocation on the main
  workspace.
- Tests use real `jj` (no mocks). The `internal/jjtest` helpers handle setup.
- `internal/git/` must not exist on disk after Task 11.

## Task 8 — Rewire `wgo add` and `wgo to`

These are the two user-facing commands from issue #21 and the most visible part
of the migration.

### `internal/cmd/add.go`

Current behavior: `wgo add TICKET` creates a git worktree per tracked repo at
`<worktrees_dir>/TICKET/<repo>`, creates a new branch, pushes it, then writes
and commits a spec scaffold.

Target behavior (per issue #21): `wgo add TICKET` creates a jj workspace per
tracked repo at the same path, attaches a bookmark named TICKET to the
workspace's working-copy parent, writes the spec scaffold (jj snapshots
automatically), describes the working-copy commit, sets the bookmark to
`@-`, and pushes the bookmark.

**Specific edits** (line numbers from the current file):

| Line | Current call | Replacement |
|---|---|---|
| 149, 362 | `gitClient := git.New("")` | `jjc := jj.NewCLI()` |
| 220 | `gitClient.DefaultBranch(repoPath)` | Use `internal/github.DefaultBranch(slug)` (already exists; needs repo slug, derive via `jj.RemoteURLs(repoPath)`) |
| 232 | `gitClient.WorktreeAdd(repoPath, wtPath, branchName, true, "origin/"+defaultBranch)` | `jjc.WorkspaceAdd(repoPath, branchName, wtPath, "origin/"+defaultBranch)` followed by `jjc.BookmarkCreate(repoPath, branchName, "@-")` (so the bookmark exists for push) |
| 238 | `gitClient.Push(wtPath, branchName)` | `jjc.GitPush(repoPath, jj.PushOpts{Bookmarks: []string{branchName}})` |
| 287-292 | `gitClient.AddAndCommit(specWtPath, specRel, msg)` then `gitClient.Push(specWtPath, branchName)` | jj auto-snapshots when files change; describe the working-copy commit with `jjc.Describe(specWtPath, msg)`, then `jjc.New(specWtPath, "", "")` to leave a clean WC, then `jjc.BookmarkSet(repoPath, branchName, "@-", false)` and `jjc.GitPush(...)` |
| 414 | `exec.Command("git", "rev-parse", "--show-toplevel")` | `jjc.Root(cwd)` |
| 156 | `gitClient.RemoveWorktree(wt.repoPath, wt.wtPath, true)` (cleanup-on-error) | `jjc.WorkspaceForget(wt.repoPath, branchName)` + `os.RemoveAll(wt.wtPath)` |

**Bookmark-advancement caveat:** jj bookmarks do NOT auto-advance with `jj new`.
Every code path that creates a commit and intends to push it must call
`BookmarkSet(repo, name, "@-", false)` before `GitPush`. Document this near
the spec-commit flow.

**`detectCurrentRepo` (line 413):** rename to `detectCurrentJJRepo`; switch
the helper to `jjc.Root(cwd)`. Callers across `cmd/` that take a
`*git.CLIClient` parameter need to take a `jj.Client` instead.

### `internal/cmd/to.go`

Current behavior: `wgo to <PR-URL>` discovers an existing checkout, otherwise
clones the repo and creates a git worktree. For issues / new branches, it
creates a worktree based on `origin/<default>` or `--on <parent>`.

Target behavior: same UX, but use jj.

**Specific edits:**

| Line | Current call | Replacement |
|---|---|---|
| 80, 264, 349 | `git.New("")` | `jj.NewCLI()` |
| 171, 403 | `gitClient.ListWorktrees(r.Path)` | `jjc.ListWorkspaces(r.Path)` (returns `[]jj.Workspace`; same shape, different field names — adjust call sites) |
| 384 (`findExistingCheckout`) | takes `*git.CLIClient` | take `jj.Client` |
| 419 (`matchesRemote`) | uses `gitClient.RemoteURLs` (which doesn't exist on git client; check) | use `jjc.RemoteURLs(repoPath)` |
| 436 (`findOrCloneRepo`) | `gitClient.Clone(url, dest)` | `jjc.GitClone(url, dest)` |
| 484 (`createWorktree`) | `gitClient.WorktreeAdd(repoPath, wtPath, branch, true/false, startPoint)` | Two-step: `jjc.WorkspaceAdd(repoPath, branch, wtPath, startPoint)` then `jjc.BookmarkCreate(repoPath, branch, "@-")` |
| 445-446 | `r.IsWorktree` / `r.MainRepoPath` from discovery — already works (Task 7 preserved the field shape) | no change |

**PR fetch flow (per issue #21):** `wgo to https://github.com/owner/repo/pull/42`:

1. Use `internal/github.GetPRHeadRef(slug, 42)` (already implemented) to get
   `headRefName`, `headRefOid`, and `headRepository.FullName`.
2. If the head repo equals the local repo's `origin`, fetch the PR ref:
   `jjc.GitFetch(repoPath, "origin", []string{"glob:pull/42/head"})`.
3. For fork PRs (head repo != origin), add the fork as a temporary remote
   first, then fetch its branch. Keep the existing upstream-then-origin
   fallback logic in spirit.
4. `jjc.BookmarkCreate(repoPath, "pr-42-<slug>", "<headRefOid>")`.
5. `jjc.WorkspaceAdd(repoPath, "pr-42-<slug>", wtPath, "pr-42-<slug>")`.

**Workspace path:** `<worktrees_dir>/pr-<N>-<normalized-headRef>/<owner>/<repo>`
(matches the spec from issue #21).

### Test plan for Task 8

- `internal/jjtest` already creates real jj repos. Add a fixture helper for
  "tracked repo with N workspaces and bookmarks" if needed.
- Integration test: `wgo add WGO-X --no-push --no-spec` creates a workspace
  at the expected path with the expected bookmark on `@-`.
- Integration test: `wgo to <local-PR-like-ref>` fetches and creates a
  workspace at `pr-N-<slug>/owner/repo`.
- Existing `internal/cmd/add_test.go` (if any) needs rewriting to use
  `jjtest` setup; tests that mock `git.Client` go away.

### Acceptance — Task 8

- `wgo add WGO-XYZ` against a real jj-tracked repo creates the workspace and
  bookmark, writes the spec scaffold, and pushes the bookmark — no `.git/`
  directory anywhere along the path.
- `wgo to <PR URL>` fetches and checks out the PR's head ref in a new
  workspace.
- `go build ./internal/cmd/...` clean. No `git.` references remain in
  `internal/cmd/add.go` or `internal/cmd/to.go`.

---

## Task 9 — Rewire the remaining cmd files

Each file below currently imports `github.com/virtru/wgo/internal/git`. The
work is mostly mechanical: swap `git.Client` for `jj.Client`, replace the
specific method calls per the mapping table below.

### Files

| File | git.* usage |
|---|---|
| `internal/cmd/ls.go` | `git.New(repo.Path)` |
| `internal/cmd/clean.go` | `git.New("")` (two sites), `git.Client` in helpers, `RemoveWorktree` |
| `internal/cmd/checkout.go` | `git.New("").MainRepoPath(path)` |
| `internal/cmd/dot.go` | `git.NewFromCwd()`, `*git.CLIClient` arg in `showSiblings`, dozens of method calls (status, branch, remote, last commit, etc.) |
| `internal/cmd/status.go` | `git.New("")` + `exec.Command("gh", "pr", "view", "--web")` |
| `internal/cmd/plan.go` | `git.NewFromCwd()` |
| `internal/cmd/join.go` | `git.New("")` + `exec.Command("git", "rev-parse", "--show-toplevel")` |
| `internal/cmd/track.go` | `git.New(absPath)` |
| `internal/cmd/spec.go` | `git.New("")` (4 sites) + `exec.Command("git", "rev-parse", "--show-toplevel")` |
| `internal/cmd/pr.go` | `exec.Command("gh", "pr", "view", ..., "--web")` |
| `internal/cmd/today.go` | 5× `exec.Command("git", ...)` calls (branch, status, remote, log) |
| `internal/cmd/clean_test.go` | uses `git.Client` mocks |

### Mapping table (apply uniformly)

| Old git call | jj/github replacement |
|---|---|
| `git.New(path)` / `git.NewFromCwd()` | `jj.NewCLI()` (no per-call path needed; methods take `repo string`) |
| `(*CLIClient).IsRepo(path)` | `jjc.IsRepo(path)` (returns `bool`, not `(bool, error)`) |
| `(*CLIClient).RepoName(path)` | `filepath.Base(jjc.Root(path))` |
| `(*CLIClient).CurrentBranch(path)` | `jjc.CurrentChange(path).Bookmarks[0]` (empty string when anonymous) |
| `(*CLIClient).Status(path)` | `jjc.Status(path)` — different return shape; add a small converter in `cmd/` for the existing `models.GitStatus` consumers, or migrate consumers to `jj.Status` |
| `(*CLIClient).LastCommit(path)` | `jjc.Log(path, "@")[0]` (returns `[]jj.LogEntry`; map fields) |
| `(*CLIClient).AheadBehind(path, branch)` | revset: `count(remote_bookmarks(exact:B, remote=origin)..bookmarks(exact:B))` and the reverse — expose a `jj.AheadBehind(repo, bookmark) (ahead, behind int, err)` helper in `internal/jj` |
| `(*CLIClient).RemoteURL(path)` | `jjc.RemoteURLs(path)["origin"]` |
| `(*CLIClient).DefaultBranch(path)` | `internal/github.DefaultBranch(slug)` (slug from `jjc.RemoteURLs`) |
| `(*CLIClient).RootDir(path)` | `jjc.Root(path)` |
| `(*CLIClient).MainRepoPath(path)` | For pure jj, this is `jjc.Root(path)` of the main workspace; expose `jj.MainWorkspaceRoot(path) (string, error)` that finds the main workspace via `ListWorkspaces` |
| `(*CLIClient).ListWorktrees(path)` | `jjc.ListWorkspaces(path)` — call sites change `WorktreeInfo` → `jj.Workspace` |
| `(*CLIClient).RemoveWorktree(repo, path, force)` | `jjc.WorkspaceForget(repo, name)` + `os.RemoveAll(path)` |
| `(*CLIClient).Push(path, branch)` | `jjc.GitPush(path, jj.PushOpts{Bookmarks: []string{branch}})` |
| `exec.Command("git", "rev-parse", "--show-toplevel")` | `jjc.Root(cwd)` |
| `exec.Command("git", "-C", path, "branch", "--show-current")` | `jjc.CurrentChange(path).Bookmarks[0]` |
| `exec.Command("git", "-C", path, "status", "--short")` | `jjc.Status(path)` |
| `exec.Command("git", "-C", path, "remote", "get-url", "origin")` | `jjc.RemoteURLs(path)["origin"]` |
| `exec.Command("git", "-C", path, "log", "-1", "--format=%s")` | `jjc.Log(path, "@")[0].Description` (first line) |
| `exec.Command("gh", "pr", "view", "--web", ...)` | Build the PR URL from `github.GetPRStatus(...).URL` and shell out to `open` / xdg-open, or add an `OpenInBrowser(url)` helper in `internal/links` |

### Additions needed in `internal/jj`

Add the following methods to keep cmd-layer rewires straightforward. Each
should be tested against real jj in `internal/jj/jj_test.go`.

| Method | Implementation |
|---|---|
| `AheadBehind(repo, bookmark string) (ahead, behind int, err error)` | Two revsets: `remote_bookmarks(exact:B, remote=origin)..bookmarks(exact:B)` (ahead) and `bookmarks(exact:B)..remote_bookmarks(exact:B, remote=origin)` (behind). Use `jj log --no-graph -T '"x\n"' -r <revset>` and count lines. |
| `MainWorkspaceRoot(path string) (string, error)` | `jjc.ListWorkspaces(jjc.Root(path))`, identify the workspace whose Path equals `jjc.Root(path)`'s "main" workspace. The discovery package already does similar detection — extract the logic so both packages share it. |
| `DiffStat(repo, revset string) (added, deleted int, err error)` | `jj diff -r REV --stat` parsed for `N insertions(+), M deletions(-)`. Used by `wgo dot` for status display. |

### `internal/status/collector.go`

Heavy git user: walks worktrees, gathers status, ahead/behind, last commit
across many repos in parallel. Replace `gitClient git.Client` field with
`jjc jj.Client`. Map every git method call per the table above. The
parallel-collection structure (goroutines, sort) is untouched.

### `internal/cleanup/cleanup.go`

`FindCandidates` and `findRepoCandidate` use `gitClient git.Client` for
worktree listing, branch listing, merged detection. Replace with
`jjc jj.Client`. `isWorktreeBranch` takes `[]git.WorktreeInfo` — change to
`[]jj.Workspace`.

### Test plan for Task 9

- `internal/cmd/clean_test.go` currently mocks `git.Client`; rewrite using
  `jjtest.NewRepo` plus real workspace creation. Mocks of the github HTTP
  client survive (it's an interface).
- `internal/status/collector_test.go` similarly: use `jjtest` to set up
  multi-workspace fixtures.
- For each command rewire, run the existing in-package tests; add new ones
  only where coverage drops.

### Acceptance — Task 9

- No `import "github.com/virtru/wgo/internal/git"` in any `internal/cmd/*.go`
  or `internal/cleanup/*.go` or `internal/status/*.go`.
- No `exec.Command("git", ...)` in any `internal/cmd/*.go`.
- No `exec.Command("gh", ...)` in any `internal/cmd/*.go` (browser-open
  becomes a `links` helper).
- `go build ./...` clean. `go test ./...` passes for all rewired packages.

---

## Task 10 (remainder) — `internal/contrib`, `internal/pilot`

`internal/spec` and `internal/config` were rewired in the predecessor PR.
This task covers the two remaining packages with direct git/gh shell-outs.

### `internal/contrib/collector.go`

**Current behavior:** Counts commits per day per repo, optionally filtered
by author; resolves GitHub URLs from `git remote get-url`; lists commits
with their changed files.

**Specific edits:**

| Line | Current | Replacement |
|---|---|---|
| 84-95 (`collectRepo`) | `git -C path log --oneline --format=%cd --date=format:%Y-%m-%d --since=S [--author=A]` | `jjc.Log(path, revset)` where revset is `author_date(after:S) [& author(exact:A)]`. Iterate entries, bucket by `AuthorTimestamp.Format("2006-01-02")`. |
| 130-141 (`resolveGitHubURL`) | `git -C path remote get-url upstream/origin` | `jjc.RemoteURLs(path)["upstream"]`, fall back to `["origin"]`. |
| 248-267 (`collectRepoCommits`) | `git branch --show-current` + `git log --format=%h%x09%s --name-only` | `jjc.CurrentChange(path).Bookmarks[0]` for branch. For commits with file lists: `jjc.Log(repo, revset)` for hashes + descriptions, then per-commit `jj diff -r REV --name-only` — add a `jj.ChangedFiles(repo, revset string) ([]string, error)` helper, or expose `Diff(workspacePath, revset) ([]string, error)`. |

### `internal/pilot/summary.go`

**Current behavior:** spec-change counts per author for the pilot dashboard;
merged-PR counts via `gh pr list`; review iteration counts via `gh pr view`.

**Specific edits:**

| Line | Current | Replacement |
|---|---|---|
| 154-183 (`gitLogSpecFiles`) | `git log --format=%H\x1f%ae\x1f%aI --since=S --until=U -- spec/*.md` | `jjc.Log(repoRoot, "author_date(after:S, before:U) & files(\"spec/*.md\")")` — returns `[]jj.LogEntry` with `AuthorEmail` and `AuthorTimestamp`. **Note:** the current `jj.LogEntry` template doesn't include author email; extend `LogEntryTemplate` to add `author.email().escape_json()` and surface it as `LogEntry.AuthorEmail` (bump `TemplateSchemaVersion` if anyone external reads it). |
| 186-205 (`gitCountNoSpec`) | `git log --grep=[no-spec] --fixed-strings --since=S --until=U` | `jjc.Log(repoRoot, "description(substring-i:\"[no-spec]\") & author_date(after:S, before:U)")` and `len(entries)`. |
| 216-244 (`ghListMergedPRs`) | `gh pr list --search "author:X merged:>=S merged:<=U" --state merged` | Use `internal/github.SearchPRs(query, opts)`. If that method doesn't exist on the new HTTP client, add it: `SearchPRs(query string) ([]ExtendedPRInfo, error)` calling `GET /search/issues?q=...+is:pr`. The github subagent's report mentioned `ListMyOpenPRs` / `ListInvolvedPRs` already use the search endpoint — extract the shared helper. |
| 247-280 (`ghReviewIterations`) | `gh pr view N --repo X --json reviews` | Use a new `internal/github.GetPRReviews(slug, number) ([]ReviewSubmission, error)` (or surface what's already in `ListMyReviewsToday`). |

### `internal/pilot/summary_test.go` failures

The current `internal/pilot/summary_test.go` has 4 failing tests
(`TestCollect_SpecsCreated`, `TestCollect_SpecsUpdated`,
`TestCollect_NoSpecOverrides`, `TestCollect_SpecEditsByAuthor`). These
predate the gh-21 work but should be fixed as part of this task — they
rely on `gitLogSpecFiles` behaviour and need to be ported alongside the
implementation. Use `jjtest` fixtures.

### Acceptance — Task 10

- No `exec.Command("git", ...)` or `exec.Command("gh", ...)` in
  `internal/contrib/` or `internal/pilot/`.
- `internal/pilot/summary_test.go` passes (4 currently failing tests are
  now green).
- `go test ./internal/contrib/... ./internal/pilot/...` clean.

---

## Task 11 — Delete `internal/git/` and verify

### Preconditions

- Tasks 8, 9, 10 above are complete.
- `grep -rln 'virtru/wgo/internal/git"' --include='*.go'` returns **only**
  `internal/git/` itself. Anything else listed is a missed rewire.

### Steps

1. `git rm -r internal/git/`.
2. Delete `internal/git/git_test.go` (already inside the deleted directory).
3. `go build ./...` — must succeed. Any failure is a missed import.
4. `go vet ./...` — must succeed.
5. `go test -count=1 ./...` — must pass. CI installs `jj` so integration
   tests can run; document this if not already.
6. Update **`README.md`**:
   - Replace the git prerequisite with `jj-vcs` (link to install instructions).
   - Document the removed features: passive hooks (`wgo hooks`), cross-repo
     stacking, `wgo stack restack` (jj auto-restacks), `wgo stack
     {new,push,rm,adopt}` (jj's DAG is authoritative).
   - Document the no-migration policy for old `~/.wgo/state.json` files
     ("delete it and re-run wgo").
   - Note the `gh auth token` / `GITHUB_TOKEN` credential bootstrap for the
     HTTP-only github client.
   - Add a "Tradeoffs" section calling out: external tools that need `.git/`
     (IDE git plugins, `pre-commit` framework, `direnv` git features) won't
     work in wgo workspaces.
7. Update **`.github/workflows/*.yml`** to install jj before running tests.
   Single-binary download from the jj GitHub releases page; no pinned version
   per gh-21 constraint.
8. Update **CLAUDE.md** if it references git-specific architecture; the
   project still uses git for tooling around the wgo repo itself (CI,
   contributors), so keep the Conventional Commits + Gitmoji guidance.

### Verification

End-to-end manual test (run by a human after merge):

```bash
rm ~/.wgo/state.json
brew install jj   # if not already installed
cd /some/empty/dir
jj git init
jj describe -m initial
wgo track .
wgo add WGO-TEST --purpose "manual jj smoke"
# verify workspace + bookmark exist
cd ~/dev/WGO-TEST/<owner>/<repo>
echo hi > t.txt
jj describe -m "test commit"
jj bookmark set WGO-TEST -r @-
jj git push --bookmark WGO-TEST --allow-new
wgo sync   # should report PR-base alignment or "no changes"
wgo doctor # should report no issues
wgo ls     # lists the new workspace
wgo to https://github.com/some/repo/pull/N   # fetches and checks out the PR
wgo clean  # removes merged workspaces
```

If all of these succeed against a real jj install, gh-21 is complete and
issue #21 can be closed.

### Acceptance — Task 11

- `internal/git/` directory does not exist.
- `go build ./...`, `go vet ./...`, `go test ./...` all green.
- `grep -r '"github.com/virtru/wgo/internal/git"' --include='*.go'` returns
  zero hits.
- `grep -r 'exec.Command("git"' --include='*.go'` returns zero hits.
- `grep -r 'exec.Command("gh"' --include='*.go'` returns exactly one hit
  (`internal/github/auth.go`).
- README documents the jj prerequisite, removed features, and the no-migration
  policy.
- CI workflow installs `jj` before tests.

---

## Risks and open questions

1. **`jj.AheadBehind` performance.** Two revsets per bookmark per repo per
   `wgo .` / `wgo status` call. With many bookmarks, this could be slow.
   Mitigation: cache via `internal/cache` with a short TTL; revisit if
   `wgo status -w` (watch mode) feels sluggish.

2. **`jj log` template stability across versions.** The current
   `TemplateSchemaVersion = 1` covers fields we use today. Adding
   `author.email()` for Task 10 will bump the schema. Add a smoke test that
   parses a known-good template result against the local `jj` binary; if it
   fails, surface the version with a clear error.

3. **`gh` shell-out in `wgo pr open --web` and `wgo status --web`.**
   The cleanest fix is a small `internal/links.OpenInBrowser(url)` helper
   that shells out to `open` (macOS), `xdg-open` (Linux), or `cmd /c start`
   (Windows). Building a PR URL from `github.GetPRStatus().URL` is then
   trivial. This counts as a Task 9 deliverable.

4. **Pre-existing `internal/pilot` test failures predate this PR.**
   Fixing them is part of Task 10 (rewire to jj revsets — the current
   tests assume a git fixture). Do not silently keep them failing; the
   acceptance criterion requires `go test ./...` clean.

5. **Bookmark advancement.** Every command that creates a jj commit must
   call `BookmarkSet(name, "@-", false)` before pushing. Audit Task 8 and
   Task 9 changes specifically — add a small helper
   `jj.NewAndAdvance(workspacePath, bookmark, msg)` if the pattern appears
   more than once or twice.

6. **`wgo stack status` deletion.** Task 5 deleted the entire
   `internal/cmd/stack.go`, including the read-only `stack status` view.
   Some users may have scripts that call it. Decide before merge: restore
   it as `wgo dag` (reading `jj log -r 'bookmarks()'`), or document the
   removal in the changelog.

7. **`wgo dot` (`wgo .`) stack line.** Task 5 stubbed out the stack-line
   rendering in `wgo .` output. The PR description for the final merge
   should call this out as known-missing; a follow-up issue can restore it
   via `internal/sync.BuildFromLog` (the building blocks exist).

## Out of scope

- jj version pinning or runtime version check (`gh-21` constraint).
- Migration tooling for `~/.wgo/state.json` v1 (`gh-21` constraint).
- Cross-repo stacked PRs (`gh-21` constraint).
- Restoring the deleted `wgo stack {new,push,restack,rm,adopt,sync}`
  subcommands. `wgo sync` (top-level) and `jj`'s native DAG cover the
  use cases.
- Colocated jj repos (pure jj only, per `gh-21`). **Superseded:** see the
  constraints section above.
