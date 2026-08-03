package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/prcache"
	"github.com/virtru/wgo/internal/store"
	wgosync "github.com/virtru/wgo/internal/sync"
)

var (
	syncRepoFlag      string
	syncDryRunFlag    bool
	syncSkipFetch     bool
	syncDefaultBase   string
	syncCreatePRsFlag bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Walk each repo's jj DAG and align GitHub PR bases + markers",
	Long: `Reads the jj DAG for every tracked repo, fetches each bookmark's open PR
state from GitHub, retargets child PR bases to the nearest ancestor with an
open PR, and regenerates the wgo-stack marker block embedded in each PR body.

jj auto-restacks descendants whenever an ancestor commit changes, so sync
does not perform local rebases. It only mutates GitHub state.

With --repo, sync runs against a single repository path. Without it, sync
iterates every tracked repo that has a .jj/ directory.`,
	SilenceUsage: true,
	RunE:         runSync,
}

func init() {
	syncCmd.Flags().StringVar(&syncRepoFlag, "repo", "", "limit sync to a single repository path")
	syncCmd.Flags().BoolVar(&syncDryRunFlag, "dry-run", false, "report changes without mutating GitHub")
	syncCmd.Flags().BoolVar(&syncSkipFetch, "no-fetch", false, "skip `jj git fetch` before reading the DAG")
	syncCmd.Flags().StringVar(&syncDefaultBase, "default-base", "main", "fallback base bookmark for stack roots")
	syncCmd.Flags().BoolVar(&syncCreatePRsFlag, "create-prs", false, "open draft PRs for bookmarked changes that lack one")
	rootCmd.AddCommand(syncCmd)
}

func runSync(_ *cobra.Command, _ []string) error {
	if err := config.Init(); err != nil {
		return err
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	state, err := s.LoadState()
	if err != nil {
		return err
	}

	jjc := jj.NewCLI()
	ghc := github.NewClient()

	repos := repoList(state)
	if syncRepoFlag != "" {
		repos = []string{syncRepoFlag}
	}
	sort.Strings(repos)

	cfg := config.Get()
	opts := wgosync.Options{
		Fetch:       !syncSkipFetch,
		DryRun:      syncDryRunFlag,
		DefaultBase: syncDefaultBase,
		CreatePRs:   syncCreatePRsFlag || cfg.Sync.CreatePRs,
		GHStackMode: cfg.Sync.GHStackMode(),
		Linker:      wgosync.NewCLILinker(),
	}

	exitWithErr := false
	for _, repo := range repos {
		if !jjc.IsRepo(repo) {
			fmt.Fprintf(os.Stderr, "sync: %s is not a jj repo, skipping\n", repo)
			continue
		}
		result, err := wgosync.Sync(jjc, ghc, repo, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sync: %s: %v\n", repo, err)
			exitWithErr = true
			continue
		}
		printSyncResult(repo, result)
		if !syncDryRunFlag {
			invalidatePRCache(jjc, repo, result)
		}
	}

	if exitWithErr {
		os.Exit(1)
	}
	return nil
}

func printSyncResult(repo string, r *wgosync.Result) {
	fmt.Printf("== %s ==\n", repo)
	if len(r.BaseChanges) == 0 && len(r.MarkerUpdates) == 0 &&
		len(r.Created) == 0 && len(r.Linked) == 0 && len(r.MarkerStrips) == 0 {
		fmt.Println("  no changes")
		return
	}
	for _, c := range r.Created {
		if c.PR == 0 {
			fmt.Printf("  (%s): would open draft PR based on %s\n", c.Bookmark, c.Base)
		} else {
			fmt.Printf("  PR #%d (%s): opened draft based on %s\n", c.PR, c.Bookmark, c.Base)
		}
	}
	for _, c := range r.BaseChanges {
		fmt.Printf("  PR #%d (%s): base %s → %s\n", c.PR, c.Bookmark, c.OldBase, c.NewBase)
	}
	if len(r.Linked) > 0 {
		fmt.Printf("  linked native stack: %s\n", strings.Join(r.Linked, " → "))
	}
	for _, u := range r.MarkerStrips {
		fmt.Printf("  PR #%d (%s): wgo-stack marker removed (native stack)\n", u.PR, u.Bookmark)
	}
	for _, u := range r.MarkerUpdates {
		fmt.Printf("  PR #%d (%s): marker refreshed\n", u.PR, u.Bookmark)
	}
}

// invalidatePRCache drops the on-disk PR cache entries for every bookmark whose
// PR base or marker changed, so the next `wgo .`/statusline reflects the new
// state instead of serving stale review/merge data.
func invalidatePRCache(jjc jj.Client, repo string, r *wgosync.Result) {
	remoteURL := ""
	if remotes, err := jjc.RemoteURLs(repo); err == nil {
		remoteURL = remotes["origin"]
	}
	seen := map[string]bool{}
	invalidate := func(bookmark string) {
		if bookmark == "" || seen[bookmark] {
			return
		}
		seen[bookmark] = true
		_ = prcache.Invalidate(remoteURL, repo, bookmark)
	}
	for _, c := range r.BaseChanges {
		invalidate(c.Bookmark)
	}
	for _, u := range r.MarkerUpdates {
		invalidate(u.Bookmark)
	}
}

func repoList(state *store.State) []string {
	out := make([]string, 0, len(state.Repos))
	for path := range state.Repos {
		out = append(out, path)
	}
	return out
}
