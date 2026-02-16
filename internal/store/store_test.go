package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreNew(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if s == nil {
		t.Errorf("expected non-nil store")
	}
}

func TestFileStoreEnsureDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Temporarily change home
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	s, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if err := s.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir failed: %v", err)
	}

	storeDir := filepath.Join(tmpDir, ".wgo")
	if _, err := os.Stat(storeDir); err != nil {
		t.Errorf("expected store directory to exist")
	}
}

func TestSaveLoadState(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	s, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if err := s.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir failed: %v", err)
	}

	state := NewState()
	state.AddAnnotation("/path/to/repo", "feature", "Test feature")
	state.AddRepo("/path/to/repo", "https://github.com/test/repo.git")

	if err := s.SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	loaded, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	ann := loaded.GetAnnotation("/path/to/repo", "feature")
	if ann == nil {
		t.Errorf("expected to find annotation")
	} else if ann.Purpose != "Test feature" {
		t.Errorf("expected purpose 'Test feature', got %q", ann.Purpose)
	}

	repo := loaded.GetRepo("/path/to/repo")
	if repo == nil {
		t.Errorf("expected to find repo")
	} else if repo.RemoteURL != "https://github.com/test/repo.git" {
		t.Errorf("expected URL 'https://github.com/test/repo.git', got %q", repo.RemoteURL)
	}
}

func TestSaveLoadPlan(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	s, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if err := s.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir failed: %v", err)
	}

	planContent := `# Plan

## Active Branches

- **repo:branch** — Test reason

## Notes

Test notes
`

	if err := s.SavePlan(planContent); err != nil {
		t.Fatalf("SavePlan failed: %v", err)
	}

	loaded, err := s.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan failed: %v", err)
	}

	if loaded != planContent {
		t.Errorf("expected loaded plan to match saved plan")
	}
}

func TestLoadNonexistentState(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	s, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Should return empty state if file doesn't exist
	state, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	if state == nil {
		t.Errorf("expected non-nil state")
	}

	if len(state.Repos) != 0 {
		t.Errorf("expected empty repos map")
	}
}

func TestCreatePlanSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	s, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if err := s.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir failed: %v", err)
	}

	// Create a plan file
	if err := s.SavePlan("# Plan\n"); err != nil {
		t.Fatalf("SavePlan failed: %v", err)
	}

	if err := s.CreatePlanSymlink(); err != nil {
		t.Fatalf("CreatePlanSymlink failed: %v", err)
	}

	symlinkPath := s.GetPlanSymlinkPath()
	info, err := os.Lstat(symlinkPath)
	if err != nil {
		t.Fatalf("failed to stat symlink: %v", err)
	}

	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected symlink, got regular file")
	}
}
