package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jira"
	"github.com/virtru/wgo/internal/links"
)

var (
	standupBoard    int
	standupSprint   string
	standupClosed   int
	standupAll      bool
	standupAssignee string
	standupJSON     bool
)

// standupCmd represents the `wgo standup` command.
var standupCmd = &cobra.Command{
	Use:     "standup",
	Aliases: []string{"sprint"},
	Short:   "Morning Jira view: your in-flight work grouped by sprint, with goals and days overdue",
	Long: `Show every issue assigned to you that is in flight (In Progress / In Review /
In QA), grouped by the sprint it belongs to. For each active sprint — and each of
the most recently closed sprints — the sprint goal and time to completion (or days
overdue) are shown. In-flight work that is not in an active or recent sprint is
listed in a catch-all group so nothing slips through. Every issue, board, and
backlog reference is a clickable terminal link.

Requires the Atlassian CLI (acli) authenticated via: acli jira auth login
Set the board id in ~/.wgo/config.toml under [jira] board, or pass --board.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStandup()
	},
}

func init() {
	rootCmd.AddCommand(standupCmd)
	standupCmd.Flags().IntVar(&standupBoard, "board", 0, "Jira board id (defaults to jira.board in config)")
	standupCmd.Flags().StringVar(&standupSprint, "sprint", "", "Filter to one sprint by id or name substring (e.g. PQ)")
	standupCmd.Flags().IntVar(&standupClosed, "closed", 6, "How many recently-closed sprints to attach goals/overdue for (0 = active only)")
	standupCmd.Flags().BoolVar(&standupAll, "all", false, "Show sprint groups even when you have no in-flight work in them")
	standupCmd.Flags().StringVar(&standupAssignee, "assignee", "", "Assignee to report on (defaults to the current acli user)")
	standupCmd.Flags().BoolVar(&standupJSON, "json", false, "Emit machine-readable JSON")
}

// standupFields is the (acli-allowed) set of issue fields we request.
var standupFields = []string{"key", "summary", "status", "priority"}

// standupGroup is a set of in-flight issues under one sprint. A nil sprint marks
// the catch-all group for work not in an active or recent sprint.
type standupGroup struct {
	sprint *jira.Sprint
	issues []jira.Issue
}

func runStandup() error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("failed to initialize config: %w", err)
	}
	cfg := config.Get()

	if err := jira.CheckAuth(); err != nil {
		return err
	}

	board := standupBoard
	if board == 0 {
		board = cfg.Jira.Board
	}
	if board == 0 {
		return fmt.Errorf("no Jira board configured — set jira.board in ~/.wgo/config.toml or pass --board <id>")
	}
	statuses := cfg.Jira.StandupStatusesOrDefault()

	// The master list: every in-flight issue assigned to me, exactly once.
	allIssues, err := jira.SearchIssues(jira.AllInFlightJQL(standupAssignee, statuses), standupFields)
	if err != nil {
		return err
	}
	if len(allIssues) == 0 {
		fmt.Println("No in-flight work assigned to you. 🎉")
		return nil
	}

	// Candidate sprints to attach goals/overdue to: active + recently-closed.
	candidates, err := candidateSprints(board)
	if err != nil {
		return err
	}

	groups := assembleGroups(allIssues, candidates, statuses)

	site := cfg.Jira.Site
	if site == "" {
		if h, err := jira.SiteHost(); err == nil {
			site = h
		}
	}
	project := cfg.Jira.Project
	if project == "" {
		project = deriveProject(allIssues)
	}

	if standupJSON {
		return renderStandupJSON(groups, site, project, board)
	}
	renderStandup(os.Stdout, groups, site, project, board, isTerminal())
	return nil
}

// candidateSprints returns the sprints whose in-flight membership we resolve: all
// active sprints plus the most recently-closed ones. When --sprint is set, closed
// sprints are searched broadly and filtered by the given id/name rather than being
// capped by recency.
func candidateSprints(board int) ([]jira.Sprint, error) {
	active, err := jira.ListSprints(board, "active", false)
	if err != nil {
		return nil, err
	}

	var closed []jira.Sprint
	if standupSprint != "" || standupClosed > 0 {
		all, err := jira.ListSprints(board, "closed", true)
		if err != nil {
			return nil, err
		}
		sort.Slice(all, func(i, j int) bool { return all[i].EndDate.After(all[j].EndDate) })
		switch {
		case standupSprint != "":
			closed = all // filtered by name/id below
		case standupClosed < len(all):
			closed = all[:standupClosed]
		default:
			closed = all
		}
	}

	candidates := append(active, closed...)
	if standupSprint != "" {
		candidates = filterSprints(candidates, standupSprint)
	}
	return candidates, nil
}

// assembleGroups resolves which candidate sprint each in-flight issue belongs to
// (first match wins, active before closed) and collects unmatched issues into a
// final catch-all group. Issue display data always comes from the master list, so
// each issue is shown once with consistent fields.
func assembleGroups(allIssues []jira.Issue, candidates []jira.Sprint, statuses []string) []standupGroup {
	byKey := make(map[string]jira.Issue, len(allIssues))
	order := make([]string, len(allIssues))
	for i, iss := range allIssues {
		byKey[iss.Key] = iss
		order[i] = iss.Key
	}

	// Fetch each candidate sprint's in-flight membership concurrently.
	memberKeys := make([][]string, len(candidates))
	var wg sync.WaitGroup
	for i, sp := range candidates {
		wg.Add(1)
		go func(i int, sp jira.Sprint) {
			defer wg.Done()
			// Request a real field alongside the key: acli returns null objects when
			// only "key" is requested, since key is not itself a selectable field.
			jql := jira.InFlightJQL(sp.ID, standupAssignee, statuses)
			issues, err := jira.SearchIssues(jql, standupFields)
			if err != nil {
				return // a sprint we can't read simply contributes no members
			}
			keys := make([]string, 0, len(issues))
			for _, iss := range issues {
				keys = append(keys, iss.Key)
			}
			memberKeys[i] = keys
		}(i, sp)
	}
	wg.Wait()

	assigned := make(map[string]bool, len(allIssues))
	groups := make([]standupGroup, 0, len(candidates)+1)
	for i := range candidates {
		sp := candidates[i]
		var issues []jira.Issue
		for _, key := range memberKeys[i] {
			if iss, ok := byKey[key]; ok && !assigned[key] {
				assigned[key] = true
				issues = append(issues, iss)
			}
		}
		if len(issues) == 0 && !standupAll {
			continue
		}
		groups = append(groups, standupGroup{sprint: &sp, issues: issues})
	}

	// Catch-all: in-flight work not matched to a candidate sprint. Skipped when
	// filtering to a specific sprint.
	if standupSprint == "" {
		var rest []jira.Issue
		for _, key := range order {
			if !assigned[key] {
				rest = append(rest, byKey[key])
			}
		}
		if len(rest) > 0 {
			groups = append(groups, standupGroup{sprint: nil, issues: rest})
		}
	}
	return groups
}

// filterSprints keeps sprints whose id matches exactly or whose name contains the
// filter substring (case-insensitive).
func filterSprints(sprints []jira.Sprint, filter string) []jira.Sprint {
	lower := strings.ToLower(filter)
	var out []jira.Sprint
	for _, s := range sprints {
		if fmt.Sprintf("%d", s.ID) == filter || strings.Contains(strings.ToLower(s.Name), lower) {
			out = append(out, s)
		}
	}
	return out
}

// deriveProject extracts a project key (e.g. "DSPX") from the first issue key.
func deriveProject(issues []jira.Issue) string {
	for _, iss := range issues {
		if k, _, ok := strings.Cut(iss.Key, "-"); ok && k != "" {
			return k
		}
	}
	return ""
}

func renderStandup(w *os.File, groups []standupGroup, site, project string, board int, isTTY bool) {
	// pf ignores write errors: this is best-effort terminal output.
	pf := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

	total := 0
	for _, g := range groups {
		total += len(g.issues)
	}
	pf("Standup — %s · %d in flight\n", time.Now().Format("Mon Jan 2"), total)

	boardURL := links.JiraBoardURL(site, project, board)
	backlogURL := links.JiraBacklogURL(site, project, board)

	for _, g := range groups {
		pf("\n")
		if g.sprint == nil {
			pf("Other in-flight — not in an active or recent sprint   %s\n",
				links.Link(boardURL, "[board]", isTTY))
		} else {
			deadline, _ := sprintDeadline(g.sprint.EndDate, g.sprint.State)
			name := links.Link(boardURL, g.sprint.Name, isTTY)
			pf("%s   %s   %s  %s\n", name, deadline,
				links.Link(boardURL, "[board]", isTTY),
				links.Link(backlogURL, "[backlog]", isTTY))
			if goal := strings.TrimSpace(g.sprint.Goal); goal != "" {
				for i, line := range strings.Split(goal, "\n") {
					prefix := "        "
					if i == 0 {
						prefix = "Goal:   "
					}
					pf("%s%s\n", prefix, line)
				}
			}
		}

		if len(g.issues) == 0 {
			pf("  (no in-flight work assigned to you)\n")
			continue
		}
		pf("\n  %-10s %-12s %-8s %s\n", "KEY", "STATUS", "PRI", "SUMMARY")
		for _, iss := range g.issues {
			key := links.Link(links.JiraIssueURL(site, iss.Key), iss.Key, isTTY)
			pad := ""
			if n := 10 - len(iss.Key); n > 0 {
				pad = strings.Repeat(" ", n)
			}
			pf("  %s%s %-12s %-8s %s\n",
				key, pad,
				truncatePR(iss.Fields.Status.Name, 12),
				truncatePR(iss.Fields.Priority.Name, 8),
				truncatePR(iss.Fields.Summary, 60))
		}
	}
}

// --- JSON output ---

type standupIssueJSON struct {
	Key      string `json:"key"`
	Summary  string `json:"summary"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	URL      string `json:"url"`
}

type standupSprintJSON struct {
	ID          int                `json:"id,omitempty"`
	Name        string             `json:"name"`
	State       string             `json:"state,omitempty"`
	Goal        string             `json:"goal,omitempty"`
	EndDate     string             `json:"endDate,omitempty"`
	DaysOverdue int                `json:"daysOverdue"` // positive when overdue, negative when days remain
	BoardURL    string             `json:"boardUrl,omitempty"`
	BacklogURL  string             `json:"backlogUrl,omitempty"`
	Issues      []standupIssueJSON `json:"issues"`
}

func renderStandupJSON(groups []standupGroup, site, project string, board int) error {
	out := struct {
		Sprints []standupSprintJSON `json:"sprints"`
	}{Sprints: []standupSprintJSON{}}

	boardURL := links.JiraBoardURL(site, project, board)
	backlogURL := links.JiraBacklogURL(site, project, board)

	for _, g := range groups {
		sj := standupSprintJSON{Issues: []standupIssueJSON{}}
		if g.sprint == nil {
			sj.Name = "(no active or recent sprint)"
		} else {
			_, days := sprintDeadline(g.sprint.EndDate, g.sprint.State)
			sj.ID = g.sprint.ID
			sj.Name = g.sprint.Name
			sj.State = g.sprint.State
			sj.Goal = g.sprint.Goal
			sj.DaysOverdue = days
			sj.BoardURL = boardURL
			sj.BacklogURL = backlogURL
			if !g.sprint.EndDate.IsZero() {
				sj.EndDate = g.sprint.EndDate.Format(time.RFC3339)
			}
		}
		for _, iss := range g.issues {
			sj.Issues = append(sj.Issues, standupIssueJSON{
				Key:      iss.Key,
				Summary:  iss.Fields.Summary,
				Status:   iss.Fields.Status.Name,
				Priority: iss.Fields.Priority.Name,
				URL:      links.JiraIssueURL(site, iss.Key),
			})
		}
		out.Sprints = append(out.Sprints, sj)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// sprintDeadline returns a human-readable deadline string and the signed day
// count (positive when overdue, negative when days remain, 0 when due today or
// no end date is set). A closed sprint is always framed as overdue if past.
func sprintDeadline(end time.Time, state string) (string, int) {
	if end.IsZero() {
		return "no end date set", 0
	}
	end = end.Local()
	remaining := dayDiff(time.Now(), end) // >0 future, <0 past
	switch {
	case remaining < 0:
		verb := "overdue"
		if state == "closed" {
			verb = "overdue, sprint closed"
		}
		return fmt.Sprintf("%dd %s (ended %s)", -remaining, verb, end.Format("Jan 2")), -remaining
	case remaining == 0:
		return fmt.Sprintf("due today (%s)", end.Format("Jan 2")), 0
	default:
		return fmt.Sprintf("%dd left (ends %s)", remaining, end.Format("Jan 2")), -remaining
	}
}

// dayDiff returns the number of whole calendar days from `from` to `to` in local
// time (positive when `to` is later).
func dayDiff(from, to time.Time) int {
	from = from.Local()
	to = to.Local()
	fy, fm, fd := from.Date()
	ty, tm, td := to.Date()
	f := time.Date(fy, fm, fd, 0, 0, 0, 0, time.Local)
	t := time.Date(ty, tm, td, 0, 0, 0, 0, time.Local)
	return int(t.Sub(f).Hours() / 24)
}
