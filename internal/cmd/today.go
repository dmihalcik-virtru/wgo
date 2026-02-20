package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/bujo"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

// todayCmd represents the `wgo today` command.
var todayCmd = &cobra.Command{
	Use:   "today",
	Short: "Morning view: active tasks and yesterday's summary",
	Long:  `Show a bullet journal morning view: active tasks, backlog, and yesterday's summary.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return showToday()
	},
}

func init() {
	rootCmd.AddCommand(todayCmd)
}

func showToday() error {
	now := time.Now()

	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	content, err := s.LoadPlan()
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	p, err := plan.Parse(content)
	if err != nil {
		return fmt.Errorf("failed to parse plan: %w", err)
	}

	// Date header
	fmt.Printf("📅 %s\n\n", now.Format("Monday, Jan 2 2006"))

	// Active tasks
	active := make([]bujo.Task, 0)
	backlog := make([]bujo.Task, 0)
	for _, t := range p.GetPendingTasks() {
		if t.Bullet == bujo.BulletInProgress || t.Bullet == bujo.BulletPriority {
			active = append(active, t)
		} else {
			backlog = append(backlog, t)
		}
	}

	if len(active) > 0 {
		fmt.Println("◉  Active Work")
		for _, t := range active {
			printTaskLine(t)
		}
		fmt.Println()
	}

	if len(backlog) > 0 {
		show := backlog
		overflow := 0
		if len(backlog) > 5 {
			overflow = len(backlog) - 5
			show = backlog[:5]
		}
		fmt.Printf("○  Backlog")
		if overflow > 0 {
			fmt.Printf(" (%d more)", overflow)
		}
		fmt.Println()
		for _, t := range show {
			printTaskLine(t)
		}
		fmt.Println()
	}

	// Yesterday's summary
	yesterday := bujo.FormatDate(now.AddDate(0, 0, -1))
	yl, err := s.LoadDailyLog(yesterday)
	if err == nil && yl != nil && (len(yl.Completed) > 0 || len(yl.Cancelled) > 0 || len(yl.Events) > 0) {
		fmt.Println("📊 Yesterday")
		if len(yl.Completed) > 0 {
			fmt.Printf("   ✓ %d completed", len(yl.Completed))
		}
		if len(yl.Cancelled) > 0 {
			fmt.Printf("  ✗ %d cancelled", len(yl.Cancelled))
		}
		if len(yl.Completed)+len(yl.Cancelled) > 0 {
			fmt.Println()
		}
		created := 0
		pushed := 0
		for _, ev := range yl.Events {
			switch ev.Kind {
			case "created":
				created++
			case "pushed":
				pushed++
			}
		}
		if created > 0 {
			fmt.Printf("   🌿 %d branches created", created)
		}
		if pushed > 0 {
			fmt.Printf("  📤 %d pushes", pushed)
		}
		if created+pushed > 0 {
			fmt.Println()
		}
		fmt.Println()
	}

	return nil
}

func printTaskLine(t bujo.Task) {
	line := "   " + string(t.Bullet) + " " + t.Text
	if len(t.Refs) > 0 {
		ref := t.Refs[0]
		refStr := ""
		if ref.Branch != "" {
			refStr = ref.Repo + ":" + ref.Branch
		} else if ref.PR > 0 {
			refStr = fmt.Sprintf("%s PR #%d", ref.Repo, ref.PR)
		} else if ref.Issue > 0 {
			refStr = fmt.Sprintf("%s issue #%d", ref.Repo, ref.Issue)
		}
		if refStr != "" {
			line = fmt.Sprintf("%-60s %s", line, refStr)
		}
	}
	fmt.Println(line)
}
