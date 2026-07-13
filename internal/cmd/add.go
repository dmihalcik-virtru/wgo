package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/bujo"
	"github.com/virtru/wgo/internal/config"
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jira"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/store"
)

var (
	addPriority    bool
	addRepos       []string
	addNoSpec      bool
	addSpecRepo    string
	addJira        bool
	addJiraProject string
	addJiraType    string
)

type repoSpec struct {
	owner string
	repo  string
}

func (r repoSpec) String() string { return r.owner + "/" + r.repo }

func parseRepoSpecs(repos []string) ([]repoSpec, error) {
	specs := make([]repoSpec, 0, len(repos))
	for _, r := range repos {
		parts := strings.SplitN(r, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid repo %q: expected owner/repo", r)
		}
		specs = append(specs, repoSpec{owner: parts[0], repo: parts[1]})
	}
	return specs, nil
}

var addCmd = &cobra.Command{
	Use:   "add [TICKET] <task description> [-r owner/repo]...",
	Short: "Add a task to the plan, optionally creating worktrees",
	Long: `Add a bullet journal task to the Tasks section of plan.md.

If the first argument is a Jira ticket ID (e.g. DSPX-2674), also creates
git worktrees for one or more repos, branching from latest main:

  wgo add DSPX-2674 remove volume directive
  wgo add DSPX-2674 remove volume directive -r virtru/platform -r virtru/cli

When run from inside a git checkout, that repo is used by default.
Each repo gets a new branch and worktree at:
  <worktrees_dir>/<ticket-slug>/<repo>

The branches are pushed to origin. The shared root is printed to stdout
so you can cd into it:
  cd $(wgo add DSPX-2674 my task)

Plain task (no ticket):
  wgo add fix the login bug
  wgo add -p ship v2 release`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if addJira {
			if len(args) >= 1 && isJiraTicket(args[0]) {
				return fmt.Errorf("--jira creates a new ticket; pass a description, not an existing ticket key")
			}
			return runAddWithJiraCreate(joinArgs(args), addRepos, addPriority)
		}
		if len(args) >= 1 && isJiraTicket(args[0]) {
			ticket := args[0]
			desc := joinArgs(args[1:])
			return addWithWorktree(ticket, desc, addRepos, addPriority)
		}
		return addTask(joinArgs(args), addPriority)
	},
}

func init() {
	rootCmd.AddCommand(addCmd)
	addCmd.Flags().BoolVarP(&addPriority, "priority", "p", false, "Mark as priority task")
	addCmd.Flags().StringArrayVarP(&addRepos, "repo", "r", nil, "owner/repo to create worktree for (repeatable)")
	addCmd.Flags().BoolVar(&addNoSpec, "no-spec", false, "Skip spec scaffold commit")
	addCmd.Flags().StringVar(&addSpecRepo, "spec-repo", "", "owner/repo to write spec into (default: first -r repo)")
	addCmd.Flags().BoolVar(&addJira, "jira", false, "Create a new Jira work item first, then proceed with the ticket")
	addCmd.Flags().StringVar(&addJiraProject, "jira-project", "", "Jira project key for new ticket (default: jira.default_project in config)")
	addCmd.Flags().StringVar(&addJiraType, "jira-type", "", "Jira issue type for new ticket (default: jira.default_type in config, e.g. \"Task\")")
}

func joinArgs(args []string) string {
	return strings.Join(args, " ")
}

func addTask(text string, priority bool) error {
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	if err := s.EnsureDir(); err != nil {
		return err
	}

	content, err := s.LoadPlan()
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	p, err := plan.Parse(content)
	if err != nil {
		return fmt.Errorf("failed to parse plan: %w", err)
	}

	bullet := bujo.BulletOpen
	if priority {
		bullet = bujo.BulletPriority
	}

	p.AddTask(bullet, text)

	if err := s.SavePlan(p.Render()); err != nil {
		return fmt.Errorf("failed to save plan: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Added task: %s %s\n", string(bullet), text)
	return nil
}

func addWithWorktree(ticket, desc string, repos []string, priority bool) error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()
	if cfg.Worktree.WorktreesDir == "" {
		return fmt.Errorf("worktree.worktrees_dir not configured; set it in ~/.wgo/config.toml")
	}
	jjc := jj.NewCLI()

	// Resolve repos: default to current repo if none specified.
	if len(repos) == 0 {
		ownerRepo, err := detectCurrentJJRepo(jjc)
		if err != nil {
			return fmt.Errorf("not in a jj repo with a GitHub remote; pass -r owner/repo: %w", err)
		}
		repos = []string{ownerRepo}
	}

	// Validate and split owner/repo entries.
	specs, err := parseRepoSpecs(repos)
	if err != nil {
		return err
	}

	// Validate --spec-repo if provided (fail-fast before any worktree creation)
	specRepoIdx := 0
	if addSpecRepo != "" {
		found := false
		for i, sp := range specs {
			if sp.owner+"/"+sp.repo == addSpecRepo {
				specRepoIdx = i
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("--spec-repo %q is not in the -r list", addSpecRepo)
		}
	}

	// Build branch name and shared root dir name.
	branchName := slugTicketBranch(ticket, desc)
	if branchName == "" || strings.HasSuffix(branchName, "-") {
		return fmt.Errorf("could not generate valid branch name from ticket=%q desc=%q", ticket, desc)
	}
	sharedDirName := truncateSlug(branchName, 30)
	sharedRoot := filepath.Join(cfg.Worktree.WorktreesDir, sharedDirName)

	fmt.Fprintf(os.Stderr, "branch: %s\n", branchName)
	fmt.Fprintf(os.Stderr, "shared root: %s\n", sharedRoot)

	// For each repo: find/clone, fetch, create workspace, push.
	var specRepoPath string
	for i, spec := range specs {
		repoPath, err := findOrCloneRepo(jjc, cfg, spec.owner, spec.repo)
		if err != nil {
			return fmt.Errorf("repo %s/%s: %w", spec.owner, spec.repo, err)
		}

		if i == specRepoIdx {
			specRepoPath = repoPath
		}

		fmt.Fprintf(os.Stderr, "fetching %s/%s...\n", spec.owner, spec.repo)
		if err := jjc.GitFetch(repoPath, "", nil); err != nil {
			fmt.Fprintf(os.Stderr, "warning: fetch failed for %s/%s (using cached state): %v\n", spec.owner, spec.repo, err)
		}

		defaultBranch, err := defaultBranchFor(jjc, repoPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not detect default branch for %s/%s, assuming 'main': %v\n", spec.owner, spec.repo, err)
			defaultBranch = "main"
		}

		wtPath := filepath.Join(sharedRoot, spec.repo)
		if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(wtPath), err)
		}

		if err := ensureWorkspaceAndBookmark(jjc, repoPath, branchName, wtPath, defaultBranch, spec.repo); err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "pushing %s...\n", branchName)
		if _, err := jjc.GitPush(repoPath, jj.PushOpts{Bookmarks: []string{branchName}, AllowNew: true}); err != nil && !errors.Is(err, jj.ErrNothingToPush) {
			return fmt.Errorf("push %s: %w", spec.repo, err)
		}

		fmt.Fprintf(os.Stderr, "created: %s\n", wtPath)
	}

	// Write and commit spec.
	specRel := filepath.Join("spec", ticket+".md")
	specWtPath := filepath.Join(sharedRoot, specs[specRepoIdx].repo)
	specAbs := filepath.Join(specWtPath, specRel)

	if !addNoSpec {
		branches := make([]string, len(specs))
		for i, sp := range specs {
			branches[i] = sp.owner + "/" + sp.repo + ":" + branchName
		}
		specWritten := false
		if _, statErr := os.Stat(specAbs); os.IsNotExist(statErr) {
			specTitle, specDesc, specPriority := fetchJiraEnrichment(ticket, desc)
			if specTitle != desc || specDesc != desc {
				fmt.Fprintf(os.Stderr, "enriched spec from Jira: %s\n", ticket)
			}

			data, err := spec.RenderTemplate(spec.TemplateData{
				Ticket:      ticket,
				Title:       specTitle,
				Description: specDesc,
				Authors:     []string{cfg.Author},
				Branches:    branches,
				Now:         time.Now(),
			})
			if err != nil {
				return fmt.Errorf("render spec template: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(specAbs), 0o755); err != nil {
				return fmt.Errorf("mkdir spec dir: %w", err)
			}
			if err := os.WriteFile(specAbs, data, 0o644); err != nil {
				return fmt.Errorf("write spec: %w", err)
			}
			if specPriority != "" {
				_ = spec.UpdateFrontmatter(specAbs, func(fm *spec.Frontmatter) error {
					fm.JiraPriority = specPriority
					return nil
				})
			}
			specWritten = true
		} else {
			fmt.Fprintf(os.Stderr, "spec already exists: %s (skipping)\n", specRel)
		}
		// Only run the commit dance when there is actually something uncommitted
		// to capture: either we just wrote the spec this run, or a prior run
		// wrote it but crashed before committing (dirty @). On a clean re-run the
		// spec is already committed and the bookmark already points at it, so
		// skipping here avoids re-describing @ and moving the bookmark onto a
		// fresh empty change.
		clean, _, _ := jjc.IsClean(specWtPath)
		if specWritten || !clean {
			// jj snapshots the spec file into the workspace's @ automatically;
			// describe it to give the change a message, capture its commit id,
			// then start a fresh empty change so the WC is clean. Bookmarks
			// don't auto-advance with `jj new`, so explicitly move the
			// branchName bookmark to the spec commit before pushing.
			msg := fmt.Sprintf("spec: scaffold for %s\n\n%s", ticket, desc)
			if err := jjc.Describe(specWtPath, msg); err != nil {
				return fmt.Errorf("describe spec change: %w", err)
			}
			specChange, err := jjc.CurrentChange(specWtPath)
			if err != nil {
				return fmt.Errorf("read spec change: %w", err)
			}
			if err := jjc.New(specWtPath, "", ""); err != nil {
				return fmt.Errorf("new change after spec: %w", err)
			}
			if err := jjc.BookmarkSet(specRepoPath, branchName, specChange.CommitID, false); err != nil {
				return fmt.Errorf("set bookmark %s to spec: %w", branchName, err)
			}
			if _, err := jjc.GitPush(specRepoPath, jj.PushOpts{Bookmarks: []string{branchName}, AllowNew: true}); err != nil && !errors.Is(err, jj.ErrNothingToPush) {
				return fmt.Errorf("push spec: %w", err)
			}
		}
	}

	// Update plan.
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := s.EnsureDir(); err != nil {
		return fmt.Errorf("store ensure dir: %w", err)
	}
	content, err := s.LoadPlan()
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	p, err := plan.Parse(content)
	if err != nil {
		return fmt.Errorf("parse plan: %w", err)
	}

	bullet := bujo.BulletOpen
	if priority {
		bullet = bujo.BulletPriority
	}
	taskText := ticket
	if desc != "" {
		taskText += " " + desc
	}
	p.AddTask(bullet, taskText)

	for i, sp := range specs {
		relSpec := ""
		if i == specRepoIdx && !addNoSpec {
			relSpec = specRel
		}
		p.AddBranch(sp.repo, branchName, ticket+": "+desc, relSpec)
	}

	if err := s.SavePlan(p.Render()); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	// Update state annotation with spec path
	if !addNoSpec {
		_ = s.MutateState(func(state *store.State) (bool, error) {
			state.SetSpec(specRepoPath, branchName, specRel, "draft")
			return true, nil
		})
	}

	fmt.Fprintf(os.Stderr, "Added task: %s %s\n", string(bullet), taskText)
	fmt.Println(sharedRoot)
	return nil
}

// runAddWithJiraCreate creates a new Jira work item then proceeds with the
// full wgo add TICKET workflow using the returned ticket key.
func runAddWithJiraCreate(desc string, repos []string, priority bool) error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()

	// Resolve ownerRepo for rule matching: prefer first -r flag, fall back to cwd repo.
	ownerRepo := ""
	if len(repos) > 0 {
		ownerRepo = repos[0]
	} else {
		jjc := jj.NewCLI()
		if r, err := detectCurrentJJRepo(jjc); err == nil {
			ownerRepo = r
		}
	}
	cwd, _ := resolveCwd()
	project, issueType := cfg.Jira.ResolveProject(ownerRepo, cwd)

	// Explicit flags take priority over resolved values.
	if addJiraProject != "" {
		project = addJiraProject
	}
	if addJiraType != "" {
		issueType = addJiraType
	}

	if project == "" {
		return fmt.Errorf("missing Jira project key: pass --jira-project, set jira.default_project, or add a matching [[jira.project_rules]] entry in ~/.wgo/config.toml")
	}
	if issueType == "" {
		return fmt.Errorf("missing Jira issue type: pass --jira-type or set jira.default_type in ~/.wgo/config.toml")
	}

	ticket, err := jira.CreateIssue(project, desc, issueType, "")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "created Jira ticket: %s\n", ticket)
	return addWithWorktree(ticket, desc, repos, priority)
}

// fetchJiraEnrichment silently tries to fetch spec enrichment data from Jira.
// Returns (fallback, fallback, "") on any error so the caller always gets usable values.
func fetchJiraEnrichment(ticket, fallback string) (title, description, priority string) {
	issue, err := jira.GetIssue(ticket)
	if err != nil {
		return fallback, fallback, ""
	}
	title = issue.Fields.Summary
	if title == "" {
		title = fallback
	}
	description = issue.Fields.DescriptionText()
	if description == "" {
		description = fallback
	}
	priority = issue.Fields.Priority.Name
	return
}

// detectCurrentJJRepo returns "owner/repo" for the jj repo containing the cwd.
// Prefers the "origin" remote; falls back to the first remote found.
func detectCurrentJJRepo(jjc jj.Client) (string, error) {
	cwd, err := resolveCwd()
	if err != nil {
		return "", err
	}
	root, err := jjc.Root(cwd)
	if err != nil {
		return "", fmt.Errorf("jj root: %w", err)
	}
	remotes, err := jjc.RemoteURLs(root)
	if err != nil || len(remotes) == 0 {
		return "", fmt.Errorf("no remote URL found in %s", root)
	}
	url := remotes["origin"]
	if url == "" {
		for _, u := range remotes {
			url = u
			break
		}
	}
	ownerRepo := extractOwnerRepo(url)
	if ownerRepo == "" {
		return "", fmt.Errorf("could not parse owner/repo from remote %s", url)
	}
	return ownerRepo, nil
}

// defaultBranchFor returns the default branch of the GitHub repo backing
// repoPath. Resolves owner/repo from jj's origin remote, then calls the
// GitHub API.
// workspaceBookmarkClient is the narrow slice of jj.Client that
// ensureWorkspaceAndBookmark needs. Keeping it small makes the idempotency
// logic easy to unit-test with a fake. jj.Client satisfies it.
type workspaceBookmarkClient interface {
	ListWorkspaces(repo string) ([]jj.Workspace, error)
	WorkspaceAdd(repo, name, dest, revset string) error
	BookmarkList(repo string, opts jj.BookmarkListOpts) ([]jj.Bookmark, error)
	BookmarkCreate(repo, name, revset string) error
}

// ensureWorkspaceAndBookmark creates the workspace and bookmark for branchName
// in repoPath, basing both at <defaultBranch>@origin. It is idempotent: if a
// prior (possibly failed) run already registered the workspace or bookmark, the
// corresponding step is skipped so `wgo add` can be safely re-run. An existing
// bookmark is left where it points — it may already carry commits — rather than
// being reset. repoLabel is used only for error messages.
func ensureWorkspaceAndBookmark(jjc workspaceBookmarkClient, repoPath, branchName, wtPath, defaultBranch, repoLabel string) error {
	wss, _ := jjc.ListWorkspaces(repoPath)
	if slices.ContainsFunc(wss, func(w jj.Workspace) bool { return w.Name == branchName }) {
		fmt.Fprintf(os.Stderr, "workspace %s exists, skipping\n", branchName)
	} else {
		fmt.Fprintf(os.Stderr, "creating workspace %s...\n", wtPath)
		if err := jjc.WorkspaceAdd(repoPath, branchName, wtPath, defaultBranch+"@origin"); err != nil {
			return fmt.Errorf("workspace add %s: %w", repoLabel, err)
		}
	}

	bms, _ := jjc.BookmarkList(repoPath, jj.BookmarkListOpts{Local: true})
	if slices.ContainsFunc(bms, func(b jj.Bookmark) bool { return b.Name == branchName }) {
		fmt.Fprintf(os.Stderr, "bookmark %s exists, skipping\n", branchName)
	} else if err := jjc.BookmarkCreate(repoPath, branchName, defaultBranch+"@origin"); err != nil {
		return fmt.Errorf("create bookmark %s: %w", branchName, err)
	}
	return nil
}

func defaultBranchFor(jjc jj.Client, repoPath string) (string, error) {
	remotes, err := jjc.RemoteURLs(repoPath)
	if err != nil {
		return "", err
	}
	url := remotes["origin"]
	if url == "" {
		return "", fmt.Errorf("no origin remote in %s", repoPath)
	}
	ownerRepo := extractOwnerRepo(url)
	if ownerRepo == "" {
		return "", fmt.Errorf("could not parse owner/repo from remote %s", url)
	}
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("unexpected owner/repo %q", ownerRepo)
	}
	return gh.RepoDefaultBranch(parts[0], parts[1])
}

var jiraRe = regexp.MustCompile(`^[A-Z]+-\d+$`)

func isJiraTicket(s string) bool { return jiraRe.MatchString(s) }

// slugTicketBranch builds "TICKET-slug-of-description" capped at 60 chars.
func slugTicketBranch(ticket, desc string) string {
	if desc == "" {
		return ticket
	}
	slug := gh.SanitizeBranch(strings.ToLower(desc))
	full := ticket + "-" + slug
	if len(full) > 60 {
		full = full[:60]
		full = strings.TrimRight(full, "-")
	}
	return full
}

// truncateSlug trims s to maxLen characters, cutting at the last dash boundary.
func truncateSlug(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	cut := s[:maxLen]
	if idx := strings.LastIndex(cut, "-"); idx > 0 {
		cut = cut[:idx]
	}
	return strings.TrimRight(cut, "-")
}
