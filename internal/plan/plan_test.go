package plan

import (
	"strings"
	"testing"
)

func TestParseBasic(t *testing.T) {
	content := `# Plan

## Active Branches

- **virtru/wgo:feat/plan-parser** — Add initial plan file parsing

## Notes

This is a note
`

	plan, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	entry := plan.GetBranch("virtru/wgo", "feat/plan-parser")
	if entry == nil {
		t.Errorf("expected to find branch entry")
	} else if entry.Reason != "Add initial plan file parsing" {
		t.Errorf("expected reason 'Add initial plan file parsing', got %q", entry.Reason)
	}

	if !strings.Contains(plan.Notes, "This is a note") {
		t.Errorf("expected notes to contain 'This is a note'")
	}
}

func TestRoundTrip(t *testing.T) {
	content := `# Plan

## Active Branches

- **virtru/wgo:feat/plan-parser** — Add initial plan file parsing
- **virtru/api:fix/auth** — Fix auth bug

## Notes

Some manual notes here.
`

	plan, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	rendered := plan.Render()

	// Parse again
	plan2, err := Parse(rendered)
	if err != nil {
		t.Fatalf("Parse of rendered content failed: %v", err)
	}

	// Check branches are preserved
	entry1 := plan2.GetBranch("virtru/wgo", "feat/plan-parser")
	if entry1 == nil {
		t.Errorf("expected to find first branch after round-trip")
	} else if entry1.Reason != "Add initial plan file parsing" {
		t.Errorf("expected reason preserved after round-trip")
	}

	entry2 := plan2.GetBranch("virtru/api", "fix/auth")
	if entry2 == nil {
		t.Errorf("expected to find second branch after round-trip")
	}

	// Check notes are preserved
	if !strings.Contains(plan2.Notes, "Some manual notes") {
		t.Errorf("expected notes preserved after round-trip, got %q", plan2.Notes)
	}
}

func TestAddBranch(t *testing.T) {
	plan := &Plan{
		ActiveBranches: make(map[string]BranchEntry),
		Efforts:        make(map[string]EffortEntry),
	}

	plan.AddBranch("repo1", "feature", "Test feature")

	entry := plan.GetBranch("repo1", "feature")
	if entry == nil {
		t.Errorf("expected to find added branch")
	} else if entry.Reason != "Test feature" {
		t.Errorf("expected reason 'Test feature', got %q", entry.Reason)
	}
}

func TestRemoveBranch(t *testing.T) {
	plan := &Plan{
		ActiveBranches: make(map[string]BranchEntry),
		Efforts:        make(map[string]EffortEntry),
	}

	plan.AddBranch("repo1", "feature", "Test feature")
	plan.RemoveBranch("repo1", "feature")

	entry := plan.GetBranch("repo1", "feature")
	if entry != nil {
		t.Errorf("expected branch to be removed")
	}
}

func TestEmptyPlan(t *testing.T) {
	content := `# Plan

## Active Branches

## Notes
`

	plan, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(plan.ActiveBranches) != 0 {
		t.Errorf("expected empty active branches")
	}

	rendered := plan.Render()
	if !strings.Contains(rendered, "# Plan") {
		t.Errorf("expected rendered plan to contain header")
	}
}

func TestParseMultipleBranches(t *testing.T) {
	content := `# Plan

## Active Branches

- **repo1:branch1** — Reason 1
- **repo2:branch2** — Reason 2
- **repo1:branch3** — Reason 3

## Notes
`

	plan, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(plan.ActiveBranches) != 3 {
		t.Errorf("expected 3 branches, got %d", len(plan.ActiveBranches))
	}

	if plan.GetBranch("repo1", "branch1") == nil {
		t.Errorf("expected to find repo1:branch1")
	}

	if plan.GetBranch("repo2", "branch2") == nil {
		t.Errorf("expected to find repo2:branch2")
	}

	if plan.GetBranch("repo1", "branch3") == nil {
		t.Errorf("expected to find repo1:branch3")
	}
}

func TestRenderPreservesStructure(t *testing.T) {
	plan := &Plan{
		ActiveBranches: make(map[string]BranchEntry),
		Efforts:        make(map[string]EffortEntry),
		Notes:          "Test notes",
	}

	plan.AddBranch("repo", "branch", "reason")

	rendered := plan.Render()

	if !strings.Contains(rendered, "# Plan") {
		t.Errorf("expected plan header in rendered output")
	}

	if !strings.Contains(rendered, "## Active Branches") {
		t.Errorf("expected Active Branches section in rendered output")
	}

	if !strings.Contains(rendered, "repo:branch") {
		t.Errorf("expected branch in rendered output")
	}

	if !strings.Contains(rendered, "## Notes") {
		t.Errorf("expected Notes section in rendered output")
	}

	if !strings.Contains(rendered, "Test notes") {
		t.Errorf("expected notes content in rendered output")
	}
}
