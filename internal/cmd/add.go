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
	specdoc "github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/store"
)

var (
	addPriority bool
	addRepos    []string
	addNoSpec   bool
	addSpecRepo string
)

type repoSpec struct {
	owner string
	repo  string
}

func (r repoSpec) String() string {
	return r.owner + "/" + r.repo
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
	addCmd.Flags().BoolVar(&addNoSpec, "no-spec", false, "Skip spec scaffolding for ticket-based worktrees")
	addCmd.Flags().StringVar(&addSpecRepo, "spec-repo", "", "owner/repo that should receive the canonical spec file")
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

func addWithWorktree(ticket, desc string, repos []string, priority bool) (retErr error) {
	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()
	if cfg.Worktree.WorktreesDir == "" {
		return fmt.Errorf("worktree.worktrees_dir not configured; set it in ~/.wgo/config.toml")
	}
	gitClient := git.New("")

	var created []struct{ repoPath, wtPath string }
	var specCommit struct{ wtPath string }
	defer func() {
		if retErr != nil {
			if specCommit.wtPath != "" {
				fmt.Fprintf(os.Stderr, "rolling back spec commit in %s...\n", specCommit.wtPath)
				_ = gitClient.ResetHard(specCommit.wtPath, "HEAD~1")
			}
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
	specs, err := parseRepoSpecs(repos)
	if err != nil {
		return err
	}
	specRepo := specs[0]
	if addSpecRepo != "" {
		specRepo, err = parseSpecRepoFlag(addSpecRepo, specs)
		if err != nil {
			return err
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
	for _, spec := range specs {
		repoPath, err := findOrCloneRepo(gitClient, cfg, spec.owner, spec.repo)
		if err != nil {
			return fmt.Errorf("repo %s/%s: %w", spec.owner, spec.repo, err)
		}

		fmt.Fprintf(os.Stderr, "fetching %s/%s...\n", spec.owner, spec.repo)
		if err := gitClient.Fetch(repoPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: fetch failed for %s/%s (using cached state): %v\n", spec.owner, spec.repo, err)
		}

		defaultBranch, err := gitClient.DefaultBranch(repoPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not detect default branch for %s/%s, assuming 'main': %v\n", spec.owner, spec.repo, err)
			defaultBranch = "main"
		}

		wtPath := filepath.Join(sharedRoot, spec.repo)
		if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(wtPath), err)
		}

		fmt.Fprintf(os.Stderr, "creating worktree %s...\n", wtPath)
		if err := gitClient.WorktreeAdd(repoPath, wtPath, branchName, true, "origin/"+defaultBranch); err != nil {
			return fmt.Errorf("worktree add %s: %w", spec.repo, err)
		}
		created = append(created, struct{ repoPath, wtPath string }{repoPath, wtPath})
	}

	reason := branchReason(ticket, desc)
	specRel := filepath.ToSlash(filepath.Join("spec", ticket+".md"))
	specState := ""

	if !addNoSpec {
		specRepoWtPath := filepath.Join(sharedRoot, specRepo.repo)
		specAbs := filepath.Join(specRepoWtPath, filepath.FromSlash(specRel))

		wrote, err := writeSpecStub(specAbs, ticket, desc, cfg, branchName, specs)
		if err != nil {
			return fmt.Errorf("write spec: %w", err)
		}
		if wrote {
			if err := gitClient.AddAndCommit(specRepoWtPath, specRel, specCommitMessage(ticket, desc)); err != nil {
				return fmt.Errorf("commit spec: %w", err)
			}
			specCommit.wtPath = specRepoWtPath
		} else {
			fmt.Fprintf(os.Stderr, "spec already exists: %s (skipping)\n", specRel)
		}

		if specFile, err := specdoc.Parse(specAbs); err == nil {
			specState = string(specFile.Frontmatter.Status)
		}
	}

	for _, spec := range pushOrder(specs, specRepo, addNoSpec) {
		wtPath := filepath.Join(sharedRoot, spec.repo)
		fmt.Fprintf(os.Stderr, "pushing %s...\n", branchName)
		if err := gitClient.Push(wtPath, branchName); err != nil {
			return fmt.Errorf("push %s: %w", spec.repo, err)
		}
		fmt.Fprintf(os.Stderr, "created: %s\n", wtPath)
	}

	// Update plan.
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := s.EnsureDir(); err != nil {
		return fmt.Errorf("store ensure dir: %w", err)
	}
	state, err := s.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
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

	for _, spec := range specs {
		relSpec := ""
		if !addNoSpec && spec == specRepo {
			relSpec = specRel
		}
		p.AddBranch(spec.repo, branchName, reason, relSpec)

		wtPath := filepath.Join(sharedRoot, spec.repo)
		state.AddAnnotation(wtPath, branchName, reason)
		state.AddRepo(wtPath, "")
		if relSpec != "" {
			state.SetSpec(wtPath, branchName, relSpec, specState)
		}
	}

	if err := s.SavePlan(p.Render()); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}
	if err := s.SaveState(state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Added task: %s %s\n", string(bullet), taskText)
	fmt.Println(sharedRoot)
	return nil
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

func parseSpecRepoFlag(value string, specs []repoSpec) (repoSpec, error) {
	for _, spec := range specs {
		if spec.String() == value {
			return spec, nil
		}
	}
	return repoSpec{}, fmt.Errorf("--spec-repo %q must match one of the configured repos", value)
}

func pushOrder(specs []repoSpec, specRepo repoSpec, noSpec bool) []repoSpec {
	if noSpec {
		return append([]repoSpec(nil), specs...)
	}

	ordered := make([]repoSpec, 0, len(specs))
	for _, spec := range specs {
		if spec != specRepo {
			ordered = append(ordered, spec)
		}
	}
	ordered = append(ordered, specRepo)
	return ordered
}

func branchReason(ticket, desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return ticket
	}
	return ticket + ": " + desc
}

func specCommitMessage(ticket, desc string) string {
	message := fmt.Sprintf("spec: scaffold for %s", ticket)
	desc = strings.TrimSpace(desc)
	if desc != "" {
		message += "\n\n" + desc
	}
	return message
}

func writeSpecStub(path, ticket, desc string, cfg *config.Config, branchName string, specs []repoSpec) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}

	data, err := specdoc.RenderTemplate(specdoc.TemplateData{
		Ticket:      ticket,
		Title:       strings.TrimSpace(desc),
		Description: strings.TrimSpace(desc),
		Authors:     specAuthors(cfg),
		Branches:    specBranchRefs(specs, branchName),
		Now:         time.Now(),
	})
	if err != nil {
		return false, err
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return false, err
	}

	return true, nil
}

func specAuthors(cfg *config.Config) []string {
	var authors []string
	seen := make(map[string]struct{})

	for _, author := range []string{strings.TrimSpace(cfg.Author), strings.TrimSpace(cfg.Pair.Teammate)} {
		if author == "" {
			continue
		}
		if _, exists := seen[author]; exists {
			continue
		}
		seen[author] = struct{}{}
		authors = append(authors, author)
	}

	return authors
}

func specBranchRefs(specs []repoSpec, branchName string) []string {
	refs := make([]string, 0, len(specs))
	for _, spec := range specs {
		refs = append(refs, spec.String()+":"+branchName)
	}
	return refs
}
