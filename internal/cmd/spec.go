package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/store"
)

var (
	specLsStatus   string
	specLsMine     bool
	specLsPair     bool
	specLsTeam     bool
	specLsJSON     bool
	specAllRepos   bool
	specLinkTicket string
)

var specCmd = &cobra.Command{
	Use:   "spec",
	Short: "Manage spec files",
	Long:  "Create, view, edit, and track spec files linked to branches.",
}

var specNewCmd = &cobra.Command{
	Use:   "new <TICKET> [title]",
	Short: "Create a new spec linked to the current branch",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ticket := strings.ToUpper(args[0])
		title := ""
		if len(args) > 1 {
			title = args[1]
		}
		return runSpecNew(ticket, title)
	},
}

var specShowCmd = &cobra.Command{
	Use:   "show [TICKET]",
	Short: "Show the spec for the current branch or a named ticket",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSpecShow(args)
	},
}

var specEditCmd = &cobra.Command{
	Use:   "edit [TICKET]",
	Short: "Open a spec in $EDITOR; bumps updated: on save",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSpecEdit(args)
	},
}

var specLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all specs",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSpecLs()
	},
}

var specStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show drift report; exits 1 if drift is detected",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSpecStatus()
	},
}

var specLinkCmd = &cobra.Command{
	Use:   "link [TICKET]",
	Short: "Associate the current branch with an existing spec",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ticket := ""
		if len(args) > 0 {
			ticket = strings.ToUpper(args[0])
		}
		return runSpecLink(ticket)
	},
}

func init() {
	rootCmd.AddCommand(specCmd)
	specCmd.AddCommand(specNewCmd)
	specCmd.AddCommand(specShowCmd)
	specCmd.AddCommand(specEditCmd)
	specCmd.AddCommand(specLsCmd)
	specCmd.AddCommand(specStatusCmd)
	specCmd.AddCommand(specLinkCmd)

	specLsCmd.Flags().StringVar(&specLsStatus, "status", "", "Filter by lifecycle state (draft/in_progress/shipped/abandoned)")
	specLsCmd.Flags().BoolVar(&specLsMine, "mine", false, "Filter to specs I authored")
	specLsCmd.Flags().BoolVar(&specLsPair, "pair", false, "Filter to specs authored by me and my pair")
	specLsCmd.Flags().BoolVar(&specLsTeam, "team", false, "Show all specs regardless of authorship")
	specLsCmd.Flags().BoolVar(&specLsJSON, "json", false, "JSON output")
	specLsCmd.Flags().BoolVar(&specAllRepos, "all-repos", false, "Walk all repos in state")

	specStatusCmd.Flags().BoolVar(&specAllRepos, "all-repos", false, "Walk all repos in state")

	specLinkCmd.Flags().StringVar(&specLinkTicket, "ticket", "", "Ticket override for branches without a ticket in their name")
}

// --- helpers ---

// repoNameFromJJRoot returns the basename of the main workspace root for
// the workspace containing path, or "" on any error. This is the jj-side
// equivalent of the deleted git.CLIClient.RepoName helper.
func repoNameFromJJRoot(jjc jj.Client, path string) string {
	root, err := jjc.MainWorkspaceRoot(path)
	if err != nil {
		root, err = jjc.Root(path)
		if err != nil {
			return ""
		}
	}
	return filepath.Base(root)
}

// repoRoot returns the jj workspace root for the current working directory.
func repoRoot() (string, error) {
	cwd, err := resolveCwd()
	if err != nil {
		return "", err
	}
	root, err := jj.NewCLI().Root(cwd)
	if err != nil {
		return "", fmt.Errorf("not a jj repository")
	}
	return root, nil
}

// resolveSpecTarget returns repoRoot and ticket for a command that takes an optional TICKET arg.
// When args is empty it derives the ticket from the current branch.
func resolveSpecTarget(args []string) (root, ticket string, err error) {
	root, err = repoRoot()
	if err != nil {
		return
	}
	if len(args) > 0 {
		ticket = strings.ToUpper(args[0])
		return
	}
	jjc := jj.NewCLI()
	cwd, _ := resolveCwd()
	branch := currentBookmark(jjc, cwd)
	if branch == "" {
		err = fmt.Errorf("could not determine current bookmark; check `jj log -r @`")
		return
	}
	ticket = spec.ParseTicketFromBranch(branch)
	if ticket == "" {
		err = fmt.Errorf("branch %q does not parse to a ticket; pass TICKET as argument", branch)
	}
	return
}

// specRoots returns the list of repo roots to search, based on --all-repos.
func specRoots() ([]string, error) {
	if !specAllRepos {
		root, err := repoRoot()
		if err != nil {
			return nil, err
		}
		return []string{root}, nil
	}
	s, err := store.New()
	if err != nil {
		return nil, err
	}
	state, err := s.LoadState()
	if err != nil {
		return nil, err
	}
	var roots []string
	for path := range state.Repos {
		roots = append(roots, path)
	}
	if len(roots) == 0 {
		root, err := repoRoot()
		if err != nil {
			return nil, err
		}
		roots = []string{root}
	}
	return roots, nil
}

// --- spec new ---

func runSpecNew(ticket, title string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}

	specRel := filepath.Join("spec", ticket+".md")
	specAbs := filepath.Join(root, specRel)

	if _, statErr := os.Stat(specAbs); statErr == nil {
		return fmt.Errorf("spec/%s.md already exists — use: wgo spec edit %s", ticket, ticket)
	}

	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()

	jjc := jj.NewCLI()
	cwd, _ := resolveCwd()
	branch := currentBookmark(jjc, cwd)

	var branches []string
	if branch != "" {
		repoName := repoNameFromJJRoot(jjc, cwd)
		if repoName != "" {
			branches = []string{repoName + ":" + branch}
		} else {
			branches = []string{branch}
		}
	}

	data, err := spec.RenderTemplate(spec.TemplateData{
		Ticket:      ticket,
		Title:       title,
		Description: title,
		Authors:     []string{cfg.Author},
		Branches:    branches,
		Now:         time.Now(),
	})
	if err != nil {
		return fmt.Errorf("render template: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(specAbs), 0o755); err != nil {
		return fmt.Errorf("mkdir spec dir: %w", err)
	}
	if err := os.WriteFile(specAbs, data, 0o644); err != nil {
		return fmt.Errorf("write spec: %w", err)
	}

	// Update plan and state.
	s, _ := store.New()
	if s.EnsureDir() == nil && branch != "" {
		repoName := repoNameFromJJRoot(jjc, cwd)
		if content, err := s.LoadPlan(); err == nil {
			if p, err := plan.Parse(content); err == nil {
				p.AddBranch(repoName, branch, ticket+": "+title, specRel)
				_ = s.SavePlan(p.Render())
			}
		}
		_ = s.MutateState(func(state *store.State) (bool, error) {
			state.SetSpec(root, branch, specRel, "draft")
			return true, nil
		})
	}

	fmt.Fprintf(os.Stderr, "created: %s\n", specRel)
	return nil
}

// --- spec show ---

func runSpecShow(args []string) error {
	root, ticket, err := resolveSpecTarget(args)
	if err != nil {
		return err
	}

	specPath, err := spec.FindByTicket(root, ticket)
	if err != nil {
		return fmt.Errorf("no spec for %s — run: wgo spec new %s", ticket, ticket)
	}

	data, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	fmt.Print(string(data))
	return nil
}

// --- spec edit ---

func runSpecEdit(args []string) error {
	root, ticket, err := resolveSpecTarget(args)
	if err != nil {
		return err
	}

	specPath, err := spec.FindByTicket(root, ticket)
	if err != nil {
		return fmt.Errorf("no spec for %s — run: wgo spec new %s", ticket, ticket)
	}

	infoBefore, err := os.Stat(specPath)
	if err != nil {
		return fmt.Errorf("stat spec: %w", err)
	}
	mtimeBefore := infoBefore.ModTime()

	sfBefore, parseErr := spec.Parse(specPath)
	malformed := parseErr != nil || sfBefore.Frontmatter.Ticket == ""
	if malformed {
		fmt.Fprintf(os.Stderr, "warning: spec has malformed frontmatter; updated: will not be auto-bumped\n")
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	editorCmd := exec.Command(editor, specPath)
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr
	if err := editorCmd.Run(); err != nil {
		return fmt.Errorf("editor: %w", err)
	}

	infoAfter, _ := os.Stat(specPath)
	if malformed || infoAfter == nil || !infoAfter.ModTime().After(mtimeBefore) {
		return nil
	}

	// Bump updated: and capture new status.
	var newStatus spec.Status
	if err := spec.UpdateFrontmatter(specPath, func(fm *spec.Frontmatter) error {
		fm.Updated = time.Now().Truncate(24 * time.Hour)
		newStatus = fm.Status
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not bump updated: %v\n", err)
		return nil
	}

	// Sync annotation.
	jjc := jj.NewCLI()
	cwd, _ := resolveCwd()
	if branch := currentBookmark(jjc, cwd); branch != "" {
		s, _ := store.New()
		_ = s.MutateState(func(state *store.State) (bool, error) {
			state.SetSpec(root, branch, filepath.Join("spec", ticket+".md"), string(newStatus))
			return true, nil
		})
	}

	return nil
}

// --- spec ls ---

type specEntry struct {
	Ticket   string   `json:"ticket"`
	Status   string   `json:"status"`
	Authors  []string `json:"authors"`
	Branches []string `json:"branches"`
	Updated  string   `json:"updated"`
	Path     string   `json:"path"`
}

func runSpecLs() error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()

	roots, err := specRoots()
	if err != nil {
		return err
	}

	var entries []specEntry
	seen := make(map[string]bool)

	for _, root := range roots {
		specDir := filepath.Join(root, "spec")
		dirEntries, err := os.ReadDir(specDir)
		if err != nil {
			continue
		}
		for _, de := range dirEntries {
			if de.IsDir() || !strings.HasSuffix(de.Name(), ".md") {
				continue
			}
			specPath := filepath.Join(specDir, de.Name())
			sf, err := spec.Parse(specPath)
			if err != nil || sf.Frontmatter.Ticket == "" {
				continue
			}
			ticket := sf.Frontmatter.Ticket
			if seen[ticket] {
				continue
			}
			if specLsStatus != "" && string(sf.Frontmatter.Status) != specLsStatus {
				continue
			}
			if specLsPair {
				if !cfg.HasPair() {
					return fmt.Errorf("pair not configured: set [pair] teammate in ~/.wgo/config.toml")
				}
				if !containsAuthor(sf.Frontmatter.Authors, cfg.Author) ||
					!containsAuthor(sf.Frontmatter.Authors, cfg.Pair.Teammate) {
					continue
				}
			} else if !specLsTeam {
				if cfg.Author != "" && !containsAuthor(sf.Frontmatter.Authors, cfg.Author) {
					continue
				}
			}
			seen[ticket] = true

			updated := ""
			if !sf.Frontmatter.Updated.IsZero() {
				updated = sf.Frontmatter.Updated.Format("2006-01-02")
			}
			entries = append(entries, specEntry{
				Ticket:   ticket,
				Status:   string(sf.Frontmatter.Status),
				Authors:  sf.Frontmatter.Authors,
				Branches: sf.Frontmatter.Branches,
				Updated:  updated,
				Path:     specPath,
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Ticket < entries[j].Ticket
	})

	if specLsJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	fmt.Printf("  %-12s %-12s %-20s %-30s %s\n", "TICKET", "STATUS", "AUTHORS", "BRANCHES", "UPDATED")
	for _, e := range entries {
		authors := strings.Join(e.Authors, ", ")
		branches := strings.Join(e.Branches, ", ")
		fmt.Printf(
			"  %-12s %-12s %-20s %-30s %s\n",
			specTruncate(e.Ticket, 12),
			specTruncate(e.Status, 12),
			specTruncate(authors, 20),
			specTruncate(branches, 30),
			e.Updated,
		)
	}
	return nil
}

func containsAuthor(authors []string, author string) bool {
	for _, a := range authors {
		if strings.EqualFold(a, author) {
			return true
		}
	}
	return false
}

func specTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

// --- spec status ---

func runSpecStatus() error {
	roots, err := specRoots()
	if err != nil {
		return err
	}

	var all []spec.DriftReport
	for _, root := range roots {
		reports, err := spec.DetectAll(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", root, err)
			continue
		}
		all = append(all, reports...)
	}

	if len(all) == 0 {
		fmt.Println("no drift detected")
		return nil
	}

	hasDrift := false
	for _, r := range all {
		var label string
		switch r.Kind {
		case spec.DriftStale:
			label = "STALE"
			hasDrift = true
		case spec.DriftUntracked:
			label = "UNTRACKED"
			hasDrift = true
		case spec.DriftOrphaned:
			label = "ORPHANED"
		case spec.DriftSpecOnly:
			label = "SPEC_ONLY"
		}
		ref := r.Branch
		if ref == "" {
			ref = r.Spec
		}
		fmt.Printf("  %-12s %-30s %s\n", label, ref, r.Detail)
	}

	if hasDrift {
		os.Exit(1)
	}
	return nil
}

// --- spec link ---

func runSpecLink(ticket string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}

	jjc := jj.NewCLI()
	cwd, _ := resolveCwd()
	branch := currentBookmark(jjc, cwd)
	if branch == "" {
		return fmt.Errorf("could not determine current bookmark; check `jj log -r @`")
	}

	// Resolve ticket from arg, flag, or branch name.
	if ticket == "" {
		ticket = spec.ParseTicketFromBranch(branch)
	}
	if ticket == "" {
		if specLinkTicket != "" {
			ticket = strings.ToUpper(specLinkTicket)
		} else {
			return fmt.Errorf("branch %q does not parse to a ticket; pass TICKET or --ticket WGO-XXX", branch)
		}
	}

	specPath, err := spec.FindByTicket(root, ticket)
	if err != nil {
		return fmt.Errorf("no spec/%s.md found — run: wgo spec new %s", ticket, ticket)
	}

	repoName := repoNameFromJJRoot(jjc, cwd)
	branchRef := branch
	if repoName != "" {
		branchRef = repoName + ":" + branch
	}

	if err := spec.UpdateFrontmatter(specPath, func(fm *spec.Frontmatter) error {
		for _, b := range fm.Branches {
			if b == branchRef || b == branch {
				return nil // already linked
			}
		}
		fm.Branches = append(fm.Branches, branchRef)
		fm.Updated = time.Now().Truncate(24 * time.Hour)
		return nil
	}); err != nil {
		return fmt.Errorf("update frontmatter: %w", err)
	}

	specRel := filepath.Join("spec", ticket+".md")
	s, _ := store.New()
	if s.EnsureDir() == nil {
		_ = s.MutateState(func(state *store.State) (bool, error) {
			state.SetSpec(root, branch, specRel, "draft")
			return true, nil
		})
		if content, err := s.LoadPlan(); err == nil {
			if p, err := plan.Parse(content); err == nil {
				p.AddBranch(repoName, branch, ticket, specRel)
				_ = s.SavePlan(p.Render())
			}
		}
	}

	fmt.Fprintf(os.Stderr, "linked %s → spec/%s.md\n", branch, ticket)
	return nil
}
