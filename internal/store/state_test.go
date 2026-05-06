package store

import "testing"

func TestSetSpecPreservesPurpose(t *testing.T) {
	state := NewState()
	state.AddAnnotation("/repo", "WGO-101-spec-scaffold", "WGO-101: scaffold specs")

	state.SetSpec("/repo", "WGO-101-spec-scaffold", "spec/WGO-101.md", "draft")

	ann := state.GetAnnotation("/repo", "WGO-101-spec-scaffold")
	if ann == nil {
		t.Fatalf("expected annotation")
	}
	if ann.Purpose != "WGO-101: scaffold specs" {
		t.Fatalf("expected purpose to survive SetSpec, got %q", ann.Purpose)
	}
	if ann.SpecPath != "spec/WGO-101.md" {
		t.Fatalf("expected spec path, got %q", ann.SpecPath)
	}
	if ann.SpecState != "draft" {
		t.Fatalf("expected spec state draft, got %q", ann.SpecState)
	}
}

func TestAddAnnotationPreservesSpecMetadata(t *testing.T) {
	state := NewState()
	state.SetSpec("/repo", "WGO-101-spec-scaffold", "spec/WGO-101.md", "draft")

	state.AddAnnotation("/repo", "WGO-101-spec-scaffold", "updated reason")

	ann := state.GetAnnotation("/repo", "WGO-101-spec-scaffold")
	if ann == nil {
		t.Fatalf("expected annotation")
	}
	if ann.SpecPath != "spec/WGO-101.md" {
		t.Fatalf("expected spec path to survive AddAnnotation, got %q", ann.SpecPath)
	}
	if ann.SpecState != "draft" {
		t.Fatalf("expected spec state to survive AddAnnotation, got %q", ann.SpecState)
	}
}
