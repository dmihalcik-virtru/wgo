package dash

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAcknowledgeBaselineWriteFailure: when last-seen.json cannot be written
// Acknowledge fails and neither the in-memory baseline nor the served delta
// moves; once the obstruction is gone a later Acknowledge succeeds.
func TestAcknowledgeBaselineWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, BaselineFile)
	d := openDash(t, dir)
	g1 := publish(t, d, synth(snapOpts{changesA: []string{"a1"}}))

	// A directory where the file belongs: the atomic rename onto it fails.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.Acknowledge(g1); err == nil {
		t.Fatal("Acknowledge succeeded although last-seen.json is unwritable")
	}
	if dl := d.Current().Delta; dl.Status != DeltaNoPreviousLook {
		t.Fatalf("a failed acknowledge must not create a baseline: %+v", dl)
	}
	if d.baseline != nil {
		t.Fatalf("in-memory baseline moved: %+v", d.baseline)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := d.Acknowledge(g1); err != nil {
		t.Fatalf("Acknowledge after clearing the obstruction: %v", err)
	}
	if dl := d.Current().Delta; dl.Status != DeltaCompared || dl.BaselineGeneration != g1 {
		t.Fatalf("delta after a good acknowledge: %+v", dl)
	}

	// With a baseline in place, a failed acknowledge keeps comparing to it.
	g2 := publish(t, d, synth(snapOpts{changesA: []string{"a2", "a1"}}))
	if dl := d.Current().Delta; dl.Summary.NewChanges != 1 {
		t.Fatalf("setup: %+v", dl.Summary)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.Acknowledge(g2); err == nil {
		t.Fatal("Acknowledge succeeded although last-seen.json is unwritable")
	}
	dl := d.Current().Delta
	if dl.BaselineGeneration != g1 || dl.Summary.NewChanges != 1 {
		t.Fatalf("failed acknowledge changed the delta: %+v", dl)
	}
	if d.baseline.Generation != g1 {
		t.Fatalf("in-memory baseline generation = %d, want %d", d.baseline.Generation, g1)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := d.Acknowledge(g2); err != nil {
		t.Fatal(err)
	}
	if dl := d.Current().Delta; dl.BaselineGeneration != g2 || dl.Summary.NewChanges != 0 {
		t.Fatalf("delta after the retry: %+v", dl)
	}
}
