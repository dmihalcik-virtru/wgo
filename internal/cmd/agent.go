package cmd

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/store"
	"github.com/virtru/wgo/models"
)

const (
	// heartbeatThrottle bounds how often the env-detected agent heartbeat
	// rewrites state.json on the hot path: a session touched within this window
	// is left alone.
	heartbeatThrottle = 60 * time.Second
	// agentStaleAfter is how long after its last activity a session is still
	// considered active. Env-detected sessions are kept alive by the hot-path
	// heartbeat; once an agent stops running wgo, its session expires.
	agentStaleAfter = 10 * time.Minute
)

// agentCmd is the parent for agent-session tracking.
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Track which AI agent is working in which workspace",
	Long: `Record and inspect AI agent sessions across workspaces.

Claude Code sessions self-register automatically: when wgo runs with the
CLAUDECODE environment variable set (as it is inside Claude Code), "wgo ." and
"wgo statusline" refresh a session for the current workspace, which appears as
🤖 in the context and expires shortly after the agent stops. Use the
subcommands to record sessions for other agents by hand.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runAgentStatus()
	},
}

var agentStartCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Record an agent session for the current workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runAgentStart(args[0])
	},
}

var agentStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Clear the agent session for the current workspace",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runAgentStop()
	},
}

var agentStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show active agent sessions across workspaces",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runAgentStatus()
	},
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentStartCmd)
	agentCmd.AddCommand(agentStopCmd)
	agentCmd.AddCommand(agentStatusCmd)
}

// workspaceRoot resolves the current workspace root used as the agent-session
// key, matching the key buildContextOpts uses when populating ctx.Agent.
func workspaceRoot() (string, error) {
	cwd, err := resolveCwd()
	if err != nil {
		return "", err
	}
	jjc := jj.NewCLI()
	if !jjc.IsRepo(cwd) {
		return "", fmt.Errorf("not a jj repository")
	}
	wsRoot, err := jjc.Root(cwd)
	if err != nil {
		return "", fmt.Errorf("failed to get workspace root: %w", err)
	}
	return wsRoot, nil
}

func runAgentStart(name string) error {
	wsRoot, err := workspaceRoot()
	if err != nil {
		return err
	}
	branch := currentBookmark(jj.NewCLI(), wsRoot)

	s, err := store.New()
	if err != nil {
		return err
	}
	state, err := s.LoadState()
	if err != nil {
		return err
	}
	state.UpsertAgentSession(wsRoot, name, branch, os.Getppid())
	if err := s.SaveState(state); err != nil {
		return err
	}
	fmt.Printf("🤖 %s working in %s\n", name, wsRoot)
	return nil
}

func runAgentStop() error {
	wsRoot, err := workspaceRoot()
	if err != nil {
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
	if state.GetAgentSession(wsRoot) == nil {
		fmt.Printf("no agent session for %s\n", wsRoot)
		return nil
	}
	state.RemoveAgentSession(wsRoot)
	if err := s.SaveState(state); err != nil {
		return err
	}
	fmt.Printf("cleared agent session for %s\n", wsRoot)
	return nil
}

func runAgentStatus() error {
	s, err := store.New()
	if err != nil {
		return err
	}
	state, err := s.LoadState()
	if err != nil {
		return err
	}
	active := state.ActiveAgentSessions(agentStaleAfter)
	if len(active) == 0 {
		fmt.Println("No active agent sessions.")
		return nil
	}

	// Mark the current workspace when it is a jj repo (best-effort).
	current, _ := workspaceRoot()

	roots := make([]string, 0, len(active))
	for root := range active {
		roots = append(roots, root)
	}
	sort.Strings(roots)

	for _, root := range roots {
		sess := active[root]
		marker := ""
		if root == current {
			marker = " (current)"
		}
		branch := sess.Branch
		if branch == "" {
			branch = "(no bookmark)"
		}
		fmt.Printf("🤖 %-8s %s  %s  since %s%s\n",
			sess.Tool, root, branch, formatTime(sess.StartTime), marker)
	}
	return nil
}

// detectAgent returns the agent name when wgo is running inside a recognized
// agent, or "" otherwise. Claude Code sets CLAUDECODE in the environment.
func detectAgent() string {
	if os.Getenv("CLAUDECODE") != "" {
		return "claude"
	}
	return ""
}

// heartbeatAgent refreshes the env-detected agent session for wsRoot on the hot
// path. It is best-effort and throttled: a same-tool session touched within
// heartbeatThrottle is left alone so frequent statusline renders don't churn
// state.json. Writes are local disk only (no network), so the statusline hot
// path stays network-free.
func heartbeatAgent(wsRoot, branch string) {
	name := detectAgent()
	if name == "" || wsRoot == "" {
		return
	}
	s, err := store.New()
	if err != nil {
		return
	}
	state, err := s.LoadState()
	if err != nil {
		return
	}
	if existing := state.GetAgentSession(wsRoot); existing != nil &&
		existing.Tool == name && time.Since(existing.LastActivity) < heartbeatThrottle {
		return
	}
	state.UpsertAgentSession(wsRoot, name, branch, os.Getppid())
	_ = s.SaveState(state)
}

// resolveAgent returns the active (non-stale) agent session for wsRoot, or nil.
func resolveAgent(wsRoot string) *models.AgentRef {
	if wsRoot == "" {
		return nil
	}
	s, err := store.New()
	if err != nil {
		return nil
	}
	state, err := s.LoadState()
	if err != nil {
		return nil
	}
	sess := state.GetAgentSession(wsRoot)
	if sess == nil || time.Since(sess.LastActivity) > agentStaleAfter {
		return nil
	}
	return &models.AgentRef{Name: sess.Tool, Since: sess.StartTime}
}
