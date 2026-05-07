package plan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseBasic(t *testing.T) {
	content := `# Plan

## Active Branches

- **virtru/wgo:feat/plan-parser** — Add initial plan file parsing

## Notes

This is a note
`

	plan, err := Parse(content)
	require.NoError(t, err)

	entry := plan.GetBranch("virtru/wgo", "feat/plan-parser")
	require.NotNil(t, entry)
	assert.Equal(t, "Add initial plan file parsing", entry.Reason)
	assert.Contains(t, plan.Notes, "This is a note")
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
	require.NoError(t, err)

	rendered := plan.Render()

	// Parse again
	plan2, err := Parse(rendered)
	require.NoError(t, err)

	// Check branches are preserved
	entry1 := plan2.GetBranch("virtru/wgo", "feat/plan-parser")
	require.NotNil(t, entry1)
	assert.Equal(t, "Add initial plan file parsing", entry1.Reason)

	entry2 := plan2.GetBranch("virtru/api", "fix/auth")
	assert.NotNil(t, entry2)

	// Check notes are preserved
	assert.Contains(t, plan2.Notes, "Some manual notes")
}

func TestAddBranch(t *testing.T) {
	plan := &Plan{
		ActiveBranches: make(map[string]BranchEntry),
		Efforts:        make(map[string]EffortEntry),
	}

	plan.AddBranch("repo1", "feature", "Test feature", "spec/WGO-101.md")

	entry := plan.GetBranch("repo1", "feature")
	require.NotNil(t, entry)
	assert.Equal(t, "Test feature", entry.Reason)
	assert.Equal(t, "spec/WGO-101.md", entry.SpecPath)
}

func TestRemoveBranch(t *testing.T) {
	plan := &Plan{
		ActiveBranches: make(map[string]BranchEntry),
		Efforts:        make(map[string]EffortEntry),
	}

	plan.AddBranch("repo1", "feature", "Test feature")
	plan.RemoveBranch("repo1", "feature")

	entry := plan.GetBranch("repo1", "feature")
	assert.Nil(t, entry)
}

func TestEmptyPlan(t *testing.T) {
	content := `# Plan

## Active Branches

## Notes
`

	plan, err := Parse(content)
	require.NoError(t, err)

	assert.Empty(t, plan.ActiveBranches)

	rendered := plan.Render()
	assert.Contains(t, rendered, "# Plan")
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
	require.NoError(t, err)

	assert.Len(t, plan.ActiveBranches, 3)
	assert.NotNil(t, plan.GetBranch("repo1", "branch1"))
	assert.NotNil(t, plan.GetBranch("repo2", "branch2"))
	assert.NotNil(t, plan.GetBranch("repo1", "branch3"))
}

func TestRenderPreservesStructure(t *testing.T) {
	plan := &Plan{
		ActiveBranches: make(map[string]BranchEntry),
		Efforts:        make(map[string]EffortEntry),
		Notes:          "Test notes",
	}

	plan.AddBranch("repo", "branch", "reason")

	rendered := plan.Render()

	assert.Contains(t, rendered, "# Plan")
	assert.Contains(t, rendered, "## Active Branches")
	assert.Contains(t, rendered, "repo:branch")
	assert.Contains(t, rendered, "## Notes")
	assert.Contains(t, rendered, "Test notes")
}

func TestParseBranchWithSpecPath(t *testing.T) {
	content := `# Plan

## Active Branches

- **virtru/wgo:WGO-101-spec-scaffold** — WGO-101: scaffold specs 📄 spec/WGO-101.md

## Notes
`

	plan, err := Parse(content)
	require.NoError(t, err)

	entry := plan.GetBranch("virtru/wgo", "WGO-101-spec-scaffold")
	require.NotNil(t, entry)
	assert.Equal(t, "spec/WGO-101.md", entry.SpecPath)
	assert.Equal(t, "WGO-101: scaffold specs", entry.Reason)
}

func TestAddBranchPreservesExistingSpecPath(t *testing.T) {
	plan := &Plan{
		ActiveBranches: map[string]BranchEntry{
			"repo1:feature": {
				Repo:     "repo1",
				Branch:   "feature",
				Reason:   "old",
				SpecPath: "spec/WGO-101.md",
			},
		},
		Efforts: make(map[string]EffortEntry),
	}

	plan.AddBranch("repo1", "feature", "updated")

	entry := plan.GetBranch("repo1", "feature")
	require.NotNil(t, entry)
	assert.Equal(t, "spec/WGO-101.md", entry.SpecPath)
}
