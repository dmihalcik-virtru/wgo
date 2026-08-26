package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/rig"
	"github.com/virtru/wgo/internal/store"
)

var doctorStrict bool

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check tracked workspaces for spec compliance",
	Long: `Walk every tracked workspace and report bookmarks that violate the
spec policy in config.toml (doctor.spec_required, doctor.exclude_bookmarks).

Reports are written to stdout. With --strict, the command exits 1 if any
violations are found; otherwise it exits 0.

This replaces the previous pre-commit hook-based enforcement, which is no
longer possible without a .git directory in pure jj workspaces.`,
	RunE: runDoctor,
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorStrict, "strict", false, "exit non-zero if any spec violations are found")
	rootCmd.AddCommand(doctorCmd)
}

type doctorFinding struct {
	Repo      string
	Workspace string
	Bookmark  string
	Issue     string
}

func runDoctor(_ *cobra.Command, _ []string) error {
	if err := config.Init(); err != nil {
		return err
	}
	cfg := config.Get()
	if cfg == nil {
		cfg = &config.Config{}
	}

	s, err := store.New()
	if err != nil {
		return err
	}
	state, err := s.LoadState()
	if err != nil {
		return err
	}

	client := jj.NewCLI()
	var findings []doctorFinding

	repoPaths := make([]string, 0, len(state.Repos))
	for p := range state.Repos {
		repoPaths = append(repoPaths, p)
	}
	sort.Strings(repoPaths)

	for _, repo := range repoPaths {
		if !client.IsRepo(repo) {
			findings = append(findings, doctorFinding{
				Repo:  repo,
				Issue: "not a jj repository (missing .jj/)",
			})
			continue
		}
		findings = append(findings, checkRepo(client, state, cfg, repo)...)
	}
	findings = append(findings, checkRigs(client, cfg)...)

	for _, f := range findings {
		printFinding(f)
	}

	if len(findings) == 0 {
		fmt.Println("doctor: no issues found")
		return nil
	}
	fmt.Fprintf(os.Stderr, "\ndoctor: %d issue(s) found\n", len(findings))
	if doctorStrict {
		os.Exit(1)
	}
	return nil
}

func checkRepo(client jj.Client, state *store.State, cfg *config.Config, repo string) []doctorFinding {
	workspaces, err := client.ListWorkspaces(repo)
	if err != nil {
		return []doctorFinding{{Repo: repo, Issue: fmt.Sprintf("listing workspaces failed: %v", err)}}
	}

	out := checkMainWorkspace(client, cfg, repo)
	for _, ws := range workspaces {
		// A rig checkout is pinned to a released commit and carries no
		// bookmark, so the spec policy below has nothing to say about it — and
		// when someone does park a bookmark on the pinned commit, it would say
		// the wrong thing. checkRigs judges them against their manifest.
		if rig.IsWorkspace(cfg.Rig.Dir, ws.Path, ws.Name) {
			continue
		}
		current, err := client.CurrentChange(ws.Path)
		if err != nil {
			out = append(out, doctorFinding{
				Repo: repo, Workspace: ws.Name,
				Issue: fmt.Sprintf("could not read current change: %v", err),
			})
			continue
		}

		bookmark := firstBookmark(current.Bookmarks)
		if bookmark == "" {
			continue // anonymous working copy; nothing to enforce
		}
		if bookmarkExcluded(bookmark, cfg.Doctor.ExcludeBookmarks) {
			continue
		}
		out = append(out, checkBookmark(state, &cfg.Doctor, repo, ws, bookmark)...)
	}
	return out
}

// checkMainWorkspace reports two conditions specific to a repo's main clone,
// which wgo treats as the trunk checkout: work stranded on its @ (which belongs
// in a feature workspace, and which `wgo to` will warn about every time), and a
// redundant <worktrees_dir>/<trunk>/<repo> workspace left over from before
// `wgo to` routed trunk URLs to the clone itself.
//
// Read-only, like the rest of doctor: it names the fix rather than applying it.
func checkMainWorkspace(client jj.Client, cfg *config.Config, repo string) []doctorFinding {
	main, err := client.MainWorkspaceRoot(repo)
	if err != nil || absResolved(main) != absResolved(repo) {
		// Not a main clone (or unreadable); neither check applies.
		return nil
	}

	var out []doctorFinding

	if p, ok, err := planPark(client, cfg, repo, parkOpts{}); err == nil && ok {
		out = append(out, doctorFinding{
			Repo: repo,
			Issue: fmt.Sprintf("work stranded on @ (%d change(s)); run `wgo park %s` to move it to %s",
				len(p.Work), repo, p.Dest),
		})
	}

	if trunk := localTrunkBookmark(client, repo); trunk != "" {
		if junk := redundantTrunkWorkspace(client, cfg, repo, trunk); junk != "" {
			out = append(out, doctorFinding{
				Repo:  repo,
				Issue: fmt.Sprintf("redundant trunk workspace duplicates this clone: %s", junk),
			})
		}
	}

	return out
}

// checkRigs reports rig checkouts that no longer sit on the commit their
// manifest pins.
//
// This is the failure a rig cannot survive quietly. The go.work still resolves,
// `go build` still succeeds and `wgo rig verify` still passes — it compares
// dependency *versions*, and a checkout moved off its pin keeps the version it
// was recorded with. The only thing that changed is that the source under the
// debugger is no longer the source that shipped, which is the entire point of
// the rig.
//
// Read-only, like the rest of doctor: it names `wgo rig sync` rather than
// running one, because restoring the pin discards whatever the user moved the
// checkout to.
// The jj surface is narrowed to rig.Pinned rather than jj.Client so this is
// testable against a fake without standing up nine workspaces.
func checkRigs(p rig.Pinned, cfg *config.Config) []doctorFinding {
	rigDir := strings.TrimSpace(cfg.Rig.Dir)
	if rigDir == "" {
		return nil
	}
	manifests, err := rig.List(rigDir)
	if err != nil {
		return []doctorFinding{{Repo: rigDir, Issue: fmt.Sprintf("listing rigs failed: %v", err)}}
	}

	var out []doctorFinding
	for _, m := range manifests {
		for _, cond := range rig.Inspect(p, m.Manifest, m.Root) {
			if cond.OK() {
				continue
			}
			out = append(out, doctorFinding{
				Repo:      m.Root,
				Workspace: cond.Checkout.Dir,
				Issue:     rigConditionIssue(cond),
			})
		}
	}
	return out
}

// rigConditionIssue turns a drifted checkout into a line that names the fix.
func rigConditionIssue(c rig.Condition) string {
	switch c.Health {
	case rig.HealthMissing:
		return fmt.Sprintf("checkout directory is missing; restore it with `wgo rig sync %s`", c.Rig)
	case rig.HealthMoved:
		return fmt.Sprintf("moved off its pin: %s is pinned to %s but sits on %s.\n"+
			"    put it back with `jj -R %s edit %s`, or re-materialise it with `wgo rig sync %s`",
			c.Checkout.Repo, rigPin(c.Checkout), shortCommitID(c.At),
			c.Path, shortCommitID(c.Checkout.Commit), c.Rig)
	case rig.HealthUnreadable:
		return fmt.Sprintf("could not read the checkout: %s", c.Detail)
	default:
		return string(c.Health)
	}
}

func checkBookmark(state *store.State, cfg *config.DoctorConfig, repo string, ws jj.Workspace, bookmark string) []doctorFinding {
	var out []doctorFinding
	ann := state.GetAnnotation(repo, bookmark)

	if cfg.SpecRequired {
		if ann == nil || ann.SpecPath == "" {
			out = append(out, doctorFinding{
				Repo: repo, Workspace: ws.Name, Bookmark: bookmark,
				Issue: "spec_required: no spec recorded for this bookmark",
			})
			return out
		}
	}
	if ann != nil && ann.SpecPath != "" {
		specPath := ann.SpecPath
		if !filepath.IsAbs(specPath) {
			specPath = filepath.Join(repo, specPath)
		}
		if _, statErr := os.Stat(specPath); statErr != nil {
			out = append(out, doctorFinding{
				Repo: repo, Workspace: ws.Name, Bookmark: bookmark,
				Issue: fmt.Sprintf("spec file not found: %s", ann.SpecPath),
			})
		}
	}
	return out
}

func firstBookmark(bookmarks []string) string {
	if len(bookmarks) == 0 {
		return ""
	}
	return bookmarks[0]
}

func bookmarkExcluded(name string, patterns []string) bool {
	for _, p := range patterns {
		if matched, _ := filepath.Match(p, name); matched {
			return true
		}
	}
	return false
}

func printFinding(f doctorFinding) {
	switch {
	case f.Bookmark != "":
		fmt.Printf("  %s [%s @ %s]\n    %s\n", f.Repo, f.Bookmark, f.Workspace, f.Issue)
	case f.Workspace != "":
		fmt.Printf("  %s [%s]\n    %s\n", f.Repo, f.Workspace, f.Issue)
	default:
		fmt.Printf("  %s\n    %s\n", f.Repo, f.Issue)
	}
}
