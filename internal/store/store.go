// Package store provides persistent storage for wgo state and plan files.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/virtru/wgo/internal/bujo"
)

// Store is the interface for storing and loading wgo state.
type Store interface {
	LoadState() (*State, error)
	SaveState(state *State) error
	LoadPlan() (string, error)
	SavePlan(content string) error
	EnsureDir() error
	LoadDailyLog(date string) (*bujo.DailyLog, error)
	SaveDailyLog(dl *bujo.DailyLog) error
}

// FileStore implements Store using the filesystem.
type FileStore struct {
	baseDir   string // ~/.wgo
	stateFile string // ~/.wgo/state.json
	planFile  string // ~/.wgo/plan.md
}

// New creates a new FileStore at the default location (~/.wgo).
func New() (*FileStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	baseDir := filepath.Join(home, ".wgo")
	return &FileStore{
		baseDir:   baseDir,
		stateFile: filepath.Join(baseDir, "state.json"),
		planFile:  filepath.Join(baseDir, "plan.md"),
	}, nil
}

// NewWithDir creates a FileStore at the specified directory.
func NewWithDir(baseDir string) *FileStore {
	return &FileStore{
		baseDir:   baseDir,
		stateFile: filepath.Join(baseDir, "state.json"),
		planFile:  filepath.Join(baseDir, "plan.md"),
	}
}

// BaseDir returns the base directory for the store (~/.wgo).
func (fs *FileStore) BaseDir() string {
	return fs.baseDir
}

// EnsureDir creates the store directory if it doesn't exist.
func (fs *FileStore) EnsureDir() error {
	if err := os.MkdirAll(fs.baseDir, 0o755); err != nil {
		return fmt.Errorf("failed to create store directory: %w", err)
	}
	return nil
}

// LoadState loads the state from disk.
func (fs *FileStore) LoadState() (*State, error) {
	data, err := os.ReadFile(fs.stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty state if file doesn't exist
			return NewState(), nil
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state file: %w", err)
	}
	if state.Version > 0 && state.Version < StateVersion {
		return nil, fmt.Errorf(
			"state file %s is at schema version %d; wgo expects version %d. "+
				"This file was produced by the pre-jj wgo and is no longer migratable. "+
				"Delete it and let wgo start fresh: rm %s",
			fs.stateFile, state.Version, StateVersion, fs.stateFile,
		)
	}
	if state.Version == 0 {
		state.Version = StateVersion
	}

	if state.Repos == nil {
		state.Repos = make(map[string]RepoInfo)
	}
	if state.Annotations == nil {
		state.Annotations = make(map[string]Annotation)
	}
	if state.Efforts == nil {
		state.Efforts = make(map[string]Effort)
	}
	if state.AgentSessions == nil {
		state.AgentSessions = make(map[string]AgentSession)
	}

	return &state, nil
}

// SaveState saves the state to disk atomically.
func (fs *FileStore) SaveState(state *State) error {
	if err := fs.EnsureDir(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	tmpFile := fs.stateFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	if err := os.Rename(tmpFile, fs.stateFile); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// LoadPlan loads the plan from disk.
func (fs *FileStore) LoadPlan() (string, error) {
	data, err := os.ReadFile(fs.planFile)
	if err != nil {
		if os.IsNotExist(err) {
			// Return default plan if file doesn't exist
			return defaultPlan(), nil
		}
		return "", fmt.Errorf("failed to read plan file: %w", err)
	}

	return string(data), nil
}

// SavePlan saves the plan to disk atomically.
func (fs *FileStore) SavePlan(content string) error {
	if err := fs.EnsureDir(); err != nil {
		return err
	}

	tmpFile := fs.planFile + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write plan file: %w", err)
	}

	if err := os.Rename(tmpFile, fs.planFile); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to rename plan file: %w", err)
	}

	return nil
}

// GetPlanSymlinkPath returns the path to the ~/.plan symlink.
func (fs *FileStore) GetPlanSymlinkPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".plan")
}

// CreatePlanSymlink creates a symlink from ~/.plan to ~/.wgo/plan.md.
func (fs *FileStore) CreatePlanSymlink() error {
	if err := fs.EnsureDir(); err != nil {
		return err
	}

	symlinkPath := fs.GetPlanSymlinkPath()

	// Remove existing symlink if it exists
	if _, err := os.Lstat(symlinkPath); err == nil {
		if err := os.Remove(symlinkPath); err != nil {
			return fmt.Errorf("failed to remove existing symlink: %w", err)
		}
	}

	// Create symlink
	if err := os.Symlink(fs.planFile, symlinkPath); err != nil {
		return fmt.Errorf("failed to create symlink: %w", err)
	}

	return nil
}

// logsDir returns the path to the daily logs directory.
func (fs *FileStore) logsDir() string {
	return filepath.Join(fs.baseDir, "logs")
}

// logPath returns the path for a given date (YYYY-MM-DD).
func (fs *FileStore) logPath(date string) string {
	return filepath.Join(fs.logsDir(), date+".md")
}

// LoadDailyLog loads (or creates empty) the daily log for the given date string (YYYY-MM-DD).
func (fs *FileStore) LoadDailyLog(date string) (*bujo.DailyLog, error) {
	t, err := bujo.ParseDateArg(date, time.Now())
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(fs.logPath(date))
	if err != nil {
		if os.IsNotExist(err) {
			return &bujo.DailyLog{Date: t}, nil
		}
		return nil, fmt.Errorf("failed to read daily log: %w", err)
	}

	return bujo.ParseDailyLog(string(data), t)
}

// SaveDailyLog saves the daily log to disk atomically.
func (fs *FileStore) SaveDailyLog(dl *bujo.DailyLog) error {
	if err := os.MkdirAll(fs.logsDir(), 0o755); err != nil {
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	date := bujo.FormatDate(dl.Date)
	path := fs.logPath(date)
	tmp := path + ".tmp"

	content := dl.Render()
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write daily log: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("failed to rename daily log: %w", err)
	}
	return nil
}

func defaultPlan() string {
	return `# Plan

## Active Branches

## Notes
`
}
