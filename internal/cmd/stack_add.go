package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/stack"
	"github.com/virtru/wgo/internal/store"
)

var (
	stackAddParents []string
	stackAddStackID string
)

var stackAddCmd = &cobra.Command{
	Use:   "add [<branch>]",
	Short: "Add an existing branch to a stack with specified parent(s)",
	Long: `Add an existing branch to a stack, creating non-linear DAG relationships.

Unlike 'wgo stack push' which creates new branches, 'add' works with branches
that already exist locally or on origin. The branch can have one or more parents
(for merge nodes). If no stack is specified, it inherits the stack ID from the
first parent.

Examples:
  # Add current branch to stack, branching off 'b'
  wgo stack add --on b

  # Add branch 'm' to stack, parallel to 'c' (both on 'b')
  wgo stack add m --on b

  # Add merge node with multiple parents
  wgo stack add merge-feature --on feature-a --on feature-b

  # Explicitly specify stack ID
  wgo stack add m --on b --stack abc123`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, args []string) error {
		branch := ""
		if len(args) == 1 {
			branch = args[0]
		}
		return runStackAdd(branch)
	},
}

func init() {
	stackAddCmd.Flags().StringSliceVar(&stackAddParents, "on", nil,
		"Parent branch(es) to base on (repeatable for merge nodes)")
	stackAddCmd.Flags().StringVar(&stackAddStackID, "stack", "",
		"Explicit stack ID (defaults to inheriting from first parent)")
}

func runStackAdd(branch string) error {
	// 1. Resolve target branch
	co, err := currentCheckout()
	if err != nil {
		return err
	}
	if branch == "" {
		branch = co.Branch
	}

	// 2. Load state
	s, state, err := loadStateForStack()
	if err != nil {
		return err
	}

	// 3. Validate branch exists
	g := git.New("")
	exists, err := g.BranchExists(co.RepoPath, branch)
	if err != nil {
		return fmt.Errorf("check branch %s: %w", branch, err)
	}
	if !exists {
		return fmt.Errorf("branch %q not found locally or on origin", branch)
	}

	// 4. Resolve and validate parents
	if len(stackAddParents) == 0 {
		return fmt.Errorf("at least one parent required (use --on <parent>)")
	}

	var parentKeys []string
	for _, p := range stackAddParents {
		pExists, err := g.BranchExists(co.RepoPath, p)
		if err != nil {
			return fmt.Errorf("check parent %s: %w", p, err)
		}
		if !pExists {
			return fmt.Errorf("parent branch %q not found locally or on origin", p)
		}
		parentKeys = append(parentKeys, store.AnnotationKey(co.RepoPath, p))
	}

	// 5. Check existing stack membership
	childKey := store.AnnotationKey(co.RepoPath, branch)
	existingAnn := state.GetAnnotation(co.RepoPath, branch)
	if existingAnn != nil && existingAnn.StackID != "" {
		// Branch already in a stack
		if stackAddStackID != "" && existingAnn.StackID != stackAddStackID {
			// Trying to move to different stack
			return fmt.Errorf("branch %q is already in stack %q\nremove it first with: wgo stack rm %s\nor move it with: wgo stack move %s --to %s",
				branch, existingAnn.StackID, branch, branch, stackAddStackID)
		}
		if stackAddStackID == "" {
			// Re-parenting within same stack
			stackAddStackID = existingAnn.StackID
		}
	}

	// 6. Infer or validate stack ID
	if stackAddStackID == "" {
		// Inherit from first parent
		for _, pk := range parentKeys {
			pRepo, pBranch, err := keyParts(pk)
			if err != nil {
				return err
			}
			if pAnn := state.GetAnnotation(pRepo, pBranch); pAnn != nil && pAnn.StackID != "" {
				stackAddStackID = pAnn.StackID
				break
			}
		}
		if stackAddStackID == "" {
			return fmt.Errorf("no parent is part of a stack; specify --stack or run: wgo stack new <name>")
		}
	}

	// Validate stack exists
	st := state.GetStack(stackAddStackID)
	if st == nil {
		return fmt.Errorf("stack %q does not exist\nlist stacks with: wgo stack status --all", stackAddStackID)
	}

	// 7. Cycle detection for each parent
	for _, pk := range parentKeys {
		if stack.WouldCreateCycle(state, stackAddStackID, childKey, pk) {
			_, pBranch, _ := keyParts(pk)
			return fmt.Errorf("adding %s as parent of %s would create a cycle", pBranch, branch)
		}
	}

	// 8. Commit to state
	if state.GetAnnotation(co.RepoPath, branch) == nil {
		state.AddAnnotation(co.RepoPath, branch, "")
	}
	state.SetStackID(co.RepoPath, branch, stackAddStackID)
	state.SetParents(co.RepoPath, branch, parentKeys)

	if err := s.SaveState(state); err != nil {
		return err
	}

	// 9. Print success output
	stackName := st.Name
	if stackName == "" {
		stackName = stackAddStackID
	}

	// Format parent names
	var parentNames []string
	for _, pk := range parentKeys {
		_, pBranch, _ := keyParts(pk)
		parentNames = append(parentNames, pBranch)
	}
	sort.Strings(parentNames)

	fmt.Printf("added %s to stack %q (%s)\n", branch, stackName, stackAddStackID)
	if len(parentNames) == 1 {
		fmt.Printf("  %s ↳ on %s\n", branch, parentNames[0])
	} else {
		fmt.Printf("  %s ↳ on %s (merge node)\n", branch, strings.Join(parentNames, ", "))
	}

	return nil
}
