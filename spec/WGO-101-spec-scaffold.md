---
ticket: WGO-101
title: Spec scaffold and plan integration
status: draft
authors: [dmihalcik-virtru, sujankota]
branches: []
prs: []
created: 2026-05-06
updated: 2026-05-06
phase: 1
estimate: 1d
depends_on: []
---

# WGO-101 — Spec scaffold and plan integration

## Summary

Add a `/spec/<TICKET>.md` file to the work of `wgo add`, parse it as a first-class artifact via a new `internal/spec` package, and surface it in `wgo .` and `wgo plan`. After this ticket, every branch created by `wgo add` ships with a committed spec stub on its first commit, and the spec path round-trips through plan and state.

## Problem / Motivation

The May 2026 spec-driven pair-programming pilot requires every work item to start with a written spec, committed to the repo before implementation. Today `wgo add` creates worktrees and pushes branches but writes nothing into them. Without help, engineers will forget to scaffold the spec, branch names won't match spec filenames, and the plan/state will lose the spec → branch mapping.

This ticket makes the spec the default path of least resistance: doing nothing extra still produces a spec. It is the foundation for WGO-102, WGO-103, and WGO-104, all of which assume specs exist and are linked to branches.

## Proposed Solution

1. **New `internal/spec/` package.** Owns parsing, locating, templating, and frontmatter updates for spec files. No commands here — pure model + I/O so the package is reusable from `wgo add`, `wgo .`, `wgo plan`, and (later) `wgo spec`, `wgo team`, and `wgo pilot-summary`.
2. **Extend `wgo add`** to write `<wt>/spec/<TICKET>.md` from a template, stage and commit it as `spec: scaffold for <TICKET>`, then push. Default on; opt out with `--no-spec`. For multi-repo `wgo add ... -r owner/a -r owner/b`, the spec lives in the **first repo** (or `--spec-repo owner/repo`); other repos get a plan branch entry whose reason references the canonical spec path.
3. **Extend the data model**:
   - `internal/plan/plan.go` — `BranchEntry.SpecPath`, `EffortEntry.SpecPath`. Round-trip through `Render()` and `parseActiveBranchLine`.
   - `internal/store/state.go` — `Annotation.SpecPath`, `Annotation.SpecState` (cached frontmatter status; refreshed lazily when the file's mtime exceeds the annotation's `UpdatedAt`).
4. **Surface in `wgo .`** — display a "Spec:" line: path + status when present, `⚠ no spec` otherwise.
5. **Surface in `wgo plan`** — render `📄 spec/WGO-101.md` after each branch's reason line.

## Inputs / Outputs / Contracts

### New package: `internal/spec/`

```go
// spec.go
type Status string
const (
    StatusDraft       Status = "draft"
    StatusInProgress  Status = "in_progress"
    StatusShipped     Status = "shipped"
    StatusAbandoned   Status = "abandoned"
)

type Frontmatter struct {
    Ticket    string    `yaml:"ticket"`
    Title     string    `yaml:"title,omitempty"`
    Status    Status    `yaml:"status"`
    Authors   []string  `yaml:"authors"`
    Branches  []string  `yaml:"branches"`           // "owner/repo:branch"
    PRs       []string  `yaml:"prs"`                // "owner/repo#123"
    Created   time.Time `yaml:"created"`
    Updated   time.Time `yaml:"updated"`
    Phase     int       `yaml:"phase,omitempty"`
    DependsOn []string  `yaml:"depends_on,omitempty"`
}

type SpecFile struct {
    Path        string            // absolute
    Frontmatter Frontmatter
    Sections    map[string]string // section name → body (Summary, Problem / Motivation, …)
    Body        string            // full markdown body after frontmatter
}

func Parse(path string) (*SpecFile, error)
func ParseBytes(data []byte) (*SpecFile, error)

// template.go (uses go:embed default_template.md)
type TemplateData struct {
    Ticket, Title, Description string
    Authors                    []string
    Branches                   []string
    Now                        time.Time
}
func RenderTemplate(d TemplateData) ([]byte, error)

// locate.go
func FindByTicket(repoRoot, ticket string) (string, error)   // returns absolute path or os.ErrNotExist
func FindByBranch(repoRoot, branch string) (string, error)   // parses TICKET from branch, then FindByTicket
func ParseTicketFromBranch(branch string) string             // "WGO-101-spec-foo" → "WGO-101"; "" if none

// frontmatter.go
func UpdateFrontmatter(path string, fn func(*Frontmatter) error) error  // safe round-trip; preserves body byte-for-byte
```

### Spec template (embedded as `internal/spec/default_template.md`)

```markdown
---
ticket: {{ .Ticket }}
title: {{ .Title }}
status: draft
authors: [{{ range $i, $a := .Authors }}{{ if $i }}, {{ end }}{{ $a }}{{ end }}]
branches: [{{ range $i, $b := .Branches }}{{ if $i }}, {{ end }}{{ $b }}{{ end }}]
prs: []
created: {{ .Now.Format "2006-01-02" }}
updated: {{ .Now.Format "2006-01-02" }}
---

# {{ if .Title }}{{ .Title }}{{ else }}{{ .Ticket }}{{ end }}

## Summary
{{ .Description }}

## Problem / Motivation
_Why does this work need to happen? What is the user/business pain?_

## Proposed Solution
_What will you build, at a functional level? Sketch the approach._

## Inputs / Outputs / Contracts
_Function signatures, data shapes, API contracts, CLI flags._

## Edge Cases & Constraints
_Boundary conditions, error states, performance limits, security considerations._

## Out of Scope
_What this work item explicitly does not cover._

## Acceptance Criteria
- [ ] _Clear, testable condition_
- [ ] _…_
```

### Modified `internal/plan/plan.go`

```go
type BranchEntry struct {
    Repo      string
    Branch    string
    Reason    string
    SpecPath  string    // NEW; relative to repoRoot, e.g. "spec/WGO-101.md"
    CreatedAt time.Time
}

type EffortEntry struct {
    Name        string
    Description string
    Branches    []string
    SpecPath    string  // NEW; for cross-repo efforts, points at the canonical spec
}
```

Render rule for branch line: `- **repo:branch** — reason 📄 spec/TICKET.md` (the 📄 emoji and path are appended only when `SpecPath` is non-empty). The parser extends the existing regex in `parseActiveBranchLine` with an optional trailing `\s+📄\s+(\S+)$` group.

### Modified `internal/store/state.go`

```go
type Annotation struct {
    Purpose   string    `json:"purpose"`
    SpecPath  string    `json:"spec_path,omitempty"`
    SpecState string    `json:"spec_state,omitempty"`  // cached: "draft"|"in_progress"|...
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

func (s *State) SetSpec(repoPath, branch, specPath, specState string)
```

`omitempty` on the new fields means existing `state.json` files round-trip without migration. `Version` stays at `1`.

### Modified `internal/cmd/add.go` (`addWithWorktree`)

Insert between the worktree-creation loop and the plan update (current line 191):

```go
specRepo := specs[0]
if specRepoFlag != "" {
    specRepo = parseSpecRepoFlag(specRepoFlag, specs)  // errors if not in -r list
}
specRepoWtPath := filepath.Join(sharedRoot, specRepo.repo)
specRel := filepath.Join("spec", ticket+".md")
specAbs := filepath.Join(specRepoWtPath, specRel)

if !noSpecFlag {
    if err := writeSpecStub(specAbs, ticket, desc, cfg, branchName, specs); err != nil {
        return fmt.Errorf("write spec: %w", err)
    }
    if err := gitClient.AddAndCommit(specRepoWtPath, specRel,
        fmt.Sprintf("spec: scaffold for %s\n\n%s", ticket, desc)); err != nil {
        return fmt.Errorf("commit spec: %w", err)
    }
}
```

`gitClient.AddAndCommit` is a thin new helper in `internal/git/client.go` — `git -C <wt> add <path>` then `git -C <wt> commit -m <msg> -- <path>`.

When updating the plan (current line 218 loop), pass the spec path:

```go
for _, sp := range specs {
    relSpec := ""
    if sp == specRepo && !noSpecFlag {
        relSpec = specRel
    }
    p.AddBranch(sp.repo, branchName, ticket+": "+desc, relSpec)
}
```

`AddBranch` gains a variadic `specPath ...string` arg to keep callers that don't yet know about specs working unchanged.

### `wgo .` change (`internal/cmd/dot.go` or wherever the cwd command lives)

```
Repo:    virtru/wgo
Branch:  WGO-101-spec-scaffold
Status:  3 modified, clean otherwise
Spec:    📄 spec/WGO-101.md (draft, updated 2026-05-06)
```

When `Annotation.SpecPath` is empty AND the branch name parses to a TICKET AND `spec/<TICKET>.md` doesn't exist, print:

```
Spec:    ⚠ no spec (run: wgo spec new WGO-101)
```

When the branch name doesn't parse to a ticket at all, the line is omitted.

## Edge Cases & Constraints

- **Spec already exists** (re-running `wgo add` on an existing branch): detect via `os.Stat`; do not overwrite. Log `spec already exists: spec/WGO-101.md (skipping)`. Still update plan/state to link.
- **Ticket has no Jira-style prefix** (e.g. `wgo add lowercase-stuff`): no `TICKET-NNN` match → spec filename falls back to the slug, but skip scaffold by default and warn. User can pass `--spec` to force.
- **Multi-repo with `--spec-repo` not in `-r` list**: error out before any worktree creation; fail-fast keeps rollback semantics intact.
- **Pre-existing rollback** in `addWithWorktree:114-121` — extend the rollback to also `git reset --hard HEAD~1` if the spec commit landed before failure (best-effort; if push has not happened, local-only).
- **Frontmatter clobber**: never touch the body during programmatic updates. `UpdateFrontmatter` re-emits via `yaml.Marshal` only the frontmatter block; body bytes are preserved (verified by round-trip test).
- **Author detection** for frontmatter `authors:`: use `cfg.Author`; if `[pair].teammate` is set in config (added in WGO-103), include it. Test: with no pair config, `authors: [dmihalcik]`; with pair, `authors: [dmihalcik, sujan]`.
- **`wgo .` performance**: reading frontmatter on every invocation adds ~0.5ms. Cache via `Annotation.SpecState`; refresh when spec file mtime > annotation `UpdatedAt`.
- **Migration**: `state.json` Version stays at 1 (additive fields with `omitempty`). Plan parser must accept old branch lines without the 📄 suffix — use an optional regex group. Test: parse a plan written before this ticket and re-render with no diff.

## Out of Scope

- `wgo spec` command family (covered by WGO-102).
- Drift detection beyond the mtime check (WGO-102).
- Pair-aware views (WGO-103).
- Pre-commit hook enforcement (WGO-104).
- Pulling Jira ticket title/description via Jira API (manual `desc` argument only).
- Cross-repo spec mirroring or symlinks — only one canonical spec per ticket.
- Editing the spec body programmatically (only frontmatter).

## Acceptance Criteria

- [ ] `internal/spec/` package compiles with `Parse`, `RenderTemplate`, `FindByTicket`, `FindByBranch`, `ParseTicketFromBranch`, `UpdateFrontmatter` exported and unit-tested
- [ ] `internal/spec/spec_test.go` covers: parse-with-frontmatter, parse-without-frontmatter, frontmatter round-trip preserves body bytes, `ParseTicketFromBranch` for `WGO-101`, `WGO-101-foo`, `feature-WGO-101`, `not-a-ticket`
- [ ] `wgo add WGO-999 "demo"` creates a worktree and a `spec/WGO-999.md` committed as `spec: scaffold for WGO-999`; the new branch has both the worktree's initial state and the spec commit
- [ ] `wgo add WGO-999 "demo" --no-spec` skips the spec; no commit is created on top of the worktree's initial state
- [ ] `wgo add WGO-999 "demo" -r virtru/a -r virtru/b` puts the spec in `virtru/a` only; `virtru/b`'s plan branch entry references the spec via plain path
- [ ] `wgo add WGO-999 "demo" -r virtru/a -r virtru/b --spec-repo virtru/b` puts the spec in `virtru/b`
- [ ] `wgo .` from the new worktree shows the spec line with status and last-updated date
- [ ] `wgo plan` renders the new branch with a 📄 emoji and relative spec path
- [ ] `~/.wgo/state.json` annotation for the new branch has `spec_path` and `spec_state: "draft"`
- [ ] Old `state.json` and `plan.md` files (without spec fields) parse and re-render with no diff aside from explicit changes
- [ ] Rolling back a failed `wgo add` (e.g. push fails on second repo) cleans up the spec commit on the first repo
