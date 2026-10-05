package plan

import (
	"strings"
	"testing"
)

func TestParseEfforts(t *testing.T) {
	content := `# Plan

## Efforts

### Feature X
This is a description for Feature X.

- repo1:branch1
- repo2:branch2

### Feature Y
Another feature.

- repo3:branch3

## Notes
Some notes here.
`

	p, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(p.Efforts) != 2 {
		t.Errorf("expected 2 efforts, got %d", len(p.Efforts))
	}

	// Check Feature X
	featureXID := GenerateEffortID("Feature X")
	if effort, exists := p.Efforts[featureXID]; !exists {
		t.Errorf("Feature X not found")
	} else {
		if effort.Name != "Feature X" {
			t.Errorf("expected name 'Feature X', got '%s'", effort.Name)
		}
		if effort.Description != "This is a description for Feature X." {
			t.Errorf("unexpected description: %s", effort.Description)
		}
		if len(effort.Branches) != 2 {
			t.Errorf("expected 2 branches, got %d", len(effort.Branches))
		}
	}

	// Check effort order
	if len(p.EffortOrder) != 2 {
		t.Errorf("expected 2 efforts in order, got %d", len(p.EffortOrder))
	}
}

func TestPreserveUnmanagedSections(t *testing.T) {
	content := `# Plan

## Tasks

- [ ] Task 1

## Custom Section

This is custom content.
More content here.

## Active Branches

- **repo:branch** — reason

## Notes
`

	p, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	// Check that custom section was preserved
	if len(p.UnmanagedSections) != 1 {
		t.Fatalf("expected 1 unmanaged section, got %d", len(p.UnmanagedSections))
	}

	sec := p.UnmanagedSections[0]
	if sec.Header != "## Custom Section" {
		t.Errorf("expected header '## Custom Section', got '%s'", sec.Header)
	}

	if !strings.Contains(sec.Content, "This is custom content.") {
		t.Errorf("custom content not preserved: %s", sec.Content)
	}

	// Render and check that custom section appears
	rendered := p.Render()
	if !strings.Contains(rendered, "## Custom Section") {
		t.Errorf("custom section not in rendered output")
	}
	if !strings.Contains(rendered, "This is custom content.") {
		t.Errorf("custom content not in rendered output")
	}
}

func TestEffortIDStable(t *testing.T) {
	// Same name should produce same ID
	id1 := GenerateEffortID("Feature X")
	id2 := GenerateEffortID("Feature X")

	if id1 != id2 {
		t.Errorf("IDs not stable: %s != %s", id1, id2)
	}

	// Different names should produce different IDs
	id3 := GenerateEffortID("Feature Y")
	if id1 == id3 {
		t.Errorf("Different names produced same ID")
	}

	// ID should be URL-safe
	if strings.Contains(id1, " ") || strings.Contains(id1, "/") {
		t.Errorf("ID contains unsafe characters: %s", id1)
	}
}

func TestEffortOrderPreserved(t *testing.T) {
	content := `# Plan

## Efforts

### Zebra

- repo:branch1

### Apple

- repo:branch2

### Middle

- repo:branch3

## Notes
`

	p, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	// Effort order should match file order (Zebra, Apple, Middle)
	expectedOrder := []string{
		GenerateEffortID("Zebra"),
		GenerateEffortID("Apple"),
		GenerateEffortID("Middle"),
	}

	if len(p.EffortOrder) != len(expectedOrder) {
		t.Fatalf("expected %d efforts in order, got %d", len(expectedOrder), len(p.EffortOrder))
	}

	for i, id := range p.EffortOrder {
		if id != expectedOrder[i] {
			t.Errorf("order mismatch at position %d: expected %s, got %s", i, expectedOrder[i], id)
		}
	}

	// Render and ensure order is preserved
	rendered := p.Render()
	zebraIdx := strings.Index(rendered, "### Zebra")
	appleIdx := strings.Index(rendered, "### Apple")
	middleIdx := strings.Index(rendered, "### Middle")

	if zebraIdx == -1 || appleIdx == -1 || middleIdx == -1 {
		t.Fatal("Not all efforts in rendered output")
	}

	if zebraIdx > appleIdx || appleIdx > middleIdx {
		t.Errorf("Effort order not preserved in render: Zebra=%d, Apple=%d, Middle=%d",
			zebraIdx, appleIdx, middleIdx)
	}
}

func TestMalformedEffortTolerant(t *testing.T) {
	content := `# Plan

## Efforts

### Effort A

Description here

- repo:branch1

Some random text

- repo:branch2

### Effort B
- repo:branch3

## Notes
`

	p, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(p.Efforts) != 2 {
		t.Errorf("expected 2 efforts despite malformed content, got %d", len(p.Efforts))
	}

	effortA := p.Efforts[GenerateEffortID("Effort A")]
	if len(effortA.Branches) != 2 {
		t.Errorf("expected 2 branches for Effort A, got %d", len(effortA.Branches))
	}
	if got := p.Render(); got != content {
		t.Errorf("stray text must survive a round trip; got:\n%s", got)
	}
	if !containsSubstring(p.Diagnostics, `stray text "Some random text"`) {
		t.Errorf("expected a stray-text diagnostic, got %q", p.Diagnostics)
	}
}
