package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/bujo"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/git"
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/store"
)

var (
	addPriority bool
	addRepos    []string
	addNoSpec   bool
	addSpecRepo string
)

type repoSpec struct{ owner, repo string }

// createdWorktree tracks a worktree we created so we can roll it back on error.
type createdWorktree struct{ repoPath, wtPath string }

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
		if len(args) >= 1 && isJiraTicket(args[0]) {
			ticket := args[0]
			desc := joinArgs(args[1:])
			return addWithWorktree(ticket, desc, addRepos, addPriority, addNoSpec, addSpecRepo)
		}
		return addTask(joinArgs(args), addPriority)
	},
}

func init() {
	rootCmd.AddCommand(addCmd)
	addCmd.Flags().BoolVarP(&addPriority, "priority", "p", false, "Mark as priority task")
	addCmd.Flags().StringArrayVarP(&addRepos, "repo", "r", nil, "owner/repo to create worktree for (repeatable)")
	addCmd.Flags().BoolVar(&addNoSpec, "no-spec", false, "Skip writing spec stub")
	addCmd.Flags().StringVar(&addSpecRepo, "spec-repo", "", "owner/repo to place spec in (default: first -r)")
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

func addWithWorktree(ticket, desc string, repos []string, priority, noSpec bool, specRepo string) (retErr error) {
	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()
	if cfg.Worktree.WorktreesDir == "" {
		return fmt.Errorf("worktree.worktrees_dir not configured; set it in ~/.wgo/config.toml")
	}
	gitClient := git.New("")

	var created []createdWorktree
	defer func() {
		if retErr != nil {
			for _, wt := range created {
				fmt.Fprintf(os.Stderr, "rolling back worktree %s...\n", wt.wtPath)
				_ = gitClient.RemoveWorktree(wt.repoPath, wt.wtPath, true)
			}
		}
	}()

	// Resolve repos: default to current repo if none specified.
	if len(repos) == 0 {
		ownerRepo, err := detectCurrentRepo(gitClient)
		if err != nil {
			return fmt.Errorf("not in a git repo with a GitHub remote; pass -r owner/repo: %w", err)
		}
		repos = []string{ownerRepo}
	}

	// Validate and split owner/repo entries.
	specs := make([]repoSpec, 0, len(repos))
	for _, r := range repos {
		parts := strings.SplitN(r, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("invalid repo %q: expected owner/repo", r)
		}
		specs = append(specs, repoSpec{parts[0], parts[1]})
	}

	// Validate --spec-repo before creating any worktrees (fail-fast).
	specRepoIdx := 0
	if specRepo != "" {
		found := false
		for i, rs := range specs {
			if rs.owner+"/"+rs.repo == specRepo {
				specRepoIdx = i
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("--spec-repo %q is not in the -r list", specRepo)
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

	// For each repo: find/clone, fetch, create worktree, push.
	for _, rs := range specs {
		repoPath, err := findOrCloneRepo(gitClient, cfg, rs.owner, rs.repo)
		if err != nil {
			return fmt.Errorf("repo %s/%s: %w", rs.owner, rs.repo, err)
		}

		fmt.Fprintf(os.Stderr, "fetching %s/%s...\n", rs.owner, rs.repo)
		if err := gitClient.Fetch(repoPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: fetch failed for %s/%s (using cached state): %v\n", rs.owner, rs.repo, err)
		}

		defaultBranch, err := gitClient.DefaultBranch(repoPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not detect default branch for %s/%s, assuming 'main': %v\n", rs.owner, rs.repo, err)
			defaultBranch = "main"
		}

		wtPath := filepath.Join(sharedRoot, rs.repo)
		if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(wtPath), err)
		}

		fmt.Fprintf(os.Stderr, "creating worktree %s...\n", wtPath)
		if err := gitClient.WorktreeAdd(repoPath, wtPath, branchName, true, "origin/"+defaultBranch); err != nil {
			return fmt.Errorf("worktree add %s: %w", rs.repo, err)
		}
		created = append(created, createdWorktree{repoPath, wtPath})

		fmt.Fprintf(os.Stderr, "pushing %s...\n", branchName)
		if err := gitClient.Push(wtPath, branchName); err != nil {
			return fmt.Errorf("push %s: %w", rs.repo, err)
		}

		fmt.Fprintf(os.Stderr, "created: %s\n", wtPath)
	}

	// Write spec stub to the designated spec repo worktree.
	var specRel string
	if !noSpec && len(specs) > 0 {
		specWtPath := filepath.Join(sharedRoot, specs[specRepoIdx].repo)
		rel, err := scaffoldSpec(gitClient, cfg, specWtPath, ticket, desc, branchName, specs)
		if err != nil {
			return err
		}
		specRel = rel
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

	branchReason := ticket + ": " + desc
	for i, rs := range specs {
		rel := ""
		if i == specRepoIdx {
			rel = specRel
		}
		p.AddBranch(rs.repo, branchName, branchReason, rel)
	}

	if err := s.SavePlan(p.Render()); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	// Record spec in state so wgo . can surface it without parsing the file.
	if specRel != "" {
		if state, err := s.LoadState(); err == nil {
			specWtPath := filepath.Join(sharedRoot, specs[specRepoIdx].repo)
			state.SetSpec(specWtPath, branchName, specRel, string(spec.StatusDraft))
			_ = s.SaveState(state)
		}
	}

	fmt.Fprintf(os.Stderr, "Added task: %s %s\n", string(bullet), taskText)
	fmt.Println(sharedRoot)
	return nil
}

// scaffoldSpec writes a spec stub at <specWtPath>/spec/<ticket>.md, then
// commits and pushes it on branchName. If the file already exists, it logs a
// notice and skips writing/committing. Returns the spec path relative to the
// repo root (e.g. "spec/WGO-101.md").
func scaffoldSpec(gitClient *git.CLIClient, cfg *config.Config, specWtPath, ticket, desc, branchName string, specs []repoSpec) (string, error) {
	specRel := filepath.Join("spec", ticket+".md")
	specAbs := filepath.Join(specWtPath, specRel)

	if _, err := os.Stat(specAbs); !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "spec already exists: %s (skipping)\n", specRel)
		return specRel, nil
	}

	if err := writeSpecStub(specAbs, ticket, desc, cfg, branchName, specs); err != nil {
		return "", fmt.Errorf("write spec: %w", err)
	}
	msg := fmt.Sprintf("spec: scaffold for %s\n\n%s", ticket, desc)
	if err := gitClient.AddAndCommit(specWtPath, specRel, msg); err != nil {
		return "", fmt.Errorf("commit spec: %w", err)
	}
	if err := gitClient.Push(specWtPath, branchName); err != nil {
		return "", fmt.Errorf("push spec commit: %w", err)
	}
	return specRel, nil
}

// writeSpecStub renders the spec template and writes it to absPath.
func writeSpecStub(absPath, ticket, desc string, cfg *config.Config, branchName string, specs []repoSpec) error {
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	var authors []string
	if cfg.Author != "" {
		authors = []string{cfg.Author}
	}
	branches := make([]string, len(specs))
	for i, rs := range specs {
		branches[i] = rs.owner + "/" + rs.repo + ":" + branchName
	}
	data, err := spec.RenderTemplate(spec.TemplateData{
		Ticket:   ticket,
		Title:    desc,
		Authors:  authors,
		Branches: branches,
		Now:      time.Now(),
	})
	if err != nil {
		return err
	}
	return os.WriteFile(absPath, data, 0o644)
}

// detectCurrentRepo returns "owner/repo" for the git repo containing the cwd.
func detectCurrentRepo(gitClient *git.CLIClient) (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	root := strings.TrimSpace(string(out))

	remoteURLs, err := gitClient.RemoteURLs(root)
	if err != nil || len(remoteURLs) == 0 {
		return "", fmt.Errorf("no remote URL found in %s", root)
	}

	ownerRepo := extractOwnerRepo(remoteURLs[0])
	if ownerRepo == "" {
		return "", fmt.Errorf("could not parse owner/repo from remote %s", remoteURLs[0])
	}
	return ownerRepo, nil
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
