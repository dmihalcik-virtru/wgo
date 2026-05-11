package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/jira"
	"github.com/virtru/wgo/internal/spec"
)

var specJiraCmd = &cobra.Command{
	Use:   "jira",
	Short: "Jira integration for spec files (requires acli)",
}

var specJiraFocusCmd = &cobra.Command{
	Use:   "focus [TICKET]",
	Short: "Show spec body then live Jira context (details + recent comments)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSpecJiraFocus(args)
	},
}

var specJiraSyncPush bool

var specJiraSyncCmd = &cobra.Command{
	Use:   "sync [TICKET]",
	Short: "Update spec frontmatter (title, status, jira_priority) from Jira; --push writes back",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSpecJiraSync(args)
	},
}

var specJiraPullCmd = &cobra.Command{
	Use:   "pull [TICKET]",
	Short: "Append or replace ## Jira Notes section in spec body with recent comments",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSpecJiraPull(args)
	},
}

var specJiraUsersCmd = &cobra.Command{
	Use:   "users [TICKET]",
	Short: "Append or replace ## Stakeholders section with Jira assignee and watchers",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSpecJiraUsers(args)
	},
}

func init() {
	specCmd.AddCommand(specJiraCmd)
	specJiraCmd.AddCommand(specJiraFocusCmd)
	specJiraCmd.AddCommand(specJiraSyncCmd)
	specJiraCmd.AddCommand(specJiraPullCmd)
	specJiraCmd.AddCommand(specJiraUsersCmd)

	specJiraSyncCmd.Flags().BoolVar(&specJiraSyncPush, "push", false, "Also write spec title and summary back to Jira; transition ticket if status is terminal")
}

// --- focus ---

func runSpecJiraFocus(args []string) error {
	if err := jira.CheckAuth(); err != nil {
		return err
	}
	root, ticket, err := resolveSpecTarget(args)
	if err != nil {
		return err
	}
	specPath, err := spec.FindByTicket(root, ticket)
	if err != nil {
		return fmt.Errorf("no spec for %s — run: wgo spec new %s", ticket, ticket)
	}

	// Print spec body.
	data, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	fmt.Print(string(data))

	// Print Jira context.
	fmt.Printf("\n%s\n", strings.Repeat("─", 60))
	fmt.Printf("JIRA: %s\n", ticket)
	fmt.Printf("%s\n\n", strings.Repeat("─", 60))

	issue, err := jira.GetIssue(ticket)
	if err != nil {
		return err
	}
	fmt.Printf("Title:    %s\n", issue.Fields.Summary)
	fmt.Printf("Status:   %-20s Priority: %s\n", issue.Fields.Status.Name, issue.Fields.Priority.Name)
	if issue.Fields.Assignee != nil {
		fmt.Printf("Assignee: %s <%s>\n", issue.Fields.Assignee.DisplayName, issue.Fields.Assignee.EmailAddress)
	} else {
		fmt.Printf("Assignee: (unassigned)\n")
	}

	comments, err := jira.GetComments(ticket, 10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not fetch comments: %v\n", err)
		return nil
	}
	if len(comments) == 0 {
		fmt.Printf("\nNo comments.\n")
		return nil
	}
	fmt.Printf("\nRecent Comments (%d):\n", len(comments))
	for _, c := range comments {
		date := ""
		if !c.Created.IsZero() {
			date = c.Created.Format("2006-01-02")
		}
		body := c.Body
		if len(body) > 200 {
			body = body[:197] + "…"
		}
		// Replace internal newlines with spaces for compact display.
		body = strings.ReplaceAll(strings.TrimSpace(body), "\n", " ")
		fmt.Printf("\n[%s, %s]\n%s\n", c.Author.DisplayName, date, body)
	}
	return nil
}

// --- sync ---

func runSpecJiraSync(args []string) error {
	if err := jira.CheckAuth(); err != nil {
		return err
	}
	root, ticket, err := resolveSpecTarget(args)
	if err != nil {
		return err
	}
	specPath, err := spec.FindByTicket(root, ticket)
	if err != nil {
		return fmt.Errorf("no spec for %s — run: wgo spec new %s", ticket, ticket)
	}

	issue, err := jira.GetIssue(ticket)
	if err != nil {
		return err
	}

	var (
		newTitle    string
		newStatus   spec.Status
		newPriority string
	)

	if err := spec.UpdateFrontmatter(specPath, func(fm *spec.Frontmatter) error {
		newTitle = issue.Fields.Summary
		newPriority = issue.Fields.Priority.Name

		if newTitle != "" {
			fm.Title = newTitle
		}
		if newPriority != "" {
			fm.JiraPriority = newPriority
		}
		if mapped := jira.MapJiraStatus(issue.Fields.Status); mapped != "" {
			newStatus = spec.Status(mapped)
			fm.Status = newStatus
		} else {
			newStatus = fm.Status
		}
		fm.Updated = time.Now().Truncate(24 * time.Hour)
		return nil
	}); err != nil {
		return fmt.Errorf("update frontmatter: %w", err)
	}

	fmt.Printf("synced %s: title=%q status=%s priority=%s\n",
		ticket, newTitle, newStatus, newPriority)

	if specJiraSyncPush {
		if err := pushSpecToJira(specPath, ticket, string(newTitle), string(newStatus)); err != nil {
			return fmt.Errorf("push to Jira: %w", err)
		}
	}
	return nil
}

// pushSpecToJira writes the spec title and ## Summary section body back to Jira,
// and transitions the ticket if the spec status is terminal.
func pushSpecToJira(specPath, ticket, title, status string) error {
	sf, err := spec.Parse(specPath)
	if err != nil {
		return fmt.Errorf("parse spec: %w", err)
	}

	summaryBody := extractSection(sf.Body, "## Summary")

	if err := jira.UpdateIssue(ticket, title, summaryBody); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "pushed title and summary to Jira: %s\n", ticket)

	if jiraStatus := jira.MapSpecStatus(status); jiraStatus != "" && isTerminalSpecStatus(status) {
		if err := jira.TransitionIssue(ticket, jiraStatus); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not transition %s to %q: %v\n", ticket, jiraStatus, err)
		} else {
			fmt.Fprintf(os.Stderr, "transitioned %s → %s\n", ticket, jiraStatus)
		}
	}
	return nil
}

// extractSection returns the text body of the first matching ## heading section,
// stopping at the next ## heading or EOF. Returns "" if the section is not found.
func extractSection(body, heading string) string {
	marker := "\n" + heading
	idx := strings.Index(body, marker)
	if idx < 0 {
		// Also try at the very start of body.
		if strings.HasPrefix(strings.TrimSpace(body), heading) {
			body = strings.TrimSpace(body)
			idx = 0
			marker = heading
		} else {
			return ""
		}
	}
	after := body[idx+len(marker):]
	if next := strings.Index(after, "\n## "); next >= 0 {
		after = after[:next]
	}
	return strings.TrimSpace(after)
}

func isTerminalSpecStatus(status string) bool {
	return status == "shipped" || status == "abandoned"
}

// --- pull ---

func runSpecJiraPull(args []string) error {
	if err := jira.CheckAuth(); err != nil {
		return err
	}
	root, ticket, err := resolveSpecTarget(args)
	if err != nil {
		return err
	}
	specPath, err := spec.FindByTicket(root, ticket)
	if err != nil {
		return fmt.Errorf("no spec for %s — run: wgo spec new %s", ticket, ticket)
	}

	comments, err := jira.GetComments(ticket, 20)
	if err != nil {
		return err
	}

	section := renderJiraNotes(comments)
	if err := replaceSection(specPath, "## Jira Notes", section); err != nil {
		return fmt.Errorf("update spec: %w", err)
	}

	fmt.Fprintf(os.Stderr, "pulled %d comments into spec/%s.md\n", len(comments), ticket)
	return nil
}

func renderJiraNotes(comments []jira.Comment) string {
	var sb strings.Builder
	sb.WriteString("## Jira Notes\n")
	fmt.Fprintf(&sb, "_Last synced: %s_\n", time.Now().Format("2006-01-02"))
	if len(comments) == 0 {
		sb.WriteString("\n_No comments._\n")
		return sb.String()
	}
	for _, c := range comments {
		date := ""
		if !c.Created.IsZero() {
			date = c.Created.Format("2006-01-02")
		}
		fmt.Fprintf(&sb, "\n**%s** (%s):\n", c.Author.DisplayName, date)
		for line := range strings.SplitSeq(strings.TrimSpace(c.Body), "\n") {
			sb.WriteString("> " + line + "\n")
		}
	}
	return sb.String()
}

// --- users ---

func runSpecJiraUsers(args []string) error {
	if err := jira.CheckAuth(); err != nil {
		return err
	}
	root, ticket, err := resolveSpecTarget(args)
	if err != nil {
		return err
	}
	specPath, err := spec.FindByTicket(root, ticket)
	if err != nil {
		return fmt.Errorf("no spec for %s — run: wgo spec new %s", ticket, ticket)
	}

	issue, err := jira.GetIssue(ticket)
	if err != nil {
		return err
	}
	watchers, err := jira.GetWatchers(ticket)
	if err != nil {
		return err
	}

	users := deduplicateUsers(issue.Fields.Assignee, watchers)
	section := renderStakeholders(users)
	if err := replaceSection(specPath, "## Stakeholders", section); err != nil {
		return fmt.Errorf("update spec: %w", err)
	}

	fmt.Fprintf(os.Stderr, "updated ## Stakeholders with %d users in spec/%s.md\n", len(users), ticket)
	return nil
}

func deduplicateUsers(assignee *jira.User, watchers []jira.User) []jira.User {
	seen := make(map[string]bool)
	var users []jira.User
	add := func(u jira.User) {
		if u.AccountID != "" && !seen[u.AccountID] {
			seen[u.AccountID] = true
			users = append(users, u)
		}
	}
	if assignee != nil {
		add(*assignee)
	}
	for _, w := range watchers {
		add(w)
	}
	return users
}

func renderStakeholders(users []jira.User) string {
	var sb strings.Builder
	sb.WriteString("## Stakeholders\n")
	fmt.Fprintf(&sb, "_Last synced: %s_\n", time.Now().Format("2006-01-02"))
	if len(users) == 0 {
		sb.WriteString("\n_No stakeholders found._\n")
		return sb.String()
	}
	sb.WriteString("\n| Name | Email |\n|------|-------|\n")
	for _, u := range users {
		fmt.Fprintf(&sb, "| %s | %s |\n", u.DisplayName, u.EmailAddress)
	}
	return sb.String()
}

// --- shared helpers ---

// replaceSection replaces or appends a section (identified by its heading) in a spec file.
// The section runs from the heading to the next top-level ## heading (or EOF).
func replaceSection(specPath, heading, newSection string) error {
	data, err := os.ReadFile(specPath)
	if err != nil {
		return err
	}
	content := string(data)

	// Find the section start: must be at the beginning of a line.
	marker := "\n" + heading
	if idx := strings.Index(content, marker); idx >= 0 {
		before := content[:idx]
		rest := content[idx+len(marker):]
		// Find the next ## heading to know where the section ends.
		nextH2 := strings.Index(rest, "\n## ")
		var after string
		if nextH2 >= 0 {
			after = rest[nextH2:]
		}
		content = before + "\n" + newSection + after
	} else {
		// Append.
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n" + newSection
	}

	tmpPath := specPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, specPath)
}
